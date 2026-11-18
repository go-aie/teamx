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
