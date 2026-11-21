//go:build e2e && darwin

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Real DSH, shipped instructions, ordinary scheduler, CLI and durable actions.
// Only the GitLab HTTP destination is a local TLS fixture. No real forge writes.
func TestGitlabImagesRealAgent(t *testing.T) {
	testGitlabImagesRealAgent(t, false)
}

func TestGitlabActionURLRealAgent(t *testing.T) {
	testGitlabImagesRealAgent(t, true)
}

func testGitlabImagesRealAgent(t *testing.T, urlMode bool) {
	if os.Getenv("TEAMX_GITLAB_IMAGE_AGENT_REAL") != "1" {
		t.Skip("opt-in real model; local GitLab HTTP fixture only")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	out := imageRealOutput(t)
	guide, err := tool.ReadToolSkill(tool.Definition{Provider: "gitlab", Skill: "skills/gitlab/SKILL.md"}, "managed-send")
	if err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"managed_send_skill_utf8_bytes": len(guide), "resolve_help_utf8_bytes": len(referenceHelpText("actions", "resolve")), "execute_help_utf8_bytes": len(referenceHelpText("actions", "execute")), "status": "failed", "remote_business_writes": 0, "platform": "local HTTPS fixture", "model": entry.materialize().ModelID, "recorded_at": time.Now().UTC(), "old_image_baseline": nil}
	report["url_locator"] = urlMode
	defer imageRealReport(t, out, report)
	const key = "TEAMX_GITLAB_IMAGE_AGENT_MODEL_KEY"
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
			if only := os.Getenv("TEAMX_GITLAB_IMAGE_AGENT_MODE"); only != "" && only != mode {
				t.Skip("selected mode")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 25*time.Minute)
			defer cancel()
			audit := &policyRealAudit{Calls: map[string]int{}}
			m := directTestManager(t, filepath.Join(out, mode), &runtime)
			m.maxTurns, m.config.unifiedInputs = 30, true
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
				room = createTeamRoom(t, m, "gitlab-image-agent")
			}
			svc := eventLiveService(t, &eventHarness{m: m, room: room})
			_, snapshot := readManagedRoom(t, m, room)
			connection := verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
			bindCLITool(t, m, room.RoomIdentity, snapshot.Members[0], connection, "gitlab", 0)
			member := snapshot.Members[0]
			id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
			if err := svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id}); err != nil {
				t.Fatal(err)
			}
			if err := svc.store.initReferenceFollowups(id); err != nil {
				t.Fatal(err)
			}
			m.bindPlatformReferences(svc)
			base := &managedActionFixture{m: m, room: room, svc: svc, connection: connection, member: member, id: id, req: apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id), TurnNumber: 1}}
			managedReferenceHTTP(t, m, base.serve)
			targets := []string{"", ""}
			if !urlMode {
				// Seed existing native targets, verified through the admitted read
				// capability. This is fixture setup, not an Agent locator API.
				session, err := m.prepareGitlabReferenceAction(ctx, id, platform.AccessContext{Receiver: platform.AccountIdentity{ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "101"}}})
				if err != nil {
					t.Fatal(err)
				}
				defer session.Close()
				for i, thread := range []bool{false, true} {
					target := platform.TargetIdentity{IdentityScope: session.Access.Account.IdentityScope, Containers: []platform.NativeIdentity{{Type: "Project", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "123"}}}, Object: platform.NativeIdentity{Type: "MergeRequest", ID: platform.ExternalID{Namespace: "gitlab:rest:id", Value: "9001"}}}
					if thread {
						target.Discussion = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "thread"}
					}
					alias := target
					alias.Object.ID = platform.ExternalID{Namespace: "gitlab:rest:iid", Value: "42"}
					resolution, e := session.Capabilities.Resolve(ctx, platform.TargetLocator{Candidate: &target, VerifiedAliases: []platform.TargetIdentity{alias}}, session.Access)
					if e != nil {
						t.Fatal(e)
					}
					ref, e := svc.store.saveActionTarget(id, resolution, session.Access)
					if e != nil {
						t.Fatal(e)
					}
					targets[i] = ref
				}
				session.Close()
			}
			policy, err := svc.store.readAgentPolicy(id)
			if err != nil {
				t.Fatal(err)
			}
			base.action = referenceActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: targets[1], ID: "fixture-bootstrap", Operation: platform.OperationDiscussionReply, PolicyToken: policy.PolicyToken}
			f := configureGitlabImageFixture(t, base)
			if urlMode {
				installGitlabURLFixtureReads(t, m)
			}
			f.server.Close()
			objectRef, threadRef := targets[0], targets[1]
			if err = svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id}); err != nil {
				t.Fatal(err)
			}
			if err = svc.store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: id, Enabled: false}); err != nil {
				t.Fatal(err)
			}
			closeService(t, svc)
			m.mu.Lock()
			delete(m.runtimes, room.RoomIdentity)
			m.mu.Unlock()
			m.config.unifiedInputs = true
			svc = openRoomOK(t, m, room, "gitlab-image-open")
			f.svc = svc
			row := map[string]any{"mode": mode, "status": "failed", "member": id, "source_publications": 4, "target_setup": "two existing native refs verified by admitted resolver; two saved target contracts before Agent turns, no Inbox/matter fabrication"}
			if urlMode {
				row["target_setup"] = "no target refs, native IDs, notifications, matters or Workspace; only account binding and ordinary human images"
			}
			rows = append(rows, row)
			costs := []map[string]any{}
			defer func() {
				row["costs"] = costs
				row["audit"] = audit
				a, _ := svc.store.queryActions(id, "", "", "")
				row["actions"] = a
				row["note_posts"] = f.posts.Load()
			}()
			turn := uint64(0)
			run := func(name, text string, maxBusiness int) {
				t.Helper()
				turn++
				audit.mu.Lock()
				start := len(audit.Turns)
				audit.mu.Unlock()
				posted, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: name, Text: text})
				if e != nil {
					t.Fatal(e)
				}
				if turn == 1 {
					if _, e = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); e != nil {
						t.Fatal(e)
					}
				}
				status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
					return !s.Active && (s.Agent.TurnNumber >= turn || s.Paused && s.Error != "")
				})
				if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
					t.Fatal("real Agent turn failed", name, status.Error)
				}
				saved, e := svc.store.LoadRoom()
				if e != nil {
					t.Fatal(e)
				}
				replied := false
				for _, msg := range saved.Messages {
					if msg.Seq > posted.Seq && msg.Author.Kind == "agent" && msg.Author.ID == id.AgentID && strings.TrimSpace(msg.Text) != "" {
						replied = true
					}
				}
				if !replied {
					t.Fatal("Agent did not publish result/clarification", name)
				}
				audit.mu.Lock()
				observed := append([]policyRealTurn(nil), audit.Turns[start:]...)
				audit.mu.Unlock()
				observed = gitlabImageFullToolTrace(t, svc, id, observed)
				cost := gitlabImageAgentCosts(t, observed)
				for _, tr := range observed {
					for _, call := range tr.Result.Tools {
						if strings.Contains(call.Output, "request: input.") || strings.Contains(call.Output, "command not found") {
							cost["format_errors"] = cost["format_errors"].(int) + 1
						}
					}
				}
				cost["scenario"] = name
				fullTools := []dsh.ToolUpdate{}
				for _, observedTurn := range observed {
					fullTools = append(fullTools, observedTurn.Result.Tools...)
				}
				cost["full_tools"] = fullTools
				costs = append(costs, cost)
				if name == "url-comment" && (cost["managed_send_skill_reads"].(int) != 1 || cost["complete_skill_reads"].(int) != 0) {
					t.Fatal("new URL send did not directly read its topic once", cost["managed_send_skill_reads"], cost["complete_skill_reads"])
				}
				logCost := map[string]any{}
				for k, v := range cost {
					if k != "full_tools" {
						logCost[k] = v
					}
				}
				t.Log(name, logCost)
				if cost["format_errors"].(int) != 0 || cost["business_calls"].(int) > maxBusiness {
					t.Fatal("normal path required format repair or repeated discovery", name, logCost)
				}

			}
			expected := 0
			checkedURLActions := map[string]bool{}
			check := func(name, status, body string, refs []gitlab.ImageRef, op platform.Operation) {
				t.Helper()
				expected++
				a, e := svc.store.queryActions(id, "", "", "")
				if e != nil || len(a) != expected {
					t.Fatal("split, missing or replaced action", name, len(a), expected, e)
				}
				if urlMode {
					for _, candidate := range a {
						if !checkedURLActions[candidate.Request.ID] {
							checkedURLActions[candidate.Request.ID] = true
							costs[len(costs)-1]["action_id"] = candidate.Request.ID
							a = []referenceAction{candidate}
							break
						}
					}
				} else {
					a, e = svc.store.queryActions(id, "agent-"+name, "", "")
				}
				if e != nil || len(a) != 1 {
					t.Fatal("missing stable action", name, e)
				}
				input, e := gitlab.DecodeImageActionInput(actionInputJSON(a[0].Request.Input))
				if e != nil || a[0].Status != status || a[0].Attempts != 1 || input.Body != body || !reflect.DeepEqual(input.Images, refs) || a[0].Request.Operation != op {
					t.Fatalf("wrong complete action %s: %s %s", name, a[0].Status, actionInputJSON(a[0].Request.Input))
				}
				if len(refs) > 0 && (!a[0].Spec.DirectRoomInstructionOnly || a[0].Spec.Retry.MaxAttempts != 1 || a[0].Spec.Retry.RetryUnknown) {
					t.Fatal("image strategy lost")
				}
				if status == "succeeded" && (a[0].Result == nil || a[0].Result.ResultID.Value != fmt.Sprint(98+f.posts.Load())) {
					t.Fatal("note receipt missing")
				}
				assertActionCallsSettled(t, m)
				f.requests.Wait()
				assertActionImagePinsReleased(t, svc.store)
			}
			send := func(name, text, body string, refs []gitlab.ImageRef, op platform.Operation, max int, status string) {
				t.Helper()
				f.action.Operation = op
				f.setInput(t, body, refs)
				f.uploads.Store(0)
				before := f.posts.Load()
				if !urlMode {
					text += " 稳定动作 ID 为 agent-" + name + "。"
				}
				run(name, text, max)
				check(name, status, body, refs, op)
				want := int32(1)
				if status == "not_applied" {
					want = 0
				}
				if f.posts.Load() != before+want {
					t.Fatal("note POST count", name)
				}
			}
			if urlMode {
				send("url-comment", "把我上面最后一张原图评论到 "+gitlabLocatorWeb+"，不带说明。这是隔离验收，仅使用 TeamX Inbox/History 和 TeamX CLI；不要检查宿主文件或环境，不使用原生平台命令或网络，不修改配置。", "", f.refs[3:], platform.OperationObjectComment, 2, "succeeded")
				send("url-reply", "把最早两张图中的第二张原图回复到 "+gitlabLocatorWeb+"#note_99，不带说明。", "", f.refs[1:2], platform.OperationDiscussionReply, 2, "succeeded")
				if f.posts.Load() != 2 {
					t.Fatal("wrong bounded post count", f.posts.Load())
				}
				send("url-comment-text", "将准确文字「文字样本」作为一条普通评论发到刚才的 MR。", "文字样本", nil, platform.OperationObjectComment, 1, "succeeded")
				send("url-reply-caption", "把最后两张原图和准确说明「现场说明\n修复效果」作为一条回复发到刚才明确的讨论，不要发成普通评论。", "现场说明\n修复效果", f.refs[2:], platform.OperationDiscussionReply, 1, "succeeded")
				f.mode = "read-failure"
				send("url-unknown", "新任务，把最后两张原图和准确说明「未知样本」回复到刚才的准确讨论；遇错误停止，不补发。", "未知样本", f.refs[2:], platform.OperationDiscussionReply, 1, "unknown")
				originalID := costs[len(costs)-1]["action_id"].(string)
				posts := f.posts.Load()
				run("url-recover-unknown", "只查询 "+originalID+" 原回执，报告已知 note ID、准确线程和哪些内容尚未核实，不执行、不换 ID 或补发。", 0)
				if costs[len(costs)-1]["receipt_reads"].(int) == 0 || f.posts.Load() != posts {
					t.Fatal("unknown recovery did not read original receipt or resent")
				}
				row["status"] = "passed"
				return
			}
			send("comment-single", fmt.Sprintf("把我上面最后一张原图评论到 g/p 的 MR !42，不带说明。已有 MR 托管引用为 %s，准确工具入口为 %s。这是隔离验收，仅用已提供的 TeamX inbox/history 工具和 TeamX CLI；不要用原生平台 CLI、网络、文件或环境检查，不修改配置。", objectRef, connection.ToolEntryID), "", f.refs[3:], platform.OperationObjectComment, 2, "succeeded")
			send("thread-caption", fmt.Sprintf("把最早两张原图和准确说明「现场说明\n修复效果」作为一条回复发到 MR !42 的这条讨论，讨论托管引用是 %s，不要发成普通评论。", threadRef), "现场说明\n修复效果", f.refs[:2], platform.OperationDiscussionReply, 2, "succeeded")
			send("comment-multiple", "用 History 核对我最早发的两张图，将它们作为一条普通评论发到刚才的 MR，不带说明。", "", f.refs[:2], platform.OperationObjectComment, 1, "succeeded")
			send("thread-second", "只把最早两张图中的第二张原图回复到刚才明确的讨论，不带说明。", "", f.refs[1:2], platform.OperationDiscussionReply, 1, "succeeded")
			send("comment-caption", "将最后两张原图和准确说明「问题截图与修复效果」作为一条普通评论发到刚才的 MR。", "问题截图与修复效果", f.refs[2:], platform.OperationObjectComment, 1, "succeeded")
			send("thread-multiple", "把最早两张原图作为一条回复发到刚才明确的讨论，不带说明。", "", f.refs[:2], platform.OperationDiscussionReply, 1, "succeeded")
			for _, op := range []platform.Operation{platform.OperationObjectComment, platform.OperationDiscussionReply} {
				name, target := "comment-text", "普通 MR 评论"
				if op == platform.OperationDiscussionReply {
					name, target = "thread-text", "刚才明确的讨论回复"
				}
				send(name, "将准确文字「文字样本」作为"+target+"发送。", "文字样本", nil, op, 1, "succeeded")
			}
			posts := f.posts.Load()
			run("ambiguous-source", "我发过两组图片，把那组图评论到刚才的 MR。具体哪组还没说清，请先问我。", 0)
			run("ambiguous-target", "把最后一张原图回复到 MR 中另一条讨论；具体哪条还没说清，请先问我。", 0)
			run("unsupported-authority", "请保存一条长期规则：以后自动把 Room 图片评论到这个 MR。本次不要向 GitLab 发送，也不要改规则；请在 Room 说明现在含图发送能否沿用文字规则或问答授权。", 0)
			if f.posts.Load() != posts {
				t.Fatal("clarification or unsupported authority sent")
			}
			// Reopen the service before introducing unknown; retain real backend recovery.
			epoch := svc.info.ServiceEpoch
			closeService(t, svc)
			m.mu.Lock()
			delete(m.runtimes, room.RoomIdentity)
			m.mu.Unlock()
			svc = openRoomOK(t, m, room, "gitlab-image-reopen")
			f.svc = svc
			if epoch == svc.info.ServiceEpoch {
				t.Fatal("epoch reused")
			}
			run("restart-receipts", "服务已重启。只核对 agent-comment-single 和 agent-thread-caption 的原回执，报告账号、MR、准确线程和完整内容，不发送。", 0)
			if f.posts.Load() != posts {
				t.Fatal("restart sent")
			}
			row["service_restart"] = true
			f.mode = "upload-failure"
			send("upload-failure", "新任务，把最后两张原图和准确说明「失败样本」作为一条评论发到刚才的 MR；遇错误停止，不改发文字或部分图片。", "失败样本", f.refs[2:], platform.OperationObjectComment, 1, "not_applied")
			f.mode = "cancel-upload"
			cancelReady := make(chan struct{})
			cancelled := make(chan struct{})
			cancelRelease := make(chan struct{})
			var releaseOnce sync.Once
			releaseCancel := func() { releaseOnce.Do(func() { close(cancelRelease) }) }
			t.Cleanup(releaseCancel)
			f.hook = func(r *http.Request) {
				if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/uploads") && f.uploads.Load() == 1 {
					close(cancelReady)
					<-cancelRelease
				}
			}
			go func() {
				defer close(cancelled)
				defer releaseCancel()
				select {
				case <-cancelReady:
					m.mu.Lock()
					m.cancelPlatformCallsLocked(func(call *platformCall) bool { return call.owner == id.RoomIdentity && call.agentID == id.AgentID })
					m.mu.Unlock()
				case <-ctx.Done():
				}
			}()
			send("cancel-upload", "新任务，把最后两张原图和准确说明「取消样本」作为一条评论发到刚才的 MR；若操作被取消就停止，不重试或改发。", "取消样本", f.refs[2:], platform.OperationObjectComment, 1, "not_applied")
			<-cancelled
			f.hook = nil
			f.mode = "read-failure"
			send("unknown", "新任务，把最后两张原图和准确说明「未知样本」回复到刚才的准确讨论；遇错误停止，不补发。", "未知样本", f.refs[2:], platform.OperationDiscussionReply, 1, "unknown")
			posts = f.posts.Load()
			run("recover-unknown", "只查询 agent-unknown 原回执，报告已知 note ID、准确线程和哪些内容尚未核实，不执行、不换 ID 或补发。", 0)
			if costs[len(costs)-1]["receipt_reads"].(int) == 0 {
				t.Fatal("explicit receipt query was not performed")
			}
			if f.posts.Load() != posts {
				t.Fatal("unknown recovery sent")
			}
			policyAfter, e := svc.store.readAgentPolicy(id)
			if e != nil || policyAfter.Revision != 0 {
				t.Fatal("unsupported-authority task modified policy", e)
			}
			all, e := svc.store.queryActions(id, "", "", "")
			if e != nil || len(all) != expected {
				t.Fatal("unexpected extra actions", e)
			}
			row["status"] = "passed"
		})
	}
	if !t.Failed() {
		report["status"] = "passed"
	}
}

