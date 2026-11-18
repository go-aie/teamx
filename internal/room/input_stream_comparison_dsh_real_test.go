//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	inputstream "github.com/go-aie/teamx/internal/inputstreamtest"
	"github.com/mark3labs/mcp-go/mcp"
)

type comparisonResponseBody struct {
	io.ReadCloser
	mu    *sync.Mutex
	count *int
}

func (b *comparisonResponseBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.mu.Lock()
	*b.count += n
	b.mu.Unlock()
	return n, err
}

type comparisonCall struct {
	Name         string              `json:"name"`
	Arguments    json.RawMessage     `json:"arguments"`
	Result       *mcp.CallToolResult `json:"result,omitempty"`
	Error        string              `json:"error,omitempty"`
	Before       comparisonState     `json:"before"`
	Prepared     comparisonState     `json:"after_prepare"`
	Confirmed    *comparisonState    `json:"after_confirm,omitempty"`
	ConfirmError string              `json:"confirm_error,omitempty"`
	At           float64             `json:"at_seconds"`
}

func validateComparison(scenario, mode string, f *streamFixture, initial comparisonState, calls []comparisonCall, answer string) []string {
	issues := []string{}
	check := func(ok bool, message string) {
		if !ok {
			issues = append(issues, message)
		}
	}
	check(len(calls) > 0 && len(calls) <= 16, "tool call limit")
	state := comparisonSnapshot(f)
	expected := comparisonExpectedLabels(scenario)
	order := []string{}
	for _, label := range state.Delivered {
		if len(order) == 0 || order[len(order)-1] != label {
			order = append(order, label)
		}
	}
	check(reflect.DeepEqual(order, expected), "formal delivery order/zero unrelated reads")
	initialPending := map[inputstream.Location]bool{}
	for _, p := range initial.Pending {
		initialPending[p] = true
	}
	expectedDelivered := map[inputstream.Location]bool{}
	for _, r := range f.records {
		allowed := r.Group.Kind == "room" || comparisonTargetLabel(r.Group.Facts.Label) && scenario != "stats-only" && scenario != "preview-skip"
		if scenario == "ambiguous" && r.Group.Kind != "room" {
			allowed = allowed && r.Group.Facts.AccountLabel == "release-owner"
		}
		if allowed && initialPending[r.Location] {
			expectedDelivered[r.Location] = true
		}
		if initialPending[r.Location] {
			check(r.Pending != expectedDelivered[r.Location], "exact delivered record set")
		}
	}
	expectedCount := 1
	if scenario == "stats-only" || scenario == "preview-skip" {
		expectedCount = 0
	}
	if scenario == "preview-return" {
		expectedCount = 3
	}
	check(state.Remaining == 25-expectedCount, "shared platform budget")
	check(len(state.Associations) == expectedCount, "simulated associations")
	check(len(state.Pending) == len(initial.Pending)-expectedCount-1, "unselected inputs retained")
	if expectedCount > 0 {
		check(strings.Contains(answer, "RELEASE_CHECKSUM_MISMATCH"), "target evidence reported")
	}
	if scenario == "preview-return" {
		check(strings.Contains(answer, "DOC_ONLY_VERIFIED"), "returned flow evidence reported")
	}
	check(comparisonPreservesRestrictions(answer), "human restrictions in report")
	for _, claim := range []string{"已评论", "已合并", "已写入平台"} {
		check(!strings.Contains(answer, claim), "unauthorized success claim")
	}
	previewAt := -1
	targetAt := -1
	previewOK := false
	statsOK := false
	zeroOK := false
	ambiguousOK := false
	recovery := map[string]bool{}
	for i, c := range calls {
		data, _ := json.Marshal(c.Result)
		text := string(data) + c.Error
		if c.Name == "inbox_read" && c.Error == "" {
			check(reflect.DeepEqual(c.Before.Pending, c.Prepared.Pending) && c.Before.Remaining == c.Prepared.Remaining, "read consumed before output")
			check(c.Confirmed != nil && c.ConfirmError == "", "formal page not confirmed")
		}
		if c.Name != "inbox_read" {
			check(reflect.DeepEqual(c.Before, c.Prepared), "discovery changed consumption/associations/budget")
			check(c.Confirmed == nil, "discovery created confirmation")
		}
		if c.Name == "inbox_read" && strings.Contains(text, "RELEASE_CHECKSUM_MISMATCH") {
			targetAt = i
		}
		isPreview := c.Name == "inbox_preview" || c.Name == "inbox_find" && bytes.Contains(c.Arguments, []byte(`"preview":true`))
		// Whitespace-independent argument parsing.
		var args map[string]any
		_ = json.Unmarshal(c.Arguments, &args)
		if filter, ok := args["filter"].(map[string]any); ok {
			for k, v := range filter {
				args[k] = v
			}
		}
		if c.Name == "inbox_find" {
			isPreview = args["preview"] == true
		}
		if scenario == "stats-only" {
			check(!isPreview, "stats-only unexpectedly previewed")
		}
		if isPreview && c.Error == "" && strings.Contains(text, "excerpt") {
			want := "!56"
			if scenario == "preview-return" {
				want = "!42"
			}
			var ref string
			if c.Name == "inbox_preview" {
				ref, _ = args["stream_ref"].(string)
			} else if c.Result != nil && len(c.Result.Content) > 0 {
				var out inputstream.Find
				if tc, ok := c.Result.Content[0].(mcp.TextContent); ok {
					_ = json.Unmarshal([]byte(tc.Text), &out)
					for _, s := range out.Streams {
						if len(s.Preview) > 0 {
							ref = s.Ref
							break
						}
					}
				}
			}
			for _, r := range f.records {
				rr, _ := r.Group.StreamRef(comparisonMember)
				if rr == ref && strings.Contains(r.Group.Facts.Label, want) {
					previewOK = true
					previewAt = i
				}
			}
		}
		if (args["project"] == "项目 A" || args["project"] == "acme/release-service") && c.Error == "" && c.Result != nil && len(c.Result.Content) > 0 {
			var stats inputstream.Overview
			if tc, ok := c.Result.Content[0].(mcp.TextContent); ok {
				if c.Name == "inbox_overview" || c.Name == "inbox_stats" {
					_ = json.Unmarshal([]byte(tc.Text), &stats)
				}
				if c.Name == "inbox_find" {
					var result inputstream.Find
					_ = json.Unmarshal([]byte(tc.Text), &result)
					if result.Sources != nil {
						stats = *result.Sources
					}
				}
				statsOK = statsOK || stats.Pending == 1 && stats.Entries == 1 && stats.Blocked == 0 && len(stats.Sources) == 1 && stats.Sources[0].Provider == "gitlab"
			}
		}
		zeroOK = zeroOK || strings.Contains(text, "input_no_match")
		ambiguousOK = ambiguousOK || strings.Contains(text, "input_scope_ambiguous")
		for _, key := range []string{"input_catalog_expired", "input_stream_empty", "input_stream_blocked"} {
			if strings.Contains(text, key) {
				recovery[key] = true
			}
		}
	}
	if scenario == "preview-return" {
		check(previewOK && previewAt < targetAt, "specified preview before dependent read")
	}
	if scenario == "preview-skip" {
		check(previewOK && (strings.Contains(answer, "RELEASE_CHECKSUM_MISMATCH") || strings.Contains(answer, "校验值")), "specified non-consuming preview and reported evidence")
	}
	if scenario == "stats-only" {
		check(statsOK, "source statistics")
	}
	if scenario == "zero-query" {
		check(zeroOK, "literal zero-hit recovery exercised")
	}
	if scenario == "ambiguous" {
		check(ambiguousOK, "cross-account disambiguation exercised")
	}
	if scenario == "recovery" {
		check(len(recovery) == 3, "expiry/empty/blocked recovery distinguished")
	}
	return issues
}
func TestInputDiscoveryComparisonRealDSH(t *testing.T) { runInputComparisonRealDSH(t, false) }

