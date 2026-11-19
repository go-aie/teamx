// Package calendar owns scheduled task records and time semantics. Runtime,
// persistence and caller authentication belong to the Room adapter.
package calendar

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"
)

const MaxStateBytes = 16 << 20
const MaxTasks = 100
const MaxRuns = 5000

type Error struct {
	Code          string `json:"code"`
	Field         string `json:"field,omitempty"`
	Message       string `json:"message"`
	ActualVersion uint64 `json:"actual_version,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func Fail(code, field, message string) error {
	return &Error{Code: code, Field: field, Message: message}
}

type Schedule struct {
	Frequency     string   `json:"frequency"`
	StartDate     string   `json:"start_date"`
	LocalTime     string   `json:"local_time"`
	TimeZone      string   `json:"time_zone"`
	DayFilter     string   `json:"day_filter,omitempty"`
	Weekdays      []int    `json:"weekdays,omitempty"`
	WorkCalendar  string   `json:"work_calendar,omitempty"`
	EndDate       string   `json:"end_date,omitempty"`
	ExcludedDates []string `json:"excluded_dates,omitempty"`
}
type Definition struct {
	Title       string          `json:"title"`
	Instruction string          `json:"instruction"`
	Scope       json.RawMessage `json:"scope,omitempty"`
	Schedule    Schedule        `json:"schedule"`
	Misfire     string          `json:"misfire,omitempty"`
}
type Source struct {
	MessageSeq uint64 `json:"message_seq,omitempty"`
	Operator   string `json:"operator,omitempty"`
	Text       string `json:"text,omitempty"`
}
type Task struct {
	CreationSource Source `json:"creation_source"`

	ID                string     `json:"task_id"`
	Version           uint64     `json:"version"`
	DefinitionVersion uint64     `json:"definition_version"`
	Definition        Definition `json:"definition"`
	AgentID           string     `json:"agent_id"`
	MembershipID      string     `json:"membership_id"`
	State             string     `json:"state"`
	Source            Source     `json:"source"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	NextAt            time.Time  `json:"next_at,omitzero"`
	EffectiveAfter    time.Time  `json:"effective_after,omitzero"`
	EvaluatedThrough  time.Time  `json:"evaluated_through,omitzero"`
	Evaluated         bool       `json:"evaluated"`
	BlockedReason     string     `json:"blocked_reason,omitempty"`
}
type Binding struct {
	Generation string `json:"generation"`
	Turn       uint64 `json:"turn"`
}
type Supply struct {
	Binding
	Checked bool `json:"checked"`
}
type Publication struct {
	Status          string `json:"status"`
	ClientMessageID string `json:"client_message_id"`
	Seq             uint64 `json:"seq,omitempty"`
}
type Attempt struct {
	Number      int         `json:"number"`
	Binding     Binding     `json:"binding"`
	Status      string      `json:"status"`
	Reason      string      `json:"reason,omitempty"`
	Source      Source      `json:"source"`
	Report      string      `json:"report,omitempty"`
	Publication Publication `json:"publication"`
	Settled     bool        `json:"settled"`
}
type Run struct {
	WorkCalendarVersion string    `json:"work_calendar_version,omitempty"`
	Authorization       Source    `json:"authorization"`
	PreviousAttempts    []Attempt `json:"previous_attempts,omitempty"`

	ID              string          `json:"run_id"`
	Version         uint64          `json:"version"`
	Task            Task            `json:"task"`
	DueAt           time.Time       `json:"due_at"`
	Deadline        time.Time       `json:"deadline"`
	LocalDate       string          `json:"local_date"`
	Status          string          `json:"status"`
	Reason          string          `json:"reason,omitempty"`
	Attempt         int             `json:"attempt"`
	Binding         Binding         `json:"binding"`
	StartedAt       time.Time       `json:"started_at,omitzero"`
	Settled         bool            `json:"settled"`
	CancelRequested bool            `json:"cancel_requested,omitempty"`
	Supplies        []Supply        `json:"supplies,omitempty"`
	ContinueCount   int             `json:"continue_count"`
	NextCheckAt     time.Time       `json:"next_check_at,omitzero"`
	EventDelivered  bool            `json:"event_delivered"`
	EventSeq        uint64          `json:"event_seq,omitempty"`
	Phase           string          `json:"phase,omitempty"`
	Outcome         string          `json:"outcome,omitempty"`
	Report          string          `json:"report,omitempty"`
	Evidence        json.RawMessage `json:"evidence,omitempty"`
	Publication     Publication     `json:"publication"`
}
type Request struct {
	LocalDate       string                     `json:"local_date,omitempty"`
	Action          string                     `json:"action"`
	OperationID     string                     `json:"operation_id"`
	ExpectedVersion uint64                     `json:"expected_version"`
	HumanSource     Source                     `json:"human_source"`
	TaskID          string                     `json:"task_id,omitempty"`
	RunID           string                     `json:"run_id,omitempty"`
	Definition      *Definition                `json:"definition,omitempty"`
	Changes         map[string]json.RawMessage `json:"changes,omitempty"`
	State           string                     `json:"state,omitempty"`
}
type Result struct {
	Task            *Task `json:"task,omitempty"`
	Run             *Run  `json:"run,omitempty"`
	Changed         bool  `json:"changed"`
	OperationReused bool  `json:"operation_reused"`
}
type Operation struct {
	Source Source `json:"source"`

	Digest string `json:"digest"`
	Result Result `json:"result"`
}
type State struct {
	MissedRanges  []MissedRange        `json:"missed_ranges,omitempty"`
	FormatVersion int                  `json:"format_version"`
	RoomID        string               `json:"room_id"`
	RoomInstance  string               `json:"room_instance"`
	Revision      uint64               `json:"revision"`
	Tasks         map[string]*Task     `json:"tasks"`
	Runs          map[string]*Run      `json:"runs"`
	Operations    map[string]Operation `json:"operations"`
}

