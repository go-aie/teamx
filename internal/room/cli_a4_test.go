package room

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

func TestAgentPolicyUpdateSavedRecordAndReplay(t *testing.T) {
	h, svc, _, _ := conversationHarness(t, 1)
	cli := entryCLI(t, svc)
	source := postConversationReply(t, h, "suggest", "这次请用简短中文回复", 0)
	q := conversationPolicyChange{Change: PolicyChange{ID: "language", Kind: "preference", Status: "suggested", Text: "简短中文回复\n保留必要限制", SourceSeq: source.Seq, TurnNumber: 1}, Mode: "inferred_habit", Basis: "本次偏好只支持建议"}
	update := func(q conversationPolicyChange) agentPolicyUpdateWireTest {
		t.Helper()
		var got agentPolicyUpdateWireTest
		if err := cli(entryChange(t, "policy", "update", q), &got); err != nil {
			t.Fatal(err)
		}
		persisted, err := h.store.readPolicy(h.id)
		if err != nil {
			t.Fatal(err)
		}
		var saved PolicyRevision
		digest := policyConversationDigest(q)
		for _, r := range persisted.History {
			if r.Conversation != nil && r.Conversation.RequestDigest == digest {
				saved = r
			}
		}
		if saved.Version == 0 || !reflect.DeepEqual(got.Record, projectPolicyRecord(saved)) {
			t.Fatalf("receipt must contain the exact persisted revision: %+v / %+v", got.Record, saved)
		}
		// Removing the sole approved addition restores the complete old Agent view.
		actual := readViewJSON(t, got)
		delete(actual, "record")
		expected := agentPolicy(persisted.PolicySnapshot, q.Change.TurnNumber, time.Now())
		if !reflect.DeepEqual(actual, readViewJSON(t, expected)) {
			t.Fatal("existing policy result changed", actual)
		}
		return got
	}
	suggested := update(q)
	if len(suggested.Effective) != 0 || suggested.Record.Status != "suggested" || suggested.Record.ID != q.Change.ID || suggested.Record.Version != 1 || suggested.Record.Text != q.Change.Text || suggested.Record.Source.Text != "这次请用简短中文回复" {
		t.Fatal(suggested)
	}
	if retry := update(q); !reflect.DeepEqual(retry, suggested) {
		t.Fatal("identical save was not reused", retry)
	}
	receipt, err := svc.publishPolicySuggestion(h.ctx, h.id, 1, suggested.Record.ID, suggested.Record.Version)
	if err != nil {
		t.Fatal(err)
	}
	answer := postConversationReply(t, h, "confirm", "以后就这样", receipt.Seq)
	confirm := q
	confirm.Change.Version, confirm.Change.Revision, confirm.Change.SourceSeq = 1, 1, answer.Seq
	confirm.Change.Status, confirm.Mode, confirm.ProposalSeq, confirm.Basis = "active", "confirm_suggestion", receipt.Seq, "确认准确的已发布建议"
	active := update(confirm)
	if active.Record.Status != "active" || active.Record.Version != 2 || len(active.Effective) != 1 {
		t.Fatal(active)
	}
	revoke := confirm
	revoke.Change.Version, revoke.Change.Revision = 2, 2
	revoke.Change.Status, revoke.Mode, revoke.ProposalSeq, revoke.Basis = "revoked", "explicit_long_term", 0, "明确撤销"
	revoke.Change.SourceSeq = postConversationReply(t, h, "revoke", "撤销这条偏好", 0).Seq
	revoked := update(revoke)
	if revoked.Record.Status != "revoked" || revoked.Record.Version != 3 || len(revoked.Effective) != 0 {
		t.Fatal(revoked)
	}
	// Response recovery after history clear/restart returns the OLD request's
	// saved record, alongside the CURRENT policy revision/token/effective set.
	roomCall[ClearReceipt](t, h.roomHarness, ClearMessages{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ThroughSeq: revoke.Change.SourceSeq})
	reloadConversation(t, h)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	cli = entryCLI(t, svc)
	q.Change.TurnNumber = 2
	reused := update(q)
	if !reflect.DeepEqual(reused.Record, suggested.Record) || reused.Revision != 3 || reused.CurrentTurn != 2 || reused.PolicyToken != revoked.PolicyToken || len(reused.Effective) != 0 {
		t.Fatal("replay fabricated current record or reactivated policy", reused)
	}
	confirm.Change.TurnNumber = 2
	if reused := update(confirm); !reflect.DeepEqual(reused.Record, active.Record) || len(reused.Effective) != 0 {
		t.Fatal(reused)
	}
	var read map[string]any
	if err := cli([]string{"policy", "read"}, &read); err != nil {
		t.Fatal(err)
	}
	if _, ok := read["record"]; ok {
		t.Fatal("update field leaked into read")
	}
}

