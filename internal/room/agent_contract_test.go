package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestAgentBTypedActionsRestoreExactTarget(t *testing.T) {
	for _, discussion := range []bool{false, true} {
		for _, authority := range []string{"rule", "instruction", "human_reply"} {
			t.Run(fmt.Sprintf("discussion=%t/%s", discussion, authority), func(t *testing.T) {
				f := managedActionTest(t, discussion)
				q := agentActionFixture(f.action)
				if authority != "rule" {
					post, err := f.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: f.id.RoomIdentity, Caller: humanCaller, ClientMessageID: "exact-instruction", Text: "只在这个准确目标回复 exact reply"})
					if err != nil {
						t.Fatal(err)
					}
					q.Authority = agentActionAuthority{Type: "human_reply", RoomSeq: post.Seq, Reason: "当前真实用户明确指向准确目标与正文"}
					if authority == "instruction" {
						u, err := f.svc.store.saveFollowupInstruction(f.id, followupInstructionChange{followupCommand: q.followupCommand, SourceSeq: post.Seq, Intent: followupIntent{Operation: q.Operation, Body: q.Input.Body}, Basis: q.Authority.Reason})
						if err != nil {
							t.Fatal(err)
						}
						q.Revision = u.Revision
						q.Authority = agentActionAuthority{Type: "instruction", ID: u.Instructions[0].Ref, Version: 1, Reason: "落实已保存的单次指令"}
					}
				}
				server := httptest.NewServer(f.m)
				defer server.Close()
				cli := contractCLI(t, server.URL, f.svc, f.id)
				args := entryChange(t, "actions", "execute", q)
				args[2] = "--request"
				var got referenceActionReceipt
				if err := cli(args, &got); err != nil || got.Status != "succeeded" || got.Attempts != 1 || got.Result == nil {
					t.Fatal(got, err)
				}
				u, err := f.svc.store.readFollowup(f.id, q.ObjectRef, q.UnitRef)
				if err != nil || len(u.Actions) != 1 || got.TargetRef != u.TargetRef || !sameNativeTarget(got.Result.Target, u.Actions[0].Target) || got.Result.Target.Discussion.Valid() != discussion {
					t.Fatal(u, got, err)
				}
				if authority == "instruction" && u.Instructions[0].Status != "fulfilled" {
					t.Fatal("single-use instruction was not settled", u)
				}
				// Lost-response recovery uses the original observed version and evidence.
				if err := cli(args, &got); err != nil || got.Status != "succeeded" || f.posts.Load() != 1 {
					t.Fatal(got, err, f.posts.Load())
				}
				changed := q
				changed.Input.Body = "different"
				bad := entryChange(t, "actions", "execute", changed)
				bad[2] = "--request"
				if err := cli(bad, nil); err == nil || f.posts.Load() != 1 {
					t.Fatal("same ID changed body accepted", err)
				}
				assertActionCallsSettled(t, f.m)
			})
		}
	}
}

