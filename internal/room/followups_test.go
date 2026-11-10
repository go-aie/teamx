package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

func referenceTestService(h *inboxHarness) *service {
	return &service{store: h.store, room: h.roomHarness.addr, rt: h.rt, info: ServiceInfo{RoomIdentity: h.id.RoomIdentity}, inboxes: map[string]actorx.Addr{h.id.MembershipID: h.addr}}
}

func referenceTestSource(key string) platform.SourceRef {
	s, _, _ := gitlabReferenceFixture()
	s.ItemKey = key
	s.URL = strings.Split(s.URL, "#")[0]
	s.SubscriptionID = strings.Repeat("a", 32)
	s.Reference.NoteID, s.Reference.DiscussionID = platform.ExternalID{}, platform.ExternalID{}
	return s
}

func referenceTestResolver(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
	_, access, _ := gitlabReferenceFixture()
	access.RoomIdentity, access.AgentID, access.MembershipID = id.RoomIdentity, id.AgentID, id.MembershipID

	target := platform.TargetIdentity{IdentityScope: access.Account.IdentityScope, Containers: []platform.NativeIdentity{{Type: "Project", ID: source.Reference.ProjectID}}, Object: platform.NativeIdentity{Type: source.Reference.Object.Type, ID: source.Reference.Object.ID}, Discussion: source.Reference.DiscussionID, Message: source.Reference.NoteID}
	r := platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: access.Account, ObjectVerified: true, DiscussionVerified: target.Discussion.Valid(), MessageVerified: target.Message.Valid(), Complete: true, CheckedAt: time.Now().UTC()}
	if source.Reference.Object.IID.Valid() {
		alias := target
		alias.Object.ID = source.Reference.Object.IID
		r.Aliases = []platform.TargetIdentity{alias}
	}
	var err error

	return r, access, err
}

func receiveReference(t *testing.T, h *inboxHarness, key string, source platform.SourceRef) InboxReceipt {
	t.Helper()
	q := h.receive(key)
	q.Item.Source = source
	return inboxCall[InboxReceipt](t, h, q)
}

func initReferenceTest(t *testing.T, h *inboxHarness) {
	t.Helper()
	if err := h.store.initReferenceFollowups(h.id); err != nil {
		t.Fatal(err)
	}
}

func referenceRunning(t *testing.T, h *inboxHarness, turn, start, end uint64) {
	t.Helper()
	if err := h.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: h.id, TurnNumber: turn,
		LastOfferedInboxSeq: end, LastOfferedInboxThrough: start, Turn: &TurnRecord{Number: turn, Status: "running", InboxStart: start, InboxSnapshotSeq: end}}); err != nil {
		t.Fatal(err)
	}
}

