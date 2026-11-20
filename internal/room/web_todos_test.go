package room

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Only the remote page query is replaced. UI requests use the production HTTP
// handler, notification manager, identity validation and Room actor/store.
func todosAppManager(t *testing.T) (*roomManager, roomSummary) {
	t.Helper()
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "todos-ui")
	verifyTodoAccount(t, m, platformTestSave(t, m, platformTestRequest(m)), 101)
	req := platformTestRequest(m)
	req.ID = strings.Repeat("b", 32)
	req.Name = "Personal GitLab"
	verifyTodoAccount(t, m, platformTestSave(t, m, req), 202)
	eventLiveService(t, &eventHarness{m: m, room: room})
	m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(_ context.Context, _ string, _ RoomIdentity, b gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(_ context.Context, q gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			items := []gitlab.Todo{}
			if q.State == "pending" {
				items = append(items, gitlab.Todo{ID: 9, RecipientID: b.Binding.UserID, ProjectID: 7, Project: "group/project", Action: "assigned", Reason: "assigned", Kind: "work_item", TargetType: "WorkItem", TargetID: 70, TargetIID: 42, Title: "GT-03 UI fixture", URL: b.Binding.URL + "/group/project/-/work_items/42", CreatedAt: time.Now().UTC(), State: "pending"})
			}
			return gitlab.TodoBatch{Version: 1, Todos: items, Complete: true, ResponseBytes: 200}, nil
		}), nil
	}
	if err := m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	return m, room
}
func checkTodosAppResult(t *testing.T, m *roomManager, room roomSummary) {
	t.Helper()
	// Snapshot only immutable assertion data while holding the manager lock.
	// Fatal exits the goroutine, so all assertions must run after unlocking.
	var current *gitlab.TodosState
	var err, histErr error
	var historyCount int
	var metadata []notification.Metadata
	var svc *service
	func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		current, err = m.currentTodosLocked(room.RoomIdentity, strings.Repeat("b", 32))
		if current != nil {
			copy := current.Clone()
			current = &copy
		}
		history, historyErr := m.retiredNotificationViewsLocked(room.RoomIdentity)
		historyCount, histErr = len(history), historyErr
		for _, state := range m.notifications.StatesLocked(room.RoomIdentity) {
			metadata = append(metadata, state.Metadata())
		}
		svc, _ = m.runtimeServiceLocked(room.RoomIdentity)
	}()
	if len(metadata) != 2 {
		t.Fatal("platform subscriptions were replaced", metadata)
	}
	for _, meta := range metadata {
		if meta.Enabled || meta.Retired {
			t.Fatal("platform disable did not preserve subscriptions", meta)
		}
	}
	if err != nil || histErr != nil || current == nil || current.Config.Enabled || current.Binding.UserID != 202 || historyCount != 0 {
		t.Fatal(current, historyCount, err, histErr)
	}
	_, snapshot := readManagedRoom(t, m, room)
	if snapshot.LatestSeq != 0 || inboxRoutingPreview(t, svc, subscriptionMember(current.Metadata())).SnapshotSeq != 2 {
		t.Fatal("expected two Inbox deliveries and no conversation messages", snapshot.LatestSeq)
	}
}
