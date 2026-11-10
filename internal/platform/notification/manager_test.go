package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// A second, non-GitLab schema exercises the common engine without Room or any
// concrete provider. Its progress is deliberately unrelated to date pagination.
type fixtureState struct {
	Target   Target
	Retired  bool
	Format   int
	K        Key
	Version  uint64
	Enabled  bool
	Platform string
	Offset   int
	Seen     map[string]bool
	Inbox    map[string]Item
	RuntimeState
}

func (s fixtureState) Metadata() Metadata {
	return Metadata{Target: s.Target, Retired: s.Retired, Key: s.K, Version: s.Version, Enabled: s.Enabled, PlatformID: s.Platform, Observed: len(s.Seen), Pending: len(s.Inbox), DeliveryError: s.DeliveryErrorCode}
}
func (s fixtureState) Validate(owner platform.RoomIdentity) error {
	if s.Format != 7 || s.K.Owner != owner || s.K.SubscriptionID == "" || s.Version == 0 || s.Seen == nil || s.Inbox == nil {
		return ErrInvalid
	}
	return s.RuntimeState.Validate(s.Enabled)
}
func (s fixtureState) copy() fixtureState {
	data, _ := json.Marshal(s)
	var next fixtureState
	_ = json.Unmarshal(data, &next)
	return next
}
func (s fixtureState) CloneState() State { return s.copy() }
func (s fixtureState) PendingItems() []Item {
	out := []Item{}
	for _, item := range s.Inbox {
		out = append(out, item)
	}
	return out
}
func (s fixtureState) Notification(key string) (Item, bool) {
	item, ok := s.Inbox[key]
	return item, ok
}
func (s fixtureState) Acknowledge(key string) State {
	next := s.copy()
	delete(next.Inbox, key)
	next.DeliveryErrorCode = ""
	return next
}
func (s fixtureState) WithDeliveryError(code string) State {
	next := s.copy()
	next.DeliveryErrorCode = code
	return next
}
func (s fixtureState) InvalidateCredential() (State, error) {
	next := s.copy()
	next.Version++
	next.RuntimeState.InvalidateCredential(next.Enabled)
	return next, nil
}
func (s fixtureState) ScopeFailed(err error) State {
	next := s.copy()
	next.RuntimeState.ScopeFailed(err)
	return next
}

type fixtureSource struct {
	id   SourceID
	step func(context.Context, StepHost) (time.Duration, bool)
}

func (s fixtureSource) ID() SourceID { return s.id }
func (fixtureSource) Schema() int    { return 7 }
func (s fixtureSource) Decode(data []byte, owner platform.RoomIdentity) (State, error) {
	var state fixtureState
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&state) != nil || dec.Decode(new(any)) != io.EOF || state.Validate(owner) != nil || state.K.Source != s.id {
		return nil, ErrInvalid
	}
	return state, nil
}
func (s fixtureSource) NewRunner(h StepHost) Runner { return fixtureRunner{h: h, step: s.step} }

type fixtureRunner struct {
	h    StepHost
	step func(context.Context, StepHost) (time.Duration, bool)
}

func (r fixtureRunner) Step(ctx context.Context) (time.Duration, bool) {
	if r.step == nil {
		return time.Second, true
	}
	return r.step(ctx, r.h)
}

type notificationFixture struct {
	mu                             sync.Mutex
	m                              *Manager
	dir                            string
	gate, saveErr, errorAfterWrite error
	scopeRevision, credential      string
	ready                          bool
	deliveries                     map[string]int
	order                          []string
}

