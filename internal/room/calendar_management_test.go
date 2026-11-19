package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

func calendarManagementHTTP(t *testing.T, s *service, op string, q any, status int) map[string]json.RawMessage {
	t.Helper()
	raw, _ := json.Marshal(q)
	body, _ := json.Marshal(apiRequest{ServiceInfo: s.info, Caller: humanCaller, Calendar: raw})
	response := httptest.NewRecorder()
	s.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/calendar/"+op, bytes.NewReader(body)))
	if response.Code != status {
		t.Fatalf("%s: HTTP %d: %s", op, response.Code, response.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCalendarManagementDetailsHistoryAndStableLinks(t *testing.T) {
	s := calendarFixture(t, t.TempDir(), newControlledBackend())
	task := calendarCreate(t, s, time.Now().Add(time.Hour))
	_, err := s.calendarCurrent().call(t.Context(), true, func(_ context.Context, state *calendar.State) (any, error) {
		state.Tasks[task.ID].CreationSource = calendar.Source{MessageSeq: 1, Text: "真实创建消息", Operator: humanCaller.ID}
		for i := 0; i < 55; i++ {
			due := time.Now().UTC().Add(-time.Duration(i+1) * 24 * time.Hour)
			state.Runs[fmt.Sprint("history-", i)] = &calendar.Run{ID: fmt.Sprint("history-", i), Version: 1, Task: task, DueAt: due, Status: "succeeded", Settled: true, Report: "冻结报告", Publication: calendar.Publication{Status: "published", Seq: uint64(i + 2)}}
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before := s.calendarCurrent().projection.Load().state.Revision
	data := calendarManagementHTTP(t, s, "get", calendarQuery{TaskID: task.ID, History: true}, 200)
	var detail calendarManagementTask
	raw, _ := json.Marshal(data)
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != task.ID || len(detail.History) != 50 || detail.NextCursor == "" || !detail.FirstAt.Equal(task.NextAt) || detail.History[0].Report != "" {
		t.Fatal("unbounded or incomplete task history", detail)
	}
	next := calendarManagementHTTP(t, s, "get", calendarQuery{TaskID: task.ID, History: true, Cursor: detail.NextCursor}, 200)
	var history []calendarRunView
	json.Unmarshal(next["history"], &history)
	if len(history) != 5 || next["next_cursor"] != nil {
		t.Fatal(next)
	}
	// The displayed week need not contain the original message or report dates.
	from := time.Now().Add(24 * time.Hour)
	to := from.Add(7 * 24 * time.Hour)
	list := calendarManagementHTTP(t, s, "list", calendarQuery{From: from, To: to}, 200)
	var links []calendarMessageLink
	json.Unmarshal(list["message_links"], &links)
	if len(links) != 56 || links[0].MessageSeq != 1 || links[0].TaskID != task.ID {
		t.Fatal("source and reports outside range lost", links)
	}
	if s.calendarCurrent().projection.Load().state.Revision != before {
		t.Fatal("management reads changed committed calendar")
	}
	// Submit an edit through the real manager API; history keeps the old snapshot.
	edited := calendarManagementHTTP(t, s, "update", map[string]any{"action": "edit", "operation_id": "manager-edit", "task_id": task.ID, "expected_version": task.Version, "changes": map[string]any{"title": "新安排"}}, 200)
	var updated calendar.Task
	json.Unmarshal(edited["task"], &updated)
	if updated.Definition.Title != "新安排" || !bytes.Equal(updated.Definition.Scope, task.Definition.Scope) || updated.Definition.Schedule.TimeZone != task.Definition.Schedule.TimeZone {
		t.Fatal(updated)
	}
	run := calendarManagementHTTP(t, s, "get", calendarQuery{RunID: "history-0"}, 200)
	var frozen calendar.Run
	raw, _ = json.Marshal(run)
	json.Unmarshal(raw, &frozen)
	if frozen.Task.Definition.Title != task.Definition.Title || frozen.Report != "冻结报告" {
		t.Fatal("edit rewrote past execution", frozen)
	}
	stale := calendarManagementHTTP(t, s, "get", calendarQuery{TaskID: task.ID, History: true, Cursor: detail.NextCursor}, 409)
	if string(stale["field"]) != `"cursor"` {
		t.Fatal(stale)
	}
	conflict := calendarManagementHTTP(t, s, "update", map[string]any{"action": "pause", "operation_id": "manager-stale", "task_id": task.ID, "expected_version": 1}, 409)
	if string(conflict["actual_version"]) != "2" {
		t.Fatal(conflict)
	}
}

func TestCalendarManagementRangeMatchesEvaluatedCandidates(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-11-18T00:00:00Z")
	state := calendar.New("direct", "instance")
	d := calendar.Definition{Title: "中国工作日", Instruction: "核对", Schedule: calendar.Schedule{Frequency: "daily", StartDate: "2026-11-19", LocalTime: "09:00", TimeZone: "Asia/Shanghai", DayFilter: "business_days", WorkCalendar: "CN"}}
	result, err := state.Update(calendar.Request{Action: "create", OperationID: "task", Definition: &d}, calendar.Source{}, "A", "member", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	from := now
	to := now.Add(7 * 24 * time.Hour)
	q := calendarQuery{From: from, To: to}
	preview, err := calendarManagementList(state, q, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]bool{}
	for _, o := range preview.Occurrences {
		expected[o.ID] = true
	}
	state.Evaluate(result.Task.NextAt.Add(time.Second))
	actual, err := calendarManagementList(state, q, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	found := false
	for _, o := range actual.Occurrences {
		if seen[o.ID] || !expected[o.ID] {
			t.Fatal("preview and execution diverged", o)
		}
		seen[o.ID] = true
		if o.Run != nil {
			found = true
			if o.Status != "queued" {
				t.Fatal(o)
			}
		}
	}
	if !found || len(seen) != len(expected) || actual.TimeZone != "America/New_York" {
		t.Fatal(actual)
	}
	for _, q := range []calendarQuery{{From: from}, {To: to}, {From: from, To: from}, {From: from, To: from.Add(43 * 24 * time.Hour)}} {
		if _, err := calendarManagementList(state, q, "UTC"); err == nil {
			t.Fatal("invalid range accepted", q)
		}
	}
}

func TestCalendarManagementSixWeekRangeAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, start := range []string{"2026-02-23", "2026-10-26"} {
		t.Run(start, func(t *testing.T) {
			from, err := time.ParseInLocation("2006-01-02", start, loc)
			if err != nil {
				t.Fatal(err)
			}
			to := from.AddDate(0, 0, 42)
			state := calendar.New("dm", "instance")
			// A UTC task still expands when the display zone crosses DST.
			d := calendar.Definition{Title: "daily", Instruction: "remind", Schedule: calendar.Schedule{Frequency: "daily", StartDate: start, LocalTime: "12:00", TimeZone: "UTC", DayFilter: "all_days"}}
			_, err = state.Update(calendar.Request{Action: "create", OperationID: "dst", Definition: &d}, calendar.Source{}, "A", "member", "UTC", from.Add(-time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			result, err := calendarManagementList(state, calendarQuery{From: from, To: to}, loc.String())
			if err != nil || len(result.Days) != 42 || len(result.Occurrences) != 42 {
				t.Fatalf("incomplete six-week range: days=%d occurrences=%d error=%v", len(result.Days), len(result.Occurrences), err)
			}
			for _, o := range result.Occurrences {
				if o.DueAt.Before(from) || !o.DueAt.Before(to) {
					t.Fatal("occurrence outside display range", o)
				}
			}
			if _, err := calendarManagementList(state, calendarQuery{From: from, To: to.Add(time.Minute)}, loc.String()); err == nil {
				t.Fatal("range beyond six display weeks accepted")
			}
		})
	}
}
