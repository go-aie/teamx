package room

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

// Agent views deliberately do not embed persisted task/run records. The manager
// API retains its calendar projection; execution and storage identities stay here.
type calendarTaskView struct {
	TaskID               string                  `json:"task_id"`
	Version              uint64                  `json:"version"`
	Title                string                  `json:"title,omitempty"`
	State                string                  `json:"state"`
	RuleSummary          string                  `json:"rule_summary,omitempty"`
	NextAt               time.Time               `json:"next_at,omitzero"`
	BlockedReason        string                  `json:"blocked_reason,omitempty"`
	PendingRuns          []calendarPendingView   `json:"pending_runs,omitempty"`
	Definition           *calendar.Definition    `json:"definition,omitempty"`
	WorkCalendars        []calendar.WorkCoverage `json:"work_calendars,omitempty"`
	History              []calendarRunView       `json:"history,omitempty"`
	NextCursor           string                  `json:"next_cursor,omitempty"`
	HistoryRetentionDays int                     `json:"history_retention_days,omitempty"`
}
type calendarPendingView struct {
	RunID  string    `json:"run_id"`
	DueAt  time.Time `json:"due_at"`
	Status string    `json:"status"`
}
type calendarRunView struct {
	Kind                string              `json:"kind,omitempty"`
	Count               int                 `json:"count,omitempty"`
	UntilAt             time.Time           `json:"until_at,omitzero"`
	WorkCalendarVersion string              `json:"work_calendar_version,omitempty"`
	RunID               string              `json:"run_id"`
	Version             uint64              `json:"version"`
	Definition          calendar.Definition `json:"definition"`
	DueAt               time.Time           `json:"due_at"`
	TimeZone            string              `json:"time_zone"`
	Status              string              `json:"status"`
	Reason              string              `json:"reason,omitempty"`
	ReportSeq           uint64              `json:"report_seq,omitempty"`
	Report              string              `json:"report,omitempty"`
	Evidence            json.RawMessage     `json:"evidence,omitempty"`
}
type calendarMutationView struct {
	TaskID          string    `json:"task_id,omitempty"`
	RunID           string    `json:"run_id,omitempty"`
	Version         uint64    `json:"version"`
	State           string    `json:"state,omitempty"`
	Status          string    `json:"status,omitempty"`
	RuleSummary     string    `json:"rule_summary,omitempty"`
	NextAt          time.Time `json:"next_at,omitzero"`
	BlockedReason   string    `json:"blocked_reason,omitempty"`
	Reason          string    `json:"reason,omitempty"`
	Changed         *bool     `json:"changed,omitempty"`
	OperationReused bool      `json:"operation_reused,omitempty"`
}
type calendarFinishView struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
	ReportSeq uint64 `json:"report_seq,omitempty"`
}
type calendarClaimView struct {
	RunID       string          `json:"run_id"`
	Status      string          `json:"status"`
	Title       string          `json:"title"`
	Instruction string          `json:"instruction"`
	Scope       json.RawMessage `json:"scope,omitempty"`
	DueAt       time.Time       `json:"due_at"`
	TimeZone    string          `json:"time_zone"`
	Misfire     string          `json:"misfire"`
}

