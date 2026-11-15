package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestPolicyIndependentRecordEditsAcrossHumanAndAgent(t *testing.T) {
	h, id, svc := policyTestService(t)
	cli := entryCLI(t, svc)
	seq := roomCall[PostReceipt](t, h, h.post(humanCaller, "two-rules", "以后简短中文；先给结论")).Seq
	// Both records were observed absent in the same file snapshot.
	a := agentPolicyChange{Type: "save", ID: "a", SourceSeq: seq, TurnNumber: 1, Basis: "明确长期要求", Body: &agentPolicyBody{Text: "简短中文"}}
	b := PolicyChange{ID: "b", Status: "active", Text: "先给结论", SourceText: "以后先给结论"}
	var first agentPolicyUpdateWireTest
	if err := cli(entryChange(t, "policy", "update", a), &first); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.changePolicy(id, humanCaller, b); err != nil {
		t.Fatal("independent absent rule blocked", err)
	}
	// Edit both from the same observed record versions, then race their saves.
	nextSeq := roomCall[PostReceipt](t, h, h.post(humanCaller, "amend-two", "以后中文简明并保留限制；结论后给理由")).Seq
	a.Version, a.SourceSeq, a.Body.Text = 1, nextSeq, "中文简明并保留限制"
	b.Version, b.Text = 1, "结论后给理由"
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- cli(entryChange(t, "policy", "update", a), nil) }()
	go func() { defer wg.Done(); _, err := h.store.changePolicy(id, humanCaller, b); errs <- err }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("independent edit blocked", err)
		}
	}
	v, err := h.store.readPolicy(id)
	if err != nil || v.Revision != 4 || len(v.Effective) != 2 {
		t.Fatal(v, err)
	}
	// The same record still conflicts, including after withdrawal.
	b.Status = "revoked"
	if _, err := h.store.changePolicy(id, humanCaller, b); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit accepted", err)
	}
	b.Version = 2
	if _, err := h.store.changePolicy(id, humanCaller, b); err != nil {
		t.Fatal(err)
	}
	b.Status = "active"
	if _, err := h.store.changePolicy(id, humanCaller, b); !errors.Is(err, ErrConflict) {
		t.Fatal("stale reactivation accepted", err)
	}
}

func TestPolicyLegacyDigestReplayAfterUnrelatedChangesAndRevocation(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	source := postConversationReply(t, h, "legacy-policy", "以后按文档回复", 0)
	original := conversationRule(source.Seq)
	// Save an unrelated rule first: legacy digest uses history position, not
	// this rule's version. Install the old wire evidence as a persisted fixture.
	unrelated := policyRule()
	unrelated.ID = "unrelated"
	if _, err := h.store.changePolicy(h.id, humanCaller, unrelated); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), original); err != nil {
		t.Fatal(err)
	}
	h.store.mu.Lock()
	p, path, err := h.store.loadPolicyLocked(h.id, true)
	if err == nil {
		e := p.History[1].Conversation
		e.DigestVersion = 0
		e.RequestDigest = legacyPolicyConversationDigest(original, 1)
		err = h.store.save(path, p, maxPrivateFileBytes)
	}
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	answer := postConversationReply(t, h, "legacy-revoke", "撤销文档回复规则", 0)
	revoke := original
	revoke.Change.Version = 1
	revoke.Change.SourceSeq = answer.Seq
	revoke.Change.Status = "revoked"
	if _, err := svc.referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), revoke); err != nil {
		t.Fatal(err)
	}
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: answer.Seq})
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	original.Change.TurnNumber = 2
	got, err := referenceTestService(h).referencePolicyChange(h.ctx, h.id, agentCallerIdentity(h.id), original)
	if err != nil || got.Revision != 3 || got.Record.Version != 1 || got.Record.Conversation.DigestVersion != 0 || len(got.Effective) != 1 || got.Effective[0].ID != "unrelated" {
		t.Fatal(got, err)
	}
	before, _ := h.store.readPolicy(h.id)
	altered := original
	altered.Change.Text += " altered"
	if _, err := h.store.changeConversationPolicy(h.id, agentCallerIdentity(h.id), altered); !errors.Is(err, ErrConflict) {
		t.Fatal("unproven legacy request appended", err)
	} else {
		var d *requestDiagnostic
		if !errors.As(err, &d) || d.Outcome != "not_applied" || d.Record == nil || d.Record.ID != original.Change.ID || d.Record.Version != 1 {
			t.Fatal("missing original evidence", err)
		}
		w := httptest.NewRecorder()
		writeAPIError(w, err)
		var failure apiError
		_ = json.Unmarshal(w.Body.Bytes(), &failure)
		if failure.Record == nil || failure.Record.Version != 1 {
			t.Fatal(w.Body.String())
		}
	}
	after, _ := h.store.readPolicy(h.id)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("legacy retry changed history")
	}
}

