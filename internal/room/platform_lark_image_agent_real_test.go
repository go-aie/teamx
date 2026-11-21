//go:build e2e && darwin

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// A real installed DSH/model drives normal managed members, Inbox/History,
// shipped Skill, tools run, native subprocess fixtures and durable actions.
// There is no real Lark profile, credential or remote business write here.
func TestLarkImagesRealAgent(t *testing.T) {
	if os.Getenv("TEAMX_LARK_IMAGE_AGENT_REAL") != "1" {
		t.Skip("opt-in real model; local Lark fixture only")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	if selected := os.Getenv("TEAMX_LARK_IMAGE_AGENT_MODEL_ENTRY"); selected != "" {
		var err error
		entry, err = cfg.Models.resolve(modelSelection{Mode: "explicit", EntryID: selected})
		if err != nil {
			t.Fatal(err)
		}
		credentials, err := newModelCredentials(filepath.Dir(os.Getenv("TEAMX_INPUT_COMPARISON_CONFIG")))
		if err != nil {
			t.Fatal(err)
		}
		credential, err = credentials.get(entry.CredentialRef)
		if err != nil {
			t.Fatal(err)
		}
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "remote_business_writes": 0, "platform": "local CLI subprocess fixture", "model": entry.materialize().ModelID, "image_recognition": false, "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, out, report)
	const key = "TEAMX_IMAGE_AGENT_MODEL_KEY"
	t.Setenv(key, credential.Value)
	runtime := *cfg.Runtime
	runtime.Model = entry.materialize()
	runtime.Model.APIKeyEnv = key
	runtime.Model.InputModalities = []string{"text"}
	binary := filepath.Join(out, "teamx")
	if b, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatal(err, string(b))
	}
	rows := []map[string]any{}
	defer func() { report["samples"] = rows }()
	for _, mode := range []string{"managed", "default-assistant"} {
		t.Run(mode, func(t *testing.T) {
			if only := os.Getenv("TEAMX_LARK_IMAGE_AGENT_MODE"); only != "" && only != mode {
				t.Skip("selected acceptance mode")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 22*time.Minute)
			defer cancel()
			audit := &policyRealAudit{Calls: map[string]int{}}
			m := directTestManager(t, filepath.Join(out, mode), &runtime)
			m.maxTurns = 30
			m.config.unifiedInputs = true
			server := browserTestServer(t, m, audit.serve(m))
			defer server.Close()
			m.config.endpoint, m.config.cliPath = server.URL, binary
			m.factory = func(dir, id string, config serviceConfig) (*service, error) {
				config.dshTurnTimeout = 3 * time.Minute
				return startService(dir, id, config, func(s *service, state RoomSnapshot) error {
					observePolicyReal(s, audit)
					if err := s.loadMembers(state); err != nil {
						return err
					}
					return s.attachMembers(state)
				})
			}
			var room roomSummary
			if mode == "default-assistant" {
				ensureAssistant(t, m)
				room = assistantSummary(t, m)
			} else {
				team := testTeamDefinition(t)
				team.Runtime = &runtime
				team.Members[0].Instructions = assistantInstructions
				m.teams = map[string]teamDefinition{"sample": team}
				room = createTeamRoom(t, m, "image-agent")
			}
			svc := eventLiveService(t, &eventHarness{m: m, room: room})
			_, snapshot := readManagedRoom(t, m, room)
			h := configureSelfSendFixture(t, newLarkHarnessForRoom(t, m, room, svc, snapshot.Members))
			h, id, fixtureServer, _, refs := configureImageSendHarness(t, h)
			fixtureServer.Close()
			// The native CLI fixture's minimal catalog has no embedded Skill.
			// Real Agent acceptance must use the shipped definition and content.
			m.toolCatalog = nil
			definition, err := m.toolDefinition("lark")
			if err != nil {
				t.Fatal(err)
			}
			guide, err := entrySkill(definition, h.c.ToolEntryID)
			if err != nil || !strings.Contains(string(guide), "Send selected Room images as one message") {
				t.Fatal("shipped Lark Skill not assembled", err)
			}
			// Binding setup's fixed test turn is not a model run. Restore the fresh
			// member snapshot before letting the ordinary scheduler own its turns.
			if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id}); err != nil {
				t.Fatal(err)
			}
			if err := svc.store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: false}); err != nil {
				t.Fatal(err)
			}
			closeService(t, svc)
			m.mu.Lock()
			delete(m.runtimes, room.RoomIdentity)
			m.mu.Unlock()
			svc = openRoomOK(t, m, room, "image-agent-open")
			h.svc = svc
			path := filepath.Join(h.dir, "lark-cli")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			search := `if a[:2]==['im','+chat-search']:
 print(json.dumps({'ok':True,'identity':'user','data':{'chats':[{'chat_id':'oc_group','chat_mode':'group','name':'准确测试群'},{'chat_id':'oc_other','chat_mode':'group','name':'准确测试群'}],'has_more':False}}));sys.exit()
`
			if err = os.WriteFile(path, []byte(strings.Replace(string(b), "path=a[2]", search+"path=a[2]", 1)), 0700); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(h.dir, "directory.json"), []byte(`{"chats":[{"chat_id":"oc_group","chat_mode":"group","name":"准确测试群"},{"chat_id":"oc_other","chat_mode":"group","name":"准确测试群"}],"has_more":false,"page_token":""}`), 0600); err != nil {
				t.Fatal(err)
			}
			row := map[string]any{"mode": mode, "member": id, "status": "failed", "scenarios": []string{}}
			rows = append(rows, row)
			defer func() {
				audit.mu.Lock()
				row["audit"] = audit
				audit.mu.Unlock()
				row["posts"] = selfSendPosts(t, h.dir)
			}()
			turn := uint64(0)
			costs := []map[string]any{}
			defer func() { row["costs"] = costs }()
			expectedActions := 0
			run := func(name, text string) {
				t.Helper()
				turn++
				audit.mu.Lock()
				startAudit := len(audit.Turns)
				audit.mu.Unlock()
				posted, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: name, Text: text})
				if err != nil {
					t.Fatal(err)
				}
				if turn == 1 {
					if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
						t.Fatal(err)
					}
				}
				status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
					// A settled restart may briefly pause during its existing backend
					// recovery. Wait for this task's turn, or an actual blocking error.
					if name == "restart-receipts" {
						return !s.Active && s.Agent.TurnNumber >= turn
					}
					return !s.Active && (s.Agent.TurnNumber >= turn || s.Paused && s.Error != "")
				})
				if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
					t.Fatal("real Agent turn failed", name, status.Error)
				}
				saved, err := svc.store.LoadRoom()
				if err != nil {
					t.Fatal(err)
				}
				replied := false
				for _, message := range saved.Messages {
					if message.Seq > posted.Seq && message.Author.Kind == "agent" && message.Author.ID == id.AgentID && strings.TrimSpace(message.Text) != "" {
						replied = true
					}
				}
				if !replied {
					t.Fatal("Agent did not publish the result or clarification to the Room", name)
				}
				audit.mu.Lock()
				cost := imageAgentCallCosts(audit.Turns[startAudit:])
				audit.mu.Unlock()
				cost["scenario"] = name
				costs = append(costs, cost)
				t.Logf("%s: %v", name, cost)
				if cost["format_errors"].(int) != 0 {
					t.Fatal("normal path required format repair", name, cost)
				}
				if name == "last-image" && cost["business_calls"].(int) > 2 {
					t.Fatal("self exceeded resolve + execute", cost)
				}
				if name == "ambiguous-group" && cost["business_calls"].(int) != 1 {
					t.Fatal("group query cost", cost)
				}
				if name == "choose-group" && cost["business_calls"].(int) != 2 {
					t.Fatal("group exceeded selection verification + execute", cost)
				}
				// Check actual calls, including failure handling; the model's own
				// statement that it avoided native fallback is not evidence.
				audit.mu.Lock()
				calls := append([]policyRealTurn(nil), audit.Turns...)
				audit.mu.Unlock()
				for _, observed := range calls {
					for _, call := range observed.Result.Tools {
						if call.Status != "completed" {
							continue
						}
						if strings.HasPrefix(call.Title, "mcp__teamx__") {
							continue
						}
						if imageAgentStdinFilter(call) {
							continue
						}
						var bash struct {
							Command string `json:"command"`
						}
						if call.Title != "bash" || json.Unmarshal([]byte(call.Input), &bash) != nil || !strings.Contains(bash.Command, "$TEAMX_CLI") || imageAgentForbiddenCommand.MatchString(bash.Command) {
							t.Fatal("Agent used a native fallback or unsupported tool", call.Title)
						}
					}
				}
				row["scenarios"] = append(row["scenarios"].([]string), name)
			}
			actions := func() []referenceAction {
				t.Helper()
				a, err := svc.store.queryActions(id, "", "", "")
				if err != nil {
					t.Fatal(err)
				}
				return a
			}
			defer func() { row["actions"] = actions() }()
			check := func(actionID, status, body string, selected []lark.ImageRef, posts int) {
				t.Helper()
				expectedActions++
				if len(actions()) != expectedActions {
					t.Fatal("Agent split or replaced a complete action", actionID, len(actions()), expectedActions)
				}
				records, err := svc.store.queryActions(id, actionID, "", "")
				if err != nil || len(records) != 1 {
					t.Fatal("missing exact action", actionID, err)
				}
				a := records[0]
				var input lark.ImageSendInput
				if json.Unmarshal(actionInputJSON(a.Request.Input), &input) != nil || a.Status != status || a.Attempts != 1 || input.Body != body || !reflect.DeepEqual(input.Images, selected) || len(selfSendPosts(t, h.dir)) != posts {
					t.Fatalf("unexpected complete action %s: status=%s input=%s posts=%d", actionID, a.Status, actionInputJSON(a.Request.Input), len(selfSendPosts(t, h.dir)))
				}
				if status == "succeeded" && (a.Result == nil || a.Result.ResultID.Value != "om_receipt") {
					t.Fatal("remote receipt missing")
				}
				assertImageTempCleaned(t, h)
			}
			run("last-image", "把上面最后一张图原样保存到我的飞书，不带说明。稳定动作 ID 为 agent-last-image。已有能力直接复用，不要修改配置。这个隔离任务只用已提供的 TeamX inbox/history 读取工具和 TeamX CLI；不调用 MCP resources、其他发现工具、原生平台 CLI、网络、文件或环境检查。")
			check("agent-last-image", "succeeded", "", refs[3:], 1)
			run("history-two", "用 History 核对我最早发的两张图，把这两张原图一起保存到我的飞书，不带说明，只发一条。稳定动作 ID 为 agent-history-two。")
			check("agent-history-two", "succeeded", "", refs[:2], 2)
			run("second-only", "只把我最早两张图片中的第二张保存到我的飞书，不带说明，只发一条。稳定动作 ID 为 agent-second-only。")
			check("agent-second-only", "succeeded", "", refs[1:2], 3)
			before := len(actions())
			run("ambiguous-group", "把最早两张图和说明「现场说明」一起发到准确测试群，发成一条；先按群名查找，重名时问我。稳定动作 ID 为 agent-group-caption。")
			if len(actions()) != before || len(selfSendPosts(t, h.dir)) != 3 {
				t.Fatal("ambiguous group caused a send")
			}
			run("choose-group", "选刚才返回的 oc_group 群。现在把最早两张原图和准确说明「现场说明」作为一条消息发到这个群。仍使用 agent-group-caption。")
			check("agent-group-caption", "succeeded", "现场说明", refs[:2], 4)
			a := actions()
			foundGroup := false
			for _, a := range a {
				if a.Request.ID == "agent-group-caption" {
					foundGroup = a.Target.Object.ID.Value == "oc_group" && a.Basis != nil && a.CapabilityBasis == nil
				}
			}
			if !foundGroup {
				t.Fatal("group target/authority mismatch")
			}
			run("self-text", "新任务，把准确文字「本人文字样本」保存到我的飞书，只发一条。稳定动作 ID 为 agent-self-text。")
			check("agent-self-text", "succeeded", "本人文字样本", nil, 5)
			run("self-caption", "新任务，把最后一张原图和准确说明「本人图文样本」一起保存到我的飞书，只发一条。稳定动作 ID 为 agent-self-caption。")
			check("agent-self-caption", "succeeded", "本人图文样本", refs[3:], 6)
			run("group-text", "新任务，向刚才选定的准确测试群发送准确文字「群文字样本」，只发一条。稳定动作 ID 为 agent-group-text。")
			check("agent-group-text", "succeeded", "群文字样本", nil, 7)
			run("group-single", "新任务，把最后一张原图发到刚才选定的准确测试群，不带说明，只发一条。稳定动作 ID 为 agent-group-single。")
			check("agent-group-single", "succeeded", "", refs[3:], 8)
			run("group-multiple", "新任务，把最早两张原图一起发到刚才选定的准确测试群，不带说明，只发一条。稳定动作 ID 为 agent-group-multiple。")
			check("agent-group-multiple", "succeeded", "", refs[:2], 9)
			before = len(actions())
			run("ambiguous-images", "我发过两组图片，把那组图保存到我的飞书。具体哪组我还没说清，请先问我，不要发送。")
			if len(actions()) != before || len(selfSendPosts(t, h.dir)) != 9 {
				t.Fatal("ambiguous source caused a send")
			}
			// Keep the listening endpoint: it is part of the frozen MCP identity.
			previousEpoch := svc.info.ServiceEpoch
			closeService(t, svc)
			m.mu.Lock()
			delete(m.runtimes, room.RoomIdentity)
			m.mu.Unlock()
			svc = openRoomOK(t, m, room, "image-agent-reopen")
			h.svc = svc
			if svc.info.ServiceEpoch == previousEpoch {
				t.Fatal("service epoch reused")
			}
			run("restart-receipts", "服务已经重启。只用 actions read/list 核对 agent-last-image、agent-group-caption 的原回执，核对准确目标与完整图文。不要 execute、换 ID 或发送任何新消息。")
			if len(actions()) != before || len(selfSendPosts(t, h.dir)) != 9 {
				t.Fatal("Agent resent after restart")
			}
			row["service_restart"] = true
			if err = os.WriteFile(filepath.Join(h.dir, "fail-2"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			run("upload-failure", "另起一个独立保存任务：将最后两张原图和说明「失败样本」一起保存到我的飞书，只发一条。稳定动作 ID 为 agent-upload-failure。任何错误均停止，不改发文字或部分图片。")
			check("agent-upload-failure", "not_applied", "失败样本", refs[2:], 9)
			if err = os.Remove(filepath.Join(h.dir, "fail-2")); err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(h.dir, "pause-2"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			cancelled := make(chan error, 1)
			go func() {
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						cancelled <- ctx.Err()
						return
					case <-ticker.C:
						if _, err := os.Stat(filepath.Join(h.dir, "upload-ready")); err != nil {
							continue
						}
						m.mu.Lock()
						m.cancelPlatformCallsLocked(func(call *platformCall) bool { return call.owner == id.RoomIdentity && call.agentID == id.AgentID })
						m.mu.Unlock()
						cancelled <- os.WriteFile(filepath.Join(h.dir, "release"), nil, 0600)
						return
					}
				}
			}()
			run("cancel-upload", "另起一个独立任务：将最后两张原图和说明「取消样本」一起保存到我的飞书，只发一条。稳定动作 ID 为 agent-cancel。任何取消或错误均停止，不重试或改发。")
			if err = <-cancelled; err != nil {
				t.Fatal(err)
			}
			check("agent-cancel", "not_applied", "取消样本", refs[2:], 9)
			for _, name := range []string{"pause-2", "upload-ready", "release"} {
				if err = os.Remove(filepath.Join(h.dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.WriteFile(filepath.Join(h.dir, "unknown"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			run("unknown", "另起一个独立保存任务：将最后两张原图和说明「未知样本」一起保存到我的飞书，只发一条。稳定动作 ID 为 agent-unknown。任何错误均停止，不补发。")
			check("agent-unknown", "unknown", "未知样本", refs[2:], 10)
			before = len(actions())
			run("recover-unknown", "只查询 agent-unknown 的原回执，报告是否已核实和是否有远端 ID，不要执行、换 ID 或补发。")
			if len(actions()) != before || len(selfSendPosts(t, h.dir)) != 10 {
				t.Fatal("unknown recovery resent")
			}
			// Resource permission repair must not become a fallback text action.
			b, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(path, []byte(strings.Replace(string(b), ",'im:resource']", "]", 1)), 0700); err != nil {
				t.Fatal(err)
			}
			run("missing-permission", "新的保存任务：把最后两张原图和说明「权限样本」一起保存到我的飞书，稳定动作 ID 为 agent-permission。若缺图片权限，告诉我准确修复入口后停止，暂不授权或改配置，也不要只发文字或部分图片。")
			if len(actions()) != before || len(selfSendPosts(t, h.dir)) != 10 {
				t.Fatal("missing image permission caused a fallback action")
			}
			// Existing lifecycle policy blocks a new Session while an unknown action
			// needs inspection. Receipt queries remain available without a model;
			// never erase the unknown or rotate around platform_settlement for a test.
			closeService(t, svc)
			m.mu.Lock()
			delete(m.runtimes, room.RoomIdentity)
			m.mu.Unlock()
			svc = openRoomOK(t, m, room, "image-agent-unknown-reopen")
			h.svc = svc
			blocked := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return s.Rotation != nil && s.Rotation.FailedStep == "platform_settlement" })
			if blocked.Active || blocked.Rotation.Phase == "completed" {
				t.Fatal("unknown bypassed Session settlement")
			}
			var original actionQueryRecord
			token, revoke, err := m.config.access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
			if err != nil {
				t.Fatal(err)
			}
			defer revoke()
			query := exec.CommandContext(ctx, binary, "actions", "read", "--id", "agent-unknown")
			query.Env = append(svc.toolEnv(id), "TEAMX_EXECUTION_TOKEN="+token)
			output, err := query.CombinedOutput()
			if err != nil || json.Unmarshal(output, &original) != nil || original.Status != "unknown" || original.Detail == nil || original.Detail.Attempts != 1 {
				t.Fatal("unknown receipt unavailable while Session blocked", err, string(output))
			}
			if len(actions()) != before || len(selfSendPosts(t, h.dir)) != 10 {
				t.Fatal("unknown Session restart resent")
			}
			row["unknown_restart_blocked"] = true
			audit.mu.Lock()
			reads := audit.Calls["/v1/actions/read"] + audit.Calls["/v1/actions/list"]
			history := 0
			for _, turn := range audit.Turns {
				for _, call := range turn.Result.Tools {
					if call.Status == "completed" && strings.Contains(call.Title, "history_read") {
						history++
					}
				}
			}
			audit.mu.Unlock()
			row["history_reads"], row["receipt_reads"] = history, reads
			if reads == 0 || history == 0 {
				t.Fatal("Agent did not query original receipts or retained History", reads, history)
			}
			row["actions"] = actions()
			row["status"] = "passed"
			fmt.Println("real image Agent acceptance passed:", mode)
		})
	}
	if !t.Failed() {
		report["status"] = "passed"
	}
}