func TestReferenceFollowupsNativeDiscussionsOnDemandAndPagination(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	for i := 1; i <= 20; i++ {
		source := referenceTestSource(fmt.Sprint(i))
		source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: fmt.Sprintf("thread-%d", i)}
		source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: fmt.Sprint(100 + i)}
		receiveReference(t, h, fmt.Sprint(i), source)
	}
	inboxRunning(t, h, 1, 20)
	view, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	object := view.Followups[0]
	page, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || page.Total != 1 {
		t.Fatal("discovery eagerly created threads", page, err)
	}
	var first followupUnit
	for _, source := range view.Sources {
		u, err := h.store.ensureDiscussionFollowup(h.id, source.Seq, 1, source.TargetRef)
		if err != nil {
			t.Fatal(err)
		}
		again, err := h.store.ensureDiscussionFollowup(h.id, source.Seq, 1, source.TargetRef)
		if err != nil || !reflect.DeepEqual(u, again) {
			t.Fatal("non-idempotent thread", u, again, err)
		}
		if first.Ref == "" {
			first = u
		}
	}
	first, err = h.store.updateFollowup(h.id, followupUpdate{ObjectRef: first.ObjectRef, UnitRef: first.Ref, Revision: 1, TurnNumber: 1,
		Status: "waiting_user", Summary: "用法已答复，排期待确认", Next: "等待排期答复"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: first.ObjectRef, UnitRef: "my-semantic-task", Revision: 0, TurnNumber: 1, Status: "pending", Summary: "拆分诉求"}); err == nil {
		t.Fatal("semantic unit accepted")
	}
	if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: object.ObjectRef, UnitRef: object.Ref, Revision: 1, TurnNumber: 1, Status: "completed", Summary: "对象评论已回复", Evidence: "测试回执"}); err != nil {
		t.Fatal(err)
	}
	cursor := ""
	seen := map[string]bool{}
	for {
		p, err := h.store.listFollowups(h.id, followupQuery{ObjectRef: object.ObjectRef, Cursor: cursor, Limit: 7})
		if err != nil || p.Total != 21 || len(p.Items) > 7 {
			t.Fatal(p, err)
		}
		b, _ := json.Marshal(p)
		if len(b) > followupPageBytes {
			t.Fatal("unbounded page")
		}
		for _, v := range p.Items {
			if seen[v.Ref] {
				t.Fatal("duplicate page entry")
			}
			seen[v.Ref] = true
			if v.Kind == "object" && (v.Counts == nil || v.Counts.Active != 20 || v.Counts.WaitingUser != 1 || v.AllCompleted == nil || *v.AllCompleted) {
				t.Fatal("object falsely completed", v)
			}
		}
		if p.Complete {
			break
		}
		if p.Next == "" || p.Next == cursor {
			t.Fatal("missing continuation")
		}
		if _, err := h.store.listFollowups(h.id, followupQuery{Cursor: p.Next, Limit: 7}); !errors.Is(err, ErrRequest) {
			t.Fatal("cursor scope not checked", err)
		}
		cursor = p.Next
	}
	if len(seen) != 21 {
		t.Fatal(len(seen))
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: view.Batch.Receipt})
	// A second question/message in one native thread resolves to the same unit.
	source := referenceTestSource("21")
	source.Reference.DiscussionID = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "thread-1"}
	source.Reference.NoteID = platform.ExternalID{Namespace: "gitlab:rest:id", Value: "999"}
	receiveReference(t, h, "21", source)
	referenceRunning(t, h, 2, 20, 21)
	inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 2, Limit: 25})
	link, err := referenceTestService(h).resolveFollowupEvent(h.ctx, h.id, 21, 2, false, referenceTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	u, err := h.store.ensureDiscussionFollowup(h.id, 21, 2, link.TargetRef)
	if err != nil || u.Ref != first.Ref || u.Revision != 2 {
		t.Fatal("split same native thread", u, first, err)
	}
	// Exercise the byte bound independently of the item count. Full next steps
	// survive; the service must return a continuation rather than truncate them.
	h.store.mu.Lock()
	units, err := h.store.followupUnitsLocked(h.id, object.ObjectRef)
	h.store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range units {
		if unit.TargetRef == unit.ObjectRef {
			continue
		}
		if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: unit.ObjectRef, UnitRef: unit.Ref, Revision: unit.Revision, TurnNumber: 2,
			Status: "waiting_followup", Summary: strings.Repeat("s", 4096), Next: strings.Repeat("n", 4096)}); err != nil {
			t.Fatal(err)
		}
	}
	bounded, err := h.store.listFollowups(h.id, followupQuery{ObjectRef: object.ObjectRef, Limit: 25})
	if err != nil || bounded.Total != 21 || bounded.Complete || bounded.Next == "" || len(bounded.Items) >= 21 {
		t.Fatal(bounded, err)
	}
	for _, item := range bounded.Items {
		if item.Kind == "discussion" && len(item.Next) != 4096 {
			t.Fatal("next step silently truncated")
		}
	}
}

