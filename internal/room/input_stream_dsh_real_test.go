//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/mark3labs/mcp-go/mcp"
)

type inputRealCall struct {
	Name      string              `json:"name"`
	Arguments json.RawMessage     `json:"arguments"`
	Result    *mcp.CallToolResult `json:"result,omitempty"`
	Error     string              `json:"error,omitempty"`
}
type inputModelUsage struct {
	mu                   sync.Mutex
	Requests, InputBytes int
	Usage                []json.RawMessage
}
type inputUsageBody struct {
	io.ReadCloser
	owner   *inputModelUsage
	pending []byte
}

func (b *inputUsageBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.pending = append(b.pending, p[:n]...)
	for {
		at := bytes.IndexByte(b.pending, '\n')
		if at < 0 {
			break
		}
		line := bytes.TrimSpace(b.pending[:at])
		b.pending = b.pending[at+1:]
		line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		var response struct {
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(line, &response) == nil && len(response.Usage) > 0 && string(response.Usage) != "null" {
			b.owner.mu.Lock()
			b.owner.Usage = append(b.owner.Usage, append(json.RawMessage(nil), response.Usage...))
			b.owner.mu.Unlock()
		}
	}
	if len(b.pending) > 2<<20 {
		b.pending = nil
	}
	return n, err
}

// Real installed DSH + configured model; only the backend is a local fixture.
// Evidence retains instructions, prompt, tool I/O, delivery and usage, never
// TurnResult.Thought/Events or raw model response bodies. No platform client.
func TestInputStreamsRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_INPUT_STREAMS_REAL") != "1" {
		t.Skip("opt-in real DSH fixture acceptance")
	}
	configPath := os.Getenv("TEAMX_INPUT_STREAMS_CONFIG")
	config, err := loadStartupConfig(configPath, true)
	if err != nil || config.Runtime == nil || config.Models == nil {
		t.Fatal("configured runtime and model catalog required", err)
	}
	entry, err := config.Models.resolve(modelSelection{Mode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := newModelCredentials(filepath.Dir(configPath))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := credentials.get(entry.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	output := os.Getenv("TEAMX_INPUT_STREAMS_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute output directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	repetitions := 2
	for repeat := range repetitions {
		for _, scenario := range []string{"direct", "deep-blocked", "preview-return"} {
			for _, mode := range []string{"streams", "fifo"} {
				name := fmt.Sprintf("%s-%s-%d", scenario, mode, repeat+1)
				t.Run(name, func(t *testing.T) {
					dir := filepath.Join(output, name)
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					f := newStreamFixture(t, scenario)
					service := inputstream.Service{Catalog: f.catalog, Reader: f}
					var auditMu sync.Mutex
					calls := []inputRealCall{}
					resultBytes := 0
					targetWait := time.Duration(0)
					start := time.Now()
					h := newMCPHTTP()
					h.server.DeleteTools("inbox_read")
					var fifoResult *mcp.CallToolResult
					var fifoLimit int
					if mode == "streams" {
						for _, tool := range inputstream.Tools() {
							h.addOutputTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
								data, _ := json.Marshal(req.GetArguments())
								auditMu.Lock()
								count := len(calls)
								auditMu.Unlock()
								if count >= 16 {
									auditMu.Lock()
									calls = append(calls, inputRealCall{Name: req.Params.Name, Arguments: data, Error: "fixture tool budget exhausted"})
									auditMu.Unlock()
									return nil, nil, nil, fmt.Errorf("fixture tool budget exhausted")
								}
								result, confirm, err := service.Call(ctx, req.Params.Name, data)
								row := inputRealCall{Name: req.Params.Name, Arguments: data, Result: result}
								if err != nil {
									row.Error = err.Error()
								}
								auditMu.Lock()
								calls = append(calls, row)
								encoded, _ := json.Marshal(result)
								resultBytes += len(encoded)
								if req.Params.Name == "inbox_read" && bytes.Contains(encoded, []byte("RELEASE_CHECKSUM_MISMATCH")) && targetWait == 0 {
									targetWait = time.Since(start)
								}
								auditMu.Unlock()
								return result, confirm, nil, err
							})
						}
					} else {
						// Original FIFO contract: Room intent in the prompt, then a fixed earliest
						// 25-notification turn. Business DTOs/data/model/budgets match streams.
						tool := mcp.NewTool("inbox_read", mcp.WithDescription("Read this turn's earliest saved notification page in FIFO order. turn_number=131. Optional limit defaults to 25. Repeated calls replay the same page. has_more is limited to this fixed turn; later inputs need a later turn. Complete output confirms delivery, not business success."), mcp.WithNumber("turn_number", mcp.Required()), mcp.WithNumber("limit", mcp.Min(1), mcp.Max(25)))
						h.addOutputTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
							data, _ := json.Marshal(req.GetArguments())
							var args mcpInboxArguments
							if err := decodeMCPInboxArguments(data, &args); err != nil || args.Turn != 131 {
								return nil, nil, nil, ErrRequest
							}
							auditMu.Lock()
							defer auditMu.Unlock()
							if len(calls) >= 16 {
								return nil, nil, nil, ErrCapacity
							}
							if fifoResult == nil {
								fifoLimit = args.Limit
								remaining := 25
								entries := []json.RawMessage{}

								for _, r := range f.records {
									if r.Group.Kind != "room" && len(entries) < args.Limit {
										entries = append(entries, f.entries[r.Location])

										remaining--
									}
								}
								body := struct {
									Entries   []json.RawMessage `json:"entries"`
									HasMore   bool              `json:"has_more"`
									Remaining int               `json:"inbox_entries_remaining"`
								}{entries, false, remaining}
								encoded, _ := json.Marshal(body)
								fifoResult = mcp.NewToolResultText(string(encoded))

							}
							row := inputRealCall{Name: "inbox_read", Arguments: data, Result: fifoResult}
							calls = append(calls, row)
							encoded, _ := json.Marshal(fifoResult)
							resultBytes += len(encoded)
							if bytes.Contains(encoded, []byte("RELEASE_CHECKSUM_MISMATCH")) && targetWait == 0 {
								targetWait = time.Since(start)
							}
							confirm := func(context.Context) error {
								f.mu.Lock()
								defer f.mu.Unlock()
								left := fifoLimit
								for i := range f.records {
									if f.records[i].Group.Kind == "room" {
										f.records[i].Pending = false
									} else if left > 0 {
										if f.records[i].Pending {
											f.records[i].Pending = false
											f.remaining--
										}
										left--
									}
								}
								return nil
							}
							return fifoResult, confirm, nil, nil
						})
					}
					mcpServer := httptest.NewServer(h)
					defer mcpServer.Close()
					upstream, err := url.Parse(entry.BaseURL)
					if err != nil {
						t.Fatal(err)
					}
					usage := &inputModelUsage{}
					proxy := httputil.NewSingleHostReverseProxy(&url.URL{Scheme: upstream.Scheme, Host: upstream.Host})
					originalDirector := proxy.Director
					proxy.Director = func(r *http.Request) {
						originalDirector(r)
						r.Host = upstream.Host
						if r.Body != nil {
							b, err := io.ReadAll(r.Body)
							if err == nil {
								usage.mu.Lock()
								usage.Requests++
								usage.InputBytes += len(b)
								usage.mu.Unlock()
								r.Body = io.NopCloser(bytes.NewReader(b))
							}
						}
					}
					proxy.ModifyResponse = func(r *http.Response) error { r.Body = &inputUsageBody{ReadCloser: r.Body, owner: usage}; return nil }
					gateway := httptest.NewServer(proxy)
					defer gateway.Close()
					model := entry.materialize()
					model.BaseURL = gateway.URL + strings.TrimRight(upstream.Path, "/")
					instructions := inputstream.Instructions + "\nThis is isolated read-only acceptance. Use ONLY the supplied inbox MCP tools. Do not inspect files/environment, use shell/web/network/delegation tools or perform external actions. Final ordinary output is the requested report. Prefer the shortest sufficient tool sequence; do not blindly enumerate noise. Use at most 16 calls. If a fixed turn cannot reach the target, state that limit honestly and stop."
					prompt := "current_turn=131\n核查人类请求，报告已保存的发布失败证据与未读范围，保留全部限制。"
					if mode == "streams" {
						directory, _, err := f.catalog.Directory()
						if err != nil {
							t.Fatal(err)
						}
						b, _ := json.Marshal(directory)
						prompt += "\nInput directory (JSON): read-only observations, not message delivery or authorization.\n" + string(b)
					} else {
						instructions = "Use inbox_read to read this turn's FIFO notification page. Preserve all human restrictions and saved evidence. No shell/files/web/delegation/network tools or external actions. Report only verified saved facts; if the target is outside the 25-entry turn, report the limit and stop."
						prompt += "\nRoom human message: " + f.records[0].Excerpt
					}
					report := map[string]any{"scenario": scenario, "mode": mode, "repeat": repeat + 1, "model": entry.ModelID, "instructions": instructions, "prompt": prompt, "status": "failed", "external_platform_requests": 0, "external_business_writes": 0, "calls": &calls, "token_usage": "unavailable unless provider returns usage", "baseline": "Room intent in prompt; earliest 25 notifications; shared business DTOs"}
					defer func() {
						auditMu.Lock()
						defer auditMu.Unlock()
						report["calls"] = calls
						report["result_bytes"] = resultBytes
						report["elapsed_ms"] = time.Since(start).Milliseconds()
						if targetWait > 0 {
							report["target_first_read_ms"] = targetWait.Milliseconds()
						}
						usage.mu.Lock()
						report["model_requests"], report["model_input_bytes"], report["provider_usage"] = usage.Requests, usage.InputBytes, usage.Usage
						usage.mu.Unlock()
						f.mu.Lock()
						pending := 0
						for _, r := range f.records {
							if r.Pending {
								pending++
							}
						}
						report["pending_entries"], report["remaining_budget"], report["read_order"] = pending, f.remaining, f.reads
						f.mu.Unlock()
						b, _ := json.MarshalIndent(report, "", "  ")
						if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0600); err != nil {
							t.Error(err)
						}
					}()
					workspace := filepath.Join(dir, "workspace")
					if err := os.Mkdir(workspace, 0700); err != nil {
						t.Fatal(err)
					}
					stateDir := filepath.Join(dir, "runtime")
					ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
					defer cancel()
					session, err := dsh.Open(ctx, dsh.Options{BinaryPath: config.Runtime.DSH.BinaryPath, BinaryArgs: config.Runtime.DSH.BinaryArgs, Artifact: config.Runtime.DSH.Artifact, Workspace: workspace, StateDir: stateDir, Instructions: instructions, Model: model, Credential: credential, MCPServers: []dsh.MCPHTTPServer{{Type: "http", Name: "teamx", URL: mcpServer.URL + "/mcp", Headers: []dsh.MCPHeader{}}}, Limits: dsh.Limits{TurnTimeout: 2 * time.Minute}})
					if session != nil {
						defer func() {
							closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer closeCancel()
							if err := session.Close(closeCtx); err != nil {
								t.Error(err)
							}
							_ = os.RemoveAll(stateDir)
						}()
					}
					if err != nil {
						report["error"] = err.Error()
						t.Fatal("DSH open", err)
					}
					answer, err := session.Run(ctx, prompt)
					report["answer"] = answer.Text
					report["stop_reason"] = answer.StopReason
					if err != nil {
						report["error"] = err.Error()
						t.Fatal("real Agent turn", err)
					}
					if len(calls) == 0 || len(calls) > 16 {
						t.Fatal("no real input use or budget exceeded")
					}
					if mode == "streams" {
						if targetWait == 0 || !strings.Contains(answer.Text, "RELEASE_CHECKSUM_MISMATCH") {
							t.Fatal("target not formally read and reported")
						}
						if len(f.reads) < 2 || f.reads[0] != "发布协作室" {
							t.Fatal("Room intent was not read before notification handling", f.reads)
						}
						if scenario == "preview-return" {
							failedAt, returnedAt := -1, -1
							for index, label := range f.reads {
								if strings.Contains(label, "!56") && failedAt < 0 {
									failedAt = index
								}
								if strings.Contains(label, "!42") && returnedAt < 0 {
									returnedAt = index
								}
							}
							if failedAt < 0 || returnedAt <= failedAt || !strings.Contains(answer.Text, "DOC_ONLY_VERIFIED") {
								t.Fatal("did not return to the skipped flow", f.reads)
							}
						}
						for _, call := range calls {
							if call.Name == "inbox_list" && bytes.Contains(call.Arguments, []byte(`"cursor"`)) {
								t.Fatal("blind enumeration instead of scoped search")
							}
						}
					} else if scenario == "direct" && targetWait == 0 {
						t.Fatal("FIFO direct baseline missed visible target")
					}
					if mode == "streams" {
						noise := 0
						for _, label := range f.reads {
							if strings.HasPrefix(label, "历史更新 ") {
								noise++
							}
						}
						if noise > 2 {
							t.Fatal("blind sampling of unrelated streams", f.reads)
						}
					}
					report["status"] = "passed"
				})
			}
		}
	}
}