func TestAgentBTypedActionUnknownRecovery(t *testing.T) {
	f := managedActionTest(t, true)
	managedReferenceHTTP(t, f.m, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			f.posts.Add(1)
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"invalid":"remote result"}`)
			return
		}
		f.serve(w, r)
	})
	server := httptest.NewServer(f.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, f.svc, f.id)
	q := agentActionFixture(f.action)
	args := func(q agentActionRequest) []string {
		a := entryChange(t, "actions", "execute", q)
		a[2] = "--request"
		return a
	}
	var got referenceActionReceipt
	if err := cli(args(q), &got); err != nil || got.Status != "unknown" || got.Attempts != 1 {
		t.Fatal(got, err)
	}
	if err := cli(args(q), &got); err != nil || got.Status != "unknown" || f.posts.Load() != 1 {
		t.Fatal("unknown replay sent again", got, err)
	}
	q.ID = "replacement"
	q.Revision = got.UnitVersion
	if err := cli(args(q), nil); err == nil || f.posts.Load() != 1 {
		t.Fatal("unknown replaced by new ID", err)
	}
	var view agentFollowupRead
	if err := cli([]string{"matters", "read", "--object", q.ObjectRef, "--unit", q.UnitRef, "--turn", "1", "--detail"}, &view); err != nil || len(view.Unit.Actions) != 1 || view.Unit.Actions[0].Status != "unknown" || view.Detail == nil || len(view.Detail.Actions) != 1 {
		t.Fatal(view, err)
	}
}

func TestAgentBRejectsOldContractsAndBranchMixing(t *testing.T) {
	for _, tc := range []struct{ command, op, flag, payload string }{
		{"actions", "execute", "--request", `{"target_ref":"ref_old"}`},
		{"actions", "execute", "--request", `{"authority":{"rule_id":"old","rule_version":1}}`},
		{"actions", "execute", "--request", `{"authority":{"type":"rule","id":"x","version":1,"room_seq":0}}`},
		{"actions", "execute", "--request", `{"authority":{"type":"instruction","id":"x","version":1,"message_refs":[]}}`},
		{"actions", "execute", "--request", `{"authority":{"type":"human_reply","room_seq":18,"version":0}}`},
		{"policy", "update", "--change", `{"change":{},"mode":"explicit_long_term","basis":"old"}`},
		{"policy", "update", "--change", `{"type":"confirm","version":1,"proposal_seq":4,"body":null}`},
		{"policy", "update", "--change", `{"type":"revoke","version":1,"proposal_seq":0}`},
		{"policy", "update", "--change", `{"type":"suggest","body":{},"status":"active"}`},
		{"matters", "instruction-status", "--change", `{"id":"old","version":1}`},
		{"matters", "update", "--change", `{"seq":1}`},
	} {
		t.Run(tc.command+"/"+tc.payload, func(t *testing.T) {
			// No endpoint or binding is present: malformed requests must fail locally.
			err := runCLI(context.Background(), []string{tc.command, tc.op, tc.flag, tc.payload}, io.Discard, io.Discard, func(string) string { return "" })
			if !errors.Is(err, ErrRequest) {
				t.Fatal("old/mixed contract accepted or attempted HTTP", err)
			}
		})
	}
	// HTTP itself rejects the retired mutation envelope, including callers that
	// bypass local CLI parsing.
	h, svc, units, _ := conversationHarness(t, 1)
	svc.requests = make(chan struct{}, 32)
	req := apiRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(h.id), Followup: &followupRequest{followupCommand: conversationCommand(units[0], 1), Status: "in_progress", Summary: "legacy"}}
	status, _ := diagnosticHTTP(t, svc, req, "/v1/matters/update")
	if status != 400 {
		t.Fatal(status)
	}
}

func TestAgentBPolicyBranchesAndDetailRecovery(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	cli := entryCLI(t, svc)
	seq := postConversationReply(t, h, "habit", "这次简短中文回复", 0).Seq
	q := agentPolicyChange{Type: "suggest", ID: "style", SourceSeq: seq, TurnNumber: 1, Basis: "仅建议，不推断长期许可", Body: &agentPolicyBody{Kind: "preference", Text: "简短中文；保留必要限制", Scope: PolicyScope{}}}
	var suggestion agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", q), &suggestion); err != nil || suggestion.Record.Version != 1 || len(suggestion.Effective) != 0 {
		t.Fatal(suggestion, err)
	}
	var sent PostReceipt
	if err := cli([]string{"policy", "publish", "--id", "style", "--version", "1", "--turn", "1"}, &sent); err != nil {
		t.Fatal(err)
	}
	confirmSeq := postConversationReply(t, h, "confirm-style", "以后就按这个建议处理", sent.Seq).Seq
	confirm := agentPolicyChange{Type: "confirm", ID: "style", Version: 1, Revision: 1, SourceSeq: confirmSeq, TurnNumber: 1, Basis: "明确确认已公开的原建议", ProposalSeq: sent.Seq}
	var active agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", confirm), &active); err != nil || len(active.Effective) != 1 || active.Record.Text != suggestion.Record.Text || active.Record.Scope != suggestion.Record.Scope || active.Record.Conversation.Proposal.Seq != sent.Seq {
		t.Fatal(active, err)
	}
	revoke := agentPolicyChange{Type: "revoke", ID: "style", Version: 2, Revision: 2, SourceSeq: postConversationReply(t, h, "revoke-style", "撤销这条长期偏好", 0).Seq, TurnNumber: 1, Basis: "准确撤销用户长期偏好"}
	var revoked agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", revoke), &revoked); err != nil || revoked.Record.Text != active.Record.Text || revoked.Record.Status != "revoked" || len(revoked.Effective) != 0 {
		t.Fatal(revoked, err)
	}
	// Replaying a saved confirmation restores its historical body and original
	// receipt, even though the current record has since been revoked.
	var replay agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", confirm), &replay); err != nil || !reflect.DeepEqual(replay.Record, active.Record) || replay.Revision != 3 || len(replay.Effective) != 0 {
		t.Fatal(replay, err)
	}
	stale := confirm
	stale.Revision = 3
	stale.SourceSeq = postConversationReply(t, h, "new-confirm", "再次确认", sent.Seq).Seq
	if err := cli(entryChange(t, "policy", "update", stale), nil); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatal("stale suggestion activated", err)
	}
	var detail struct {
		Record   PolicyRevision
		History  []PolicyRevision
		Complete bool
	}
	if err := cli([]string{"policy", "read", "--id", "style", "--detail"}, &detail); err != nil || !detail.Complete || len(detail.History) != 3 || detail.History[0].Conversation.Message.Text != "这次简短中文回复" || detail.History[1].Conversation.Message.ReplyTo != sent.Seq {
		t.Fatal(detail, err)
	}
	raw, err := h.store.readPolicy(h.id)
	if err != nil || !reflect.DeepEqual(detail.History, raw.History) {
		t.Fatal("Agent cannot restore full policy provenance", err)
	}
	// Projection never changes the authoritative policy digest.
	view, err := h.store.readAgentPolicy(h.id)
	if err != nil {
		t.Fatal(err)
	}
	if view.PolicyToken != revoked.PolicyToken {
		t.Fatal("policy token changed during projection")
	}
}

func TestAgentBNotifyReceiptsAndUncertainPublication(t *testing.T) {
	h, svc, units, _ := conversationHarness(t, 1)
	before := units[0]
	q := followupQuestionChange{followupCommand: conversationCommand(before, 1), Text: "@human 请确认准确正文"}
	ctx, cancel := context.WithCancel(h.ctx)
	cancel()
	pending, err := svc.notifyAgentFollowup(ctx, h.id, q, before)
	if err != nil || pending.Question == nil || pending.Publication != "unconfirmed" || pending.SendReceipt != nil || !pending.RequiresRead || pending.UnitVersion != before.Revision+1 {
		t.Fatal(pending, err)
	}
	saved, err := h.store.readFollowup(h.id, before.ObjectRef, before.Ref)
	if err != nil || len(saved.Questions) != 1 || saved.Questions[0].Ref != pending.Question.Ref {
		t.Fatal(saved, err)
	}
	complete, err := svc.notifyAgentFollowup(h.ctx, h.id, q, saved)
	if err != nil || complete.Publication != "published" || complete.SendReceipt == nil || complete.Question.Text != q.Text || !complete.Reused || !complete.RequiresRead {
		t.Fatal(complete, err)
	}
	saved, err = h.store.readFollowup(h.id, before.ObjectRef, before.Ref)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.notifyAgentFollowup(h.ctx, h.id, q, saved)
	if err != nil || !replay.Reused || replay.Question.Seq != complete.SendReceipt.Seq || replay.UnitVersion != complete.UnitVersion {
		t.Fatal(replay, err)
	}
	room, err := h.store.LoadRoom()
	if err != nil || len(room.FollowupNoticeReceipts) != 1 {
		t.Fatal(room, err)
	}
}

func TestAgentBReadDetailsPreserveBusinessAndNativeEvidence(t *testing.T) {
	f := referenceExecuteTest(t, "discussion")
	receipt, err := f.run()
	if err != nil || receipt.Status != "succeeded" {
		t.Fatal(receipt, err)
	}
	cli := entryCLI(t, f.svc)
	args := []string{"matters", "read", "--object", f.q.ObjectRef, "--unit", f.q.UnitRef, "--turn", "1"}
	var view, detail agentFollowupRead
	if err := cli(args, &view); err != nil {
		t.Fatal(err)
	}
	if err := cli(append(args, "--detail"), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Detail == nil || len(detail.Detail.Actions) != 1 || len(view.Unit.Actions) != 1 || view.Unit.Actions[0].Input != f.q.Input || view.Unit.Actions[0].Status != "succeeded" || len(view.Details.Omitted) == 0 || len(detail.Details.Omitted) != 0 || !reflect.DeepEqual(view.Unit, detail.Unit) {
		t.Fatal(view, detail)
	}
	u := f.unit(t)
	if !reflect.DeepEqual(detail.Detail.Actions[0].Basis, u.Actions[0].Basis) || !reflect.DeepEqual(detail.Detail.Actions[0].Inspection, u.Actions[0].Inspection) || !reflect.DeepEqual(detail.Unit.Questions, u.Questions) || !reflect.DeepEqual(detail.Unit.Instructions, u.Instructions) {
		t.Fatal("business or original execution evidence lost")
	}
	if len(view.Target.Aliases) == 0 || view.SourcePage == nil || !view.SourcePage.Complete {
		t.Fatal("missing native lookup path", view)
	}
	source := view.SourcePage.Items[0]
	var got agentSourceView
	if err := cli([]string{"matters", "source", "--seq", fmt.Sprint(source.Seq), "--source", source.SourceRef}, &got); err != nil {
		t.Fatal(err)
	}
	original, err := f.h.store.readFollowupSource(f.h.id, source.Seq, source.SourceRef)
	if err != nil || !reflect.DeepEqual(got.Resolution, original.Resolution) || got.Source != original.Source {
		t.Fatal(got, err)
	}
	encoded, _ := json.Marshal(detail)
	for _, internal := range []string{`"credential_version"`, `"connection_id"`, `"generation"`, `"policy_token"`, `"access"`} {
		if bytes.Contains(encoded, []byte(internal)) {
			t.Fatal("internal admission data exposed", internal)
		}
	}
	// Both default/detail entry points enforce the same member boundary.
	wrong := apiRequest{ServiceInfo: f.svc.info, Caller: agentCallerIdentity(f.h.id), AgentID: "another", Followup: &followupRequest{followupCommand: f.q.followupCommand, Detail: true}}
	if _, err := f.svc.memberFollowups(f.h.ctx, wrong, "/v1/matters/read"); !errors.Is(err, ErrIdentity) {
		t.Fatal("detail bypassed identity gate", err)
	}
}

func TestAgentBProgressAndInstructionReceipts(t *testing.T) {
	h, svc, units, _ := conversationHarness(t, 1)
	cli := entryCLI(t, svc)
	u := units[0]
	change := agentProgressChange{followupCommand: conversationCommand(u, 1), Status: "in_progress", Summary: "核实准确目标", Next: "下一步", Evidence: "原证据"}
	var receipt agentFollowupReceipt
	if err := cli(entryChange(t, "matters", "update", change), &receipt); err != nil || receipt.RequiresRead || receipt.Reused || receipt.Changed.Summary == nil || *receipt.Changed.Summary != change.Summary || receipt.UnitVersion != u.Revision+1 {
		t.Fatal(receipt, err)
	}
	// Explicitly cleared values must appear, while unchanged fields are omitted.
	change.Revision = receipt.UnitVersion
	change.Next, change.Evidence = "", ""
	receipt = agentFollowupReceipt{}
	if err := cli(entryChange(t, "matters", "update", change), &receipt); err != nil || receipt.Changed.Status != nil || receipt.Changed.Summary != nil || receipt.Changed.Next == nil || *receipt.Changed.Next != "" || receipt.Changed.Evidence == nil || *receipt.Changed.Evidence != "" {
		t.Fatal(receipt, err)
	}
	source := postConversationReply(t, h, "deferred", "本条讨论只回复准确正文", 0)
	instruction := followupInstructionChange{followupCommand: change.followupCommand, SourceSeq: source.Seq, Intent: followupIntent{Operation: "discussion.reply", Body: "准确正文"}, Basis: "本次确切指令，跨回合办理"}
	instruction.Revision = receipt.UnitVersion
	if err := cli(entryChange(t, "matters", "instruct", instruction), &receipt); err != nil || len(receipt.Changed.Instructions) != 1 || receipt.Changed.Instructions[0].Source.Text != "本条讨论只回复准确正文" {
		t.Fatal(receipt, err)
	}
	saved := receipt.Changed.Instructions[0]
	status := agentInstructionStatusChange{followupCommand: instruction.followupCommand, InstructionRef: saved.Ref, InstructionVersion: saved.Version, Status: "revoked", Evidence: "用户明确撤销", SourceSeq: postConversationReply(t, h, "withdraw-deferred", "撤销上一条回复指令", 0).Seq}
	status.Revision = receipt.UnitVersion
	if err := cli(entryChange(t, "matters", "instruction-status", status), &receipt); err != nil || len(receipt.Changed.Instructions) != 1 || receipt.Changed.Instructions[0].Status != "revoked" || receipt.Changed.Instructions[0].Revocation.Text != "撤销上一条回复指令" {
		t.Fatal(receipt, err)
	}
	if err := cli(entryChange(t, "matters", "instruct", instruction), &receipt); err != nil || !receipt.Reused || !receipt.RequiresRead || receipt.Instruction == nil || receipt.Instruction.Status != "revoked" {
		t.Fatal("revoked instruction reactivated or hidden on replay", receipt, err)
	}
}

func TestAgentBNotifyPublicationConfirmedLinkPending(t *testing.T) {
	h, svc, units, _ := conversationHarness(t, 1)
	u := units[0]
	q := followupQuestionChange{followupCommand: conversationCommand(u, 1), Text: "@human 确认准确范围？"}
	original := h.store.ops
	writes := 0
	h.store.ops.rename = func(from, to string) error {
		if to == h.store.unitPath(h.id, u.ObjectRef, u.Ref) {
			writes++
			if writes == 2 {
				return errors.New("unit linkage save failed")
			}
		}
		return original.rename(from, to)
	}
	pending, err := svc.notifyAgentFollowup(h.ctx, h.id, q, u)
	h.store.ops = original
	if err != nil || pending.Publication != "published_link_pending" || pending.SendReceipt == nil || pending.Question == nil || pending.Question.Seq != 0 || !pending.RequiresRead {
		t.Fatal(pending, err)
	}
	// Snapshot replacement uncertainty retains the existing write freeze.
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	q.TurnNumber = 2
	saved, err := h.store.readFollowup(h.id, u.ObjectRef, u.Ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := svc.notifyAgentFollowup(h.ctx, h.id, q, saved)
	if err != nil || got.Publication != "published" || got.Question.Seq != pending.SendReceipt.Seq || !got.Reused {
		t.Fatal(got, err)
	}
	room, err := h.store.LoadRoom()
	if err != nil || len(room.FollowupNoticeReceipts) != 1 {
		t.Fatal("recovery repeated public question", err)
	}
}

func TestAgentBSourcePreservesFailedRefresh(t *testing.T) {
	h, svc, _, sources := conversationHarness(t, 1)
	cli := entryCLI(t, svc)
	original, err := h.store.readFollowupSource(h.id, sources[0].Seq, sources[0].SourceRef)
	if err != nil {
		t.Fatal(err)
	}
	svc.referenceResolver = func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return platform.TargetResolution{Status: platform.ResolutionUnavailable, ReasonCode: "permission_denied", Reason: "当前读取受限", CheckedAt: time.Now().UTC()}, platform.AccessContext{}, nil
	}
	var got agentSourceView
	if err := cli([]string{"matters", "resolve", "--seq", fmt.Sprint(original.Seq), "--source", original.SourceRef, "--turn", "1"}, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Resolution, original.Resolution) || got.LastAttempt == nil || got.LastAttempt.Status != platform.ResolutionUnavailable || got.LastAttempt.ReasonCode != "permission_denied" || got.SourceVersion != original.Revision+1 || got.TargetRef != original.TargetRef {
		t.Fatal("failed refresh erased verified mapping or appeared absent", got)
	}
	var source agentSourceView
	if err := cli([]string{"matters", "source", "--seq", fmt.Sprint(original.Seq), "--source", original.SourceRef}, &source); err != nil || !reflect.DeepEqual(source, got) {
		t.Fatal(source, err)
	}
	encoded, _ := json.Marshal(got)
	if bytes.Contains(encoded, []byte(`"access"`)) || !bytes.Contains(encoded, []byte(`gitlab:rest:iid`)) || !bytes.Contains(encoded, []byte(`gitlab:rest:id`)) || !bytes.Contains(encoded, []byte(`gitlab:rest:discussion`)) {
		t.Fatal("native namespaces missing or admission data exposed")
	}
}

func TestAgentBDiagnosticFieldsUseCurrentContract(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "invalid-scope", "以后按准确范围办理", 0)
	base := agentPolicyFixture(conversationRule(source.Seq))
	for _, tc := range []struct {
		field  string
		mutate func(*agentPolicyBody)
	}{
		{"body.scope.project", func(b *agentPolicyBody) { b.Scope.Project = "*" }},
		{"body.scope.description", func(b *agentPolicyBody) { b.Scope.Description = "unsupported" }},
		{"body.text", func(b *agentPolicyBody) { b.Text = strings.Repeat("x", maxPolicyTextBytes+1) }},
	} {
		q := base
		body := *base.Body
		q.Body = &body
		tc.mutate(q.Body)
		_, err := svc.changeAgentPolicy(h.ctx, h.id, agentCallerIdentity(h.id), q)
		var diagnostic *requestDiagnostic
		if !errors.As(err, &diagnostic) || diagnostic.Field != tc.field {
			t.Fatal(tc.field, err)
		}
	}
}