func newNotificationFixture(t *testing.T, sources ...Source) *notificationFixture {
	t.Helper()
	f := &notificationFixture{dir: t.TempDir(), scopeRevision: "revision-1", credential: "credential-1", ready: true, deliveries: map[string]int{}}
	f.m = New(Host{
		Gate: &f.mu,
		Check: func(writing bool) error {
			if writing {
				return f.gate
			}
			return nil
		},
		Directory: func(owner platform.RoomIdentity) (string, error) {
			if !f.ready {
				return "", ErrIdentity
			}
			return filepath.Join(f.dir, owner.RoomID), nil
		},
		EnsureDirectory: func(path string) error { return os.MkdirAll(path, 0700) },
		Save: func(path string, value any, _ int64) error {
			if f.saveErr != nil {
				return f.saveErr
			}
			data, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if err = os.WriteFile(path, data, 0600); err != nil {
				return err
			}
			if f.errorAfterWrite != nil {
				f.gate = f.errorAfterWrite
				return f.errorAfterWrite
			}
			return nil
		},
		Admit: func(ctx context.Context, _ State) (Scope, error) {
			if err := ctx.Err(); err != nil {
				return Scope{}, err
			}
			if f.gate != nil {
				return Scope{}, f.gate
			}
			if !f.ready {
				return Scope{}, ErrIdentity
			}
			return Scope{Revision: f.scopeRevision, CredentialStamp: f.credential, Current: true}, nil
		},
		Deliver: func(_ context.Context, _ Metadata, item Item) error {
			// Receipt semantics: repeating a stable delivery ID is one public message.
			if f.deliveries[item.DeliveryID] == 0 {
				f.order = append(f.order, item.Key)
				f.deliveries[item.DeliveryID]++
			}
			return nil
		},
	}, sources...)
	t.Cleanup(func() {
		if err := f.m.Stop(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return f
}
func (f *notificationFixture) path(key Key) string {
	return filepath.Join(f.dir, key.Owner.RoomID, key.Source.Provider+"."+key.Source.Channel+"."+key.SubscriptionID+".json")
}
func fixtureSubscription(id string, source SourceID) fixtureState {
	return fixtureState{Format: 7, K: Key{Owner: platform.RoomIdentity{RoomID: "room", RoomInstance: "instance"}, Source: source, SubscriptionID: id}, Version: 1, Enabled: true, Platform: "connection", Seen: map[string]bool{}, Inbox: map[string]Item{}, RuntimeState: RuntimeState{Status: "running"}}
}
func (f *notificationFixture) save(t *testing.T, state State) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.m.SaveLocked(state); err != nil {
		t.Fatal(err)
	}
}
func (f *notificationFixture) state(t *testing.T, key Key) fixtureState {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.m.StateLocked(key)
	if !ok {
		t.Fatal("missing subscription")
	}
	return s.(fixtureState)
}
func signal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("notification ownership did not settle")
	}
}