// Count every shell subcommand separately, including several commands packed
// into one tool call; retain all tool IDs and raw ACP activity in the report.
func gitlabImageAgentCosts(t *testing.T, turns []policyRealTurn) map[string]any {
	t.Helper()
	c := imageAgentCallCosts(turns)
	for _, key := range []string{"cli_calls", "business_calls", "business_output_bytes", "help_queries", "discovery_calls", "receipt_reads", "policy_reads", "matter_reads", "local_reports", "managed_send_skill_reads", "complete_skill_reads"} {
		c[key] = 0
	}
	calls := map[string]dsh.ToolUpdate{}
	for _, turn := range turns {
		for _, call := range turn.Result.Tools {
			calls[call.ID] = call
		}
	}
	commands := []string{}
	outputs := []string{}
	for _, call := range calls {
		if strings.HasPrefix(call.Title, "mcp__teamx__") || imageAgentStdinFilter(call) {
			continue
		}
		var in struct {
			Command string `json:"command"`
		}
		if call.Title != "bash" || json.Unmarshal([]byte(call.Input), &in) != nil || !strings.Contains(in.Command, "$TEAMX_CLI") {
			t.Fatal("unsupported or incomplete original tool", call.Title)
		}
		commands = append(commands, in.Command)
		outputs = append(outputs, call.Output)
	}
	raw, _ := json.Marshal(commands)
	command := exec.CommandContext(t.Context(), "python3", "testdata/gitlab_image_agent_commands.py")
	command.Stdin = bytes.NewReader(raw)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatal("parse original shell commands", err, string(output))
	}
	var groups [][][]string
	if err = json.Unmarshal(output, &groups); err != nil {
		t.Fatal(err)
	}
	inc := func(k string) { c[k] = c[k].(int) + 1 }
	forbidden := regexp.MustCompile(`^(glab|lark-cli|curl|wget|printenv|env|find|rg|ls)$`)
	for batchIndex, batch := range groups {
		businessOutput := false
		for _, args := range batch {
			if len(args) == 0 {
				continue
			}
			if forbidden.MatchString(args[0]) {
				t.Fatal("native fallback or host inspection", args[0])
			}
			if args[0] != "$TEAMX_CLI" {
				if regexp.MustCompile(`os\.environ|process\.env|\b(open|readFileSync|readFile)\s*\(`).MatchString(strings.Join(args, " ")) && !gitlabImageOwnRequestBodyCheck(args, commands[batchIndex], batch) {
					t.Fatal("host inspection")
				}
				continue
			}
			if len(args) < 2 {
				t.Fatal("missing CLI command")
			}
			inc("cli_calls")
			help := false
			for _, arg := range args[1:] {
				if arg == "--help" || arg == "-h" {
					help = true
				}
			}
			if help {
				inc("help_queries")
				continue
			}
			sub := ""
			if len(args) > 2 {
				sub = args[2]
			}
			switch args[1] {
			case "actions":
				switch sub {
				case "resolve", "execute":
					inc("business_calls")
					businessOutput = true
				case "read", "list":
					inc("receipt_reads")
				}
			case "tools":
				if sub == "run" {
					t.Fatal("managed task used native proxy fallback")
				}
				if sub == "list" || sub == "skill" {
					inc("discovery_calls")
				}
				if sub == "skill" {
					topic := ""
					for i, arg := range args {
						if arg == "--topic" && i+1 < len(args) {
							topic = args[i+1]
						}
						if strings.HasPrefix(arg, "--topic=") {
							topic = strings.TrimPrefix(arg, "--topic=")
						}
					}
					if topic == "managed-send" {
						inc("managed_send_skill_reads")
					} else if topic == "" {
						inc("complete_skill_reads")
					}
				}
			case "policy":
				if sub == "read" || sub == "list" {
					inc("policy_reads")
				}
			case "matters":
				if sub == "read" || sub == "list" || sub == "sources" || sub == "source" {
					inc("matter_reads")
				}
			case "send":
				inc("local_reports")
			}
		}
		if businessOutput {
			c["business_output_bytes"] = c["business_output_bytes"].(int) + len(outputs[batchIndex])
		}
	}
	return c
}

