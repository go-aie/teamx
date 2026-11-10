package gitlab

import (
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform/notification"
	"testing"
	"time"
)

func TestInboxExpiryEventsAndTodosDoNotAcknowledge(t *testing.T) {
	_, host, _ := standaloneEvents(t)
	s := host.state
	s.Config.Target = notification.Target{Mode: "inbox", AgentID: "assistant", MembershipID: "member"}
	s.ProjectID = 7
	event := Event{ID: 1, ProjectID: 7, Kind: "push", Action: "pushed to", CreatedAt: s.Baseline, Branch: "main"}
	if _, _, err := s.Accept([]Event{event}, host.now); err != nil {
		t.Fatal(err)
	}
	s.Status = "capacity"
	expired := s.Expire(event.CreatedAt).(EventsState)
	if len(expired.Inbox) != 0 || len(expired.Observed) != 0 || expired.Status != "running" || expired.Metrics.ExpiredSkipped != 1 {
		t.Fatal(expired)
	}
	raw, _ := json.Marshal(expired)
	decoded, err := (&EventsSource{}).Decode(raw, s.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	restored := decoded.(EventsState)
	// Requery after changing policy to keep old records still enforces confirmed floor.
	if _, _, err := restored.Accept([]Event{event}, host.now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(restored.Inbox) != 0 {
		t.Fatal("event resurrected")
	}
	room := s.Clone()
	room.Config.Target = notification.Target{}
	for key, e := range room.Inbox {
		e.Route.Target = notification.Target{}
		room.Inbox[key] = e
	}
	if notification.ApplyExpiry(room, event.CreatedAt).Metadata().Pending != 1 {
		t.Fatal("Room delivery expired")
	}
	todoState := sourceTodoState(t)
	todoState.Config.Target = s.Config.Target
	todo := Todo{ID: 9, RecipientID: 101, ProjectID: 7, Project: "group/project", Action: "review_requested", Reason: "review_requested", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Title: "test", URL: "https://gitlab.example.com/group/project/-/merge_requests/42", CreatedAt: todoState.Baseline, State: "pending"}
	todoState.Accept([]Todo{todo}, todoState.Baseline.Add(time.Second))
	todoState.Status = "credentials_invalid"
	expiredTodo := todoState.Expire(todo.CreatedAt).(TodosState)
	if len(expiredTodo.Inbox) != 0 || len(expiredTodo.DeliveredObjects) != 0 || expiredTodo.Status != "credentials_invalid" {
		t.Fatal("expiry implied success or resumed invalid credentials")
	}
	raw, _ = json.Marshal(expiredTodo)
	decoded, err = (&TodosSource{}).Decode(raw, todoState.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	restoredTodo := decoded.(TodosState)
	restoredTodo.Accept([]Todo{todo}, todo.CreatedAt.Add(time.Hour))
	if len(restoredTodo.Inbox) != 0 || len(restoredTodo.DeliveredObjects) != 0 {
		t.Fatal("todo resurrected or success fabricated")
	}
}