func TestAgentFollowupListVerifiedDisplayIsAdditive(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	for i := range 3 {
		source := referenceTestSource(fmt.Sprint(i))
		source.URL = "https://untrusted.example/guessed/999#note_456"
		source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: fmt.Sprintf("thread-%d", i)}
		source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(100 + i)}
		receiveReference(t, h, fmt.Sprint(i), source)
	}
	inboxRunning(t, h, 1, 3)
	svc := referenceTestService(h)
	calls := 0
	const canonicalURL = "https://gitlab.example.com/g/p/-/merge_requests/42"
	resolver := func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		calls++
		r, a, err := referenceTestResolver(ctx, id, source)
		if source.ItemKey == "0" {
			r.URL = canonicalURL
		}
		if source.ItemKey == "1" {
			r.Aliases = nil
		} // later event must not erase known labels/URL
		if source.ItemKey == "2" {
			r = platform.TargetResolution{Status: platform.ResolutionMismatch, ReasonCode: "identity_conflict", CheckedAt: time.Now()}
		}
		return r, a, err
	}
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, resolver)
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range page.Sources {
		if source.TargetRef == "" {
			continue
		}
		if _, err := h.store.ensureDiscussionFollowup(h.id, source.Seq, 1, source.TargetRef); err != nil {
			t.Fatal(err)
		}
	}
	// Preserve labels through failed refreshes, using saved verified evidence.
	_, err = svc.resolveFollowupEvent(h.ctx, h.id, 1, 1, true, func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return platform.TargetResolution{Status: platform.ResolutionUnavailable, CheckedAt: time.Now()}, platform.AccessContext{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Capture the previous summary contract, including stable target and unit IDs.
	units, err := h.store.followupUnitsLocked(h.id, "")
	if err != nil {
		t.Fatal(err)
	}
	baseline := map[string]map[string]any{}
	for _, u := range units {
		v := unitSummary(u)
		target, err := h.store.loadTargetLocked(h.id, u.ObjectRef, u.TargetRef)
		if err != nil {
			t.Fatal(err)
		}
		v.TargetLabel = target.Target.Object.Type + " " + target.Target.Object.ID.Value
		if target.Target.Discussion.Valid() {
			v.TargetLabel += " / " + target.Target.Discussion.Value
		}
		baseline[u.Ref] = readViewJSON(t, v)
	}
	svc.referenceResolver = func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		t.Fatal("list performed remote resolution")
		return platform.TargetResolution{}, platform.AccessContext{}, nil
	}
	cli := entryCLI(t, svc)
	seen := map[string]bool{}
	cursor := ""
	for {
		args := []string{"matters", "list", "--limit", "1"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		var got agentFollowupPage
		if err := cli(args, &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != 4 || len(got.Items) != 1 {
			t.Fatal(got)
		}
		item := got.Items[0]
		key := item.Ref
		if key == "" {
			key = item.SourceRef
		}
		if seen[key] {
			t.Fatal("duplicate page item")
		}
		seen[key] = true
		if item.Kind == "unresolved_source" {
			if item.ObjectLabel != "" || item.ObjectURL != "" || item.TargetRef != "" {
				t.Fatal("unverified display promoted", item)
			}
		} else {
			if item.ObjectLabel != "MergeRequest gitlab:rest:iid:42" || item.ObjectURL != canonicalURL {
				t.Fatal("missing verified display", item)
			}
			actual := readViewJSON(t, item)
			actual["ref"] = actual["unit_ref"]
			delete(actual, "unit_ref")
			actual["revision"] = actual["unit_version"]
			delete(actual, "unit_version")
			delete(actual, "object_label")
			delete(actual, "object_url")
			// Existing object aggregation remains intact and is separately asserted.
			if item.Kind == "object" {
				if item.Counts == nil || item.Counts.Units != 3 || item.AllCompleted == nil || *item.AllCompleted {
					t.Fatal(item)
				}
				delete(actual, "counts")
				delete(actual, "all_completed")
			}
			if !reflect.DeepEqual(actual, baseline[item.Ref]) {
				t.Fatal("existing list fields changed", actual, baseline[item.Ref])
			}
		}
		if got.Complete {
			if got.Next != "" {
				t.Fatal(got)
			}
			break
		}
		if got.Next == "" {
			t.Fatal("missing continuation")
		}
		cursor = got.Next
	}
	if len(seen) != 4 || calls != 3 {
		t.Fatal(seen, calls)
	}
	reloadConversation(t, h)
	cli = entryCLI(t, referenceTestService(h))
	var restored agentFollowupPage
	if err := cli([]string{"matters", "list", "--object", page.Followups[0].ObjectRef}, &restored); err != nil || restored.Total != 3 {
		t.Fatal(restored, err)
	}
	for _, item := range restored.Items {
		if item.ObjectURL != canonicalURL || item.ObjectLabel != "MergeRequest gitlab:rest:iid:42" {
			t.Fatal(item)
		}
	}
}

func TestAgentFollowupListMissingDisplayDoesNotGuess(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	source := referenceTestSource("missing")
	receiveReference(t, h, "missing", source)
	inboxRunning(t, h, 1, 1)
	_, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		r, a, err := referenceTestResolver(ctx, id, source)
		r.Aliases = nil
		return r, a, err
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || len(page.Items) != 1 {
		t.Fatal(page, err)
	}
	item := readViewJSON(t, page.Items[0])
	for _, key := range []string{"object_label", "object_url"} {
		if _, ok := item[key]; ok {
			t.Fatal("guessed display", item)
		}
	}
	if strings.Contains(page.Items[0].TargetLabel, "iid") {
		t.Fatal("REST ID became IID")
	}
	other := h.id
	other.RoomInstance += "other"
	if _, err := h.store.listFollowups(other, followupQuery{Limit: 25}); !errors.Is(err, ErrIdentity) {
		t.Fatal("cross-room list accepted", err)
	}
}

type agentPolicyUpdateWireTest struct {
	policyContext
	Effective []agentPolicyRecord `json:"effective"`
	Record    agentPolicyRecord   `json:"record"`
}
