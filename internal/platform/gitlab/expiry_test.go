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
}

func TestSourceTerminalGitLabUsesDurableObservationOrExpiryFloor(t *testing.T) {
	_, host, _ := standaloneEvents(t)
	events := host.state
	events.ProjectID = 7
	e := Event{ID: 1, ProjectID: 7, Kind: "push", Action: "pushed to", CreatedAt: events.Baseline, Branch: "main"}
	if _, _, err := events.Accept([]Event{e}, host.now); err != nil {
		t.Fatal(err)
	}
	eventItem, ok := events.Notification(EventsKey(events.Binding, 7, 1))
	if !ok {
		t.Fatal("event missing")
	}
	todos := sourceTodoState(t)
	todo := Todo{ID: 9, RecipientID: 101, ProjectID: 7, Project: "group/project", Action: "review_requested", Reason: "review_requested", Kind: "merge_request", TargetType: "MergeRequest", TargetID: 70, TargetIID: 42, Title: "review", URL: todos.Binding.URL + "/group/project/-/merge_requests/42", CreatedAt: todos.Baseline, State: "pending"}
	todos.Accept([]Todo{todo}, todos.Baseline.Add(time.Second))
	todoItem, ok := todos.Notification(TodosKey(todos.Binding, 9))
	if !ok {
		t.Fatal("todo missing")
	}
	for _, test := range []struct {
		name   string
		state  notification.State
		item   notification.Item
		source notification.Source
	}{
		{"events", events, eventItem, &EventsSource{}}, {"todos", todos, todoItem, &TodosSource{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, terminal := range []notification.State{test.state.Acknowledge(test.item.Key), notification.ApplyExpiry(test.state, test.item.CreatedAt)} {
				raw, _ := json.Marshal(terminal)
				decoded, err := test.source.Decode(raw, terminal.Metadata().Key.Owner)
				if err != nil {
					t.Fatal(err)
				}
				proof := decoded.(interface {
					SourceTerminal(notification.Item) (string, bool)
				})
				if reason, ok := proof.SourceTerminal(test.item); !ok || reason == "" {
					t.Fatal("durable terminal lost", reason)
				}
				missing := test.item
				missing.Key = "unknown"
				missing.CreatedAt = missing.CreatedAt.Add(time.Hour)
				if _, ok := proof.SourceTerminal(missing); ok {
					t.Fatal("absence without a floor proved release")
				}
				foreign := test.item
				foreign.Source.Instance = "https://other.invalid"
				if _, ok := proof.SourceTerminal(foreign); ok {
					t.Fatal("foreign namespace proved release")
				}
			}
		})
	}
}
