package room

import (
	"context"
	"errors"
	"os"
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
func TestTodosDisableSettlesAndRejectsLatePage(t *testing.T) {
	h := newTodoHarness(t)
	b := startBlockedTodo(t, h)
	s := h.state()
	cfg := s.Config
	cfg.Enabled = false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	next, err := h.m.configureTodosSubscription(ctx, h.key, s.SubscriptionVersion, cfg)
	if !errors.Is(err, context.DeadlineExceeded) || next.Config.Enabled {
		t.Fatal(next, err)
	}
	awaitEventSignal(t, b.canceled, "todo disable did not cancel")
	select {
	case <-b.done:
		t.Fatal("unsettled query reported done")
	default:
	}
	b.finish()
	awaitEventSignal(t, b.done, "todo did not settle")
	s = h.state()
	if len(s.Inbox) != 0 || len(s.Observed) != 0 || s.Config.Enabled || !s.Baseline.Equal(h.baseline) {
		t.Fatal("late canceled page committed", s)
	}
}
func TestTodosDeletionAndShutdownWaitForQueryOwnership(t *testing.T) {
	for _, action := range []string{"delete", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			h := newTodoHarness(t)
			b := startBlockedTodo(t, h)
			h.m.mu.Lock()
			path, err := h.m.notifications.StatePathLocked(h.key)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			if action == "delete" {
				beginDeletion(t, h.m, h.room)
			} else {
				go func() { done <- h.m.close() }()
			}
			awaitEventSignal(t, b.canceled, "lifecycle did not cancel todo")
			if _, err = os.Stat(path); err != nil {
				t.Fatal("removed state before query settlement", err)
			}
			select {
			case <-b.done:
				t.Fatal("unsettled query reported done")
			default:
			}
			b.finish()
			awaitEventSignal(t, b.done, "lifecycle query did not settle")
			if action == "delete" {
				if remaining := awaitDeletion(t, h.m, h.room); remaining != nil {
					t.Fatal(remaining)
				}
			} else {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestTodosCredentialRotationWaitsBeforeSavingReplacement(t *testing.T) {
	h := newTodoHarness(t)
	b := startBlockedTodo(t, h)
	req := platformTestRequest(h.m)
	req.Version = h.connection.Version

	req.ReferencedRooms = []RoomIdentity{h.room.RoomIdentity}
	done := make(chan error, 1)
	go func() { _, err := testReauthorizeConnection(h.m, req); done <- err }()
	awaitEventSignal(t, b.canceled, "rotation did not cancel personal query")
	// The public read may proceed while settlement is pending, but credentials
	// have not been replaced and the old subscription is durably paused.
	h.m.mu.Lock()
	file, err := h.m.connections.Load()
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if file.Connections[0].CredentialVersion != h.connection.CredentialVersion || h.state().Status != "credentials_invalid" {
		t.Fatal("credential replaced before settlement")
	}
	b.finish()
	awaitEventSignal(t, b.done, "rotation query did not settle")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(h.state().Inbox) != 0 || len(h.state().Observed) != 0 {
		t.Fatal("rotation accepted old page")
	}
}
func TestTodosAndEventsOperationsCancelOnlyTheirOwnChannel(t *testing.T) {
	for _, channel := range []string{"events", "todos"} {
		for _, operation := range []string{"disable", "sync"} {
			t.Run(channel+"/"+operation, func(t *testing.T) {
				h := newTodoHarness(t)
				setTestEffectiveWorkspace(t, h.m, h.room, testGitLabWorkspace(), 1)

				e, err := seedTestEvents(h.m, h.room.RoomIdentity, gitlab.EventsConfig{Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				bt := blockTodoSource(t, h)
				be := startBlockedEventWorker(t, &eventHarness{t: t, m: h.m, room: h.room, clock: h.clock, baseline: h.baseline})
				awaitEventSignal(t, bt.entered, "personal query did not enter")
				h.m.mu.Lock()
				bt.done = h.m.notifications.DoneLocked(h.key)
				h.m.mu.Unlock()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
				defer cancel()
				if channel == "todos" {
					s := h.state()
					if operation == "disable" {
						cfg := s.Config
						cfg.Enabled = false
						_, err = h.m.configureTodosSubscription(ctx, h.key, s.SubscriptionVersion, cfg)
					} else {
						err = h.m.syncTodosSubscription(ctx, h.key, s.SubscriptionVersion)
					}
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatal(err)
					}
					awaitEventSignal(t, bt.canceled, "todo operation did not cancel its query")
					select {
					case <-be.canceled:
						t.Fatal("todo operation canceled Events")
					default:
					}
					// Install a nonblocking successor before the canceled worker can restart.
					h.m.mu.Lock()
					h.m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
						return todoFetchFunc(func(context.Context, gitlab.TodoQuery) (gitlab.TodoBatch, error) {
							return gitlab.TodoBatch{Version: 1, Todos: []gitlab.Todo{}, Complete: true}, nil
						}), nil
					}
					h.m.mu.Unlock()
				} else {
					if operation == "disable" {
						cfg := e.Config
						cfg.Enabled = false
						_, err = h.m.configureEventsSubscription(ctx, e.Metadata().Key, e.SubscriptionVersion, cfg)
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatal(err)
						}
					} else {
						if err = h.m.syncEventsSubscription(e.Metadata().Key, e.SubscriptionVersion); err != nil {
							t.Fatal(err)
						}
					}
					select {
					case <-bt.canceled:
						t.Fatal("Events operation canceled Todos")
					default:
					}
				}
				// Cancel both before releasing the late responses. No replacement uses the
				// deliberately blocking fixture, and neither late page may publish facts.
				h.m.mu.Lock()
				h.m.notifications.CancelRoomLocked(h.room.RoomIdentity)
				h.m.mu.Unlock()
				bt.finish()
				be.finish()
				awaitEventSignal(t, bt.done, "todo did not settle")
				awaitEventSignal(t, be.worker, "Events did not settle")
			})
		}
	}
}
