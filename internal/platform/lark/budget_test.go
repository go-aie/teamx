package lark

import (
	"context"
	"errors"
	"testing"
	"time"
)

func budgetContext(key string) context.Context {
	return context.WithValue(context.Background(), accountContextKey{}, key)
}
func awaitQueue(t *testing.T, b *requestBudget, key string, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mu.Lock()
		n := len(b.gates[key].waiters)
		b.mu.Unlock()
		if n == count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("request did not join shared queue")
}
func TestAccountBudgetFIFOIsolationAndCancellation(t *testing.T) {
	b := &requestBudget{}
	ctx := budgetContext("same-native-account")
	release, err := b.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan int, 3)
	cancelled, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		r, e := b.acquire(cancelled)
		if e == nil {
			r()
		}
		done <- e
	}()
	awaitQueue(t, b, "same-native-account", 1)
	for i := 1; i <= 3; i++ {
		go func(i int) {
			r, e := b.acquire(ctx)
			if e == nil {
				got <- i
				r()
			}
		}(i)
		awaitQueue(t, b, "same-native-account", i+1)
	}
	other, err := b.acquire(budgetContext("another-account"))
	if err != nil {
		t.Fatal(err)
	}
	other()
	cancel()
	if !errors.Is(<-done, context.Canceled) {
		t.Fatal("waiting cancellation lost")
	}
	release()
	for i := 1; i <= 3; i++ {
		select {
		case n := <-got:
			if n != i {
				t.Fatal("account FIFO violated", n, i)
			}
		case <-time.After(time.Second):
			t.Fatal("queue did not drain")
		}
	}
}
func TestAccountBudgetSpacingAndSharedRateLimitCooldown(t *testing.T) {
	b := &requestBudget{interval: 10 * time.Millisecond, cooldown: 40 * time.Millisecond}
	ctx := budgetContext("native-account")
	release, err := b.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.limited(ctx)
	start := time.Now()
	release()
	cancelled, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer cancel()
	if r, err := b.acquire(cancelled); err == nil {
		r()
		t.Fatal("cancelled cooldown admitted a request")
	}
	release, err = b.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 35*time.Millisecond {
		t.Fatal("another Room bypassed account cooldown")
	}
	release()
	start = time.Now()
	release, err = b.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if time.Since(start) < 8*time.Millisecond {
		t.Fatal("request spacing bypassed")
	}
}