func TestReferenceFollowupsUnresolvedConsumedAndExplicitRetry(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	receiveReference(t, h, "1", referenceTestSource("1"))
	inboxRunning(t, h, 1, 1)
	svc := referenceTestService(h)
	calls := 0
	failed := func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		calls++
		return platform.TargetResolution{Status: platform.ResolutionUnavailable, ReasonCode: "permission_denied", Reason: "access denied; identity unconfirmed", CheckedAt: time.Now().UTC()}, platform.AccessContext{}, nil
	}
	view, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, failed)
	if err != nil || len(view.Followups) != 0 || view.Sources[0].ObjectRef != "" {
		t.Fatal(view, err)
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: view.Batch.Receipt})
	h.reload(t, true)
	inboxRunning(t, h, 2, 1)
	svc = referenceTestService(h)
	p, err := h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || p.Total != 1 || p.Items[0].ReasonCode != "permission_denied" || p.Items[0].Kind != "unresolved_source" {
		t.Fatal("lost consumed source", p, err)
	}
	link, err := svc.resolveFollowupEvent(h.ctx, h.id, 1, 2, false, failed)
	if err != nil || calls != 1 {
		t.Fatal("implicit retry", calls, err)
	}
	if _, err := h.store.ensureDiscussionFollowup(h.id, 1, 2, link.SourceRef); !errors.Is(err, ErrConflict) {
		t.Fatal("source became write/thread target", err)
	}
	resolved, err := svc.resolveFollowupEvent(h.ctx, h.id, 1, 2, true, referenceTestResolver)
	if err != nil || resolved.ObjectRef == "" || resolved.SourceRef != link.SourceRef {
		t.Fatal(resolved, err)
	}
	p, err = h.store.listFollowups(h.id, followupQuery{Limit: 25})
	if err != nil || p.Total != 1 || p.Items[0].Kind != "object" {
		t.Fatal(p, err)
	}
}

func TestReferenceFollowupsPartialIdentityAndOwnership(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	source := referenceTestSource("1")
	receiveReference(t, h, "1", source)
	inboxRunning(t, h, 1, 1)
	inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 25})
	r, a, err := referenceTestResolver(h.ctx, h.id, source)
	if err != nil {
		t.Fatal(err)
	}
	r.Status, r.Complete = platform.ResolutionPartial, false
	r.Target.Discussion = platform.ExternalID{Namespace: "gitlab:rest:discussion", Value: "unverified"}
	r.ReasonCode, r.Reason = "permission_denied", "object verified, discussions inaccessible"
	link, err := h.store.recordFollowupResolution(h.id, 1, 1, 0, r, a)
	if err != nil || link.ObjectRef == "" || link.TargetRef != link.ObjectRef {
		t.Fatal(link, err)
	}
	if _, err := h.store.recordFollowupResolution(h.id, 1, 0, link.Revision, r, a); !errors.Is(err, ErrRequest) {
		t.Fatal("missing turn admitted", err)
	}
	if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: link.ObjectRef, UnitRef: referenceDigest("unit_", link.ObjectRef), Revision: 1, Status: "pending", Summary: "missing turn"}); !errors.Is(err, ErrRequest) {
		t.Fatal("missing update turn admitted", err)
	}
	if _, err := h.store.ensureDiscussionFollowup(h.id, 1, 1, link.TargetRef); !errors.Is(err, ErrConflict) {
		t.Fatal("unverified thread created", err)
	}
	for _, mode := range []string{"account", "member", "tenant", "instance", "type"} {
		t.Run(mode, func(t *testing.T) {
			bad, _ := json.Marshal(r)
			var q platform.TargetResolution
			_ = json.Unmarshal(bad, &q)
			access := a
			switch mode {
			case "account":
				q.Account.ID.Value = "different"
			case "member":
				access.MembershipID = "different"
			case "tenant":
				q.Target.Tenant = platform.ExternalID{Namespace: "tenant", Value: "different"}
			case "instance":
				q.Target.Instance = "https://other.example.com"
			case "type":
				q.Target.Object.Type = "Issue"
			}
			if _, err := h.store.recordFollowupResolution(h.id, 1, 1, link.Revision, q, access); err == nil {
				t.Fatal("mismatched evidence accepted")
			}
		})
	}
	other := h.id
	other.AgentID, other.MembershipID = "B", "generation-B"
	if err := h.store.initReferenceFollowups(other); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.readFollowup(other, link.ObjectRef, referenceDigest("unit_", link.ObjectRef)); err == nil {
		t.Fatal("cross-member reference accepted")
	}
	if _, err := h.store.readFollowupSource(h.id, 1, "src_"+strings.Repeat("0", 64)); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}