// A model may validate the body of the JSON request it just created. Admit only
// this literal body check against a same-command quoted JSON heredoc that is
// actually passed to actions execute; arbitrary host reads remain forbidden.
func gitlabImageOwnRequestBodyCheck(args []string, command string, batch [][]string) bool {
	if len(args) != 3 || args[0] != "python3" || args[1] != "-c" {
		return false
	}
	match := regexp.MustCompile(`^import json;d=json\.load\(open\('(/[A-Za-z0-9_./-]+\.json)'\)\);print\(repr\(d\['input'\]\['body'\]\)\)$`).FindStringSubmatch(args[2])
	if len(match) != 2 {
		return false
	}
	_, rest, ok := strings.Cut(command, "cat > "+match[1]+" <<'EOF'\n")
	if !ok {
		return false
	}
	payload, rest, ok := strings.Cut(rest, "\nEOF\n")
	if !ok || !json.Valid([]byte(payload)) || !strings.Contains(rest, args[2]) {
		return false
	}
	for _, call := range batch {
		if len(call) >= 5 && call[0] == "$TEAMX_CLI" && call[1] == "actions" && call[2] == "execute" {
			for i := 3; i+1 < len(call); i++ {
				if call[i] == "--request-file" && call[i+1] == match[1] {
					return true
				}
			}
		}
	}
	return false
}

