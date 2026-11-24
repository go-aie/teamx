//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"github.com/go-aie/teamx/internal/calendar"
	"net/http"
	"testing"
	"time"
)

func TestCalendarManagedRecurringPauseRecoveryAndPublication(t *testing.T) {
	f := newCalendarManagedFixture(t)
	a, b := f.create(0, "私信甲重复提醒"), f.create(1, "私信乙重复提醒")
	if a.ID == b.ID || a.AgentID != b.AgentID {
		t.Fatal("same Agent role must have distinct conversation tasks", a, b)
	}
	run := f.due(0, a)
	oldInfo := f.service(0).info
	f.restart() // No /rooms/open or calendar read precedes this recovery.
	s := f.service(0)
	if s.info.ServiceEpoch == oldInfo.ServiceEpoch || s.calendarCurrent().projection.Load().state.Runs[run.ID].Status != "queued" {
		t.Fatal("queued work or service epoch lost on restart")
	}
	members, err := s.members(t.Context())
	if err != nil || !members.Members[0].Execution.Paused || members.Members[0].Execution.SessionTurns.Total != 0 {
		t.Fatal("calendar recovery enabled a paused Agent", members, err)
	}
	stale := f.request("/v1/calendar/update", apiRequest{ServiceInfo: oldInfo, Caller: humanCaller, Calendar: json.RawMessage(`{"action":"cancel","operation_id":"stale","task_id":"` + a.ID + `","expected_version":1}`)})
	if stale.Code == http.StatusOK {
		t.Fatal("old service epoch mutated recovered calendar")
	}
	finished := f.finish(0, run)
	paused := f.update(1, calendar.Request{Action: "pause", OperationID: "pause", TaskID: b.ID, ExpectedVersion: b.Version})
	f.restart()
	if f.service(1).calendarCurrent().projection.Load().state.Tasks[b.ID].State != "paused" {
		t.Fatal("task pause lost on restart")
	}
	resumed := f.update(1, calendar.Request{Action: "resume", OperationID: "resume", TaskID: b.ID, ExpectedVersion: paused.Version})
	second := f.finish(1, f.due(1, resumed))
	f.restart()
	for i, expected := range []*calendar.Run{finished, second} {
		s := f.service(i)
		state := s.calendarCurrent().projection.Load().state
		if len(state.Runs) != 1 || state.Runs[expected.ID].Publication.Seq != expected.Publication.Seq || state.Tasks[expected.Task.ID].State != "active" || !state.Tasks[expected.Task.ID].NextAt.After(expected.DueAt) {
			t.Fatal("recovery duplicated or ended a recurring task", state)
		}
		room, err := s.store.LoadRoom()
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, message := range room.Messages {
			if message.Text == expected.Report && message.Seq == expected.Publication.Seq && message.Author.ID == expected.Task.AgentID {
				count++
			}
			if message.Text == []*calendar.Run{second, finished}[i].Report {
				t.Fatal("report crossed conversations")
			}
		}
		if count != 1 {
			t.Fatal("public report missing or duplicated", count)
		}
	}
}

func TestCalendarManagedOfflineRecoveryOnlyLatestWhileAgentsPaused(t *testing.T) {
	f := newCalendarManagedFixture(t)
	due := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Minute)
	tasks := make([]*calendar.Task, len(f.rooms))
	for i := range f.rooms {
		s := f.service(i)
		s.calendarRunning.Store(false)
		h, err := s.calendarOwner(true)
		if err != nil {
			t.Fatal(err)
		}
		// Persist a valid historical definition through its sole writer while
		// the controlled clock is three days earlier. Production boot then
		// evaluates the elapsed periods with the real current clock.
		created := due.Add(-73 * time.Hour)
		d := calendarDefinition(due.Add(-72 * time.Hour))
		d.Schedule.Frequency = "daily"
		v, err := h.call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
			id := s.recovered[0].id
			return state.Update(calendar.Request{Action: "create", OperationID: "offline", Definition: &d}, calendar.Source{Operator: humanCaller.ID, Text: "每天提醒"}, id.AgentID, id.MembershipID, "UTC", created)
		})
		if err != nil {
			t.Fatal(err)
		}
		tasks[i] = v.(calendar.Result).Task
	}
	for range 2 {
		f.restart()
		for i, task := range tasks {
			s := f.service(i)
			state := calendarAwait(t, s, func(state calendar.State) bool {
				for _, r := range state.Runs {
					if r.DueAt.Equal(due) && r.EventDelivered {
						return true
					}
				}
				return false
			})
			if len(state.Runs) != 1 || len(state.MissedRanges) != 1 || state.MissedRanges[0].Count != 3 || state.MissedRanges[0].Reason != "skipped_late" || !state.Tasks[task.ID].NextAt.After(due) {
				t.Fatal("offline recovery lost or duplicated elapsed periods", state)
			}
			members, err := s.members(t.Context())
			if err != nil || !members.Members[0].Execution.Paused || members.Members[0].Execution.SessionTurns.Total != 0 {
				t.Fatal("offline recovery enabled the Agent", members, err)
			}
		}
	}
	select {
	case id := <-f.entered:
		t.Fatal("offline recovery started a model while paused", id)
	default:
	}
}
