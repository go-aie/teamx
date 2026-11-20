package platforms

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestCallsCancellationRequiresSettlementAndPreservesOtherAccounts(t *testing.T) {
	type scope struct{ account, family string }
	var calls Calls[scope]
	type use struct {
		ctx  context.Context
		call *Call
	}
	uses := make([]use, 0, 3)
	for _, s := range []scope{{"a", "attachment"}, {"a", "action"}, {"b", "tool"}} {
		ctx, cancel := context.WithCancel(t.Context())
		uses = append(uses, use{ctx, calls.Register(s, cancel)})
	}
	defer func() {
		for _, u := range uses {
			calls.Finish(u.call)
		}
	}()
	match := func(s scope) bool { return s.account == "a" }
	if len(calls.Pending(match)) != 2 || calls.Len() != 3 {
		t.Fatal("call families do not share the table")
	}
	for _, u := range uses {
		if u.ctx.Err() != nil {
			t.Fatal("query canceled work")
		}
	}
	receipts := calls.Cancel(match)
	for i, u := range uses {
		if (u.ctx.Err() != nil) != (i < 2) {
			t.Fatal("cancellation escaped its account", i)
		}
		select {
		case <-u.call.Done():
			t.Fatal("cancellation was mistaken for settlement")
		default:
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := WaitCalls(ctx, receipts); !errors.Is(err, context.DeadlineExceeded) || calls.Len() != 3 {
		t.Fatal("timeout discarded unsettled work", err, calls.Len())
	}
	calls.Finish(uses[0].call)
	calls.Finish(uses[0].call)
	if calls.Len() != 2 || len(calls.Pending(match)) != 1 {
		t.Fatal("completion did not retire the exact call")
	}
	calls.Finish(uses[1].call)
	if err := WaitCalls(t.Context(), receipts); err != nil || calls.Len() != 1 {
		t.Fatal("settled calls could not be joined again", err)
	}
	if uses[2].ctx.Err() != nil {
		t.Fatal("another account was canceled")
	}
}

func TestCallsNeverRunPredicatesOrCancellationUnderTableLock(t *testing.T) {
	var calls Calls[string]
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ctx, cancel := context.WithCancel(t.Context())
		call := calls.Register("tool", func() {
			_ = calls.Len()
			_ = calls.Snapshot()
			cancel()
		})
		match := func(value string) bool {
			_ = calls.Len()
			return value == "tool"
		}
		_ = calls.Pending(match)
		_ = calls.Cancel(match)
		if ctx.Err() == nil {
			t.Error("cancellation not signaled")
		}
		calls.Finish(call)
	}()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("registry callback held the table lock")
	}
}

func TestCallsConcurrentRegisterCancelAndFinish(t *testing.T) {
	var calls Calls[int]
	var other Calls[int]
	ctx, cancel := context.WithCancel(t.Context())
	call := calls.Register(-1, cancel)
	other.Finish(call)
	if ctx.Err() != nil || calls.Len() != 1 {
		t.Fatal("another registry retired the call")
	}
	calls.Finish(call)
	var workers sync.WaitGroup
	for i := range 32 {
		workers.Go(func() {
			for range 20 {
				_, cancel := context.WithCancel(t.Context())
				call := calls.Register(i, cancel)
				match := func(value int) bool { return value == i }
				_ = calls.Pending(match)
				_ = calls.Cancel(match)
				_ = calls.Snapshot()
				var finish sync.WaitGroup
				for range 2 {
					finish.Go(func() { calls.Finish(call) })
				}
				finish.Wait()
				select {
				case <-call.Done():
				default:
					t.Error("completion receipt missing")
				}
			}
		})
	}
	workers.Wait()
	if calls.Len() != 0 || len(calls.Snapshot()) != 0 {
		t.Fatal("completed calls leaked")
	}
}

func TestWaitCallsJoinsEveryReceiptWithoutOwningThem(t *testing.T) {
	callDone, workerDone := make(chan struct{}), make(chan struct{})
	close(callDone)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WaitCalls(ctx, []<-chan struct{}{callDone, workerDone}); !errors.Is(err, context.Canceled) {
		t.Fatal("wait skipped unfinished worker", err)
	}
	select {
	case <-workerDone:
		t.Fatal("wait completed a receipt it does not own")
	default:
	}
	close(workerDone)
	if err := WaitCalls(t.Context(), []<-chan struct{}{callDone, workerDone}); err != nil {
		t.Fatal(err)
	}
	if err := WaitCalls(ctx, nil); err != nil {
		t.Fatal("empty wait changed semantics", err)
	}
}