func TestSubscriptionsInSameRoomAreIsolatedAndCancellationJoinsAll(t *testing.T) {
	slowEntered, canceled, release, fastCommitted := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	finish := func() { once.Do(func() { close(release) }) }
	source := fixtureSource{id: SourceID{Provider: "fixture", Channel: "updates"}}
	source.step = func(ctx context.Context, h StepHost) (time.Duration, bool) {
		state, _, err := h.Read(ctx)
		if err != nil {
			return 0, true
		}
		next := state.(fixtureState)
		next.Offset++
		if next.K.SubscriptionID == "slow" {
			close(slowEntered)
			<-ctx.Done()
			close(canceled)
			<-release
			if err = h.Commit(next); err == nil {
				t.Error("late canceled candidate accepted")
			}
			return 0, true
		}
		if err = h.Commit(next); err != nil {
			t.Error(err)
		}
		close(fastCommitted)
		return 0, true
	}
	f := newNotificationFixture(t, source)
	t.Cleanup(finish) // Release before manager cleanup, even if an assertion fails.
	slow, fast := fixtureSubscription("slow", source.id), fixtureSubscription("fast", source.id)
	slow.Platform, fast.Platform = "platform-slow", "platform-fast"
	f.save(t, slow)
	f.save(t, fast)
	if err := f.m.Start(); err != nil {
		t.Fatal(err)
	}
	signal(t, slowEntered)
	signal(t, fastCommitted)
	if f.state(t, fast.K).Offset != 1 || f.state(t, slow.K).Offset != 0 {
		t.Fatal("cross-subscription progress")
	}
	f.mu.Lock()
	f.m.CancelRoomLocked(slow.K.Owner)
	done := f.m.RoomDoneLocked(slow.K.Owner)
	f.mu.Unlock()
	signal(t, canceled)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := Wait(ctx, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("reported settlement before query returned", err)
	}
	finish()
	if err := Wait(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	if f.state(t, slow.K).Offset != 0 {
		t.Fatal("canceled progress persisted")
	}
}

func TestCandidatesAreAtomicAndUnknownCommitLatches(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-write", true: "after-write"}[uncertain], func(t *testing.T) {
			source := fixtureSource{id: SourceID{Provider: "fixture", Channel: "updates"}}
			f := newNotificationFixture(t, source)
			initial := fixtureSubscription("one", source.id)
			f.save(t, initial)
			session := &session{m: f.m, key: initial.K, version: 1}
			state, _, err := session.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			next := state.(fixtureState)
			next.Offset = 1
			next.Seen["item"] = true
			next.Inbox["item"] = Item{Key: "item", DeliveryID: "stable"}
			injected := errors.New("injected snapshot boundary")
			if uncertain {
				f.errorAfterWrite = injected
			} else {
				f.saveErr = injected
			}
			if err = session.Commit(next); !errors.Is(err, injected) {
				t.Fatal(err)
			}
			current := f.state(t, initial.K)
			if current.Offset != 0 || len(current.Seen) != 0 || len(current.Inbox) != 0 {
				t.Fatal("partial candidate became visible")
			}
			raw, _ := os.ReadFile(f.path(initial.K))
			var envelope snapshot
			json.Unmarshal(raw, &envelope)
			decoded, err := source.Decode(envelope.State, initial.K.Owner)
			if err != nil {
				t.Fatal(err)
			}
			if (decoded.(fixtureState).Offset == 1) != uncertain {
				t.Fatal("wrong durable boundary")
			}
			if uncertain {
				f.saveErr = nil
				f.errorAfterWrite = nil
				f.mu.Lock()
				err = f.m.SaveLocked(initial)
				f.mu.Unlock()
				if !errors.Is(err, injected) {
					t.Fatal("bypassed shared unknown-commit gate")
				}
			}
		})
	}
}

func TestScopeRevisionAndAdmissionFenceBeforeCommit(t *testing.T) {
	for _, change := range []string{"revision", "credential", "deletion", "concurrent-state", "cancellation"} {
		t.Run(change, func(t *testing.T) {
			source := fixtureSource{id: SourceID{Provider: "fixture", Channel: "updates"}}
			f := newNotificationFixture(t, source)
			initial := fixtureSubscription("one", source.id)
			f.save(t, initial)
			session := &session{m: f.m, key: initial.K, version: 1}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			state, _, err := session.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next := state.(fixtureState)
			next.Offset = 99
			switch change {
			case "revision":
				f.scopeRevision = "revision-2"
			case "credential":
				f.credential = "credential-2"
			case "deletion":
				f.ready = false
			case "concurrent-state":
				fresh := initial.copy()
				fresh.Offset = 7
				f.save(t, fresh)
			case "cancellation":
				cancel()
			}
			if err = session.Commit(next); err == nil {
				t.Fatal("stale candidate accepted")
			}
			if change == "concurrent-state" || change == "revision" {
				if !errors.Is(err, ErrBusy) {
					t.Fatal("transient conflict must retry", err)
				}
				latest, _, readErr := session.Read(ctx)
				if readErr != nil {
					t.Fatal(readErr)
				}
				retry := latest.(fixtureState)
				retry.Offset++
				if err = session.Commit(retry); err != nil {
					t.Fatal("fresh candidate failed after transient conflict", err)
				}
				if change == "concurrent-state" && f.state(t, initial.K).Offset != 8 {
					t.Fatal("retry lost concurrent state")
				}
			}
			current := f.state(t, initial.K)
			if current.Offset == 99 {
				t.Fatal("late progress committed")
			}
		})
	}
}