func TestInputViewsComparisonRealDSH(t *testing.T) { runInputComparisonRealDSH(t, true) }

func runInputComparisonRealDSH(t *testing.T, views bool) {
	native := os.Getenv("TEAMX_INPUT_NATIVE_REAL") == "1"
	if os.Getenv("TEAMX_INPUT_COMPARISON_REAL") != "1" {
		t.Skip("opt-in real DSH fixture acceptance")
	}
	configPath := os.Getenv("TEAMX_INPUT_COMPARISON_CONFIG")
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
	output := os.Getenv("TEAMX_INPUT_COMPARISON_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute output directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	repetitions := 10
	scenarios := []string{"direct", "deep-blocked", "preview-return", "ambiguous", "zero-query", "stats-only", "preview-skip", "recovery"}
	if views {
		scenarios = append(scenarios, "ext-stats", "ext-scope", "ext-explore")
		if native {
			scenarios = append(scenarios, "ext-domain")
		}
	}
	recheck := views && os.Getenv("TEAMX_INPUT_VIEWS_RECHECK") == "1"
	if recheck {
		scenarios = []string{"deep-blocked", "ext-explore"}
		repetitions = 3
	}
	pilot := os.Getenv("TEAMX_INPUT_COMPARISON_PILOT") == "1"
	if pilot {
		repetitions = 1
		scenarios = scenarios[:3]
	}
	if native && os.Getenv("TEAMX_INPUT_NATIVE_EXT_PILOT") == "1" {
		repetitions = 1
		scenarios = []string{"ambiguous", "zero-query", "stats-only", "preview-skip", "recovery", "ext-stats", "ext-scope", "ext-explore", "ext-domain"}
	}
	if native && os.Getenv("TEAMX_INPUT_NATIVE_RECHECK") == "1" {
		recheck = true
		repetitions = 5
		scenarios = []string{"ambiguous", "recovery", "ext-explore", "ext-scope"}
	}
	for repeat := range repetitions {
		for _, scenario := range scenarios {
			if strings.HasPrefix(scenario, "ext-") && repeat >= 3 {
				continue
			}
			if repeat >= 5 && scenario != "direct" && scenario != "deep-blocked" && scenario != "preview-return" {
				continue
			}
			modes := []string{"four", "two"}
			if repeat%2 == 1 {
				modes = []string{"two", "four"}
			}
			if native {
				for i := range modes {
					if modes[i] == "two" {
						modes[i] = "native"
					}
				}
			}
			if views && !native {
				for i := range modes {
					if modes[i] == "two" {
						modes[i] = "views"
					}
				}
			}
			for _, mode := range modes {
				name := fmt.Sprintf("%s-%s-%02d", scenario, mode, repeat+1)
				t.Run(name, func(t *testing.T) {
					if native && os.Getenv("TEAMX_INPUT_NATIVE_PARALLEL") == "1" {
						t.Parallel()
					}
					dir := filepath.Join(output, name)
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					f := comparisonFixture(t, scenario)
					if views {
						f = viewsComparisonFixture(t, scenario)
					}
					if native && scenario == "ext-domain" {
						f = domainComparisonFixture(t)
					}
					if native {
						nativeFixtureTask(f)
					}
					initial := comparisonSnapshot(f)
					var nativeFixture *nativeComparison
					if native {
						nativeFixture = newNativeComparison(t, f)
					}
					service := inputstream.Service{Catalog: f.catalog, Reader: f}

					var auditMu sync.Mutex
					calls := []comparisonCall{}
					resultBytes := 0
					targetWait := time.Duration(0)
					start := time.Now()
					h := newMCPHTTP()
					h.server.DeleteTools("inbox_read")
					toolSet := inputstream.Tools()
					instructions := inputstream.Instructions
					if mode == "two" {
						toolSet = inputstream.FindTools()
						instructions = inputstream.FindInstructions
					}
					if mode == "views" {
						toolSet = inputstream.ViewsTools()
						instructions = inputstream.ViewsInstructions
					}
					if native {
						instructions = inputComparisonCommon + "\n" + inputComparisonBaselineParameters
						if mode == "native" {
							toolSet = nativeComparisonTools()
							instructions = inputComparisonCommon + "\n" + inputComparisonNativeParameters
						}
					}
					for _, tool := range toolSet {
						h.addOutputTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, func(context.Context) error, func(), error) {
							data, _ := json.Marshal(req.GetArguments())
							auditMu.Lock()
							count := len(calls)
							auditMu.Unlock()
							row := comparisonCall{Name: req.Params.Name, Arguments: data, Before: comparisonSnapshot(f), At: time.Since(start).Seconds()}
							var result *mcp.CallToolResult
							var confirm func(context.Context) error
							var err error
							if count >= 16 {
								err = fmt.Errorf("fixture tool budget exhausted")
							} else {
								if mode == "native" {
									result, confirm, err = nativeFixture.Call(ctx, req.Params.Name, data)
								} else if mode == "views" {
									result, confirm, err = (inputstream.ViewsService{Service: service}).Call(ctx, req.Params.Name, data)
								} else {
									result, confirm, err = service.Call(ctx, req.Params.Name, data)
								}
							}
							row.Result = result
							row.Prepared = comparisonSnapshot(f)
							if err != nil {
								row.Error = err.Error()
							}
							encoded, _ := json.Marshal(result)
							auditMu.Lock()
							index := len(calls)
							calls = append(calls, row)
							resultBytes += len(encoded)
							if req.Params.Name == "inbox_read" && comparisonIsTargetPage(result) && targetWait == 0 {
								targetWait = time.Since(start)
							}
							auditMu.Unlock()
							if confirm != nil {
								original := confirm
								confirm = func(ctx context.Context) error {
									err := original(ctx)
									state := comparisonSnapshot(f)
									auditMu.Lock()
									calls[index].Confirmed = &state
									if err != nil {
										calls[index].ConfirmError = err.Error()
									}
									auditMu.Unlock()
									return err
								}
							}
							return result, confirm, nil, err
						})
					}

					mcpServer := httptest.NewServer(h)
					defer mcpServer.Close()
					upstream, err := url.Parse(entry.BaseURL)
					if err != nil {
						t.Fatal(err)
					}
					usage := &inputModelUsage{}
					responseBytes := 0
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
					proxy.ModifyResponse = func(r *http.Response) error {
						r.Body = &inputUsageBody{ReadCloser: &comparisonResponseBody{ReadCloser: r.Body, mu: &usage.mu, count: &responseBytes}, owner: usage}
						return nil
					}
					gateway := httptest.NewServer(proxy)
					defer gateway.Close()
					model := entry.materialize()
					model.BaseURL = gateway.URL + strings.TrimRight(upstream.Path, "/")
					instructions += "\nThis is isolated read-only acceptance. Use ONLY supplied inbox MCP tools. Do not inspect files/environment, use shell/web/network/delegation tools or perform external actions. Final ordinary output is the requested report. At most 16 calls. Preserve human restrictions; scoped tasks allow ZERO unrelated formal reads. Do not enumerate the full catalog. If the turn cannot reach its target, report the limit honestly."
					prompt := "current_turn=131\n核对 Room 人类请求，按要求报告保存的证据/范围并保留限制。"
					directory, _, err := f.catalog.Directory()
					if mode == "views" {
						directory, _, err = f.catalog.ViewsDirectory()
					}
					if mode == "two" {
						directory, _, err = f.catalog.FindDirectory()
					}
					if err != nil {
						t.Fatal(err)
					}
					b, _ := json.Marshal(directory)
					if mode == "native" {
						c, e := nativeFixture.catalog()
						if e != nil {
							t.Fatal(e)
						}
						d, _, e := c.Directory()
						if e != nil {
							t.Fatal(e)
						}
						b, _ = json.Marshal(d)
					}
					prompt += "\nInput directory (JSON): read-only observation, not delivery or authorization.\n" + string(b)
					schema, _ := json.Marshal(toolSet)
					schemaHash := fmt.Sprintf("%x", sha256.Sum256(schema))
					publicModel := entry.materialize()
					publicModel.BaseURL = "configured endpoint (not retained)"
					publicModel.APIKeyEnv = ""
					report := map[string]any{"model_configuration": publicModel, "scenario": scenario, "mode": mode, "repeat": repeat + 1, "pilot": pilot, "cost_recheck": recheck, "model": entry.ModelID, "instructions": instructions, "prompt": prompt, "tools": toolSet, "schema_sha256": schemaHash, "schema_bytes": len(schema), "status": "failed", "external_platform_requests": 0, "external_business_writes": 0, "initial_state": initial, "dsh_binary": config.Runtime.DSH.BinaryPath, "dsh_args": config.Runtime.DSH.BinaryArgs, "dsh_artifact": config.Runtime.DSH.Artifact}

					defer func() {
						auditMu.Lock()
						defer auditMu.Unlock()
						report["calls"] = calls
						report["result_bytes"] = resultBytes
						report["tool_calls"] = len(calls)
						report["elapsed_seconds"] = time.Since(start).Seconds()
						if targetWait > 0 {
							report["target_first_page_wait_seconds"] = targetWait.Seconds()
						} else {
							report["target_first_page_wait_seconds"] = nil
						}
						report["final_state"] = comparisonSnapshot(f)
						usage.mu.Lock()
						report["model_requests"] = usage.Requests
						report["model_request_bytes"] = usage.InputBytes
						report["model_response_bytes"] = responseBytes
						report["provider_usage"] = usage.Usage
						usage.mu.Unlock()
						invalid := []map[string]any{}
						seen := map[string]comparisonState{}
						for index, c := range calls {
							reasons := []string{}
							key := c.Name + string(c.Arguments)
							if c.Error != "" && !strings.Contains(c.Error, "input_scope_ambiguous") && !strings.Contains(c.Error, "input_catalog_expired") && !strings.Contains(c.Error, "input_snapshot_expired") && !strings.Contains(c.Error, "input_stream_empty") && !strings.Contains(c.Error, "input_stream_blocked") {
								reasons = append(reasons, "parameter_or_budget_rejection")
							}
							if previous, ok := seen[key]; ok && c.Name != "inbox_read" && reflect.DeepEqual(previous, c.Before) {
								reasons = append(reasons, "repeated_discovery_without_state_change")
							}
							seen[key] = c.Before
							if c.Name == "inbox_read" && c.Result != nil && len(c.Result.Content) > 0 {
								var page inputstream.Read
								if text, ok := c.Result.Content[0].(mcp.TextContent); ok {
									_ = json.Unmarshal([]byte(text.Text), &page)
									if strings.HasPrefix(page.Stream.Label, "历史更新 ") {
										reasons = append(reasons, "unrelated_formal_read")
									}
								}
							}
							if len(reasons) > 0 {
								invalid = append(invalid, map[string]any{"call_index": index, "reasons": reasons})
							}
						}
						report["invalid_calls"] = len(invalid)
						report["invalid_call_details"] = invalid

						data, _ := json.MarshalIndent(report, "", "  ")
						if err := os.WriteFile(filepath.Join(dir, "evidence.json"), data, 0600); err != nil {
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
					report["runtime_agent"] = session.RuntimeAgent()
					answer, err := session.Run(ctx, prompt)
					report["answer"] = answer.Text
					report["stop_reason"] = answer.StopReason
					if err != nil {
						report["error"] = err.Error()
						t.Fatal("real Agent turn", err)
					}
					issues := validateComparison(scenario, mode, f, initial, calls, answer.Text)
					if strings.HasPrefix(scenario, "ext-") {
						issues = validateViewsExtension(scenario, mode, f, initial, calls, answer.Text)
					}
					if native {
						issues = validateNativeComparison(scenario, mode, f, initial, calls, answer.Text)
					}
					if len(issues) > 0 {
						report["issues"] = issues
						t.Fatal(strings.Join(issues, "; "))
					}
					report["status"] = "passed"
				})
			}
		}
	}
}

func validateViewsExtension(scenario, mode string, f *streamFixture, initial comparisonState, calls []comparisonCall, answer string) []string {
	issues := []string{}
	check := func(ok bool, message string) {
		if !ok {
			issues = append(issues, message)
		}
	}
	final := comparisonSnapshot(f)
	wanted := map[inputstream.Location]bool{}
	for _, r := range f.records {
		if r.Group.Kind == "room" || scenario == "ext-scope" && r.Group.Facts.ProjectID == "7" || scenario == "ext-explore" && r.Group.Facts.ChatID == "oc_release" {
			wanted[r.Location] = true
		}
	}
	for _, r := range f.records {
		check(r.Pending != wanted[r.Location], "exact delivered set/zero unrelated reads")
	}
	platform := len(wanted) - 1
	check(final.Remaining == 25-platform && len(final.Associations) == platform, "shared budget/associations")
	check(comparisonPreservesRestrictions(answer), "human restrictions")
	check(len(calls) > 0 && len(calls) <= 16, "call budget")
	stats, scope, blocked, oldest, literal := false, false, false, false, false
	for _, c := range calls {
		if c.Name != "inbox_read" {
			check(reflect.DeepEqual(c.Before, c.Prepared) && c.Confirmed == nil, "discovery changed state")
		}
		if c.Name == "inbox_read" && c.Error == "" {
			check(c.Confirmed != nil && c.ConfirmError == "" && reflect.DeepEqual(c.Before.Pending, c.Prepared.Pending), "output confirmation boundary")
		}
		var args map[string]any
		_ = json.Unmarshal(c.Arguments, &args)
		if f, ok := args["filter"].(map[string]any); ok {
			for k, v := range f {
				args[k] = v
			}
		}
		var text string
		if c.Result != nil && len(c.Result.Content) > 0 {
			if tc, ok := c.Result.Content[0].(mcp.TextContent); ok {
				text = tc.Text
			}
		}
		if scenario == "ext-stats" && c.Error == "" && args["channel"] == "todos" {
			if c.Name == "inbox_stats" {
				var s inputstream.Overview
				_ = json.Unmarshal([]byte(text), &s)
				stats = s.Pending == 2 && s.Entries == 5 && s.Blocked == 1 && len(s.Sources) == 1
			}
			if c.Name == "inbox_list" && args["state"] == "all" {
				var l inputstream.List
				_ = json.Unmarshal([]byte(text), &l)
				n, b := 0, 0
				for _, s := range l.Streams {
					n += s.Pending
					if s.Blocked != "" {
						b++
					}
				}
				stats = l.Matched == 2 && n == 5 && b == 1
			}
		}
		if scenario == "ext-scope" && c.Error == "" && args["iid"] == float64(56) {
			if mode == "views" {
				w, _ := args["within"].(string)
				scope = strings.HasPrefix(w, "scope_") && args["object_type"] == "merge_request"
			} else {
				scope = args["account_ref"] != nil && (args["project_id"] == float64(7) || args["project"] == "acme/release-service") && args["object_type"] == "merge_request"
			}
		}
		if scenario == "ext-explore" && c.Name == "inbox_list" && c.Error == "" {
			blocked = blocked || args["state"] == "blocked" && strings.Contains(text, "blocked_reason")
			oldest = oldest || args["sort"] == "oldest"
			literal = literal || args["thread_id"] == "omt_release" && args["query"] == "NEEDLE" && strings.Contains(text, "发布值班群")
		}
	}
	switch scenario {
	case "ext-stats":
		check(stats, "exact-condition whole-stream statistics")
		check(strings.Contains(answer, "5") && strings.Contains(answer, "2"), "statistics reported")
	case "ext-scope":
		check(scope, "reused native project scope and intersection")
		check(strings.Contains(answer, "RELEASE_CHECKSUM_MISMATCH"), "target evidence reported")
	case "ext-explore":
		check(blocked && oldest && literal, "blocked/oldest/thread/literal exploration")
		check(strings.Contains(answer, "FIRST_CONTEXT") && strings.Contains(answer, "NEEDLE_EVIDENCE"), "ordered early/later evidence reported")
		for _, c := range calls {
			if c.Name == "inbox_read" && c.Result != nil {
				b, _ := json.Marshal(c.Result)
				a, z := strings.Index(string(b), "FIRST_CONTEXT"), strings.Index(string(b), "NEEDLE_EVIDENCE")
				if a >= 0 && z >= 0 {
					check(a < z, "read skipped earlier entry")
				}
			}
		}
	}
	return issues
}

// Contract-only oracle checks use predetermined operations, never model output.
// The extended tasks remain held out from real-model development pilots.
func TestInputViewsExtensionOracles(t *testing.T) {
	for _, scenario := range []string{"ext-stats", "ext-scope", "ext-explore"} {
		for _, mode := range []string{"four", "views"} {
			t.Run(scenario+"-"+mode, func(t *testing.T) {
				f := viewsComparisonFixture(t, scenario)
				initial := comparisonSnapshot(f)
				service := inputstream.Service{Catalog: f.catalog, Reader: f}
				calls := []comparisonCall{}
				call := func(name string, args map[string]any) *mcp.CallToolResult {
					t.Helper()
					args["turn_number"] = 131
					data, _ := json.Marshal(args)
					row := comparisonCall{Name: name, Arguments: data, Before: comparisonSnapshot(f)}
					var result *mcp.CallToolResult
					var confirm func(context.Context) error
					var err error
					if mode == "views" {
						result, confirm, err = (inputstream.ViewsService{Service: service}).Call(context.Background(), name, data)
					} else {
						result, confirm, err = service.Call(context.Background(), name, data)
					}
					if err != nil {
						t.Fatal(name, string(data), err)
					}
					row.Result = result
					row.Prepared = comparisonSnapshot(f)
					if confirm != nil {
						if err := confirm(context.Background()); err != nil {
							t.Fatal(err)
						}
						state := comparisonSnapshot(f)
						row.Confirmed = &state
					}
					calls = append(calls, row)
					return result
				}
				query := func(name string, filter map[string]any, extra map[string]any) *mcp.CallToolResult {
					t.Helper()
					if mode == "views" {
						extra["filter"] = filter
					} else {
						for k, v := range filter {
							extra[k] = v
						}
					}
					return call(name, extra)
				}
				roomRef, _ := f.records[0].Group.StreamRef(comparisonMember)
				call("inbox_read", map[string]any{"stream_ref": roomRef, "read_id": "room"})
				switch scenario {
				case "ext-stats":
					name := "inbox_overview"
					if mode == "views" {
						name = "inbox_stats"
					}
					if mode == "views" {
						query(name, map[string]any{"project": "acme/release-service", "channel": "todos"}, map[string]any{})
					} else {
						query("inbox_list", map[string]any{"project": "acme/release-service", "channel": "todos"}, map[string]any{"state": "all"})
					}
				case "ext-scope":
					name := "inbox_overview"
					if mode == "views" {
						name = "inbox_stats"
					}
					result := query(name, map[string]any{"project": "acme/release-service"}, map[string]any{})
					var overview inputstream.Overview
					_ = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &overview)
					extra := map[string]any{}
					filter := map[string]any{"object_type": "merge_request", "iid": 56}
					if mode == "views" {
						extra["within"] = overview.ScopeRef
					} else {
						filter["account_ref"] = overview.Sources[0].AccountRef
						filter["project"] = "acme/release-service"
					}
					query("inbox_list", filter, extra)
					ref, _ := f.records[1].Group.StreamRef(comparisonMember)
					call("inbox_read", map[string]any{"stream_ref": ref, "read_id": "target"})
				case "ext-explore":
					query("inbox_list", map[string]any{}, map[string]any{"state": "blocked"})
					result := query("inbox_list", map[string]any{"chat": "发布值班群", "thread_id": "omt_release"}, map[string]any{"query": "NEEDLE", "sort": "oldest"})
					var list inputstream.List
					_ = json.Unmarshal([]byte(result.Content[0].(mcp.TextContent).Text), &list)
					if len(list.Streams) != 1 || list.Streams[0].Pending != 2 {
						t.Fatal("exact native chat grouping", list)
					}
					call("inbox_read", map[string]any{"stream_ref": list.Streams[0].Ref, "read_id": "target"})
				}
				answer := "2 流、5 条。RELEASE_CHECKSUM_MISMATCH FIRST_CONTEXT NEEDLE_EVIDENCE。不评论、不合并、不写入平台。"
				if issues := validateViewsExtension(scenario, mode, f, initial, calls, answer); len(issues) > 0 {
					t.Fatal(issues)
				}
			})
		}
	}
}

