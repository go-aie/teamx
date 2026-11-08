package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestAgentRejectsOldContractsAndBranchMixing(t *testing.T) {
	for _, tc := range []struct{ command, op, flag, payload string }{
		{"actions", "execute", "--request", `{"provider":"unverified"}`},
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

func TestAgentNotifyReceiptsAndUncertainPublication(t *testing.T) {
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

func TestAgentReadDetailsPreserveBusinessAndNativeEvidence(t *testing.T) {
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

func TestAgentProgressAndInstructionReceipts(t *testing.T) {
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

func TestAgentNotifyPublicationConfirmedLinkPending(t *testing.T) {
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

func TestAgentSourcePreservesFailedRefresh(t *testing.T) {
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
