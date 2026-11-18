package inputstream

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sharedFixture(t *testing.T) (*Repository, Inbox, Member, Member, *diskPersistence) {
	t.Helper()
	inbox := Inbox{"room", "instance", "project"}
	a := Member{"room", "instance", "a"}
	b := Member{"room", "instance", "b"}
	disk := &diskPersistence{}
	r, err := OpenSharedRepository(t.TempDir(), inbox, "events", approvalCodec{provider: "approval"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	return r, inbox, a, b, disk
}
func sharedAppend(t *testing.T, r *Repository, key, stream string) Receipt {
	t.Helper()
	payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: stream, Node: "node", Text: key})
	receipt, err := r.AppendOnce("approval", AppendRequest{DeliveryID: key, Channel: "changes", CreatedAt: time.Now().Add(-10 * 24 * time.Hour), ReceivedAt: time.Now(), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
func sharedRequest(t *testing.T, r *Repository, inbox Inbox, g SharedGrant, page string, loc Location, from uint64) SharedPageRequest {
	t.Helper()
	physical, err := r.StreamAt(loc)
	if err != nil {
		t.Fatal(err)
	}
	return SharedPageRequest{Inbox: inbox, Member: g.Member, Grant: g.ID, Partition: loc.Partition, Stream: physical, Page: page, Execution: Execution{"session", 1}, From: from, Limit: 25}
}
func TestSharedBodyIndependentGrantsFixedPageAndReopen(t *testing.T) {
	r, inbox, a, b, disk := sharedFixture(t)
	ga, _ := r.JoinShared(a)
	gb, _ := r.JoinShared(b)
	one := sharedAppend(t, r, "one", "A")
	two := sharedAppend(t, r, "two", "B")
	three := sharedAppend(t, r, "three", "A")
	loc := Location{one.Partition, one.Seq}
	q := sharedRequest(t, r, inbox, ga, "page-A", loc, 0)
	page, err := r.FixSharedPage(q)
	if err != nil || !reflect.DeepEqual(page.Entries, []Location{loc, {three.Partition, three.Seq}}) {
		t.Fatal(page, err)
	}
	sharedAppend(t, r, "four", "A")
	replay, err := r.FixSharedPage(q)
	if err != nil || !reflect.DeepEqual(page, replay) {
		t.Fatal(replay, err)
	}
	for _, change := range []func(*SharedPageRequest){func(q *SharedPageRequest) { q.From = 1 }, func(q *SharedPageRequest) { q.Grant = gb.ID }, func(q *SharedPageRequest) { q.Limit = 1 }, func(q *SharedPageRequest) { q.Execution.Turn++ }, func(q *SharedPageRequest) { q.Stream = "other" }} {
		other := q
		change(&other)
		if _, err := r.FixSharedPage(other); !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	c := NewSharedConsumption(inbox, ga)
	if err := c.Confirm(page, "session-proof"); err != nil {
		t.Fatal(err)
	}
	if err := SaveSharedConsumption(r.dir, c, disk); err != nil {
		t.Fatal(err)
	}
	c, err = LoadSharedConsumption(r.dir, a, inbox, ga.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Confirm(page, "session-proof"); err != nil {
		t.Fatal("same-page replay after checkpoint advance", err)
	}
	facts := ReleaseSet{Revision: "source", Records: map[uint64]ReleaseFacts{1: {Revision: "r", SourceConfirmed: true, ReceiptReleased: true}}, Retention: time.Hour}
	ca := MemberReleaseCandidate{inbox, a, ga.ID, q.Stream, loc, r.parts["approval"].records[1].Digest}
	cb := ca
	cb.Member = b
	cb.Grant = gb.ID
	evidence := []MemberRelease{c.CheckRelease(ca, true), NewSharedConsumption(inbox, gb).CheckRelease(cb, true)}
	f, err := r.OverlaySharedProtection(facts, evidence)
	if err != nil || f.Records[1].Consumed || !f.Records[1].InFlight {
		t.Fatal(f, err)
	}
	if err := r.ReleaseSharedPage(page, "session-proof", false); err != nil {
		t.Fatal(err)
	}
	r2, err := OpenSharedRepository(r.dir, inbox, "events", approvalCodec{provider: "approval"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	f, err = r2.OverlaySharedProtection(facts, evidence)
	if err != nil || f.Records[1].Consumed || f.Records[1].InFlight {
		t.Fatal(f, err)
	}
	recs, err := r2.SharedRecords(b, gb.ID, 4)
	if err != nil || len(recs) != 4 {
		t.Fatal(recs, err)
	}
	if c.Through[q.Stream] != page.Through || c.Through[r2.parts["approval"].streamRefs[two.Seq]] != 0 {
		t.Fatal(c)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "agents", inbox.ID, "capacity.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("shared quota split out of publication root", err)
	}
}
func TestSharedJoinRedeliveryAndRevocationHandoff(t *testing.T) {
	r, inbox, a, b, _ := sharedFixture(t)
	ga, _ := r.JoinShared(a)
	one := sharedAppend(t, r, "one", "A")
	gb, _ := r.JoinShared(b)
	again := sharedAppend(t, r, "one", "A")
	if again != one {
		t.Fatal(again, one)
	}
	recs, err := r.SharedRecords(b, gb.ID, 1)
	if err != nil || len(recs) != 0 {
		t.Fatal(recs, err)
	}
	q := sharedRequest(t, r, inbox, ga, "page", Location{one.Partition, 1}, 0)
	page, err := r.FixSharedPage(q)
	if err != nil {
		t.Fatal(err)
	}
	if err = r.AuthorizeSharedHandoff(page); err != nil {
		t.Fatal(err)
	}
	if err = r.CloseSharedGrant(ga.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = r.FixSharedPage(q); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	if err = r.AuthorizeSharedHandoff(page); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
	root, _ := r.SharedState()
	if root.Grants[ga.ID].Status != "closing" {
		t.Fatal(root)
	}
	if err = r.ReleaseSharedPage(page, "exact-session-proof", false); err != nil {
		t.Fatal(err)
	}
	if err = r.ReleaseSharedPage(page, "exact-session-proof", false); err != nil {
		t.Fatal(err)
	}
	root, _ = r.SharedState()
	if root.Grants[ga.ID].Status != "closed" {
		t.Fatal(root)
	}
	newGrant, err := r.JoinShared(a)
	if err != nil || newGrant.ID == ga.ID || newGrant.Start != 1 {
		t.Fatal(newGrant, err)
	}
	if _, err = r.FixSharedPage(q); !errors.Is(err, ErrIdentity) {
		t.Fatal(err)
	}
}
func TestSharedMissingMemberEvidenceAndExpiryWithoutPage(t *testing.T) {
	r, inbox, a, _, disk := sharedFixture(t)
	g, _ := r.JoinShared(a)
	receipt := sharedAppend(t, r, "one", "A")
	loc := Location{receipt.Partition, receipt.Seq}
	physical, _ := r.StreamAt(loc)
	candidate := MemberReleaseCandidate{inbox, a, g.ID, physical, loc, r.parts["approval"].records[1].Digest}
	c := NewSharedConsumption(inbox, g)
	if e := c.CheckRelease(candidate, false); e.State != "held" {
		t.Fatal(e)
	}
	if _, err := LoadSharedConsumption(r.dir, a, inbox, g.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := c.Skip(candidate, "expired_input"); err != nil {
		t.Fatal(err)
	}
	if err := SaveSharedConsumption(r.dir, c, disk); err != nil {
		t.Fatal(err)
	}
	c, err := LoadSharedConsumption(r.dir, a, inbox, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if e := c.CheckRelease(candidate, false); e.State != "released" {
		t.Fatal(e)
	}
	f := ReleaseSet{Revision: "source", Records: map[uint64]ReleaseFacts{1: {Revision: "source-proof", SourceConfirmed: true, ReceiptReleased: true}}}
	f, err = r.OverlaySharedProtection(f, []MemberRelease{c.CheckRelease(candidate, false)})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := r.PrepareReclaim("approval", f, time.Now())
	if err != nil || plan.Through != 1 {
		t.Fatal(plan, err)
	}
	if _, err := r.CommitReclaim("approval", plan, f); err != nil {
		t.Fatal(err)
	}
	if u, err := r.InspectUsage(); err != nil || u.Records != 0 {
		t.Fatal(u, err)
	}
	if _, err := OpenSharedRepository(r.dir, inbox, "events", approvalCodec{provider: "approval"}, disk); err != nil {
		t.Fatal(err)
	}
}
func TestSharedProtectionAndBodyCrashBoundaries(t *testing.T) {
	for _, boundary := range []string{"reserved", "body_synced", "published", "settled", "shared_protected"} {
		t.Run(boundary, func(t *testing.T) {
			r, inbox, a, _, disk := sharedFixture(t)
			g, _ := r.JoinShared(a)
			if boundary == "shared_protected" {
				sharedAppend(t, r, "first", "A")
			}
			r.fault = func(at string) error {
				if at == boundary {
					return errors.New("process died")
				}
				return nil
			}
			payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: "A", Node: "n"})
			q := AppendRequest{DeliveryID: "crash", Channel: "changes", CreatedAt: time.Now(), ReceivedAt: time.Now(), Payload: payload}
			if boundary == "shared_protected" {
				_, _ = r.FixSharedPage(sharedRequest(t, r, inbox, g, "page", Location{r.parts["approval"].state.Spec.Ref, 1}, 0))
			} else {
				if _, err := r.AppendOnce("approval", q); !errors.Is(err, ErrCommitUnknown) {
					t.Fatal(err)
				}
			}
			next, err := OpenSharedRepository(r.dir, inbox, "events", approvalCodec{provider: "approval"}, disk)
			if err != nil {
				t.Fatal(err)
			}
			if boundary == "shared_protected" {
				root, _ := next.SharedState()
				if root.Pages["page"].State != "protected" {
					t.Fatal(root)
				}
				return
			}
			receipt, err := next.AppendOnce("approval", q)
			if err != nil || receipt.Seq != 1 {
				t.Fatal(receipt, err)
			}
			if u, _ := next.InspectUsage(); u.Records != 1 {
				t.Fatal(u)
			}
		})
	}
}
func TestSharedMemberIntentAndPlatformBudget(t *testing.T) {
	body, inbox, a, _, disk := sharedFixture(t)
	g, _ := body.JoinShared(a)
	receipt := sharedAppend(t, body, "one", "A")
	member, err := OpenRepository(body.dir, a, []Codec{approvalCodec{provider: "approval"}}, disk)
	if err != nil {
		t.Fatal(err)
	}
	e := Execution{"session", 1}
	if err = member.Begin(e); err != nil {
		t.Fatal(err)
	}
	request := sharedRequest(t, body, inbox, g, "unused", Location{receipt.Partition, 1}, 0)
	q := ReadRequest{Turn: 1, Stream: SharedStreamRef(inbox, a, g.ID, request.Stream), ID: "read", Limit: 100}
	request, err = member.ReserveSharedIntent(e, q, request)
	if err != nil || request.Limit != 25 {
		t.Fatal(request, err)
	}
	member, err = OpenRepository(body.dir, a, []Codec{approvalCodec{provider: "approval"}}, disk)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := member.ReserveSharedIntent(e, q, request)
	if err != nil || !reflect.DeepEqual(replay, request) {
		t.Fatal(replay, err)
	}
	page, err := body.FixSharedPage(request)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := member.ReserveShared(e, q, page)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Kind != "shared" || attempt.Shared == nil {
		t.Fatal(attempt)
	}
	state, _ := member.State(e)
	if state.Used != 1 || state.SharedIntent != nil {
		t.Fatal(state)
	}
	if err := member.Prepare(e, attempt.ID, json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	if err := member.Handoff(e, attempt.ID); err != nil {
		t.Fatal(err)
	}
	c := NewSharedConsumption(inbox, g)
	if err := c.Confirm(page, attempt.Proof()); err != nil {
		t.Fatal(err)
	}
	if err := SaveSharedConsumption(body.dir, c, disk); err != nil {
		t.Fatal(err)
	}
	if err := member.SettleOwned(e, attempt.ID, attempt.Proof()); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRepository(body.dir, a, []Codec{approvalCodec{provider: "approval"}}, disk); err != nil {
		t.Fatal(err)
	}
}

func TestSharedJoiningGrantRecoveryAndMissingEvidence(t *testing.T) {
	r, inbox, a, _, disk := sharedFixture(t)
	g, err := r.PrepareSharedGrant(a)
	if err != nil || g.Status != "joining" {
		t.Fatal(g, err)
	}
	payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: "A", Node: "node"})
	q := AppendRequest{DeliveryID: "before-active", Channel: "changes", CreatedAt: time.Now(), ReceivedAt: time.Now(), Payload: payload}
	if _, err := r.AppendOnce("approval", q); !errors.Is(err, ErrBlocked) {
		t.Fatal(err)
	}
	r, err = OpenSharedRepository(r.dir, inbox, "events", approvalCodec{provider: "approval"}, disk)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.PrepareSharedGrant(a)
	if err != nil || again.ID != g.ID {
		t.Fatal(again, err)
	}
	c := NewSharedConsumption(inbox, g)
	if err := SaveSharedConsumption(r.dir, c, disk); err != nil {
		t.Fatal(err)
	}
	if err := r.ActivateSharedGrant(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AppendOnce("approval", q); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(SharedConsumptionPath(r.dir, a, inbox)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSharedConsumption(r.dir, a, inbox, g.ID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	facts := ReleaseSet{Revision: "source", Records: map[uint64]ReleaseFacts{1: {Revision: "source", SourceConfirmed: true, ReceiptReleased: true}}}
	facts, err = r.OverlaySharedProtection(facts, nil)
	if err != nil || facts.Records[1].Consumed {
		t.Fatal("missing member evidence released body", facts, err)
	}
}

func TestSharedInboxPartitionDoesNotAliasPrivateMemberName(t *testing.T) {
	r, inbox, a, _, disk := sharedFixture(t)
	if _, err := r.JoinShared(a); err != nil {
		t.Fatal(err)
	}
	receipt := sharedAppend(t, r, "shared", "A")
	private, err := OpenRepository(r.dir, Member{inbox.RoomID, inbox.RoomInstance, inbox.ID}, []Codec{approvalCodec{provider: "approval"}}, disk)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: "A", Node: "node"})
	other, err := private.AppendOnce("approval", AppendRequest{DeliveryID: "private", Channel: "changes", CreatedAt: time.Now(), ReceivedAt: time.Now(), Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Partition == other.Partition {
		t.Fatal("body ownership aliased a private Membership")
	}
}

func TestSharedIntentBlocksOtherPagesAndExecutionRotation(t *testing.T) {
	body, inbox, a, _, disk := sharedFixture(t)
	g, _ := body.JoinShared(a)
	receipt := sharedAppend(t, body, "one", "A")
	member, err := OpenRepository(body.dir, a, []Codec{approvalCodec{provider: "approval"}}, disk)
	if err != nil {
		t.Fatal(err)
	}
	e := Execution{"session", 1}
	if err := member.Begin(e); err != nil {
		t.Fatal(err)
	}
	request := sharedRequest(t, body, inbox, g, "", Location{receipt.Partition, 1}, 0)
	q := ReadRequest{Turn: 1, ID: "shared-intent", Stream: SharedStreamRef(inbox, a, g.ID, request.Stream)}
	if _, err := member.ReserveSharedIntent(e, q, request); err != nil {
		t.Fatal(err)
	}
	if err := member.Barrier(e); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := member.Begin(Execution{"new-session", 2}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, err := member.ReserveOwned(e, ReadRequest{Turn: 1, ID: "room", Stream: "room"}, "room", OwnedPage{Snapshot: Snapshot{Partition: "room"}}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if _, err := member.BindCall(e, "other-call", "other-stream", 1); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
}
func TestSharedInboxByteCapacityRetainsPublishedBody(t *testing.T) {
	policy := PlatformPolicy()
	policy.MaxBytes = 1500
	codec := approvalCodec{provider: "approval", policy: policy}
	inbox := Inbox{"room", "instance", "project"}
	disk := &diskPersistence{}
	r, err := OpenSharedRepository(t.TempDir(), inbox, "events", codec, disk)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.JoinShared(Member{"room", "instance", "member"}); err != nil {
		t.Fatal(err)
	}
	first := sharedAppend(t, r, "one", "A")
	payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: "A", Node: "node", Text: "two"})
	if _, err := r.AppendOnce("approval", AppendRequest{DeliveryID: "two", Channel: "changes", CreatedAt: time.Now(), ReceivedAt: time.Now(), Payload: payload}); !errors.Is(err, ErrCapacity) {
		t.Fatal(err)
	}
	if u, err := r.InspectUsage(); err != nil || u.Records != 1 {
		t.Fatal(u, err)
	}
	r, err = OpenSharedRepository(r.dir, inbox, "events", codec, disk)
	if err != nil {
		t.Fatal(err)
	}
	if replay := sharedAppend(t, r, "one", "A"); replay != first {
		t.Fatal(replay, first)
	}
}

func TestSharedInboxFixedPageUsesExistingByteBudget(t *testing.T) {
	r, inbox, a, _, _ := sharedFixture(t)
	g, _ := r.JoinShared(a)
	for i := range 18 {
		payload, _ := json.Marshal(approvalPayload{Instance: "https://approval.example", Account: "reader", Case: "A", Node: "node", Text: strings.Repeat("x", 64<<10)})
		if _, err := r.AppendOnce("approval", AppendRequest{DeliveryID: Ref("large_", i), Channel: "changes", CreatedAt: time.Now(), ReceivedAt: time.Now(), Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	location := Location{r.parts["approval"].state.Spec.Ref, 1}
	request := sharedRequest(t, r, inbox, g, "large-page", location, 0)
	page, err := r.FixSharedPage(request)
	if err != nil {
		t.Fatal(err)
	}
	bytes := 0
	for _, l := range page.Entries {
		bytes += r.parts["approval"].offsets[l.Seq].Length
	}
	if bytes > MaxReadPageBytes || len(page.Entries) == 0 || len(page.Entries) >= 18 || page.Remaining != 18-len(page.Entries) {
		t.Fatal(page, bytes)
	}
}
