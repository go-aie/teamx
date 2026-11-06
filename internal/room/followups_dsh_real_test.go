//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Real installed DSH/model, real CLI/HTTP and durable Room state. Only the
// contract platform's remote boundary is simulated; no GitLab writes occur.
func TestReferenceFollowupsRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_REFERENCE_DSH_REAL") != "1" {
		t.Skip("set TEAMX_REFERENCE_DSH_REAL=1 and a fresh TEAMX_DSH_OUTPUT")
	}
	output := os.Getenv("TEAMX_DSH_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("new absolute TEAMX_DSH_OUTPUT required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: filepath.Join(output, "driver-room")}, output: output, report: map[string]any{
		"scenario": "R6-real-DSH-reference-conversation", "status": "failed", "platform": "contract test adapter", "remote_writes": 0,
	}}
	audit := &policyRealAudit{Calls: map[string]int{}}
	adapter := &referenceContractAdapter{}
	defer func() {
		audit.mu.Lock()
		d.report["audit"] = audit
		adapter.mu.Lock()
		d.report["adapter_calls"] = map[string]int{"resolve": adapter.resolves, "inspect": adapter.inspections, "execute": adapter.sends}
		adapter.mu.Unlock()
		b, err := json.MarshalIndent(d.report, "", "  ")
		audit.mu.Unlock()
		if err == nil {
			err = os.WriteFile(filepath.Join(output, "report.json"), append(b, '\n'), 0600)
		}
		if err != nil {
			t.Error(err)
		}
		t.Log("retained report:", filepath.Join(output, "report.json"))
	}()
	if err := d.setup(); err != nil {
		t.Fatal(err)
	}
	t.Setenv(d.report["apiKeyEnv"].(string), d.secret)
	var profiles map[string]dshProfile
	if err := jsonFile(d.profilePath, &profiles); err != nil {
		t.Fatal(err)
	}
	p := profiles["reviewer"]
	p.Model.ContextWindow, p.Model.MaxTokens, p.Model.ReasoningEffort = 1048576, 4096, "low"
	p.Instructions = `You are an isolated reference-followup acceptance assistant. Follow the runtime schema 2 protocol and current policy_token/current_turn. The contract provider is a TEST adapter: object.comment creates a local simulated message, and is enabled by the registered_actions in matters read. No project/IID or platform tools are needed. Use the configured TeamX CLI with JSON, read all outputs. Never inspect files, credentials or environment, delegate, make network requests, or use tools run. Do not create semantic matters or use enrich/relate/decisions.
For each new Inbox input, use inbox read, matters list and matters read; use the system object/unit refs. For the FIRST task there is no standing rule: use matters notify to ask the human once whether to reply with exact body "R6 已收到". Include proposed {operation:"object.comment",body:"R6 已收到"} and the source's verified message target_ref in message_refs. Do not execute yet. End the turn after asking.
When the human replies, read the existing unit to associate reply_to. Execute object.comment ONCE using the saved question_ref and typed human_reply authority with the actual human room_seq (questions[].answers[].message.seq, NOT the question's room_seq), input.body "R6 已收到", stable id "first-answer". Do not require a separate instruction/decision or ask again. Save the successful result as local followup evidence; business status may stay pending. Never change an unknown action ID or retry uncertain writes. If a command fails, report its full error privately and stop; do not inspect the repository or debug implementation code.
When the human explicitly requests a lasting rule, use policy update type save with body {text,expires_at?} and their real source_seq/current turn; save an ACTIVE long-term rule. Put the business limits in text: only Conversation acceptance receipt tasks on contract://work may receive exact body "R6 已收到", with concrete prerequisites and exceptions excluding all other tasks. Do not submit kind, scope, scope_from or split actions/conditions/exceptions fields. The server verifies native target/account independently; you must judge whether the body permits this operation. Use a stable rule id "receipt-rule". Acknowledge briefly with send, id "rule-saved". Do not execute an action merely to save this rule.
On a later Inbox task covered by that active rule, execute object.comment once with stable id "rule-answer" and the accurate rule ID/version, exact same body; no human question or UI activation. If the policy body is missing after Session rotation, policy read restores it. After a successful action keep evidence locally and end privately. Routine completion needs no public result or question.`
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	r := managerCreate(t, m, "r6-dsh")
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	svc, err := newService(m.roomDir(r.RoomID), r.RoomID, serviceConfig{endpoint: server.URL, cliPath: d.binary, maxTurns: 8, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	observePolicyReal(svc, audit)
	adapter.install(m, svc)
	m.mu.Lock()
	m.runtimes[r.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	d.report["member"] = id
	wait := func(n uint64) {
		t.Helper()
		d.report["phase"] = fmt.Sprintf("turn-%d", n)
		s := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && s.Agent.TurnNumber >= n })
		if s.Error != "" || s.Agent.TurnNumber != n || s.Agent.Turn == nil || s.Agent.Turn.Status != "completed" {
			d.report["error"] = s.Error
			t.Fatalf("real turn %d failed; inspect retained report: %s", n, s.Error)
		}
	}
	post := func(key, text string, reply uint64) {
		t.Helper()
		_, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: key, ReplyTo: reply, Text: text})
		if err != nil {
			t.Fatal(err)
		}
	}
	contractDeliver(t, m, svc, id, contractSource("first", "receipt-conversation"), "验收收件任务：请确认后回复准确正文 R6 已收到。")
	if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(1)
	page, err := svc.store.listFollowups(id, followupQuery{Limit: 25})
	if err != nil || page.Total != 1 {
		t.Fatal(page, err)
	}
	u, err := svc.store.readFollowup(id, page.Items[0].ObjectRef, page.Items[0].Ref)
	if err != nil || len(u.Questions) != 1 || u.Questions[0].Seq == 0 || len(u.Actions) != 0 {
		t.Fatal("model did not ask once before action", err)
	}
	post("natural-answer", "可以，就按你说的回复。", u.Questions[0].Seq)
	wait(2)
	u, err = svc.store.readFollowup(id, u.ObjectRef, u.Ref)
	if err != nil || len(u.Questions) != 1 || len(u.Questions[0].Answers) != 1 || len(u.Actions) != 1 || u.Actions[0].Status != "succeeded" || u.Actions[0].Basis == nil {
		t.Fatal("natural answer did not continue through exact saved question", err)
	}
	post("standing-rule", "以后 contract://work 的 Conversation 验收收件任务直接回复准确正文“R6 已收到”，无需再问我；其他任务不适用。请记住这条长期规则。", 0)
	wait(3)
	policy, err := svc.store.readAgentPolicy(id)
	if err != nil || len(policy.Effective) != 1 || policy.Effective[0].Conversation == nil {
		t.Fatal("natural lasting intent did not save an active rule", err)
	}
	// Rotate the actual persistent Session. Business refs and policy survive it.
	rotation, err := svc.rotateSession(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID})
	if err != nil {
		t.Fatal(err)
	}
	d.report["rotation"] = rotation
	contractDeliver(t, m, svc, id, contractSource("second", "receipt-conversation"), "又一条同范围验收收件任务：请按已经保存的规则办理。")
	wait(4)
	u, err = svc.store.readFollowup(id, u.ObjectRef, u.Ref)
	if err != nil || len(u.Questions) != 1 || len(u.Actions) != 2 || u.Actions[1].Status != "succeeded" || u.Actions[1].Rule == nil {
		t.Fatal("post-rotation same-scope task did not use saved rule", err)
	}
	d.report["unit"], d.report["policy"] = u, policy
	audit.mu.Lock()
	for _, path := range []string{"/v1/matters/enrich", "/v1/matters/relate", "/v1/decisions/update", "/v1/decisions/read"} {
		if audit.Calls[path] != 0 {
			t.Error("retired protocol used", path)
		}
	}
	valid := audit.Calls["/v1/matters/notify"] == 1 && audit.Calls["/v1/actions/execute"] == 2 && len(audit.Turns) == 4
	audit.mu.Unlock()
	adapter.mu.Lock()
	sends := adapter.sends
	adapter.mu.Unlock()
	if !valid || sends != 2 {
		t.Fatal("unexpected model protocol/call counts; inspect retained report")
	}
	d.report["metrics"] = map[string]int{"notifications": 2, "units": 1, "questions": 1, "repeated_questions": 0, "required_UI": 0, "simulated_sends": sends, "detail_bytes": len(mustContractJSON(u))}
	d.report["status"] = "passed"
	fmt.Println("R6 real DSH conversation + rule + Session rotation passed")
}
