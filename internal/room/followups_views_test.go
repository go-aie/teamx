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

func TestAgentFollowupListVerifiedDisplaySurvivesRefreshAndRecovery(t *testing.T) {
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
	// Preserve stable target and unit IDs alongside verified display metadata.
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
	seen := map[string]bool{}
	cursor := ""
	for {
		stored, err := svc.store.listFollowups(h.id, followupQuery{Limit: 1, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		got := projectFollowupPage(stored)
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
	restored, err := h.store.listFollowups(h.id, followupQuery{ObjectRef: page.Followups[0].ObjectRef, Limit: 25})
	if err != nil || restored.Total != 3 {
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
