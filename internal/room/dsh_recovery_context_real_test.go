//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
)

// Real installed DSH/model, CLI and service. Only the remote business action is
// a local contract sink. No real platform messages are sent by this acceptance.
func TestDSHRecoveryContextReal(t *testing.T) {
	if os.Getenv("TEAMX_RECOVERY_DSH_REAL") != "1" {
		t.Skip("set TEAMX_RECOVERY_DSH_REAL=1, TEAMX_RECOVERY_CONFIG and fresh TEAMX_DSH_OUTPUT")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "S2 real DSH manual rotation and normal resume; local contract sink", "external_business_writes": 0}
	defer imageRealReport(t, output, report)
	config, err := loadStartupConfig(os.Getenv("TEAMX_RECOVERY_CONFIG"), true)
	if err != nil || config.Runtime == nil {
		t.Fatal("current runtime config required", err)
	}
	runtime := config.Runtime
	key := runtime.Model.APIKeyEnv
	if os.Getenv(key) == "" && config.Env[key] != "" {
		t.Setenv(key, config.Env[key])
	}
	imageRealCredential(t, key)
	workspace := filepath.Join(output, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(output, "teamx")
	if data, err := exec.Command("go", "build", "-o", binary, "../../cmd/teamx").CombinedOutput(); err != nil {
		t.Fatalf("build: %s: %v", data, err)
	}
	profile := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact,
		Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are a bounded TeamX acceptance assistant. The contract provider is a LOCAL TEST sink only. Use the configured TeamX CLI and current_turn/policy_token. Read normal unread Room input and offered Inbox input. Before every action use actions resolve with locator.target_ref set to the existing object's target_ref. Every actions execute MUST include context={object_ref,unit_ref,unit_version} from a current matters read; preserve question association. Never inspect repository files, credentials or environment, delegate, make network requests, run platform tools, or debug service implementation. Do not send ordinary output to the Room.
For the first Inbox task, read its existing object matter and ask the human ONCE using matters notify whether to execute object.comment with exact body "S2 confirmed". Use the verified source message target_ref in message_refs and proposed {operation:"object.comment",body:"S2 confirmed"}. Do not execute yet.
When the human answers that question, use matters read to associate reply_to, then execute the confirmed action ONCE, stable ID "s2-confirmed", typed human_reply with the saved question_ref and actual human answer message.seq, exact body "S2 confirmed". Do not ask again.
A later direct human Room command for an uncertain test action authorizes ONE additional object.comment with ID "s2-uncertain", body "S2 uncertain", typed human_reply using that command's actual room_seq. No new question is needed. Unknown may have been applied: never resend or replace its ID.
When asked to continue/review after rotation or restart, restore the existing matter, original consumed notification through matters sources/source, and BOTH saved receipts using actions read/list. Do not execute actions or ask questions again. Update the existing matter with status unknown, a summary containing the exact task token from the original notification plus confirmed/unknown distinctions, and next explaining receipt verification and the missing attachment. No completed status. State the same verified facts privately. On tool errors stop privately without replaying writes.`}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	r := managerCreate(t, m, "s2-recovery")
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	adapter := &referenceContractAdapter{}
	var svc *service
	var recoveryTurns []uint64
	start := func() {
		t.Helper()
		var err error
		svc, err = startService(m.roomDir(r.RoomID), r.RoomID, serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 8, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": profile}}, func(s *service, state RoomSnapshot) error {
			observePolicyReal(s, audit)
			factory := s.memberFactory
			s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
				member := factory(id, runner, restored).(*MemberAgent)
				backend := member.Backend.(policyRealBackend)
				prepare := backend.recoveryContext
				backend.recoveryContext = func(ctx context.Context, input UnreadChanged, turn uint64) (*dshRecoveryContext, error) {
					result, err := prepare(ctx, input, turn)
					if err == nil {
						audit.mu.Lock()
						recoveryTurns = append(recoveryTurns, turn)
						audit.mu.Unlock()
					}
					return result, err
				}
				member.Backend = backend
				return member
			}
			if err := s.loadMembers(state); err != nil {
				return err
			}
			return s.attachMembers(state)
		})
		if err != nil {
			t.Fatal(err)
		}
		adapter.install(m, svc)
		m.referenceAdapters["contract"] = func() platform.ReferenceCapabilities {
			caps := adapter.capabilities()
			resolve := caps.Resolve
			caps.Resolve = func(ctx context.Context, locator platform.TargetLocator, access platform.AccessContext) (platform.TargetResolution, error) {
				if locator.Candidate == nil {
					return resolve(ctx, locator, access)
				}
				target := *locator.Candidate
				if !target.Valid() || target.IdentityScope != access.Account.IdentityScope {
					return platform.TargetResolution{}, ErrIdentity
				}
				return platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: access.Account, ObjectVerified: true, MessageVerified: target.Message.Valid(), DiscussionVerified: target.Discussion.Valid(), Complete: true, CheckedAt: time.Now().UTC()}, ctx.Err()
			}
			cap := caps.Actions[platform.OperationObjectComment]
			execute := cap.Execute
			cap.Execute = func(ctx context.Context, q platform.TargetActionRequest, access platform.AccessContext) (platform.TargetActionResult, error) {
				result, err := execute(ctx, q, access)
				var input CommentInput
				if err := json.Unmarshal(q.Input, &input); err != nil {
					return result, err
				}
				if result.Status == "succeeded" && input.Body == "S2 uncertain" {
					result.Status, result.ResultID = "unknown", platform.ExternalID{}
				}
				return result, err
			}
			caps.Actions[platform.OperationObjectComment] = cap
			return caps
		}
		m.mu.Lock()
		m.runtimes[r.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
		m.mu.Unlock()
	}
	start()
	defer func() {
		if svc != nil {
			closeService(t, svc)
		}
		audit.mu.Lock()
		report["audit"] = audit
		report["recovery_supply_turns"] = recoveryTurns
		audit.mu.Unlock()
		adapter.mu.Lock()
		report["sink_attempts"] = adapter.sends
		adapter.mu.Unlock()
	}()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	report["member"], report["model"] = id, profile.Model.ModelID
	wait := func(n uint64) {
		t.Helper()
		report["phase"] = fmt.Sprintf("turn-%d", n)
		status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= n || s.Paused) })
		if status.Error != "" || status.Agent.TurnNumber != n || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
			report["turn_error"] = status.Error
			t.Fatalf("turn %d: %s", n, status.Error)
		}
	}
	post := func(key, text string, reply uint64) {
		t.Helper()
		if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: key, Text: text, ReplyTo: reply}); err != nil {
			t.Fatal(err)
		}
	}
	token, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	contractDeliver(t, m, svc, id, contractSource("s2-original", "s2-work"), "请确认后办理 object.comment。任务 token: "+token+"。附件设计图未提供，内容待核实；不能据此判断设计已通过。")
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(1)
	page, err := svc.store.listFollowups(id, followupQuery{Limit: 25})
	if err != nil || len(page.Items) != 1 {
		t.Fatal("first matter", page, err)
	}
	u, err := svc.store.readFollowup(id, page.Items[0].ObjectRef, page.Items[0].Ref)
	if err != nil || len(u.Questions) != 1 {
		t.Fatal("first question", err)
	}
	post("s2-answer", "同意，按所述准确正文执行本次回复。", u.Questions[0].Seq)
	wait(2)
	post("s2-direct-uncertain", "对同一原事项额外执行一次测试 object.comment，准确正文 S2 uncertain，ID s2-uncertain。本条为本次准确目标及正文的直接授权；若 unknown 只核实，不补发。", 0)
	wait(3)
	rows, err := svc.store.queryActions(id, "", "", "")
	if err != nil || len(rows) != 2 || rows[0].Status != "succeeded" || rows[1].Status != "unknown" || rows[0].Attempts != 1 || rows[1].Attempts != 1 {
		t.Fatal("initial receipts", err)
	}
	audit.mu.Lock()
	actionRequestsBeforeRotation := audit.Calls["/v1/actions/execute"]
	audit.mu.Unlock()
	beforeInbox, err := svc.memberInboxStatus(ctx, id)
	if err != nil || beforeInbox.ConsumedThrough != 1 {
		t.Fatal("original not consumed", err)
	}
	memberDir := filepath.Join(svc.store.dir, "agents", id.MembershipID)
	var old dshReference
	if err := jsonFile(filepath.Join(memberDir, "session.json"), &old); err != nil {
		t.Fatal(err)
	}
	rotationID, _ := newOpaqueID()
	rotation, rotateErr := svc.rotateSession(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID, OperationID: rotationID, FromGeneration: old.Directory})
	if rotateErr != nil {
		rotation = waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool {
			return s.Rotation != nil && s.Rotation.OperationID == rotationID && (s.Rotation.Status == "completed" || s.Rotation.Status == "failed")
		})
	}
	if rotation.Rotation == nil || rotation.Rotation.Status != "completed" {
		t.Fatal("rotation", rotateErr)
	}
	report["rotation"] = rotation.Rotation
	post("s2-continue", "继续原事项：查回已经消费的原通知，核实此前问题的答案和两个动作回执，保留未知结果及缺失附件的待核实状态，将恢复出的任务 token 与结论写回原事项的进度。", 0)
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(4)
	check := func() {
		t.Helper()
		var err error
		u, err = svc.store.readFollowup(id, u.ObjectRef, u.Ref)
		if err != nil || len(u.Questions) != 1 || len(u.Questions[0].Answers) != 1 || len(u.Actions) != 2 || u.Status != "unknown" || !strings.Contains(u.Summary, token) || !strings.Contains(u.Summary, "s2-confirmed") || !strings.Contains(u.Summary, "s2-uncertain") || u.Next == "" {
			t.Fatal("work not continued or repeated", err)
		}
		if text := strings.ToLower(u.Summary + u.Next); !strings.Contains(text, "attachment") && !strings.Contains(text, "附件") {
			t.Fatal("missing attachment lost in continuation")
		}
		inbox, err := svc.memberInboxStatus(ctx, id)
		if err != nil || inbox.ConsumedThrough != beforeInbox.ConsumedThrough {
			t.Fatal("consumed source changed", err)
		}
	}
	check()
	var fresh dshReference
	if err := jsonFile(filepath.Join(memberDir, "session.json"), &fresh); err != nil || fresh.Directory == old.Directory {
		t.Fatal("same generation", err)
	}
	var backendBefore struct {
		SessionID string `json:"sessionId"`
	}
	if err := jsonFile(filepath.Join(memberDir, fresh.Directory, "backend.json"), &backendBefore); err != nil {
		t.Fatal(err)
	}
	report["after_rotation_unit"] = u
	closeService(t, svc)
	svc = nil
	start()
	post("s2-resume-review", "正常重启后继续核对原事项、原通知和两个回执。保存当前进度，未知结果及缺失附件仍待核实。", 0)
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(5)
	check()
	var backendAfter struct {
		SessionID string `json:"sessionId"`
	}
	if err := jsonFile(filepath.Join(memberDir, fresh.Directory, "backend.json"), &backendAfter); err != nil || backendBefore.SessionID != backendAfter.SessionID {
		t.Fatal("normal resume replaced Session", err)
	}
	audit.mu.Lock()
	valid := audit.Calls["/v1/matters/notify"] == 1 && audit.Calls["/v1/actions/execute"] == actionRequestsBeforeRotation && audit.Calls["/v1/matters/source"] >= 2 && fmt.Sprint(recoveryTurns) == "[1 4 5]"
	audit.mu.Unlock()
	if !valid {
		t.Fatal("unexpected question/action/source queries; inspect report")
	}
	adapter.mu.Lock()
	sends := adapter.sends
	adapter.mu.Unlock()
	if sends != 2 {
		t.Fatal("repeated remote action", sends)
	}
	report["final_unit"], report["resumed_session_id"], report["status"], report["phase"] = u, backendAfter.SessionID, "passed", "complete"
}
