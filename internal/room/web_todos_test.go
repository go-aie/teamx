package room

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
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
	m.mu.Lock()
	current, err := m.currentTodosLocked(room.RoomIdentity, strings.Repeat("b", 32))
	history, histErr := m.retiredNotificationViewsLocked(room.RoomIdentity)
	states := m.notifications.StatesLocked(room.RoomIdentity)
	if len(states) != 2 {
		t.Fatal("platform subscriptions were replaced", states)
	}
	for _, state := range states {
		if state.Metadata().Enabled || state.Metadata().Retired {
			t.Fatal("platform disable did not preserve subscriptions", state)
		}
	}

	m.mu.Unlock()
	if err != nil || histErr != nil || current == nil || current.Config.Enabled || current.Binding.UserID != 202 || len(history) != 0 {
		t.Fatal(current, history, err, histErr)
	}
	_, snapshot := readManagedRoom(t, m, room)
	if len(snapshot.Messages) != 2 || len(snapshot.IntegrationReceipts) != 2 {
		t.Fatal(snapshot)
	}
	for _, message := range snapshot.Messages {
		if message.Author.Kind != "integration" || message.Source.Channel != "todos" {
			t.Fatal(message)
		}
	}
}