// Determine the target from the delivered notification DTO. Human Room requests
// may mention evidence markers, so text matching alone cannot time Lark pages.
func comparisonIsTargetPage(result *mcp.CallToolResult) bool {
	if result == nil || len(result.Content) == 0 {
		return false
	}
	tc, ok := result.Content[0].(mcp.TextContent)
	if !ok {
		return false
	}
	var page inputstream.Read
	if json.Unmarshal([]byte(tc.Text), &page) != nil || page.Stream.Kind != "notification" {
		return false
	}
	return strings.Contains(tc.Text, "RELEASE_CHECKSUM_MISMATCH") || page.Stream.Label == "发布值班群"
}
func TestInputViewsTargetPageTiming(t *testing.T) {
	for _, tc := range []struct {
		kind, label, text string
		want              bool
	}{
		{"room", "Room", "FIRST_CONTEXT NEEDLE_EVIDENCE", false},
		{"notification", "发布值班群", "FIRST_CONTEXT", true},
		{"notification", "MR !56", "RELEASE_CHECKSUM_MISMATCH", true},
		{"notification", "其他群", "ordinary", false},
	} {
		b, _ := json.Marshal(inputstream.Read{Stream: inputstream.Stream{Kind: tc.kind, Label: tc.label}, Entries: []json.RawMessage{json.RawMessage(fmt.Sprintf(`{"text":%q}`, tc.text))}})
		if got := comparisonIsTargetPage(mcp.NewToolResultText(string(b))); got != tc.want {
			t.Fatal(tc, got)
		}
	}
}
