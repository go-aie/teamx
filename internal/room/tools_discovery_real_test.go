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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Real model + packaged DSH + production CLI/HTTP, with synthetic accounts and
// an executable that only emits a help marker. No real platform is contacted.
func TestToolDiscoveryRealDSHComparison(t *testing.T) {
	if os.Getenv("TEAMX_TOOL_DISCOVERY_REAL") != "1" {
		t.Skip("opt-in read-only model comparison; config and fresh output required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "external_platform_requests": 0, "external_writes": 0}
	defer imageRealReport(t, out, report)
	path := os.Getenv("TEAMX_TOOL_DISCOVERY_CONFIG")
	cfg, err := loadStartupConfig(path, true)
	if err != nil || cfg.Runtime == nil || cfg.Models == nil {
		t.Fatal("current runtime/model configuration required", err)
	}
	model, err := cfg.Models.resolve(modelSelection{Mode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	credentials, err := newModelCredentials(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := credentials.get(model.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	args, runtimeEvidence := collaborationRuntime(t, cfg.Runtime.DSH.BinaryPath, cfg.Runtime.DSH.BinaryArgs)
	report["runtime"], report["model"] = runtimeEvidence, model.ModelID
	binary := productionFixtureCLI(t)
	m, room, svc, a, members := cliToolFixture(t, "printf 'DISCOVERY_HELP_OK\\n'")
	m.toolCatalog[0].Skill = "skills/gitlab/SKILL.md"
	m.toolCatalog[1].Description = "Local build result reader"
	b := sharedToolAccount(t, m)
	for _, c := range []managedPlatform{a, b} {
		bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	}
	bindCLITool(t, m, room.RoomIdentity, members[0], managedPlatform{}, "local", 0)
	m.mu.Lock()
	f, err := m.readToolBindingsLocked(room.RoomIdentity)
	selections, selectionErr := m.selectionsLocked(f, agentCaller(members[0].AgentID, members[0].MembershipID))
	m.mu.Unlock()
	if err != nil || selectionErr != nil {
		t.Fatal(err, selectionErr)
	}
	baseline, _ := json.Marshal(toolDiscoveryBaseline(selections))
	candidate, _ := json.Marshal(projectToolSelections(selections))
	report["directory_bytes_before"], report["directory_bytes_after"] = len(baseline), len(candidate)
	for name, data := range map[string][]byte{"before": baseline, "after": candidate} {
		if err := os.WriteFile(filepath.Join(out, name+".json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	type call struct {
		Path string   `json:"path"`
		Tool string   `json:"tool,omitempty"`
		Args []string `json:"args,omitempty"`
	}
	runs := []map[string]any{}
	for repeat := 1; repeat <= 2; repeat++ {
		for _, scenario := range []struct{ name, task, entry string }{
			{"accounts", "查看 GitLab 账号 user202（实例 https://gitlab.invalid）的工具帮助。只运行该账号入口的 --help；不要查询其他账号，不执行业务操作。", b.ToolEntryID},
			{"local", "查看本地构建结果读取工具（Local build result reader）的帮助。只运行该入口的 --help；不要配置平台账号、Workspace 或读取 Skill。", "local"},
		} {
			for _, arm := range []string{"before", "after"} {
				t.Run(fmt.Sprintf("%d/%s/%s", repeat, scenario.name, arm), func(t *testing.T) {
					var mu sync.Mutex
					calls := []call{}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						data, _ := io.ReadAll(io.LimitReader(r.Body, 128<<10))
						line, _, _ := bytes.Cut(data, []byte{'\n'})
						var req toolsRequest
						_ = json.Unmarshal(line, &req)
						mu.Lock()
						calls = append(calls, call{r.URL.Path, req.Tool, req.Args})
						mu.Unlock()
						if r.URL.Path != "/v1/tools/list" && (r.URL.Path != "/v1/tools/run" || req.Tool != scenario.entry || !reflect.DeepEqual(req.Args, []string{"--help"})) {
							http.Error(w, "Only discovery and the requested exact entry's --help are allowed", http.StatusForbidden)
							return
						}
						r.Body = io.NopCloser(bytes.NewReader(data))
						if r.URL.Path == "/v1/tools/list" && arm == "before" {
							recorder := httptest.NewRecorder()
							m.serveTools(recorder, r)
							if recorder.Code != http.StatusOK {
								w.WriteHeader(recorder.Code)
								_, _ = w.Write(recorder.Body.Bytes())
								return
							}
							w.Header().Set("Content-Type", "application/json")
							_, _ = w.Write(baseline)
							return
						}
						m.serveTools(w, r)
					}))
					defer server.Close()
					env := cliToolEnv(server.URL, svc, members[0])
					toolEnv := map[string]string{"TEAMX_CLI": binary}
					for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
						toolEnv[key] = env(key)
					}
					dir := filepath.Join(out, fmt.Sprintf("%d-%s-%s", repeat, scenario.name, arm))
					workspace := filepath.Join(dir, "workspace")
					if err := os.MkdirAll(workspace, 0700); err != nil {
						t.Fatal(err)
					}
					ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
					defer cancel()
					session, err := dsh.Open(ctx, dsh.Options{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: args,
						Workspace: workspace, StateDir: filepath.Join(dir, "runtime"), Model: model.materialize(), Credential: credential, ToolEnv: toolEnv,
						Instructions: `You are a bounded read-only TeamX discovery assistant. Use the configured "$TEAMX_CLI" only. Discover bound entries with tools list once, then use the exact matching entry with tools run ENTRY --help once. Account identity, readiness and capabilities come from the directory. No detail query is needed. Report the returned help marker privately. Do not inspect files/environment/credentials, use other commands, query platforms, load Skills, configure anything, delegate or make external requests.`, Limits: dsh.Limits{TurnTimeout: 90 * time.Second}})
					if session != nil {
						defer func() {
							closeCtx, closeCancel := context.WithTimeout(context.Background(), 20*time.Second)
							defer closeCancel()
							if err := session.Close(closeCtx); err != nil {
								t.Error(err)
							}
						}()
					}
					if err != nil {
						runs = append(runs, map[string]any{"repeat": repeat, "scenario": scenario.name, "arm": arm, "passed": false, "phase": "open", "error": err.Error()})
						report["runs"] = runs
						t.Fatal(err)
					}
					result, runErr := session.Run(ctx, scenario.task)
					mu.Lock()
					observed := append([]call{}, calls...)
					mu.Unlock()
					passed := runErr == nil && len(observed) == 2 && observed[0].Path == "/v1/tools/list" && observed[1].Path == "/v1/tools/run" && observed[1].Tool == scenario.entry && strings.Contains(result.Text, "DISCOVERY_HELP_OK")
					row := map[string]any{"repeat": repeat, "scenario": scenario.name, "arm": arm, "passed": passed, "calls": observed, "result": result}
					if runErr != nil {
						row["error"] = runErr.Error()
					}
					runs = append(runs, row)
					report["runs"] = runs
					if !passed {
						t.Error("discovery did not reach the exact requested help in two calls", runErr, observed)
					}
				})
			}
		}
	}
	if !t.Failed() {
		report["status"] = "passed"
	}
}