func TestReferenceFollowupsPartialCommitRepairsWithoutReset(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	source := referenceTestSource("1")
	receiveReference(t, h, "1", source)
	inboxRunning(t, h, 1, 1)
	inboxCall[InboxBatchPage](t, h, ReadInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Limit: 25})
	r, a, _ := referenceTestResolver(h.ctx, h.id, source)
	create := h.store.ops.createTemp
	h.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if filepath.Base(dir) == "events" {
			return nil, errors.New("injected link save failure")
		}
		return create(dir, pattern)
	}
	if _, err := h.store.recordFollowupResolution(h.id, 1, 1, 0, r, a); err == nil {
		t.Fatal("expected save failure")
	}
	h.store.ops.createTemp = create
	objectRef := targetReference(h.id, objectIdentity(*r.Target))
	unitRef := referenceDigest("unit_", objectRef)
	if _, err := h.store.updateFollowup(h.id, followupUpdate{ObjectRef: objectRef, UnitRef: unitRef, Revision: 1, TurnNumber: 1, Status: "waiting_followup", Summary: "已开展工作", Next: "保留后续"}); err != nil {
		t.Fatal(err)
	}
	link, err := h.store.recordFollowupResolution(h.id, 1, 1, 0, r, a)
	if err != nil || link.ObjectRef != objectRef {
		t.Fatal(link, err)
	}
	u, err := h.store.readFollowup(h.id, objectRef, unitRef)
	if err != nil || u.Revision != 2 || u.Status != "waiting_followup" {
		t.Fatal("progress reset", u, err)
	}
	// An ambiguous event commit freezes the whole Store until reopened.
	syncDir := h.store.ops.syncDir
	h.store.ops.syncDir = func(dir string) error {
		if filepath.Base(dir) == "events" {
			return errors.New("injected post-rename failure")
		}
		return syncDir(dir)
	}
	if _, err := h.store.recordFollowupResolution(h.id, 1, 1, 1, r, a); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal(err)
	}
	if _, err := h.store.recordFollowupResolution(h.id, 1, 1, 1, r, a); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("uncertain writes not frozen", err)
	}
	h.store.ops.syncDir = syncDir
	h.reload(t, true)
	link, err = h.store.readFollowupSource(h.id, 1, link.SourceRef)
	if err != nil || link.Revision != 2 {
		t.Fatal("did not reconcile committed link", link, err)
	}
}

func TestReferenceFollowupsGitlabLocalAndVerifiedAlias(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	first := referenceTestSource("1")
	second := referenceTestSource("2")
	second.Reference.Object.ID = platform.ExternalID{} // IID alone requires verified mapping
	receiveReference(t, h, "1", first)
	receiveReference(t, h, "2", second)
	inboxRunning(t, h, 1, 2)
	remoteCalls := 0
	resolve := func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		canonical, access, _ := referenceTestResolver(ctx, id, first)

		result, err := gitlab.ResolveTarget(ctx, platform.TargetLocator{Source: &source}, access, nil)
		if !source.Reference.Object.ID.Valid() {
			remoteCalls++
			result = canonical
			err = nil
		}

		return result, access, err
	}
	page, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, resolve)
	if err != nil || remoteCalls != 1 || len(page.Followups) != 1 || page.Sources[0].ObjectRef != page.Sources[1].ObjectRef {
		t.Fatal(page, remoteCalls, err)
	}
	if page.Entries[1].Item.Source.Reference.Object.ID.Valid() {
		t.Fatal("raw source rewritten to canonical identity")
	}
	// A later failed account/query check preserves native identity and records
	// the failure separately; it is not evidence that the target disappeared.
	link, err := referenceTestService(h).resolveFollowupEvent(h.ctx, h.id, 1, 1, true, func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return platform.TargetResolution{}, platform.AccessContext{}, ErrConflict
	})
	if err != nil || link.ObjectRef != page.Sources[0].ObjectRef || link.LastAttempt == nil || sourceSummary(link).Status != platform.ResolutionUnavailable {
		t.Fatal(link, err)
	}
	stored, err := h.store.readFollowupSource(h.id, 1, link.SourceRef)
	if err != nil || stored.LastAttempt == nil || !stored.Resolution.ObjectVerified {
		t.Fatal(stored, err)
	}
}

