package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

func TestCalendarRecurringCLIAndDurableRecovery(t *testing.T) {
	s, _, env, _ := calendarAgentFixture(t)
	now := time.Now().UTC()
	due := now.Add(15 * time.Minute).Truncate(time.Minute)
	q := map[string]any{"action": "create", "operation_id": "daily", "human_source": map[string]any{"message_seq": 1}, "definition": map[string]any{"title": "工作日提醒", "instruction": "RECURRING_SERVICE_REPORT", "schedule": map[string]any{"frequency": "daily", "local_time": due.Format("15:04"), "time_zone": "UTC", "day_filter": "business_days", "work_calendar": "CN"}}}
	got := calendarAgentCLI(t, env, "update", q)
	id := got["task_id"].(string)
	if !strings.Contains(got["rule_summary"].(string), "含调休补班") {
		t.Fatal(got)
	}
	task := *s.calendarCurrent().projection.Load().state.Tasks[id]
	if task.Definition.Schedule.StartDate != now.Format("2006-01-02") {
		t.Fatal("missing recurring start_date default", task)
	}
	full := calendarAgentCLI(t, env, "get", map[string]any{"task_id": id})
	if full["work_calendars"] == nil {
		t.Fatal("coverage not visible", full)
	}
	// Skip a single occurrence through the CLI.
	skip := calendar.Request{Action: "skip_once", TaskID: id, ExpectedVersion: 1, OperationID: "skip", LocalDate: task.NextAt.Format("2006-01-02"), HumanSource: calendar.Source{MessageSeq: 1}}
	raw, _ := json.Marshal(skip)
	var output bytes.Buffer
	if err := runCalendarCLI(t.Context(), []string{"update", "--turn", "1", "--request-file", "-"}, bytes.NewReader(raw), &output, io.Discard, env); err != nil {
		t.Fatal(err)
	}
	assertCalendarBusinessOnly(t, output.String())
	current := s.calendarCurrent().projection.Load().state.Tasks[id]
	if current.Version != 2 || current.DefinitionVersion != 1 || !current.NextAt.After(task.NextAt) {
		t.Fatal(current)
	}
	// Advance the explicit evaluation clock, passing multiple calendar periods.
	evaluatedAt := current.NextAt.Add(10*24*time.Hour + 30*time.Minute)
	_, err := s.calendarCurrent().call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		state.Evaluate(evaluatedAt)
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := loadCalendar(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.MissedRanges) == 0 || persisted.Tasks[id].Version != 2 {
		t.Fatal(persisted)
	}
	count := len(persisted.Runs)
	watermark := persisted.Tasks[id].EvaluatedThrough
	_, err = s.calendarCurrent().call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		*state = persisted.Clone()
		state.Evaluate(evaluatedAt)
		state.Evaluate(evaluatedAt.Add(-24 * time.Hour))
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := loadCalendar(s)
	if err != nil || len(reopened.Runs) != count || reopened.Tasks[id].EvaluatedThrough != watermark {
		t.Fatal("persisted cursor replay", err, reopened)
	}
	before := reopened.Revision
	data, _ := json.Marshal(calendarQuery{From: task.NextAt.Add(-time.Minute), To: task.NextAt.Add(20 * 24 * time.Hour)})
	view, err := s.calendarCall(t.Context(), "list", data, humanCaller, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	listed := view.(calendarList)
	if len(listed.Occurrences) == 0 || len(listed.WorkCalendars) == 0 || len(listed.Days) == 0 {
		t.Fatal(listed)
	}
	seen := map[string]bool{}
	for _, o := range listed.Occurrences {
		if seen[o.ID] {
			t.Fatal("duplicate preview/run", o)
		}
		seen[o.ID] = true
	}
	loaded, _ := loadCalendar(s)
	if loaded.Revision != before {
		t.Fatal("read mutated calendar")
	}
	history := calendarAgentCLI(t, env, "get", map[string]any{"task_id": id, "history": true})
	found := false
	for _, entry := range history["history"].([]any) {
		if entry.(map[string]any)["kind"] == "missed_range" {
			found = true
		}
	}
	if !found {
		t.Fatal("missed range missing from business history", history)
	}
}
func TestCalendarRecurringProjectionPaginationAndSnapshotIdentity(t *testing.T) {
	state := calendar.New("dm", "instance")
	from, _ := time.Parse(time.RFC3339, "2026-11-01T00:00:00Z")
	to := from.Add(42 * 24 * time.Hour)
	for i := range 6 {
		d := calendar.Definition{Title: "daily", Instruction: "remind", Schedule: calendar.Schedule{Frequency: "daily", StartDate: "2026-11-01", LocalTime: "09:00", TimeZone: "UTC", DayFilter: "all_days"}}
		_, err := state.Update(calendar.Request{Action: "create", OperationID: string(rune('a' + i)), Definition: &d}, calendar.Source{}, "A", "member", "UTC", from.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	first, err := calendarManagementList(state, calendarQuery{From: from, To: to}, "UTC")
	if err != nil || len(first.Occurrences) != 200 || first.Complete || first.NextCursor == "" {
		t.Fatal(first, err)
	}
	second, err := calendarManagementList(state, calendarQuery{From: from, To: to, Cursor: first.NextCursor}, "UTC")
	if err != nil || len(second.Occurrences) != 52 || !second.Complete {
		t.Fatal(second, err)
	}
	seen := map[string]bool{}
	for _, page := range []calendarList{first, second} {
		for _, o := range page.Occurrences {
			if seen[o.ID] {
				t.Fatal("duplicate page item")
			}
			seen[o.ID] = true
		}
	}
	state.RoomInstance = "other"
	if _, err := calendarManagementList(state, calendarQuery{From: from, To: to, Cursor: first.NextCursor}, "UTC"); err == nil {
		t.Fatal("cursor crossed conversation identity")
	}
}
func TestCalendarRecurringPublicationKeepsTaskActive(t *testing.T) {
	s := calendarFixture(t, t.TempDir(), newControlledBackend())
	due := time.Now().UTC().Add(15 * time.Minute).Truncate(time.Minute)
	d := calendarDefinition(due)
	d.Schedule.Frequency = "daily"
	raw, _ := json.Marshal(calendar.Request{Action: "create", OperationID: "daily-report", Definition: &d})
	v, err := s.calendarCall(t.Context(), "update", raw, humanCaller, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := v.(calendar.Result).Task
	run := calendarRunNow(t, s, *task)
	bound := calendarRunning(t, s, 1)
	_, err = s.calendarCurrent().call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		if _, err := state.Claim(run.ID, bound, "generation-A", due.Add(time.Second)); err != nil {
			return nil, err
		}
		return state.Finish(run.ID, bound, "generation-A", "succeeded", "RECURRING_PUBLICATION_OK", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := calendarAwait(t, s, func(state calendar.State) bool { return state.Runs[run.ID].Publication.Status == "published" })
	if saved.Tasks[task.ID].State != "active" || !saved.Tasks[task.ID].NextAt.After(due) {
		t.Fatal("publication terminated recurring task", saved.Tasks[task.ID])
	}
}

func TestCalendarRecurringReopenRetainsWatermark(t *testing.T) {
	dir := t.TempDir()
	s := calendarFixture(t, dir, newControlledBackend())
	due := time.Now().UTC().Add(10 * time.Minute).Truncate(time.Minute)
	d := calendarDefinition(due)
	d.Schedule.Frequency = "daily"
	raw, _ := json.Marshal(calendar.Request{Action: "create", OperationID: "reopen", Definition: &d})
	result, err := s.calendarCall(t.Context(), "update", raw, humanCaller, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	task := result.(calendar.Result).Task
	future := due.Add(5*24*time.Hour + 30*time.Minute)
	_, err = s.calendarCurrent().call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) { state.Evaluate(future); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	saved, err := loadCalendar(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.close(t.Context()); err != nil {
		t.Fatal(err)
	}
	reopened := calendarFixture(t, dir, newControlledBackend())
	owner := reopened.calendarCurrent()
	if owner == nil {
		t.Fatal("calendar not restored")
	}
	_, err = owner.call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) { state.Evaluate(future); return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadCalendar(reopened)
	if err != nil || len(loaded.Runs) != len(saved.Runs) || loaded.Tasks[task.ID].EvaluatedThrough != saved.Tasks[task.ID].EvaluatedThrough || len(loaded.MissedRanges) != len(saved.MissedRanges) {
		t.Fatal("reopen replayed old periods", err, loaded)
	}
}