func calendarBusinessStatus(r *calendar.Run) string {
	if r.Phase == "publishing" {
		return "publishing"
	}
	return r.Status
}
func calendarRuleSummary(t *calendar.Task) string {
	return calendar.RuleSummary(t.Definition)
}
func calendarTaskBusinessView(t *calendar.Task, state calendar.State, full bool) calendarTaskView {
	v := calendarTaskView{TaskID: t.ID, Version: t.Version, Title: t.Definition.Title, State: t.State, RuleSummary: calendarRuleSummary(t), NextAt: t.NextAt, BlockedReason: t.BlockedReason}
	if t.State != "active" || t.BlockedReason == "work_calendar_uncovered" {
		v.NextAt = time.Time{}
	}
	if full {
		v.Title = ""
		v.RuleSummary = ""
		d := t.Definition
		v.Definition = &d
		if d.Schedule.DayFilter == "business_days" {
			v.WorkCalendars = calendar.WorkCalendarCoverage()
		}
	}
	for _, r := range state.Runs {
		if r.Task.ID == t.ID && (r.Status == "queued" || r.Status == "running" || r.Status == "unknown") {
			v.PendingRuns = append(v.PendingRuns, calendarPendingView{r.ID, r.DueAt, calendarBusinessStatus(r)})
		}
	}
	sort.Slice(v.PendingRuns, func(i, j int) bool {
		if v.PendingRuns[i].DueAt.Equal(v.PendingRuns[j].DueAt) {
			return v.PendingRuns[i].RunID < v.PendingRuns[j].RunID
		}
		return v.PendingRuns[i].DueAt.Before(v.PendingRuns[j].DueAt)
	})
	return v
}
func calendarRunBusinessView(r *calendar.Run, result bool) calendarRunView {
	v := calendarRunView{WorkCalendarVersion: r.WorkCalendarVersion, RunID: r.ID, Version: r.Version, Definition: r.Task.Definition, DueAt: r.DueAt, TimeZone: r.Task.Definition.Schedule.TimeZone, Status: calendarBusinessStatus(r), Reason: r.Reason, ReportSeq: r.Publication.Seq}
	if result {
		v.Report = r.Report
		v.Evidence = r.Evidence
	}
	return v
}
func calendarMutationBusinessView(r calendar.Result) calendarMutationView {
	v := calendarMutationView{OperationReused: r.OperationReused}
	if !r.Changed {
		changed := false
		v.Changed = &changed
	}
	if t := r.Task; t != nil {
		v.TaskID = t.ID
		v.Version = t.Version
		v.State = t.State
		v.RuleSummary = calendarRuleSummary(t)
		if t.State == "active" && t.BlockedReason != "work_calendar_uncovered" {
			v.NextAt = t.NextAt
		}
		v.BlockedReason = t.BlockedReason
	}
	if run := r.Run; run != nil {
		v.RunID = run.ID
		v.Version = run.Version
		v.Status = calendarBusinessStatus(run)
		v.Reason = run.Reason
	}
	return v
}

const calendarAgentPageSize = 50

