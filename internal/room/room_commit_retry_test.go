package room

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
)

// Exercise the real HTTP/CLI paths with a commit held after replacement and
// before its receipt. Other members can read, but writes return explicit busy.
func TestRoomTurnsConcurrentSendAndConfirmRecoverBusy(t *testing.T) {
	svc, client := testService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var reqs []apiRequest
	var pages []ReadPage
	for _, name := range []string{"A", "B", "C"} {
		reqs = append(reqs, addFixtureMemberCLI(t, svc, client, name))
	}
	seed := apiRequest{ServiceInfo: svc.info, Caller: humanCaller, ClientMessageID: "seed", Text: "initial"}
	var receipt PostReceipt
	if err := client.request(ctx, "/v1/send", &seed, &receipt); err != nil {
		t.Fatal(err)
	}
	for _, req := range reqs {
		req.Mode = ReadUnread
		var page ReadPage
		if err := client.request(ctx, "/v1/read", &req, &page); err != nil {
			t.Fatal(err)
		}
		pages = append(pages, page)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	defer onceRelease(release)()
	var once sync.Once
	real := svc.store.ops.syncDir
	svc.store.mu.Lock()
	svc.store.ops.syncDir = func(dir string) error { once.Do(func() { close(entered); <-release }); return real(dir) }
	svc.store.mu.Unlock()
	pending := seed
	pending.ClientMessageID = "mid-read"
	pending.Text = "arrived after page snapshot"
	first := make(chan error, 1)
	go func() { var out PostReceipt; first <- client.request(ctx, "/v1/send", &pending, &out) }()
	<-entered
	// Confirm the exact contention the parallel tool calls will encounter.
	if _, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: svc.info.RoomIdentity, Caller: humanCaller, ClientMessageID: "probe", Text: "probe"}); !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	done := make(chan error, 6)
	for i, req := range reqs {
		go func(i int, req apiRequest) {
			req.ReadReceipt = pages[i].ReadReceipt
			var confirmed ReadConfirmation
			err := client.request(ctx, "/v1/read/confirm", &req, &confirmed)
			if err == nil && confirmed.ReadThrough != 1 {
				err = fmt.Errorf("confirmation consumed concurrent input: %+v", confirmed)
			}
			done <- err
		}(i, req)
		go func(i int, req apiRequest) {
			req.ClientMessageID = fmt.Sprint("delivery-", i)
			req.Text = fmt.Sprint("member reply ", i)
			var out PostReceipt
			err := client.request(ctx, "/v1/send", &req, &out)
			if err == nil {
				var duplicate PostReceipt
				err = client.request(ctx, "/v1/send", &req, &duplicate)
				if err == nil && duplicate != out {
					err = fmt.Errorf("message ID retry changed receipt")
				}
			}
			done <- err
		}(i, req)
	}
	// Release the first commit while all member operations are in flight.
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	for range 6 {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
	state, err := svc.room.Call[MemberList](ctx, ListMembers{svc.info.RoomIdentity})
	if err != nil || state.LatestSeq != 5 {
		t.Fatal("lost or duplicated concurrent delivery", state, err)
	}
	for _, member := range state.Members {
		if member.ReadThrough != 1 {
			t.Fatal("cursor advanced beyond confirmed content", member)
		}
	}
}

// Observe retries without sleeps and prove that uncertain failures are never
// retried. The probe returns busy while a controlled commit owns the boundary.
type commitRetryProbe struct {
	attempts chan struct{}
	failure  error
}

func (p commitRetryProbe) Bindings() []actorx.Binding {
	return []actorx.Binding{actorx.Handle(func(actorx.Context, PostMessage) (PostReceipt, error) {
		p.attempts <- struct{}{}
		return PostReceipt{}, p.failure
	})}
}

func TestRoomTurnsCommitRetryStopsOnCancellationAndUncertainty(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	for _, failure := range []error{ErrBusy, ErrCommitUnknown, ErrConflict} {
		t.Run(failure.Error(), func(t *testing.T) {
			attempts := make(chan struct{}, 8)
			target, err := h.rt.Spawn(commitRetryProbe{attempts: attempts, failure: failure})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(h.ctx)
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := callRoomWrite[PostReceipt](ctx, target, PostMessage{}); done <- err }()
			<-attempts
			if errors.Is(failure, ErrBusy) {
				<-attempts
				cancel()
			}
			got := <-done
			if errors.Is(failure, ErrBusy) {
				if !errors.Is(got, context.Canceled) {
					t.Fatal(got)
				}
			} else {
				if !errors.Is(got, failure) {
					t.Fatal(got)
				}
				if len(attempts) != 0 {
					t.Fatal("retried an uncertain or conflicting write")
				}
			}
		})
	}
}

func TestRoomTurnsCommitBusyWaitIsBounded(t *testing.T) {
	h := newRoomHarness(t, emptyRoom(), nil)
	attempts := make(chan struct{}, 1024)
	target, err := h.rt.Spawn(commitRetryProbe{attempts: attempts, failure: ErrBusy})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	_, err = callRoomWrite[PostReceipt](ctx, target, PostMessage{})
	if !errors.Is(err, ErrBusy) || ctx.Err() != nil || len(attempts) < 2 {
		t.Fatal("busy wait exceeded its own bound or did not retry", err, ctx.Err(), len(attempts))
	}
}