func TestDeliveryAcknowledgesOnlyAfterCommitAndKeepsStableOrder(t *testing.T) {
	source := fixtureSource{id: SourceID{Provider: "fixture", Channel: "updates"}}
	f := newNotificationFixture(t, source)
	state := fixtureSubscription("one", source.id)
	at := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	state.Inbox["later"] = Item{Key: "later", DeliveryID: "delivery-later", CreatedAt: at.Add(time.Second), Order: "1"}
	state.Inbox["second"] = Item{Key: "second", DeliveryID: "delivery-second", CreatedAt: at, Order: "2"}
	state.Inbox["first"] = Item{Key: "first", DeliveryID: "delivery-first", CreatedAt: at, Order: "1"}
	f.save(t, state)
	f.saveErr = errors.New("injected ack failure")
	if err := f.m.Deliver(context.Background(), state.K, 1); err == nil {
		t.Fatal("ack failure lost")
	}
	if len(f.state(t, state.K).Inbox) != 3 || len(f.order) != 1 {
		t.Fatal("ack removed unconfirmed entry")
	}
	f.saveErr = nil
	for range 3 {
		if err := f.m.Deliver(context.Background(), state.K, 1); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(f.order, ",") != "first,second,later" || len(f.state(t, state.K).Inbox) != 0 {
		t.Fatal("ordering or stable receipt changed", f.order)
	}
}

func TestSourceSchemaReloadAndPlatformInvalidationAreIsolated(t *testing.T) {
	a, b := fixtureSource{id: SourceID{Provider: "fixture", Channel: "alpha"}}, fixtureSource{id: SourceID{Provider: "fixture", Channel: "beta"}}
	f := newNotificationFixture(t, a, b)
	one, two := fixtureSubscription("one", a.id), fixtureSubscription("two", b.id)
	two.Platform = "other-connection"
	f.save(t, one)
	f.save(t, two)
	f.mu.Lock()
	err := f.m.InvalidatePlatformLocked(one.Platform)
	f.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if f.state(t, one.K).Version != 2 || !f.state(t, one.K).QueryPaused() || f.state(t, two.K).Version != 1 {
		t.Fatal("rotation crossed subscription scope")
	}
	f.mu.Lock()
	delete(f.m.states, one.K)
	delete(f.m.states, two.K)
	err = f.m.LoadLocked(one.K.Owner)
	f.mu.Unlock()
	if err != nil || f.state(t, one.K).Version != 2 {
		t.Fatal("reload changed identity/progress", err)
	}
	data, _ := os.ReadFile(f.path(one.K))
	var envelope snapshot
	json.Unmarshal(data, &envelope)
	bad := strings.Replace(string(envelope.State), `"Format":7`, `"Format":8`, 1)
	if _, err = a.Decode([]byte(bad), one.K.Owner); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted wrong progress schema")
	}
	if _, err = b.Decode(envelope.State, one.K.Owner); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted another source schema")
	}
}

func TestQueryDeadlineCanCommitBackoffWithLiveSubscriptionContext(t *testing.T) {
	completed := make(chan struct{})
	source := fixtureSource{id: SourceID{Provider: "fixture", Channel: "updates"}}
	source.step = func(ctx context.Context, h StepHost) (time.Duration, bool) {
		if _, ok := ctx.Deadline(); ok {
			t.Error("request deadline leaked into subscription lifecycle")
		}
		state, _, err := h.Read(ctx)
		if err != nil {
			t.Error(err)
			close(completed)
			return 0, true
		}
		request, cancel := QueryContext(ctx, time.Millisecond)
		<-request.Done()
		cancel()
		if !errors.Is(request.Err(), context.DeadlineExceeded) || ctx.Err() != nil {
			t.Error("request timeout canceled subscription")
		}
		next := state.(fixtureState)
		next.RuntimeState.Failure(h.Now(), Failure{Called: true}, h.Jitter)
		if err = h.Commit(next); err != nil {
			t.Error("timeout could not persist backoff", err)
		}
		close(completed)
		return time.Second, false
	}
	f := newNotificationFixture(t, source)
	initial := fixtureSubscription("one", source.id)
	f.save(t, initial)
	if err := f.m.Start(); err != nil {
		t.Fatal(err)
	}
	signal(t, completed)
	if err := f.m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := f.state(t, initial.K)
	if after.Status != "backoff" || after.ConsecutiveFailures != 1 || after.RetryAt.IsZero() {
		t.Fatal("timeout stopped subscription without retry state")
	}
}

