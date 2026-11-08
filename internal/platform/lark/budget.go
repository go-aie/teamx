package lark

import (
	"context"
	"sync"
	"time"
)

type accountContextKey struct{}

// requestBudget is process-wide and keyed by instance/application/open ID, not
// profile or Room. FIFO admission covers verification, directory and message
// requests, including independent clients pointing at the same native account.
// Profiles additionally serialize authentication operations through Client.lock.
type requestBudget struct {
	mu       sync.Mutex
	gates    map[string]*requestQueue
	interval time.Duration
	cooldown time.Duration
	capacity int
}
type requestQueue struct {
	waiters []*requestWaiter
	busy    bool
	next    time.Time
}
type requestWaiter struct{ ready chan struct{} }

const AccountRequestInterval = 250 * time.Millisecond

var selfSendRequests = &requestBudget{interval: time.Second, cooldown: time.Minute, capacity: 8}

var accountRequests = &requestBudget{interval: AccountRequestInterval, cooldown: time.Minute}

func (b *requestBudget) acquire(ctx context.Context) (func(), error) {
	key, _ := ctx.Value(accountContextKey{}).(string)
	if key == "" {
		return nil, failure("identity_mismatch")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w := &requestWaiter{ready: make(chan struct{})}
	b.mu.Lock()
	if b.gates == nil {
		b.gates = map[string]*requestQueue{}
	}
	q := b.gates[key]
	if q == nil {
		q = &requestQueue{}
		b.gates[key] = q
	}
	if b.capacity > 0 && len(q.waiters) >= b.capacity {
		b.mu.Unlock()
		return nil, failure("send_capacity")
	}
	q.waiters = append(q.waiters, w)
	b.admit(q)
	b.mu.Unlock()
	release := func() { b.mu.Lock(); q.busy = false; b.admit(q); b.mu.Unlock() }
	select {
	case <-ctx.Done():
		b.mu.Lock()
		found := false
		for i, v := range q.waiters {
			if v == w {
				q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			q.busy = false
			b.admit(q)
		}
		b.mu.Unlock()
		return nil, ctx.Err()
	case <-w.ready:
	}
	// Recheck next after sleeping: a concurrently observed 429 may extend it.
	for {
		b.mu.Lock()
		delay := time.Until(q.next)
		b.mu.Unlock()
		if delay <= 0 {
			break
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			release()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	b.mu.Lock()
	q.next = time.Now().Add(b.interval)
	b.mu.Unlock()
	return release, nil
}
func (b *requestBudget) admit(q *requestQueue) {
	if q.busy || len(q.waiters) == 0 {
		return
	}
	w := q.waiters[0]
	q.waiters = q.waiters[1:]
	q.busy = true
	close(w.ready)
}
func (b *requestBudget) limited(ctx context.Context) {
	key, _ := ctx.Value(accountContextKey{}).(string)
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if q := b.gates[key]; q != nil {
		q.next = maxTime(q.next, time.Now().Add(b.cooldown))
	}
}
func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
