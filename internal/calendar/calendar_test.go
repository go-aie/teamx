package calendar

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func at(s string) time.Time { v, _ := time.Parse(time.RFC3339, s); return v }
func definition() Definition {
	return Definition{Title: "近未来报告", Instruction: "读取已有范围并回复本私信", Scope: json.RawMessage(`{"project_ref":"verified"}`), Schedule: Schedule{Frequency: "once", StartDate: "2026-11-20", LocalTime: "09:00", TimeZone: "Asia/Shanghai", DayFilter: "all_days"}}
}
func makeTask(t *testing.T) (State, Task) {
	t.Helper()
	s := New("dm", "instance")
	d := definition()
	v, err := s.Update(Request{Action: "create", OperationID: "create", Definition: &d}, Source{MessageSeq: 1, Operator: "human", Text: "明天九点汇总"}, "A", "membership", "Asia/Shanghai", at("2026-11-19T01:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	return s, *v.Task
}
func code(t *testing.T, err error, want string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != want {
		t.Fatalf("want %s got %v", want, err)
	}
}
func TestOnceLocalTimeValidation(t *testing.T) {
	for _, tc := range []struct{ zone, date, clock, want string }{{"Asia/Shanghai", "2026-11-20", "09:00", "2026-11-20T01:00:00Z"}, {"America/New_York", "2026-03-08", "02:30", ""}, {"America/New_York", "2026-11-01", "01:30", ""}, {"Australia/Lord_Howe", "2026-04-05", "01:45", ""}} {
		t.Run(tc.zone+tc.date, func(t *testing.T) {
			v, err := Once(Schedule{Frequency: "once", StartDate: tc.date, LocalTime: tc.clock, TimeZone: tc.zone})
			if tc.want == "" {
				code(t, err, "invalid_request")
			} else if err != nil || v.Format(time.RFC3339) != tc.want {
				t.Fatal(v, err)
			}
		})
	}
	d := definition()
	d.Schedule.Frequency = "monthly"
	_, err := ValidateDefinition(d, false)
	code(t, err, "invalid_request")
}
func TestTaskIdempotencyVersionsAndFrozenSnapshot(t *testing.T) {
	s, task := makeTask(t)
	d := definition()
	q := Request{Action: "create", OperationID: "create", Definition: &d}
	v, err := s.Update(q, Source{}, "A", "membership", "Asia/Shanghai", at("2026-11-21T00:00:00Z"))
	if err != nil || !v.OperationReused || v.Task.ID != task.ID {
		t.Fatal(v, err)
	}
	q.Definition.Title = "another"
	_, err = s.Update(q, Source{}, "A", "membership", "Asia/Shanghai", time.Now())
	code(t, err, "operation_conflict")
	s.Evaluate(at("2026-11-20T01:00:01Z"))
	s.Evaluate(at("2026-11-20T01:00:02Z"))
	if len(s.Runs) != 1 {
		t.Fatal(s.Runs)
	}
	var run *Run
	for _, r := range s.Runs {
		run = r
	}
	b := Binding{"generation", 2}
	if _, err = s.Claim(run.ID, b, "membership", at("2026-11-20T01:01:00Z")); err != nil {
		t.Fatal(err)
	}
	edit := definition()
	edit.Schedule.StartDate = "2026-11-21"
	edit.Instruction = "new instruction"
	_, err = s.Update(Request{Action: "edit", TaskID: task.ID, OperationID: "edit", ExpectedVersion: 1, Changes: definitionChanges(edit)}, Source{}, "A", "membership", "Asia/Shanghai", at("2026-11-20T01:02:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if run.Task.Definition.Instruction == edit.Instruction {
		t.Fatal("history changed")
	}
	if _, err = s.Finish(run.ID, b, "membership", "succeeded", "original snapshot report", nil); err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(Request{Action: "pause", TaskID: task.ID, OperationID: "stale", ExpectedVersion: 1}, Source{}, "A", "membership", "Asia/Shanghai", time.Now())
	code(t, err, "version_conflict")
	if s.Tasks[task.ID].Version != 2 {
		t.Fatal("run changed task resource version")
	}
}
func TestPauseCancelAndPublicationOrdering(t *testing.T) {
	for _, publishFirst := range []bool{false, true} {
		s, task := makeTask(t)
		now := at("2026-11-20T01:00:01Z")
		s.Evaluate(now)
		var r *Run
		for _, v := range s.Runs {
			r = v
		}
		b := Binding{"g", 2}
		if _, err := s.Claim(r.ID, b, "membership", now); err != nil {
			t.Fatal(err)
		}
		if publishFirst {
			if _, err := s.Finish(r.ID, b, "membership", "succeeded", "saved report", nil); err != nil {
				t.Fatal(err)
			}
		}
		_, err := s.Update(Request{Action: "pause", OperationID: "pause", TaskID: task.ID, ExpectedVersion: 1}, Source{}, "A", "membership", "Asia/Shanghai", now)
		if err != nil {
			t.Fatal(err)
		}
		if publishFirst {
			if r.CancelRequested || r.Phase != "publishing" {
				t.Fatal("accepted report cancelled")
			}
		} else {
			if !r.CancelRequested {
				t.Fatal("missing cancellation intent")
			}
			_, err = s.Finish(r.ID, b, "membership", "succeeded", "late", nil)
			code(t, err, "task_inactive")
		}
		_, err = s.Claim(r.ID, b, "membership", now)
		code(t, err, "task_inactive")
	}
}
func TestLateOnceAndBoundedNoProgressSurviveRestart(t *testing.T) {
	s, _ := makeTask(t)
	s.Evaluate(at("2026-11-20T02:00:01Z"))
	for _, r := range s.Runs {
		if r.Status != "skipped" || r.Reason != "skipped_late" {
			t.Fatal(r)
		}
	}
	s, _ = makeTask(t)
	now := at("2026-11-20T01:00:01Z")
	s.Evaluate(now)
	for i := range 3 {
		b := Binding{"g", uint64(i + 1)}
		if len(s.Supply("membership", b, now)) != 1 {
			t.Fatal("missing supply")
		}
		s.TurnFinished("membership", b, "completed", true, now)
		s = s.Clone()
		now = now.Add(2 * time.Second)
	}
	for _, r := range s.Runs {
		if r.ContinueCount != 3 || r.Status != "blocked" {
			t.Fatal(r)
		}
	}
}
func TestUnknownDoesNotReplayBusinessAndTurnLimit(t *testing.T) {
	s, _ := makeTask(t)
	now := at("2026-11-20T01:00:01Z")
	s.Evaluate(now)
	var r *Run
	for _, v := range s.Runs {
		r = v
	}
	b := Binding{"g", 1}
	_, _ = s.Claim(r.ID, b, "membership", now)
	s.TurnFinished("membership", b, "crash", false, now)
	if r.Status != "unknown" || len(s.Pending("membership", now)) != 0 {
		t.Fatal(r)
	}
	_, err := s.Claim(r.ID, Binding{"new", 2}, "membership", now)
	code(t, err, "run_expired")
}

func TestExplicitRetryKeepsRunIdentityAndAttemptEvidence(t *testing.T) {
	s, task := makeTask(t)
	now := at("2026-11-20T01:00:01Z")
	s.Evaluate(now)
	var r *Run
	for _, v := range s.Runs {
		r = v
	}
	old := Binding{"g", 1}
	if _, err := s.Claim(r.ID, old, "membership", now); err != nil {
		t.Fatal(err)
	}
	s.TurnFinished("membership", old, "completed", true, now)
	q := Request{Action: "retry_run", OperationID: "retry", RunID: r.ID, ExpectedVersion: r.Version}
	v, err := s.Update(q, Source{MessageSeq: 2, Text: "请重试"}, "A", "membership", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Run.ID != r.ID || r.Attempt != 2 || len(r.PreviousAttempts) != 1 || r.PreviousAttempts[0].Reason != "result_not_submitted" {
		t.Fatal(r)
	}
	if _, err = s.Claim(r.ID, old, "membership", now); err == nil {
		t.Fatal("same turn claimed twice through retry")
	}
	if _, err = s.Claim(r.ID, Binding{"g", 2}, "membership", now); err != nil {
		t.Fatal(err)
	}
	if s.Tasks[task.ID].Version != 1 {
		t.Fatal("retry changed task resource version")
	}
}

func TestIncompleteTaskCanBeCancelledAndReloaded(t *testing.T) {
	s := New("room", "instance")
	d := Definition{Title: "进展汇总", Instruction: "等用户补充时间和项目"}
	now := at("2026-11-19T01:00:00Z")
	result, err := s.Update(Request{Action: "create", OperationID: "draft", Definition: &d, State: "needs_info"}, Source{MessageSeq: 1, Operator: "human", Text: "安排汇总"}, "A", "membership", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Task.NextAt.IsZero() {
		t.Fatal("draft is executable")
	}
	_, err = s.Update(Request{Action: "cancel", OperationID: "cancel-draft", TaskID: result.Task.ID, ExpectedVersion: 1}, Source{}, "A", "membership", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Clone().Validate("room", "instance"); err != nil {
		t.Fatal("cancelled draft broke calendar recovery", err)
	}
}

func TestUnknownEvidenceIsRetainedAndEditsCannotBypassIt(t *testing.T) {
	s, task := makeTask(t)
	now := at("2026-11-20T01:00:01Z")
	s.Evaluate(now)
	var original *Run
	for _, r := range s.Runs {
		original = r
	}
	_, _ = s.Claim(original.ID, Binding{"g", 1}, "membership", now)
	s.TurnFinished("membership", Binding{"g", 1}, "failed", true, now)
	d := definition()
	d.Schedule.StartDate = "2026-11-21"
	_, err := s.Update(Request{Action: "edit", OperationID: "new-definition", TaskID: task.ID, ExpectedVersion: 1, Changes: definitionChanges(d)}, Source{}, "A", "membership", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(24 * time.Hour)
	s.Evaluate(later)
	for id, r := range s.Runs {
		if id != original.ID {
			_, err = s.Claim(r.ID, Binding{"g", 2}, "membership", later)
			code(t, err, "result_unknown")
		}
	}
	if err := s.Compact(now.Add(100 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.Runs[original.ID] == nil {
		t.Fatal("unknown evidence was removed by retention")
	}
}

func definitionChanges(d Definition) map[string]json.RawMessage {
	b, _ := json.Marshal(d)
	var changes map[string]json.RawMessage
	_ = json.Unmarshal(b, &changes)
	return changes
}

func TestCreateDefaultsAreFrozenBeforeReplayAcrossDateAndZone(t *testing.T) {
	s := New("dm", "instance")
	d := definition()
	d.Schedule.TimeZone = ""
	d.Schedule.DayFilter = ""
	d.Misfire = ""
	q := Request{Action: "create", OperationID: "defaults", Definition: &d}
	first, err := s.Update(q, Source{}, "A", "member", "Asia/Shanghai", at("2026-11-19T01:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.Update(q, Source{}, "A", "member", "America/New_York", at("2026-12-19T01:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.OperationReused || len(s.Tasks) != 1 || replay.Task.Definition.Schedule.TimeZone != "Asia/Shanghai" || replay.Task.Definition.Schedule.DayFilter != "all_days" || replay.Task.Definition.Misfire != "latest_within_one_hour" || replay.Task.NextAt != first.Task.NextAt {
		t.Fatal(replay)
	}
}
func TestEditReplacesOnlyExplicitBusinessFields(t *testing.T) {
	s, task := makeTask(t)
	now := at("2026-11-19T01:00:00Z")
	changes := map[string]json.RawMessage{"scope": json.RawMessage(`{"workspace":"new"}`), "title": json.RawMessage(`"renamed"`)}
	q := Request{Action: "edit", OperationID: "partial", TaskID: task.ID, ExpectedVersion: 1, Changes: changes}
	v, err := s.Update(q, Source{}, "A", "membership", "UTC", now)
	if err != nil {
		t.Fatal(err)
	}
	if v.Task.Definition.Instruction != task.Definition.Instruction || string(v.Task.Definition.Scope) != `{"workspace":"new"}` || v.Task.Definition.Schedule.TimeZone != "Asia/Shanghai" {
		t.Fatal(v)
	}
	for _, bad := range []map[string]json.RawMessage{{"schedule": json.RawMessage(`{"local_time":"10:00"}`)}, {"title": json.RawMessage(`null`)}, {"definition_version": json.RawMessage(`2`)}, {"schedule": json.RawMessage(`{"frequency":"once","start_date":"2026-11-20","local_time":"10:00","extra":true}`)}} {
		q.OperationID = "bad"
		q.ExpectedVersion = 2
		q.Changes = bad
		_, err = s.Update(q, Source{}, "A", "membership", "UTC", now)
		code(t, err, "invalid_request")
	}
	q = Request{Action: "pause", OperationID: "pause", TaskID: task.ID, ExpectedVersion: 2}
	if _, err = s.Update(q, Source{}, "A", "membership", "UTC", now); err != nil {
		t.Fatal(err)
	}
	q = Request{Action: "edit", OperationID: "activate-paused", TaskID: task.ID, ExpectedVersion: 3, State: "active", Changes: changes}
	_, err = s.Update(q, Source{}, "A", "membership", "UTC", now)
	code(t, err, "invalid_request")
	q.State = ""
	q.Changes = map[string]json.RawMessage{"scope": json.RawMessage(`null`), "misfire": json.RawMessage(`null`)}
	v, err = s.Update(q, Source{}, "A", "membership", "UTC", now)
	if err != nil || len(v.Task.Definition.Scope) != 0 || v.Task.State != "paused" || v.Task.Definition.Misfire != "latest_within_one_hour" {
		t.Fatal(v, err)
	}
}