func TestSnapshotEnvelopeSeparatesChannelsAndRejectsInvalidRecords(t *testing.T) {
	a := fixtureSource{id: SourceID{Provider: "fixture", Channel: "alpha"}}
	b := fixtureSource{id: SourceID{Provider: "fixture", Channel: "beta"}}
	f := newNotificationFixture(t, a, b)
	one, two := fixtureSubscription("same-id", a.id), fixtureSubscription("same-id", b.id)
	one.Offset, two.Offset = 11, 22
	f.save(t, one)
	f.save(t, two)
	if f.path(one.K) == f.path(two.K) {
		t.Fatal("channels share a snapshot path")
	}
	reload := func() error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.m.states = map[Key]subscription{}
		return f.m.LoadLocked(one.K.Owner)
	}
	if err := reload(); err != nil || len(f.m.StatesLocked(one.K.Owner)) != 2 || f.state(t, one.K).Offset != 11 || f.state(t, two.K).Offset != 22 {
		t.Fatal("channel state lost on reload", err)
	}
	original, err := os.ReadFile(f.path(one.K))
	if err != nil {
		t.Fatal(err)
	}
	for _, corruption := range []struct {
		name   string
		change func(snapshot) []byte
	}{
		{"unknown source", func(s snapshot) []byte { s.Channel = "unknown"; b, _ := json.Marshal(s); return b }},
		{"wrong schema", func(s snapshot) []byte { s.Schema++; b, _ := json.Marshal(s); return b }},
		{"missing envelope", func(s snapshot) []byte { return s.State }},
		{"wrong source codec", func(s snapshot) []byte { s.Channel = "beta"; b, _ := json.Marshal(s); return b }},
		{"unknown field", func(s snapshot) []byte { return append([]byte(`{"unknown":true,`), original[1:]...) }},
	} {
		t.Run(corruption.name, func(t *testing.T) {
			var s snapshot
			if err := json.Unmarshal(original, &s); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(f.path(one.K), corruption.change(s), 0600); err != nil {
				t.Fatal(err)
			}
			if err := reload(); !errors.Is(err, ErrInvalid) {
				t.Fatal("accepted invalid snapshot", err)
			}
			if err := os.WriteFile(f.path(one.K), original, 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	renamed := filepath.Join(filepath.Dir(f.path(one.K)), "fixture.alpha.other-id.json")
	if err := os.Rename(f.path(one.K), renamed); err != nil {
		t.Fatal(err)
	}
	if err := reload(); !errors.Is(err, ErrInvalid) {
		t.Fatal("filename did not bind subscription identity", err)
	}
	badKey := one.K
	badKey.SubscriptionID = "../escape"
	if _, err := f.m.StatePathLocked(badKey); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted path traversal", err)
	}
}

func TestDeliveryIdentityIncludesFullSubscriptionKey(t *testing.T) {
	key := fixtureSubscription("subscription", SourceID{Provider: "fixture", Channel: "alpha"}).K
	original := DeliveryID(key, "opaque|item")
	for _, mutate := range []func(*Key){
		func(k *Key) { k.Owner.RoomID = "other-room" },
		func(k *Key) { k.Owner.RoomInstance = "other-instance" },
		func(k *Key) { k.Source.Provider = "other-provider" },
		func(k *Key) { k.Source.Channel = "other-channel" },
		func(k *Key) { k.SubscriptionID = "other-subscription" },
	} {
		next := key
		mutate(&next)
		if DeliveryID(next, "opaque|item") == original {
			t.Fatal("delivery identity crossed subscription boundary")
		}
	}
	if DeliveryID(key, "other-item") == original || DeliveryID(key, "opaque|item") != original {
		t.Fatal("unstable item identity")
	}
}
