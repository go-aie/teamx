package room

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
)

type blockedTodoWorker struct {
	entered, canceled, release chan struct{}
	done                       <-chan struct{}
	once                       sync.Once
}

func (b *blockedTodoWorker) finish() { b.once.Do(func() { close(b.release) }) }
func blockTodoSource(t *testing.T, h *todoHarness) *blockedTodoWorker {
	t.Helper()
	b := &blockedTodoWorker{entered: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(b.finish)
	item := h.todo(1, 0, "pending")
	h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(ctx context.Context, _ gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			close(b.entered)
			<-ctx.Done()
			close(b.canceled)
			<-b.release
			return gitlab.TodoBatch{Version: 1, Todos: []gitlab.Todo{item}, Complete: true, ResponseBytes: 200}, nil
		}), nil
	}
	return b
}
func startBlockedTodo(t *testing.T, h *todoHarness) *blockedTodoWorker {
	t.Helper()
	b := blockTodoSource(t, h)
	h.clock.add(time.Second)
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, b.entered, "todo query did not enter")
	h.m.mu.Lock()
	b.done = h.m.notifications.DoneLocked(h.key)
	h.m.mu.Unlock()
	return b
}