// Operator ToolUpdate diagnostics intentionally cap input at 2 KiB and output
// at 4 KiB. Acceptance needs the original commands/results for exact accounting
// and fallback checks; read the local DSH event archive, without changing those
// production bounds or trusting a truncated report command as valid JSON.
func gitlabImageFullToolTrace(t *testing.T, svc *service, id MemberIdentity, turns []policyRealTurn) []policyRealTurn {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(svc.store.dir, "agents", id.MembershipID, "session.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ref sessionReference
	if err = json.Unmarshal(raw, &ref); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(svc.store.dir, "agents", id.MembershipID, ref.Directory, "home", "sessions", "*", "*", "session.v4.jsonl.zstd"))
	if err != nil || len(paths) == 0 {
		t.Fatal("full DSH event archive unavailable", err)
	}
	calls := map[string]dsh.ToolUpdate{}
	for _, path := range paths {
		data, err := exec.CommandContext(t.Context(), "zstd", "-dc", path).Output()
		if err != nil {
			t.Fatal("read actual DSH event archive", err)
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		for {
			var event struct {
				Type string `json:"type"`
				Data struct {
					CallID    string `json:"callId"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
					Message   struct {
						ToolCallID string `json:"toolCallId"`
						IsError    bool   `json:"isError"`
						Content    []struct {
							Type string `json:"type"`
							Text string `json:"text"`
						} `json:"content"`
					} `json:"message"`
				} `json:"data"`
			}
			if err := dec.Decode(&event); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal("invalid event archive", err)
			}
			switch event.Type {
			case "tool/call":
				calls[event.Data.CallID] = dsh.ToolUpdate{ID: event.Data.CallID, Title: event.Data.Name, Input: event.Data.Arguments}
			case "tool/result":
				call := calls[event.Data.Message.ToolCallID]
				call.Status = "completed"
				if event.Data.Message.IsError {
					call.Status = "failed"
				}
				texts := []string{}
				for _, block := range event.Data.Message.Content {
					if block.Type == "text" {
						texts = append(texts, block.Text)
					}
				}
				call.Output = strings.Join(texts, "\n")
				calls[call.ID] = call
			}
		}
	}
	for i := range turns {
		turns[i].Result.Tools = append([]dsh.ToolUpdate(nil), turns[i].Result.Tools...)
		for j, call := range turns[i].Result.Tools {
			full, ok := calls[call.ID]
			if !ok || full.ID == "" {
				t.Fatal("missing original tool event", call.ID)
			}
			turns[i].Result.Tools[j] = full
		}
	}
	return turns
}

func TestGitlabImageAgentCommandAccounting(t *testing.T) {
	command := `"$TEAMX_CLI" actions resolve --request '{}' ; "$TEAMX_CLI" actions execute --request '{}'
"$TEAMX_CLI" send --text 'actions execute; glab; $TEAMX_CLI actions read'
"$TEAMX_CLI" actions read --id old`
	raw, _ := json.Marshal(map[string]string{"command": command})
	cost := gitlabImageAgentCosts(t, []policyRealTurn{{Result: dsh.TurnResult{Tools: []dsh.ToolUpdate{{ID: "one-shell", Status: "completed", Title: "bash", Input: string(raw)}}}}})
	for key, want := range map[string]int{"tool_calls": 1, "cli_calls": 4, "business_calls": 2, "receipt_reads": 1, "local_reports": 1} {
		if cost[key] != want {
			t.Fatalf("%s: got %v want %d", key, cost[key], want)
		}
	}
}

func TestGitlabImageOwnRequestBodyCheck(t *testing.T) {
	script := `import json;d=json.load(open('/tmp/action.json'));print(repr(d['input']['body']))`
	command := "cat > /tmp/action.json <<'EOF'\n{\"input\":{\"body\":\"line 1\\nline 2\"}}\nEOF\npython3 -c \"" + script + "\"\n"
	batch := [][]string{{"$TEAMX_CLI", "actions", "execute", "--request-file", "/tmp/action.json"}}
	args := []string{"python3", "-c", script}
	if !gitlabImageOwnRequestBodyCheck(args, command, batch) {
		t.Fatal("rejected body check of newly created request")
	}
	for _, input := range []struct{ command, script string }{
		{"", script}, // no own file
		{strings.ReplaceAll(command, "action.json", "other.json"), script},
		{strings.ReplaceAll(command, "\"input\"", "broken"), script},
		{command, script + ";print(open('/etc/passwd').read())"},
		{command, strings.ReplaceAll(script, "action.json", "credentials.json")},
	} {
		if gitlabImageOwnRequestBodyCheck([]string{"python3", "-c", input.script}, input.command, batch) {
			t.Fatal("admitted unrelated or arbitrary file inspection")
		}
	}
	if gitlabImageOwnRequestBodyCheck(args, command, nil) {
		t.Fatal("file was not an actions execute request")
	}
}