func New(room, instance string) State {
	return State{FormatVersion: 1, RoomID: room, RoomInstance: instance, Tasks: map[string]*Task{}, Runs: map[string]*Run{}, Operations: map[string]Operation{}}
}
func (s State) Clone() State {
	b, _ := json.Marshal(s)
	var v State
	_ = json.Unmarshal(b, &v)
	return v
}
func ID(prefix string, values ...any) string {
	b, _ := json.Marshal(values)
	h := sha256.Sum256(b)
	return prefix + hex.EncodeToString(h[:16])
}
func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func validText(s string, max int) bool { return s != "" && utf8.ValidString(s) && len(s) <= max }

func ValidateDefinition(d Definition, draft bool) (time.Time, error) {
	if !validText(d.Title, 256) || !validText(d.Instruction, 16<<10) {
		return time.Time{}, Fail("invalid_request", "definition", "title and instruction are required and bounded")
	}
	if len(d.Scope) > 32<<10 || len(d.Scope) > 0 && (!json.Valid(d.Scope) || d.Scope[0] != '{') {
		return time.Time{}, Fail("invalid_request", "scope", "use a JSON object up to 32 KiB")
	}
	if d.Misfire != "" && d.Misfire != "skip" && d.Misfire != "latest_within_one_hour" {
		return time.Time{}, Fail("invalid_request", "misfire", "unsupported policy")
	}
	if draft {
		if d.Schedule.Frequency == "" {
			return time.Time{}, nil
		}
		r := d.Schedule
		if r.StartDate == "" {
			r.StartDate = "2000-01-01"
		}
		if r.LocalTime == "" {
			r.LocalTime = "00:00"
		}
		return time.Time{}, validateSchedule(r)
	}
	if err := validateSchedule(d.Schedule); err != nil {
		return time.Time{}, err
	}
	if d.Schedule.Frequency == "once" {
		return Once(d.Schedule)
	}
	return time.Time{}, nil
}
func (s State) Validate(room, instance string) error {
	if s.FormatVersion != 1 || s.RoomID != room || s.RoomInstance != instance || s.Tasks == nil || s.Runs == nil || s.Operations == nil {
		return Fail("invalid_request", "calendar", "invalid calendar identity or format")
	}
	for id, t := range s.Tasks {
		if t == nil || id != t.ID || t.Version == 0 || t.DefinitionVersion == 0 || t.MembershipID == "" || t.AgentID == "" {
			return Fail("invalid_request", "task", "invalid saved task")
		}
		switch t.State {
		case "active", "paused", "needs_info", "cancelled", "completed":
		default:
			return Fail("invalid_request", "state", "invalid saved state")
		}
		if _, err := ValidateDefinition(t.Definition, t.State == "needs_info" || t.Definition.Schedule.Frequency == "" && (t.State == "cancelled" || t.State == "paused")); err != nil {
			return err
		}
	}
	for id, r := range s.Runs {
		if r == nil || id != r.ID || s.Tasks[r.Task.ID] == nil || r.Version == 0 || r.Attempt < 1 || r.DueAt.IsZero() {
			return Fail("invalid_request", "run", "invalid saved execution")
		}
	}
	return nil
}
func (s *State) Reuse(q Request) (Result, bool, error) {
	op, ok := s.Operations[q.OperationID]
	if !ok {
		return Result{}, false, nil
	}
	if op.Digest != Digest(q) {
		return Result{}, true, Fail("operation_conflict", "operation_id", "same operation has different fields")
	}
	v := op.Result
	v.OperationReused = true
	return v, true, nil
}
func (s *State) Update(q Request, source Source, agent, member, zone string, now time.Time) (Result, error) {
	return s.UpdateWithCalendars(q, source, agent, member, zone, now, bundledCalendars)
}
func (s *State) UpdateWithCalendars(q Request, source Source, agent, member, zone string, now time.Time, book *WorkCalendars) (Result, error) {
	if !validText(q.OperationID, 128) {
		return Result{}, Fail("invalid_request", "operation_id", "stable operation_id is required")
	}
	if v, ok, err := s.Reuse(q); ok {
		return v, err
	}
	var result Result
	if q.Action != "skip_once" && q.LocalDate != "" {
		return result, Fail("invalid_request", "local_date", "local_date is only used by skip_once")
	}
	if q.Action == "retry_run" {
		if q.RunID == "" || q.TaskID != "" || q.Definition != nil || q.Changes != nil || q.State != "" {
			return result, Fail("invalid_request", "run_id", "retry_run requires only run_id and expected run version")
		}
		r := s.Runs[q.RunID]
		if r == nil {
			return result, Fail("invalid_request", "run_id", "unknown run")
		}
		if r.Version != q.ExpectedVersion {
			return result, &Error{Code: "version_conflict", Field: "run_id", RunID: r.ID, Message: "run changed", ActualVersion: r.Version}
		}
		t := s.Tasks[r.Task.ID]
		if t.State == "paused" || t.State == "cancelled" || t.DefinitionVersion != r.Task.DefinitionVersion {
			return result, Fail("task_inactive", "task", "task is inactive or definition replaced")
		}
		if r.Status == "unknown" || !r.Settled {
			return result, Fail("result_unknown", "run_id", "settle and verify the original attempt before retry")
		}
		if r.Status != "failed" && r.Status != "blocked" && r.Status != "skipped" {
			return result, Fail("invalid_request", "run_id", "only a settled failure, block or missed run can retry")
		}
		r.PreviousAttempts = append(r.PreviousAttempts, Attempt{Number: r.Attempt, Binding: r.Binding, Status: r.Status, Reason: r.Reason, Source: r.Authorization, Report: r.Report, Publication: r.Publication, Settled: r.Settled})
		r.Attempt++
		r.Version++
		r.Status = "queued"
		r.Reason = ""
		r.Binding = Binding{}
		r.Settled = false
		r.CancelRequested = false
		r.StartedAt = time.Time{}
		r.NextCheckAt = time.Time{}
		r.ContinueCount = 0
		r.Supplies = nil
		r.Phase = ""
		r.Report = ""
		r.Outcome = ""
		r.Evidence = nil
		r.Publication = Publication{}
		r.Deadline = now.Add(time.Hour)
		r.Authorization = source
		r.EventDelivered = true
		copy := *r
		result = Result{Run: &copy, Changed: true}
		s.Operations[q.OperationID] = Operation{Digest: Digest(q), Result: result, Source: source}
		return result, nil
	}
	if q.Action == "create" {
		if q.ExpectedVersion != 0 || q.TaskID != "" || q.RunID != "" || q.Definition == nil || q.Changes != nil || q.State != "" && q.State != "active" && q.State != "needs_info" {
			return result, Fail("invalid_request", "create", "invalid create fields")
		}
		n := 0
		for _, t := range s.Tasks {
			if t.State != "cancelled" {
				n++
			}
		}
		if n >= MaxTasks {
			return result, Fail("capacity", "tasks", "calendar task limit reached")
		}
		d := *q.Definition
		if d.Schedule.TimeZone == "" {
			d.Schedule.TimeZone = zone
		}
		if d.Misfire == "" {
			d.Misfire = "latest_within_one_hour"
		}
		if d.Schedule.DayFilter == "" {
			d.Schedule.DayFilter = "all_days"
		}
		state := q.State
		if state == "" {
			state = "active"
		}
		prepareSchedule(&d.Schedule, now)
		candidate, err := firstCandidate(d, state == "needs_info", now, book)
		if err != nil {
			return result, err
		}
		var due time.Time
		if candidate != nil {
			due = candidate.DueAt
		}
		id := ID("task_", s.RoomID, s.RoomInstance, q.OperationID)
		t := &Task{ID: id, Version: 1, DefinitionVersion: 1, Definition: d, AgentID: agent, MembershipID: member, State: state, Source: source, CreationSource: source, CreatedAt: now, UpdatedAt: now, NextAt: due, EffectiveAfter: now}
		if state == "needs_info" {
			t.NextAt = time.Time{}
		}
		if candidate != nil {
			t.BlockedReason = candidate.Reason
		}
		s.Tasks[id] = t
		copy := *t
		result = Result{Task: &copy, Changed: true}
	} else {
		if q.TaskID == "" || q.RunID != "" {
			return result, Fail("invalid_request", "task_id", "task_id is required")
		}
		t := s.Tasks[q.TaskID]
		if t == nil {
			return result, Fail("invalid_request", "task_id", "task does not exist")
		}
		if t.Version != q.ExpectedVersion {
			return result, &Error{Code: "version_conflict", Field: "task_id", TaskID: t.ID, Message: "task changed", ActualVersion: t.Version}
		}
		if q.Action != "edit" && (q.Definition != nil || q.Changes != nil || q.State != "") {
			return result, Fail("invalid_request", "definition", "unrelated fields")
		}
		changed := false
		switch q.Action {
		case "pause", "cancel":
			target := "paused"
			if q.Action == "cancel" {
				target = "cancelled"
			}
			if t.State == "cancelled" && target != "cancelled" {
				return result, Fail("task_inactive", "state", "task is cancelled")
			}
			changed = t.State != target
			t.State = target
			for _, r := range s.Runs {
				if r.Task.ID == t.ID {
					if r.Status == "queued" {
						r.Status = "cancelled"
						r.Settled = true
						r.Reason = q.Action
						r.Version++
					}
					if r.Status == "running" && r.Phase != "publishing" {
						r.CancelRequested = true
						r.NextCheckAt = time.Time{}
						r.Reason = q.Action
						r.Version++
					}
				}
			}
		case "resume":
			if t.State != "paused" {
				return result, Fail("task_inactive", "state", "only a paused task can resume")
			}
			resumeAfter := now
			if t.EvaluatedThrough.After(resumeAfter) {
				resumeAfter = t.EvaluatedThrough
			}
			candidate, err := firstCandidate(t.Definition, false, resumeAfter, book)
			if err != nil {
				return result, err
			}
			t.State = "active"
			t.NextAt = candidate.DueAt
			t.BlockedReason = candidate.Reason
			t.EffectiveAfter = now
			// Keep the evaluated watermark through a wall-clock rollback.
			t.Evaluated = false
			changed = true
		case "skip_once":
			if t.State != "active" || q.LocalDate == "" {
				return result, Fail("task_inactive", "state", "skip_once requires an active task and future local_date")
			}
			if containsDate(t.Definition.Schedule.ExcludedDates, q.LocalDate) {
				break
			}
			c := candidateOn(t.Definition.Schedule, q.LocalDate, book, false)
			if c == nil || !c.DueAt.After(now) || !c.DueAt.After(t.EffectiveAfter) {
				return result, Fail("invalid_request", "local_date", "choose a future occurrence of the current rule")
			}
			id := runID(t, c)
			if s.Runs[id] != nil {
				return result, Fail("run_busy", "local_date", "occurrence already materialized")
			}
			if len(t.Definition.Schedule.ExcludedDates) >= 366 {
				return result, Fail("capacity", "excluded_dates", "excluded date limit reached")
			}
			r := newRun(t, c)
			r.Status = "skipped"
			r.Reason = "user_skipped"
			r.Settled = true
			s.Runs[id] = r
			t.Definition.Schedule.ExcludedDates = append(append([]string(nil), t.Definition.Schedule.ExcludedDates...), q.LocalDate)
			refreshNext(t, book)
			if t.Evaluated {
				pending := false
				for _, run := range s.Runs {
					if run.Task.ID == t.ID && !run.Settled {
						pending = true
					}
				}
				if !pending {
					t.State = "completed"
				}
			}
			t.Source = source
			changed = true
		case "edit":
			if len(q.Changes) == 0 || q.Definition != nil || t.State == "cancelled" || q.State != "" && !(q.State == "active" && t.State == "needs_info") {
				return result, Fail("invalid_request", "changes", "edit requires explicit changes; only needs_info may activate, paused tasks must resume")
			}
			d, err := ApplyChanges(t.Definition, q.Changes, zone)
			if err != nil {
				return result, err
			}
			draft := t.State == "needs_info" && q.State != "active"
			prepareSchedule(&d.Schedule, now)
			candidate, err := firstCandidate(d, draft, now, book)
			if err != nil {
				return result, err
			}
			var due time.Time
			if candidate != nil {
				due = candidate.DueAt
			}
			s.EvaluateWithCalendars(now, book)
			t.Definition = d
			t.Source = source
			t.DefinitionVersion++
			t.NextAt = due
			t.EffectiveAfter = now
			t.EvaluatedThrough = time.Time{}
			t.BlockedReason = ""
			if candidate != nil {
				t.BlockedReason = candidate.Reason
			}
			t.Evaluated = false
			if q.State == "active" {
				t.State = "active"
			}
			if draft {
				t.NextAt = time.Time{}
			}
			for _, r := range s.Runs {
				if r.Task.ID == t.ID && r.Status == "queued" {
					r.Status = "cancelled"
					r.Settled = true
					r.Reason = "definition_replaced"
					r.Version++
				}
			}
			changed = true
		default:
			return result, Fail("invalid_request", "action", "supported actions: create, edit, pause, resume, cancel, skip_once, retry_run")
		}
		if changed {
			t.Version++
			t.UpdatedAt = now
		}
		copy := *t
		result = Result{Task: &copy, Changed: changed}
	}
	s.Operations[q.OperationID] = Operation{Digest: Digest(q), Result: result, Source: source}
	return result, nil
}
func (s *State) Claim(id string, b Binding, member string, now time.Time) (*Run, error) {
	return s.ClaimWithCalendars(id, b, member, now, bundledCalendars)
}
func (s *State) ClaimWithCalendars(id string, b Binding, member string, now time.Time, book *WorkCalendars) (*Run, error) {
	r := s.Runs[id]
	if r == nil {
		return nil, Fail("invalid_request", "run_id", "unknown run")
	}
	t := s.Tasks[r.Task.ID]
	if r.Status == "blocked" && r.Reason == "previous_result_unverified" {
		return nil, Fail("result_unknown", "task_id", "previous attempt must be verified first")
	}
	if member != r.Task.MembershipID {
		return nil, Fail("not_authorized", "run_id", "wrong executor")
	}
	if (t.State != "active" && !(t.State == "completed" && r.Attempt > 1)) || r.CancelRequested {
		return nil, Fail("task_inactive", "state", "task is inactive")
	}
	if r.Binding == b && r.Status == "running" {
		return r, nil
	}
	if r.Status == "running" {
		return nil, Fail("run_busy", "run_id", "already claimed")
	}
	if r.Status != "queued" || !now.Before(r.Deadline) {
		return nil, Fail("run_expired", "run_id", "run is no longer claimable")
	}
	if r.Task.Definition.Schedule.DayFilter == "business_days" {
		work := book.Day(r.LocalDate)
		if work.Kind == "unknown" {
			return nil, Fail("work_calendar_uncovered", "schedule.work_calendar", "work calendar has not covered this date")
		}
		if work.Kind == "holiday" || work.Kind == "weekend" {
			return nil, Fail("task_inactive", "schedule", "calendar data now excludes this occurrence")
		}
		r.WorkCalendarVersion = work.Version
	}
	if t.DefinitionVersion != r.Task.DefinitionVersion {
		return nil, Fail("task_inactive", "definition", "definition changed")
	}
	for _, other := range s.Runs {
		if other.ID != r.ID && other.Task.ID == r.Task.ID && (other.Status == "unknown" || other.Status == "running" || !other.Settled && other.Binding.Turn != 0) {
			return nil, Fail("result_unknown", "task_id", "previous task attempt is running or unverified")
		}
		for _, previous := range other.PreviousAttempts {
			if previous.Binding == b {
				return nil, Fail("run_busy", "run_id", "only one run may be claimed per turn")
			}
		}
		if other.Binding == b && other.Binding.Turn != 0 {
			return nil, Fail("run_busy", "run_id", "only one run may be claimed per turn")
		}
	}
	r.Binding = b
	r.Status = "running"
	r.StartedAt = now
	r.NextCheckAt = now.Add(5 * time.Second)
	r.Version++
	return r, nil
}
func (s *State) Finish(id string, b Binding, member, outcome, report string, evidence json.RawMessage) (*Run, error) {
	r := s.Runs[id]
	if r == nil {
		return nil, Fail("invalid_request", "run_id", "unknown run")
	}
	if r.Task.MembershipID != member || r.Binding != b {
		return nil, Fail("not_authorized", "run_id", "finish requires original execution")
	}
	if outcome != "succeeded" && outcome != "blocked" && outcome != "failed" || !validText(report, 16<<10) || len(evidence) > 16<<10 {
		return nil, Fail("invalid_request", "report", "bounded report and succeeded/blocked/failed outcome required")
	}
	if r.Report != "" {
		if r.Report != report || r.Outcome != outcome || Digest(r.Evidence) != Digest(evidence) {
			return nil, Fail("operation_conflict", "report", "result already accepted with different content")
		}
		return r, nil
	}
	t := s.Tasks[r.Task.ID]
	if (t.State != "active" && !(t.State == "completed" && r.Attempt > 1)) || r.CancelRequested {
		return nil, Fail("task_inactive", "state", "task paused or cancelled")
	}
	if r.Status != "running" {
		return nil, Fail("run_expired", "run_id", "run is no longer running")
	}
	r.Report = report
	r.Evidence = evidence
	r.Outcome = outcome
	r.NextCheckAt = time.Time{}
	r.Phase = "publishing"
	r.Publication = Publication{Status: "pending", ClientMessageID: ID("calendar-"+r.Task.MembershipID+"-", r.ID, r.Attempt)}
	r.Version++
	return r, nil
}
func (s State) Pending(member string, now time.Time) []*Run {
	out := []*Run{}
	for _, r := range s.Runs {
		t := s.Tasks[r.Task.ID]
		if r.Task.MembershipID == member && r.Status == "queued" && r.Reason != "work_calendar_uncovered" && (t.State == "active" || t.State == "completed" && r.Attempt > 1) && t.DefinitionVersion == r.Task.DefinitionVersion && now.Before(r.Deadline) && !now.Before(r.NextCheckAt) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DueAt.Equal(out[j].DueAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].DueAt.Before(out[j].DueAt)
	})
	return out
}
func (s *State) Supply(member string, b Binding, now time.Time) []*Run {
	runs := s.Pending(member, now)
	if len(runs) > 5 {
		runs = runs[:5]
	}
	for _, r := range runs {
		found := false
		for _, v := range r.Supplies {
			found = found || v.Binding == b
		}
		if !found {
			r.Supplies = append(r.Supplies, Supply{Binding: b})
			r.NextCheckAt = now.Add(5 * time.Second)
			r.Version++
		}
	}
	return runs
}
func (s *State) TurnFinished(member string, b Binding, status string, settled bool, now time.Time) {
	claimed := false
	for _, r := range s.Runs {
		claimed = claimed || r.Task.MembershipID == member && r.Binding == b
	}
	for _, r := range s.Runs {
		if r.Task.MembershipID != member {
			continue
		}
		if r.Binding == b && r.Status == "running" && r.Phase != "publishing" {
			r.Status = "unknown"
			r.Reason = "execution_unverified"
			r.Settled = settled
			if settled && status == "completed" {
				r.Status = "failed"
				r.Reason = "result_not_submitted"
			}
			if settled && r.CancelRequested {
				r.Status = "cancelled"
				r.Reason = "cancelled"
			}
			r.Version++
		}
		for i := range r.Supplies {
			v := &r.Supplies[i]
			if v.Binding != b || v.Checked {
				continue
			}
			v.Checked = true
			r.NextCheckAt = time.Time{}
			r.Version++
			if !claimed && r.Status == "queued" && settled && status == "completed" {
				r.ContinueCount++
				r.NextCheckAt = now.Add(time.Second)
				if r.ContinueCount >= 3 {
					r.Status = "blocked"
					r.Reason = "no_progress"
					r.Settled = true
				}
			}
		}
	}
}
func (s *State) Compact(now time.Time) error {
	ranges := s.MissedRanges[:0]
	for _, v := range s.MissedRanges {
		if now.Sub(v.UntilAt) <= 90*24*time.Hour {
			ranges = append(ranges, v)
		}
	}
	s.MissedRanges = ranges
	for id, r := range s.Runs {
		if r.Settled && r.Status != "unknown" && r.Phase != "publishing" && now.Sub(r.DueAt) > 90*24*time.Hour {
			delete(s.Runs, id)
		}
	}
	if len(s.Runs) > MaxRuns {
		return Fail("capacity", "runs", "unsettled execution retention limit")
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(b) > MaxStateBytes {
		return Fail("capacity", "calendar", "calendar exceeds 16 MiB")
	}
	return nil
}
func (s State) Next(now time.Time) time.Time {
	var next time.Time
	add := func(v time.Time) {
		if !v.IsZero() && (next.IsZero() || v.Before(next)) {
			next = v
		}
	}
	for _, t := range s.Tasks {
		if t.State == "active" && !t.Evaluated {
			if t.BlockedReason == "work_calendar_uncovered" && !t.NextAt.After(now) {
				add(t.NextAt.Add(time.Hour))
			} else {
				add(t.NextAt)
			}
		}
	}
	for _, r := range s.Runs {
		switch r.Status {
		case "queued":
			add(r.Deadline)
			add(r.NextCheckAt)
			if !r.EventDelivered && r.Reason != "work_calendar_uncovered" {
				add(now)
			}
		case "running":
			if r.NextCheckAt.IsZero() {
				add(now)
			} else {
				add(r.NextCheckAt)
			}
		}
	}
	return next
}
func (s State) String() string { return fmt.Sprintf("calendar %s revision %d", s.RoomID, s.Revision) }
