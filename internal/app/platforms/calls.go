package platforms

import (
	"context"
	"sync"
)

// Call separates a cancellation signal from the receipt that work has settled.
// Only the registering Calls instance can complete the receipt, via Finish.
type Call struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (c *Call) Cancel()               { c.cancel() }
func (c *Call) Done() <-chan struct{} { return c.done }

// Calls owns the in-flight table, cancellation signals and completion receipts.
// Its zero value is ready for use. It must not be copied after first use.
//
// T is admission metadata owned by the caller. The caller still serializes
// admission, metadata access, final evidence checks and binding changes under
// its existing gate. This table's lock does not replace that gate. Predicates
// and cancellation functions always run outside the table lock.
type Calls[T any] struct {
	mu     sync.Mutex
	active map[*Call]T
}

func (r *Calls[T]) Register(value T, cancel context.CancelFunc) *Call {
	call := &Call{cancel: cancel, done: make(chan struct{})}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.active = make(map[*Call]T)
	}
	r.active[call] = value
	return call
}

// Finish is called after work and its cleanup have actually returned, even if
// their context was canceled earlier. Repeated completion is harmless.
func (r *Calls[T]) Finish(call *Call) {
	r.mu.Lock()
	_, active := r.active[call]
	r.mu.Unlock()
	if !active {
		return
	}
	call.Cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, active := r.active[call]; active {
		delete(r.active, call)
		close(call.done)
	}
}

type callEntry[T any] struct {
	call  *Call
	value T
}

func (r *Calls[T]) entries() []callEntry[T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	entries := make([]callEntry[T], 0, len(r.active))
	for call, value := range r.active {
		entries = append(entries, callEntry[T]{call, value})
	}
	return entries
}

// Snapshot copies the active metadata list, not the caller's metadata itself.
func (r *Calls[T]) Snapshot() []T {
	entries := r.entries()
	values := make([]T, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry.value)
	}
	return values
}

func (r *Calls[T]) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.active)
}

// Pending snapshots receipts without signaling cancellation.
func (r *Calls[T]) Pending(match func(T) bool) []<-chan struct{} {
	var done []<-chan struct{}
	for _, entry := range r.entries() {
		if match(entry.value) {
			done = append(done, entry.call.Done())
		}
	}
	return done
}

// Cancel only signals. Callers wait for the returned receipts outside their
// admission gate; a timeout leaves the calls registered for a later join.
func (r *Calls[T]) Cancel(match func(T) bool) []<-chan struct{} {
	var done []<-chan struct{}
	for _, entry := range r.entries() {
		if match(entry.value) {
			entry.call.Cancel()
			done = append(done, entry.call.Done())
		}
	}
	return done
}

// WaitCalls also accepts worker receipts when a control operation must join
// both platform calls and notification work before changing credentials.
func WaitCalls(ctx context.Context, done []<-chan struct{}) error {
	for _, ch := range done {
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