func TestReferenceFollowupsOpaqueStringIdentityIsolation(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	source := platform.SourceRef{Provider: "fixture", Instance: "tenant-service", Tenant: platform.ExternalID{Namespace: "tenant", Value: "team-blue"}, Channel: "messages", SubscriptionID: strings.Repeat("b", 32), ItemKey: "1"}
	receiveReference(t, h, "1", source)
	inboxRunning(t, h, 1, 1)
	target := platform.TargetIdentity{IdentityScope: platform.IdentityScope{Provider: source.Provider, Instance: source.Instance, Tenant: source.Tenant}, Object: platform.NativeIdentity{Type: "Document", ID: platform.ExternalID{Namespace: "document", Value: "opaque/doc:blue"}}}
	access := platform.AccessContext{RoomIdentity: h.id.RoomIdentity, AgentID: h.id.AgentID, MembershipID: h.id.MembershipID,
		Account: platform.AccountIdentity{IdentityScope: target.IdentityScope, ID: platform.ExternalID{Namespace: "account", Value: "reader-blue"}}, PlatformID: "fixture", Generation: "v1", PlatformVersion: 1, CredentialVersion: 1}
	r := platform.TargetResolution{Status: platform.ResolutionResolved, ObjectVerified: true, Complete: true, Target: &target, Account: access.Account, CheckedAt: time.Now().UTC()}
	view, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, func(context.Context, MemberIdentity, platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return r, access, nil
	})
	if err != nil || len(view.Followups) != 1 {
		t.Fatal(view, err)
	}
	ref := view.Sources[0].ObjectRef
	// Nil and empty containers compare identically through persistence.
	target.Containers = []platform.NativeIdentity{}
	access.CredentialVersion++
	if _, err := h.store.recordFollowupResolution(h.id, 1, 1, 1, r, access); err != nil {
		t.Fatal("credential change or empty containers renamed object", err)
	}
	for _, mode := range []string{"provider", "instance", "tenant", "type", "container", "member", "room"} {
		candidate, member := target, h.id
		switch mode {
		case "provider":
			candidate.Provider = "other"
		case "instance":
			candidate.Instance = "other"
		case "tenant":
			candidate.Tenant.Value = "other"
		case "type":
			candidate.Object.Type = "other"
		case "container":
			candidate.Containers = []platform.NativeIdentity{{Type: "space", ID: platform.ExternalID{Namespace: "space", Value: "other"}}}
		case "member":
			member.MembershipID = "other"
		case "room":
			member.RoomInstance = "other"
		}
		if targetReference(member, candidate) == ref {
			t.Fatal("reference namespace collision", mode)
		}
	}
}

func TestReferenceFollowupsConsumedDependencyLossFailsClosed(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	receiveReference(t, h, "1", referenceTestSource("1"))
	inboxRunning(t, h, 1, 1)
	page, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver)
	if err != nil {
		t.Fatal(err)
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	h.stop(t)
	u := page.Followups[0]
	if err := os.Remove(h.store.unitPath(h.id, u.ObjectRef, u.Ref)); err != nil {
		t.Fatal(err)
	}
	if _, err := SpawnAgentInbox(h.rt, h.store, h.id, InboxLimits{}); err == nil {
		t.Fatal("consumed missing unit silently recovered")
	}
	if _, err := referenceTestService(h).resolveFollowupEvent(h.ctx, h.id, 1, 1, true, referenceTestResolver); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing dependency overwritten", err)
	}
}
