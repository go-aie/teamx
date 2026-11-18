package room

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func waitInputOwner(t *testing.T, p *platformInputStorage, predicate func(inputOwnerState) bool) inputOwnerState {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		state, err := p.owner.Call[inputOwnerState](ctx, inspectInputOwner{p.id})
		if err != nil {
			t.Fatal(err)
		}
		if predicate(state) {
			return state
		}
		select {
		case <-ctx.Done():
			t.Fatal("owner state", state, ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}

func TestMemberInputInspectAndBusinessProgressDuringMergedMaintenance(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	if _, err := svc.inputStatus(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	first := make(chan error, 1)
	go func() {
		first <- p.run(h.ctx, "maintenance", func(context.Context) error {
			p.store.mu.Lock()
			defer p.store.mu.Unlock()
			close(entered)
			<-unblock
			return nil
		})
	}()
	<-entered
	var runs atomic.Int32
	const requests = 8
	maintenance := make(chan error, requests)
	order := make(chan string, 2)
	for range requests {
		go func() {
			maintenance <- p.run(h.ctx, "maintenance", func(context.Context) error { runs.Add(1); order <- "maintenance"; return nil })
		}()
	}
	waitInputOwner(t, p, func(s inputOwnerState) bool { return s.Waiters == requests+1 })
	business := make(chan error, 1)
	go func() {
		business <- p.run(h.ctx, "business", func(context.Context) error { order <- "business"; return nil })
	}()
	state := waitInputOwner(t, p, func(s inputOwnerState) bool { return s.Waiters == requests+2 })
	if state.Queued != 2 {
		t.Fatal("maintenance was not coalesced", state)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	for range 100 {
		if _, err := svc.inputStatus(ctx, id); err != nil {
			t.Fatal("Inspect waited for Store/maintenance", err)
		}
	}
	t.Logf("100 Inspect calls completed with maintenance blocked in %s", time.Since(started))
	release()
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-business; err != nil {
		t.Fatal(err)
	}
	for range requests {
		if err := <-maintenance; err != nil {
			t.Fatal(err)
		}
	}
	if first := <-order; first != "business" {
		t.Fatal("next maintenance overtook admitted business", first)
	}
	if runs.Load() != 1 {
		t.Fatal("dirty requests ran more than one next batch", runs.Load())
	}
}

func TestMemberInputCancellationAndCloseRetainOwnershipUntilSettled(t *testing.T) {
	h, p, _, _ := platformReaderFixture(t)
	ctx, cancel := context.WithCancel(h.ctx)
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	operation := make(chan error, 1)
	go func() {
		operation <- p.run(ctx, "accepted_write", func(context.Context) error { close(entered); <-unblock; return nil })
	}()
	<-entered
	cancel()
	select {
	case err := <-operation:
		t.Fatal("cancellation abandoned an admitted write", err)
	default:
	}
	closing, stop := context.WithTimeout(h.ctx, 30*time.Millisecond)
	defer stop()
	if err := h.store.closeInputOwners(closing); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if another, err := OpenStore(h.store.dir, h.id.RoomID); !errors.Is(err, ErrLocked) {
		if another != nil {
			_ = another.Close()
		}
		t.Fatal("directory released before task settlement", err)
	}
	if err := p.run(h.ctx, "late_write", func(context.Context) error { return nil }); !errors.Is(err, ErrClosing) {
		t.Fatal("closing owner admitted a new operation", err)
	}
	release()
	if err := <-operation; err != nil {
		t.Fatal(err)
	}
	if err := h.store.closeInputOwners(h.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMemberInputPreparationDoesNotHoldMutationSlotAcrossResolver(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	item := storedGitLab(id, "held-resolver", "events", "7", "42", "saved evidence")
	receivePlatform(t, p, item)
	s := startPlatformReader(t, h, p, svc, 1)
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	svc.referenceResolver = func(ctx context.Context, id MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		close(entered)
		<-unblock
		return referenceTestResolver(ctx, id, source)
	}
	stream := streamFor(t, s, "saved evidence")
	done := make(chan error, 1)
	go func() {
		_, _, cleanup, err := s.readStream(h.ctx, "held", []byte(`{"stream_ref":"`+stream+`"}`))
		if cleanup != nil {
			cleanup()
		}
		done <- err
	}()
	<-entered
	ctx, cancel := context.WithTimeout(h.ctx, 3*time.Second)
	defer cancel()
	if err := p.run(ctx, "maintenance", func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.inputStatus(ctx, id); err != nil {
		t.Fatal(err)
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestMemberInputQueueBackpressure(t *testing.T) {
	h, p, _, _ := platformReaderFixture(t)
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	done := make(chan error, memberInputQueueLimit)
	go func() {
		done <- p.run(h.ctx, "blocked", func(context.Context) error { close(entered); <-unblock; return nil })
	}()
	<-entered
	for range memberInputQueueLimit - 1 {
		go func() { done <- p.run(h.ctx, "business", func(context.Context) error { return nil }) }()
	}
	waitInputOwner(t, p, func(s inputOwnerState) bool { return s.Waiters == memberInputQueueLimit })
	if err := p.run(h.ctx, "overflow", func(context.Context) error { t.Error("overflow task executed"); return nil }); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	release()
	for range memberInputQueueLimit {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func TestMemberInputObservationFailurePreservesDurableReceipt(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	h.store.notificationAdapters = func(provider string) notification.Capabilities {
		cap := notificationCapabilities(provider)
		if provider == "gitlab" {
			cap.Admission = &notification.AdmissionCapability{Inspect: func(notification.Item, json.RawMessage) (notification.Admission, error) {
				return notification.Admission{}, ErrInvalid
			}}
		}
		return cap
	}
	item := storedGitLab(id, "receipt-before-view", "events", "7", "42", "saved evidence")
	first, err := p.Receive(item, time.Now())
	if err != nil || first.Seq != 1 {
		t.Fatal("derived view reversed a committed receipt", first, err)
	}
	if _, err := p.inspectHistory(); err == nil {
		t.Fatal("view fault was hidden")
	}
	again, err := p.Receive(item, time.Now())
	if err != nil || again != first {
		t.Fatal("receipt retry changed durable identity", again, err)
	}
	h.store.mu.Lock()
	h.store.notificationAdapters = notificationCapabilities
	h.store.mu.Unlock()
	if err := svc.reconcileMemberInputAdmission(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := p.inspectHistory(); err != nil {
		t.Fatal("background did not repair the derived view", err)
	}
}

func TestMemberInputReadLeaseDelaysCloseAndPreventsExecutionReplacement(t *testing.T) {
	h, p, _, id := platformReaderFixture(t)
	again, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil || again != p || again.owner != p.owner {
		t.Fatal("repeat open created another writer", err)
	}
	release, err := p.acquireRead(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := p.owner.Call[struct{}](h.ctx, beginInputExecution{id}); !errors.Is(err, ErrBusy) {
		t.Fatal("new generation overtook an output lease", err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Millisecond)
	defer cancel()
	if err := h.store.closeInputOwners(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("closed with an output lease still owned", err)
	}
	release()
	if err := h.store.closeInputOwners(h.ctx); err != nil {
		t.Fatal(err)
	}
}

func TestMemberInputRetirementPreventsWriterRecreationAndStaleInspection(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	if _, err := svc.inputStatus(h.ctx, id); err != nil {
		t.Fatal(err)
	}
	roomCall[MemberInfo](t, h, RemoveMember{id})
	if err := h.store.retireMemberInputs(h.ctx, id.MembershipID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.inputStorage(id); !errors.Is(err, ErrIdentity) {
		t.Fatal("retired owner reopened", err)
	}
	if _, err := p.inspectHistory(); !errors.Is(err, ErrIdentity) {
		t.Fatal("retired member exposed a stale view", err)
	}
}

func TestMemberInputDirectoryAndPreviewDoNotApplyConsumptionExclusions(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	id := s.storage.id
	receivePlatform(t, s.storage, storedGitLab(id, "saved", "events", "7", "42", "saved evidence"))
	agent, err := h.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	agent.TurnNumber, agent.Turn.Number = 2, 2
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	s = startPlatformReader(t, h, s.storage, s.service, 2)
	h.store.notificationAdapters = func(provider string) notification.Capabilities {
		cap := notificationCapabilities(provider)
		if provider == "gitlab" {
			cap.Admission = &notification.AdmissionCapability{Inspect: func(notification.Item, json.RawMessage) (notification.Admission, error) {
				return notification.Admission{Verified: true, Decision: notification.AdmissionExcluded, ReasonCode: "local_exclusion"}, nil
			}}
		}
		return cap
	}
	if _, err := s.listInputStreams(h.ctx, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.storage.previewHistory(""); err != nil {
		t.Fatal(err)
	}
	positions, err := s.storage.repository.PendingLocations(nil)
	if err != nil || len(positions) != 1 {
		t.Fatal("metadata/preview committed a consumption exclusion", positions, err)
	}
}

func TestMemberInputBackgroundRefreshObservesSharedAdmissionEvidence(t *testing.T) {
	h, p, svc, id := platformReaderFixture(t)
	var verified atomic.Bool
	h.store.notificationAdapters = func(provider string) notification.Capabilities {
		cap := notificationCapabilities(provider)
		if provider == "gitlab" {
			cap.Admission = &notification.AdmissionCapability{
				EvidencePath: func(platform.SourceRef) string { return "admission-fixture.json" },
				Inspect: func(notification.Item, json.RawMessage) (notification.Admission, error) {
					if !verified.Load() {
						return notification.Admission{}, nil
					}
					return notification.Admission{Verified: true, Decision: notification.AdmissionAllow}, nil
				},
			}
		}
		return cap
	}
	receivePlatform(t, p, storedGitLab(id, "unverified", "events", "7", "42", "saved evidence"))
	before, err := svc.inputStatus(h.ctx, id)
	if err != nil || before.Ready || before.Blocked != "source_admission_unverified" {
		t.Fatal(before, err)
	}
	// Another domain owner may have installed account/conversation evidence.
	// No new receive, network query or polling side effect is needed.
	verified.Store(true)
	if err := svc.reconcileMemberInputAdmission(h.ctx, p); err != nil {
		t.Fatal(err)
	}
	after, err := svc.inputStatus(h.ctx, id)
	if err != nil || !after.Ready || after.Pending != 1 || after.Blocked != "" {
		t.Fatal(after, err)
	}
}