// Formatting the CLI's JSON on stdin is allowed. Reject native platform/network
// fallback and file/environment inspection, rather than banning a formatter's
// interpreter (which does not itself imply any of those operations).
var imageAgentForbiddenCommand = regexp.MustCompile(`\b(lark-cli|curl|wget|printenv|find|rg)\b|\b(open|readFileSync|readFile)\s*\(|os\.environ|process\.env`)

func imageAgentStdinFilter(call dsh.ToolUpdate) bool {
	var input struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	return call.Title == "grep" && strictToolJSON([]byte(call.Input), &input) == nil && input.Path == "/dev/stdin"
}

// Count unique actual tool calls (including errors), never just successful CLI
// business requests. Discovery, History, reports and recovery remain separate.
func imageAgentCallCosts(turns []policyRealTurn) map[string]any {
	calls := map[string]dsh.ToolUpdate{}
	for _, turn := range turns {
		for _, call := range turn.Result.Tools {
			calls[call.ID] = call
		}
	}
	counts := map[string]any{"tool_calls": len(calls), "business_calls": 0, "help_queries": 0, "format_errors": 0, "history_reads": 0, "discovery_calls": 0, "local_reports": 0, "receipt_reads": 0, "business_output_bytes": 0}
	business := regexp.MustCompile(`\bactions\s+(resolve|execute)\b`)
	for _, call := range calls {
		var in struct {
			Command string `json:"command"`
			Cmd     string `json:"cmd"`
		}
		_ = json.Unmarshal([]byte(call.Input), &in)
		cmd := in.Command + in.Cmd
		inc := func(k string) { counts[k] = counts[k].(int) + 1 }
		help := strings.Contains(cmd, "--help") || strings.Contains(cmd, " -h")
		if help {
			inc("help_queries")
		}
		if business.MatchString(cmd) && !help {
			counts["business_calls"] = counts["business_calls"].(int) + len(business.FindAllString(cmd, -1))
			counts["business_output_bytes"] = counts["business_output_bytes"].(int) + len(call.Output)
		}
		if strings.Contains(call.Title, "history_read") {
			inc("history_reads")
		}
		if strings.Contains(cmd, "tools list") || strings.Contains(cmd, "tools skill") {
			inc("discovery_calls")
		}
		if strings.Contains(cmd, " send ") {
			inc("local_reports")
		}
		if strings.Contains(cmd, "actions read") || strings.Contains(cmd, "actions list") {
			inc("receipt_reads")
		}
		for _, bad := range []string{"unknown JSON field", "invalid request:", "flag provided but not defined", "usage: teamx tools"} {
			if strings.Contains(call.Output, bad) {
				inc("format_errors")
				break
			}
		}
	}
	return counts
}
