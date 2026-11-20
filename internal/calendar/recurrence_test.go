package calendar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func recurring(t *testing.T, rule Schedule, now time.Time) (State, *Task) {
	t.Helper()
	s := New("dm", "instance")
	d := definition()
	d.Schedule = rule
	v, err := s.Update(Request{Action: "create", OperationID: "recurring", Definition: &d}, Source{MessageSeq: 1, Operator: "human", Text: "每日安排"}, "A", "member", "Asia/Shanghai", now)
	if err != nil {
		t.Fatal(err)
	}
	return s, s.Tasks[v.Task.ID]
}
func daily() Schedule {
	return Schedule{Frequency: "daily", LocalTime: "09:00", TimeZone: "Asia/Shanghai", DayFilter: "all_days"}
}
func onlyRun(t *testing.T, s State) *Run {
	t.Helper()
	if len(s.Runs) != 1 {
		t.Fatal("expected one run", s.Runs)
	}
	for _, r := range s.Runs {
		return r
	}
	return nil
}
func TestRecurringCommitDefaultsAndLocalCalendar(t *testing.T) {
	for _, tc := range []struct{ now, want string }{{"2026-12-31T00:59:59Z", "2026-12-31T01:00:00Z"}, {"2026-12-31T01:00:00Z", "2027-01-01T01:00:00Z"}, {"2026-12-31T02:00:00Z", "2027-01-01T01:00:00Z"}} {
		s, task := recurring(t, daily(), at(tc.now))
		if task.NextAt != at(tc.want) || task.Definition.Schedule.StartDate != "2026-12-31" {
			t.Fatal(task)
		}
		d := definition()
		d.Schedule = daily()
		q := Request{Action: "create", OperationID: "recurring", Definition: &d}
		cloned := s.Clone()
		got, err := cloned.Update(q, Source{}, "A", "member", "America/New_York", at("2028-01-01T00:00:00Z"))
		if err != nil || !got.OperationReused || got.Task.NextAt != task.NextAt {
			t.Fatal(got, err)
		}
	}
	r := daily()
	r.Frequency = "weekly"
	r.Weekdays = []int{1, 3}
	r.EndDate = "2027-01-04"
	_, task := recurring(t, r, at("2026-12-31T00:00:00Z"))
	if task.NextAt != at("2027-01-04T01:00:00Z") {
		t.Fatal(task.NextAt)
	}
	c := NextCandidate(task.Definition.Schedule, task.NextAt, bundledCalendars)
	if c != nil {
		t.Fatal("end_date not inclusive", c)
	}
}
func TestRecurringDSTGapsFoldsAndFixedZone(t *testing.T) {
	for _, tc := range []struct{ zone, start, clock, after, want string }{{"America/New_York", "2026-03-07", "02:30", "2026-03-07T08:00:00Z", "2026-03-09T06:30:00Z"}, {"America/New_York", "2026-10-31", "01:30", "2026-10-31T06:00:00Z", "2026-11-01T05:30:00Z"}, {"Australia/Lord_Howe", "2026-04-04", "01:45", "2026-04-04T00:00:00Z", "2026-04-04T14:45:00Z"}} {
		r := Schedule{Frequency: "daily", StartDate: tc.start, LocalTime: tc.clock, TimeZone: tc.zone}
		c := NextCandidate(r, at(tc.after), bundledCalendars)
		if c == nil || c.DueAt != at(tc.want) {
			t.Fatal(tc, c)
		}
		next := NextCandidate(r, c.DueAt, bundledCalendars)
		if next != nil && next.LocalDate == c.LocalDate {
			t.Fatal("fold duplicated", c, next)
		}
	}
	r := daily()
	r.TimeZone = "America/New_York"
	r.LocalTime = "09:00"
	s, task := recurring(t, r, at("2026-03-07T15:00:00Z"))
	if task.NextAt != at("2026-03-08T13:00:00Z") {
		t.Fatal(task)
	}
	v, err := s.Update(Request{Action: "edit", OperationID: "title", TaskID: task.ID, ExpectedVersion: 1, Changes: map[string]json.RawMessage{"title": json.RawMessage(`"renamed"`)}}, Source{}, "A", "member", "Asia/Tokyo", at("2026-03-07T15:00:00Z"))
	if err != nil || v.Task.NextAt != task.NextAt || v.Task.Definition.Schedule.TimeZone != "America/New_York" {
		t.Fatal(v, err)
	}
}
func TestOfficialWorkCalendarHolidayMakeupAndCoverage(t *testing.T) {
	b, err := LoadWorkCalendars()
	if err != nil {
		t.Fatal(err)
	}
	for date, want := range map[string]string{"2026-01-01": "holiday", "2026-01-04": "makeup", "2026-02-14": "makeup", "2026-02-16": "holiday", "2026-09-20": "makeup", "2027-01-31": "makeup", "2027-02-05": "holiday", "2027-06-09": "holiday", "2027-09-15": "holiday", "2027-10-09": "makeup", "2028-01-03": "unknown", "2025-12-31": "unknown", "2026-11-21": "weekend", "2026-11-20": "workday"} {
		if got := b.Day(date); got.Kind != want {
			t.Fatal(date, got, want)
		}
	}
	r := daily()
	r.StartDate = "2026-01-01"
	r.DayFilter = "business_days"
	r.WorkCalendar = "CN"
	c := NextCandidate(r, at("2025-12-31T00:00:00Z"), b)
	if c.LocalDate != "2026-01-04" || c.WorkCalendarVersion == "" {
		t.Fatal(c)
	}
	r.DayFilter = "weekdays"
	r.WorkCalendar = ""
	c = NextCandidate(r, at("2025-12-31T00:00:00Z"), b)
	if c.LocalDate != "2026-01-01" {
		t.Fatal("weekdays incorrectly uses holidays", c)
	}
	r.DayFilter = "all_days"
	c = NextCandidate(r, at("2026-01-02T02:00:00Z"), b)
	if c.LocalDate != "2026-01-03" {
		t.Fatal(c)
	}
	for _, raw := range []string{`{"region":"CN","from":"2026-01-01","to":"2026-12-31","version":"test","source":"https://www.gov.cn/a","published":"2025-11-04","days":{"2026-01-01":"holiday","2026-01-01":"makeup"}}`, `{"region":"CN","from":"2026-01-02","to":"2026-12-31","version":"test","days":{}}`} {
		if _, err := ParseWorkCalendars([]byte(raw)); err == nil {
			t.Fatal("accepted corrupt data")
		}
	}
	raw, _ := workCalendarFiles.ReadFile("data/CN-2026.json")
	if _, err := ParseWorkCalendars(raw, raw); err == nil {
		t.Fatal("accepted overlapping coverage")
	}
}
func TestRecurringRecoveryOnlyLatestAndWatermarkSurvivesCompaction(t *testing.T) {
	s, task := recurring(t, daily(), at("2026-01-01T00:00:00Z"))
	now := at("2026-11-19T01:30:00Z")
	s.Evaluate(now)
	r := onlyRun(t, s)
	if r.LocalDate != "2026-11-19" || r.Status != "queued" || task.Version != 1 || len(s.MissedRanges) != 1 || s.MissedRanges[0].Count != 322 {
		t.Fatal(r, task, s.MissedRanges)
	}
	s = s.Clone()
	if err := s.Validate("dm", "instance"); err != nil {
		t.Fatal(err)
	}
	s.Evaluate(now.Add(-time.Hour))
	if len(s.Runs) != 1 {
		t.Fatal("clock rollback duplicated work")
	}
	r = onlyRun(t, s)
	r.Status = "succeeded"
	r.Settled = true
	if err := s.Compact(now.Add(100 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(s.Runs) != 0 {
		t.Fatal("did not compact")
	}
	s.Evaluate(now)
	if len(s.Runs) != 0 {
		t.Fatal("compacted old occurrence regenerated")
	}
	s.Evaluate(now.Add(24 * time.Hour))
	r = onlyRun(t, s)
	if r.LocalDate != "2026-11-20" {
		t.Fatal(r)
	}
	// Even a multi-year gap is handled in bounded batches; no old model work is queued.
	s, _ = recurring(t, daily(), at("2023-01-01T00:00:00Z"))
	for i := 0; i < 5; i++ {
		s.Evaluate(now)
	}
	if len(s.Runs) != 1 || onlyRun(t, s).LocalDate != "2026-11-19" {
		t.Fatal(s.Runs)
	}
}
func TestWorkCalendarUnknownWaitsAndDataRefreshRespectsOriginalDeadline(t *testing.T) {
	r := daily()
	r.DayFilter = "business_days"
	r.WorkCalendar = "CN"
	s, task := recurring(t, r, at("2027-12-31T02:00:00Z"))
	due := at("2028-01-01T01:00:00Z")
	if task.BlockedReason != "work_calendar_uncovered" || task.NextAt != due {
		t.Fatal(task)
	}
	s.Evaluate(due.Add(30 * time.Minute))
	if len(s.Runs) != 0 || !task.EvaluatedThrough.IsZero() {
		t.Fatal("unknown date executed or cursor advanced", task)
	}
	raw := `{"region":"CN","from":"2028-01-01","to":"2028-12-31","version":"test-2028","source":"https://www.gov.cn/test","published":"2027-11-01","days":{"2028-01-01":"makeup"}}`
	book, err := ParseWorkCalendars([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	s = s.Clone()
	s.EvaluateWithCalendars(due.Add(45*time.Minute), book)
	run := onlyRun(t, s)
	if run.Status != "queued" || run.Deadline != due.Add(time.Hour) || run.WorkCalendarVersion != "test-2028" {
		t.Fatal(run)
	}
	s2, task2 := recurring(t, r, at("2027-12-31T02:00:00Z"))
	s2.Evaluate(due.Add(time.Hour))
	run = onlyRun(t, s2)
	if run.Status != "skipped" || run.Reason != "work_calendar_uncovered" || !task2.EvaluatedThrough.Equal(due) {
		t.Fatal(run, task2)
	}
	s2.EvaluateWithCalendars(due.Add(61*time.Minute), book)
	if onlyRun(t, s2).Status != "skipped" {
		t.Fatal("expired calendar block resurrected")
	}
	r.DayFilter = "all_days"
	r.WorkCalendar = ""
	s3, _ := recurring(t, r, at("2027-12-31T02:00:00Z"))
	s3.EvaluateWithCalendars(due, nil)
	if onlyRun(t, s3).Status != "queued" {
		t.Fatal("all_days depended on annual calendar")
	}
}
func TestRecurringSkipPauseResumeAndDefinitionBoundary(t *testing.T) {
	s, task := recurring(t, daily(), at("2026-11-19T00:00:00Z"))
	q := Request{Action: "skip_once", OperationID: "skip", TaskID: task.ID, ExpectedVersion: 1, LocalDate: "2026-11-20"}
	v, err := s.Update(q, Source{MessageSeq: 2}, "A", "member", "UTC", at("2026-11-19T00:00:00Z"))
	if err != nil || v.Task.Version != 2 || v.Task.DefinitionVersion != 1 {
		t.Fatal(v, err)
	}
	skipped := onlyRun(t, s)
	if skipped.Reason != "user_skipped" || !skipped.Settled {
		t.Fatal(skipped)
	}
	preview := PreviewBundled(task, at("2026-11-19T00:00:00Z"), at("2026-11-22T00:00:00Z"))
	if len(preview) != 3 || preview[1].Reason != "user_skipped" {
		t.Fatal(preview)
	}
	s.Evaluate(at("2026-11-19T01:00:00Z"))
	if len(s.Runs) != 2 {
		t.Fatal(s.Runs)
	}
	_, err = s.Update(Request{Action: "pause", OperationID: "pause", TaskID: task.ID, ExpectedVersion: 2}, Source{}, "A", "member", "UTC", at("2026-11-19T01:05:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	s.Evaluate(at("2026-11-22T01:00:00Z"))
	if len(s.Runs) != 2 {
		t.Fatal("paused schedule ran")
	}
	_, err = s.Update(Request{Action: "resume", OperationID: "resume", TaskID: task.ID, ExpectedVersion: 3}, Source{}, "A", "member", "UTC", at("2026-11-22T01:00:00Z"))
	if err != nil || task.NextAt != at("2026-11-23T01:00:00Z") {
		t.Fatal(task, err)
	}
	// Editing at an overdue boundary preserves the old definition's occurrence,
	// then cancels its eligibility and starts the new rule strictly after commit.
	_, err = s.Update(Request{Action: "edit", OperationID: "edit", TaskID: task.ID, ExpectedVersion: 4, Changes: map[string]json.RawMessage{"instruction": json.RawMessage(`"new instruction"`)}}, Source{}, "A", "member", "UTC", at("2026-11-23T01:10:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range s.Runs {
		if run.LocalDate == "2026-11-23" && (run.Task.Definition.Instruction == "new instruction" || run.Status != "cancelled") {
			t.Fatal("old definition rewritten", run)
		}
	}
	if task.NextAt != at("2026-11-24T01:00:00Z") {
		t.Fatal(task)
	}
}
func TestRecurringOverlapUnknownAndSettledFailure(t *testing.T) {
	for _, status := range []string{"running", "unknown", "failed"} {
		s, task := recurring(t, daily(), at("2026-11-19T00:00:00Z"))
		now := at("2026-11-19T01:00:00Z")
		s.Evaluate(now)
		first := onlyRun(t, s)
		first.Status = status
		first.Binding = Binding{"g", 1}
		first.Settled = status == "failed"
		s.Evaluate(now.Add(24 * time.Hour))
		for _, r := range s.Runs {
			if r.ID == first.ID {
				continue
			}
			want := "queued"
			if status == "running" {
				want = "skipped"
			}
			if status == "unknown" {
				want = "blocked"
			}
			if r.Status != want {
				t.Fatal(status, r)
			}
		}
		if task.State != "active" {
			t.Fatal("single run terminated recurrence", task)
		}
	}
}

func TestRecurringFinishPreservesSchedule(t *testing.T) {
	for _, frequency := range []string{"daily", "weekly"} {
		for _, outcome := range []string{"succeeded", "blocked", "failed"} {
			t.Run(frequency+"/"+outcome, func(t *testing.T) {
				rule := daily()
				rule.Frequency = frequency
				if frequency == "weekly" {
					rule.Weekdays = []int{4} // Thursday, the first due date below.
				}
				s, task := recurring(t, rule, at("2026-11-19T00:00:00Z"))
				due := task.NextAt
				s.Evaluate(due)
				run := onlyRun(t, s)
				next := task.NextAt
				watermark := task.EvaluatedThrough
				binding := Binding{"generation", 1}
				if _, err := s.Claim(run.ID, binding, "member", due.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Finish(run.ID, binding, "member", outcome, "accepted recurring report", nil); err != nil {
					t.Fatal(err)
				}
				s.TurnFinished("member", binding, "completed", true, due.Add(2*time.Second))
				if task.State != "active" || task.NextAt != next || !next.After(due) || task.EvaluatedThrough != watermark || task.Version != 1 || task.DefinitionVersion != 1 {
					t.Fatal("finishing a run changed its recurring task", task)
				}
				if run.Phase != "publishing" || run.Publication.Status != "pending" || run.Outcome != outcome || run.Report != "accepted recurring report" {
					t.Fatal("accepted report did not survive turn settlement", run)
				}
			})
		}
	}
}

func TestRecurringValidationAndOnceFilters(t *testing.T) {
	for _, rule := range []Schedule{{Frequency: "weekly", StartDate: "2026-11-20", LocalTime: "09:00", TimeZone: "UTC"}, {Frequency: "weekly", StartDate: "2026-11-20", LocalTime: "09:00", TimeZone: "UTC", Weekdays: []int{1, 1}}, {Frequency: "daily", StartDate: "2026-11-20", LocalTime: "09:00", TimeZone: "UTC", DayFilter: "business_days"}, {Frequency: "daily", StartDate: "2026-11-20", LocalTime: "09:00", TimeZone: "UTC", EndDate: "2026-11-19"}} {
		if validateSchedule(rule) == nil {
			t.Fatal(rule)
		}
	}
	d := definition()
	d.Schedule.StartDate = "2026-01-01"
	d.Schedule.DayFilter = "business_days"
	d.Schedule.WorkCalendar = "CN"
	s := New("dm", "instance")
	_, err := s.Update(Request{Action: "create", OperationID: "holiday", Definition: &d}, Source{}, "A", "m", "UTC", at("2025-12-31T00:00:00Z"))
	if err == nil || !strings.Contains(err.Error(), "excluded") {
		t.Fatal("accepted permanently filtered once", err)
	}
}

func TestWorkCalendarRevalidatesQueuedButFreezesStartedRun(t *testing.T) {
	rule := daily()
	rule.DayFilter = "business_days"
	rule.WorkCalendar = "CN"
	s, _ := recurring(t, rule, at("2026-11-19T00:00:00Z"))
	due := at("2026-11-19T01:00:00Z")
	s.Evaluate(due)
	r := onlyRun(t, s)
	originalVersion := r.WorkCalendarVersion
	empty, _ := ParseWorkCalendars()
	s.EvaluateWithCalendars(due.Add(time.Minute), empty)
	if r.Reason != "work_calendar_uncovered" || len(s.Pending("member", due.Add(time.Minute))) != 0 {
		t.Fatal("uncovered queued work remained eligible", r)
	}
	if _, err := s.ClaimWithCalendars(r.ID, Binding{"g", 1}, "member", due.Add(time.Minute), empty); err == nil {
		t.Fatal("claim used stale calendar")
	}
	s.EvaluateWithCalendars(due.Add(2*time.Minute), bundledCalendars)
	if r.Reason != "" || len(s.Pending("member", due.Add(2*time.Minute))) != 1 {
		t.Fatal("calendar fill did not restore eligibility", r)
	}
	if _, err := s.Claim(r.ID, Binding{"g", 1}, "member", due.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	s.EvaluateWithCalendars(due.Add(4*time.Minute), empty)
	if r.Status != "running" || r.WorkCalendarVersion != originalVersion {
		t.Fatal("started run calendar changed", r)
	}
	// A corrected holiday must suppress an unstarted occurrence, never move it.
	s, _ = recurring(t, rule, at("2026-11-19T00:00:00Z"))
	s.Evaluate(due)
	r = onlyRun(t, s)
	raw, _ := workCalendarFiles.ReadFile("data/CN-2026.json")
	var year WorkYear
	_ = json.Unmarshal(raw, &year)
	year.Version = "reviewed-correction"
	year.Days["2026-11-19"] = "holiday"
	raw, _ = json.Marshal(year)
	book, err := ParseWorkCalendars(raw)
	if err != nil {
		t.Fatal(err)
	}
	s.EvaluateWithCalendars(due.Add(time.Minute), book)
	if r.Status != "skipped" || r.Reason != "work_calendar_changed" {
		t.Fatal(r)
	}
}
func TestExplicitExcludedDatesRemainVisibleAfterEvaluation(t *testing.T) {
	rule := daily()
	rule.ExcludedDates = []string{"2026-11-20"}
	s, task := recurring(t, rule, at("2026-11-19T00:00:00Z"))
	s.Evaluate(at("2026-11-21T01:00:00Z"))
	preview := PreviewBundled(task, at("2026-11-20T00:00:00Z"), at("2026-11-21T00:00:00Z"))
	if len(preview) != 1 || preview[0].Reason != "user_skipped" {
		t.Fatal("explicit skip disappeared", preview)
	}
}

func TestRecurringResumeAfterClockRollbackKeepsEvaluatedBoundary(t *testing.T) {
	s, task := recurring(t, daily(), at("2026-11-19T00:00:00Z"))
	future := at("2026-11-22T01:00:00Z")
	s.Evaluate(future)
	_, err := s.Update(Request{Action: "pause", OperationID: "pause-clock", TaskID: task.ID, ExpectedVersion: 1}, Source{}, "A", "member", "UTC", future)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Update(Request{Action: "resume", OperationID: "resume-clock", TaskID: task.ID, ExpectedVersion: 2}, Source{}, "A", "member", "UTC", at("2026-11-20T00:00:00Z"))
	if err != nil || task.NextAt != at("2026-11-23T01:00:00Z") {
		t.Fatal("resume forgot evaluated watermark", task, err)
	}
	count := len(s.Runs)
	s.Evaluate(future)
	if len(s.Runs) != count {
		t.Fatal("resume re-created an old period")
	}
}
func TestCalendarDayAnnotationsHonorExclusiveRangeEnd(t *testing.T) {
	days := DayAnnotations(at("2027-12-31T00:00:00Z"), at("2028-01-01T00:00:00Z"), "UTC")
	if len(days) != 1 || days[0].Date != "2027-12-31" || days[0].Kind == "unknown" {
		t.Fatal(days)
	}
}

func TestSkippingLastOccurrenceDoesNotDeactivateQueuedWork(t *testing.T) {
	rule := daily()
	rule.EndDate = "2026-11-20"
	s, task := recurring(t, rule, at("2026-11-19T00:00:00Z"))
	now := at("2026-11-19T01:00:00Z")
	s.Evaluate(now)
	run := onlyRun(t, s)
	_, err := s.Update(Request{Action: "skip_once", OperationID: "last-skip", TaskID: task.ID, ExpectedVersion: 1, LocalDate: "2026-11-20"}, Source{}, "A", "member", "UTC", now)
	if err != nil || task.State != "active" {
		t.Fatal("last skip deactivated a pending run", task, err)
	}
	if _, err := s.Claim(run.ID, Binding{"g", 1}, "member", now); err != nil {
		t.Fatal(err)
	}
}