// A cursor is bound to identity, revision, operation and every query option.
// A changed snapshot fails explicitly; it can never silently omit records.
func calendarAgentPage(state calendar.State, q calendarQuery, op string, count int) (int, int, string, error) {
	filter := calendar.ID("", state.RoomID, state.RoomInstance, op, q.TaskID, q.RunID, q.From, q.To, q.State, q.History, q.Result)
	start := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		if len(parts) != 3 || parts[0] != strconv.FormatUint(state.Revision, 10) || parts[2] != filter {
			return 0, 0, "", calendar.Fail("version_conflict", "cursor", "calendar snapshot or query changed; restart read")
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 || n >= count {
			return 0, 0, "", calendar.Fail("invalid_request", "cursor", "invalid page")
		}
		start = n
	}
	end := min(start+calendarAgentPageSize, count)
	next := ""
	if end < count {
		next = fmt.Sprintf("%d:%d:%s", state.Revision, end, filter)
	}
	return start, end, next, nil
}
func calendarAgentRead(state calendar.State, op string, q calendarQuery) (any, error) {
	if op == "list" {
		if q.TaskID != "" || q.RunID != "" || q.History || q.Result {
			return nil, ErrRequest
		}
		if q.State != "" && q.State != "active" && q.State != "paused" && q.State != "needs_info" && q.State != "completed" && q.State != "cancelled" {
			return nil, calendar.Fail("invalid_request", "state", "unknown task state")
		}
		if !q.From.IsZero() || !q.To.IsZero() {
			if q.From.IsZero() || !q.To.After(q.From) || q.To.Sub(q.From) > 42*24*time.Hour {
				return nil, calendar.Fail("invalid_request", "range", "use from/to with a positive range of at most 42 days")
			}
		}
		coverageIncomplete := false
		tasks := []*calendar.Task{}
		for _, t := range state.Tasks {
			if q.State != "" {
				if q.State != t.State {
					continue
				}
			} else if t.State != "active" && t.State != "paused" && t.State != "needs_info" {
				continue
			}
			if !q.From.IsZero() {
				// Use the same candidate expansion as dispatch; do not ask the Agent
				// to enumerate recurring dates.
				if t.State != "active" {
					continue
				}
				candidates := calendar.PreviewBundled(t, q.From, q.To)
				matched := len(candidates) > 0
				for _, c := range candidates {
					if c.Reason == "work_calendar_uncovered" {
						coverageIncomplete = true
					}
				}
				for _, r := range state.Runs {
					if r.Task.ID == t.ID && !r.DueAt.Before(q.From) && r.DueAt.Before(q.To) && r.Reason != "user_skipped" {
						matched = true
					}
				}
				if !matched {
					continue
				}
			}
			if t.BlockedReason == "work_calendar_uncovered" {
				coverageIncomplete = true
			}
			tasks = append(tasks, t)
		}
		sort.Slice(tasks, func(i, j int) bool {
			if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
				return tasks[i].ID < tasks[j].ID
			}
			return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
		})
		start, end, next, err := calendarAgentPage(state, q, op, len(tasks))
		if err != nil {
			return nil, err
		}
		out := struct {
			CoverageIncomplete bool                    `json:"coverage_incomplete,omitempty"`
			WorkCalendars      []calendar.WorkCoverage `json:"work_calendars,omitempty"`
			Tasks              []calendarTaskView      `json:"tasks"`
			NextCursor         string                  `json:"next_cursor,omitempty"`
		}{Tasks: []calendarTaskView{}, NextCursor: next, CoverageIncomplete: coverageIncomplete}
		if coverageIncomplete {
			out.WorkCalendars = calendar.WorkCalendarCoverage()
		}
		for _, t := range tasks[start:end] {
			out.Tasks = append(out.Tasks, calendarTaskBusinessView(t, state, false))
		}
		return out, nil
	}
	if !q.From.IsZero() || !q.To.IsZero() || q.State != "" || (q.TaskID == "") == (q.RunID == "") {
		return nil, ErrRequest
	}
	if q.RunID != "" {
		if q.History || q.Cursor != "" {
			return nil, ErrRequest
		}
		r := state.Runs[q.RunID]
		if r == nil {
			return nil, calendar.Fail("invalid_request", "run_id", "unknown run")
		}
		return calendarRunBusinessView(r, q.Result), nil
	}
	if q.Result || q.Cursor != "" && !q.History {
		return nil, ErrRequest
	}
	t := state.Tasks[q.TaskID]
	if t == nil {
		return nil, calendar.Fail("invalid_request", "task_id", "unknown task")
	}
	v := calendarTaskBusinessView(t, state, true)
	if q.History {
		runs := []calendarRunView{}
		for _, r := range state.Runs {
			if r.Task.ID == t.ID {
				runs = append(runs, calendarRunBusinessView(r, false))
			}
		}
		for _, m := range state.MissedRanges {
			if m.Task.ID == t.ID {
				runs = append(runs, calendarRunView{Kind: "missed_range", Count: m.Count, DueAt: m.FromAt, UntilAt: m.UntilAt, Definition: m.Task.Definition, TimeZone: m.Task.Definition.Schedule.TimeZone, Status: "skipped", Reason: m.Reason, WorkCalendarVersion: m.WorkCalendarVersion})
			}
		}
		sort.Slice(runs, func(i, j int) bool {
			if runs[i].DueAt.Equal(runs[j].DueAt) {
				return runs[i].RunID < runs[j].RunID
			}
			return runs[i].DueAt.After(runs[j].DueAt)
		})
		start, end, next, err := calendarAgentPage(state, q, op, len(runs))
		if err != nil {
			return nil, err
		}
		v.History = []calendarRunView{}
		for _, r := range runs[start:end] {
			v.History = append(v.History, r)
		}
		v.NextCursor = next
		v.HistoryRetentionDays = 90
	}
	return v, nil
}

// Evidence uses the source_refs returned by existing input/reference reads.
// It is a bounded set of references, never another free-form report or authority.
func validateCalendarEvidence(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var v struct {
		SourceRefs []string `json:"source_refs"`
	}
	if len(raw) > 4096 || decodeRequestJSON(string(raw), &v) != nil || len(v.SourceRefs) == 0 || len(v.SourceRefs) > 16 {
		return calendar.Fail("invalid_request", "evidence", "use {source_refs:[verified src_ references]}, at most 16 references / 4 KiB")
	}
	for _, ref := range v.SourceRefs {
		if len(ref) != 68 || !strings.HasPrefix(ref, "src_") || strings.Trim(ref[4:], "0123456789abcdef") != "" {
			return calendar.Fail("invalid_request", "evidence.source_refs", "use source_refs returned by existing input/reference tools")
		}
	}
	return nil
}