func TestPolicyIntentDigestNormalizesRuntimeAndExpiry(t *testing.T) {
	expiry := time.Date(2099, 1, 1, 8, 0, 0, 0, time.FixedZone("CST", 8*3600))
	a := conversationRule(18)
	a.Change.ExpiresAt = &expiry
	b := a
	b.Change.TurnNumber = 123
	utc := expiry.UTC()
	b.Change.ExpiresAt = &utc
	if policyConversationDigest(a) != policyConversationDigest(b) {
		t.Fatal("equivalent intent has different digest")
	}
	for _, change := range []func(*conversationPolicyChange){
		func(q *conversationPolicyChange) { q.Change.Version++ },
		func(q *conversationPolicyChange) { q.Change.SourceSeq++ },
		func(q *conversationPolicyChange) { q.Change.Text += " limit" },
		func(q *conversationPolicyChange) { q.Basis += " different" },
		func(q *conversationPolicyChange) { q.ProposalSeq++ },
	} {
		q := b
		change(&q)
		if policyConversationDigest(q) == policyConversationDigest(a) {
			t.Fatal("business input omitted from digest")
		}
	}
}

func TestPolicyWriteRejectsFileRevisionIncludingZeroAndNull(t *testing.T) {
	h, id, svc := policyTestService(t)
	cli := entryCLI(t, svc)
	seq := roomCall[PostReceipt](t, h, h.post(humanCaller, "reject-file-revision", "以后简短中文")).Seq
	q := agentPolicyChange{Type: "save", ID: "style", SourceSeq: seq, TurnNumber: 1, Basis: "明确长期要求", Body: &agentPolicyBody{Text: "简短中文"}}
	for _, value := range []any{0, 1, nil} {
		wire := map[string]any{}
		raw, _ := json.Marshal(q)
		_ = json.Unmarshal(raw, &wire)
		wire["revision"] = value
		raw, _ = json.Marshal(wire)
		if err := cli([]string{"policy", "update", "--change", string(raw)}, nil); err == nil {
			t.Fatal("Agent file revision accepted", value)
		}
		human := map[string]any{"id": "style", "version": 0, "status": "active", "text": "简短中文", "source_text": "长期要求", "revision": value}
		req := map[string]any{"service_epoch": svc.info.ServiceEpoch, "room_id": id.RoomID, "room_instance": id.RoomInstance, "caller": humanCaller, "agent_id": id.AgentID, "membership_id": id.MembershipID, "policy": human}
		body, _ := json.Marshal(req)
		recorder := httptest.NewRecorder()
		svc.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/policy/update", bytes.NewReader(body)))
		var failure apiError
		_ = json.Unmarshal(recorder.Body.Bytes(), &failure)
		status := recorder.Code
		if status != 400 || failure.Field != "policy.revision" {
			t.Fatal(status, failure)
		}
	}
	if v, err := h.store.readPolicy(id); err != nil || v.Revision != 0 {
		t.Fatal(v, err)
	}
}
