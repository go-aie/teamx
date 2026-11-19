package calendar

import (
	"fmt"
	"slices"
	"time"
)

func dateValue(s string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", s)
	if err != nil || d.Format("2006-01-02") != s || d.Year() < 1 {
		return time.Time{}, Fail("invalid_request", "schedule", "use YYYY-MM-DD dates")
	}
	return d, nil
}
func validateSchedule(r Schedule) error {
	if r.Frequency != "once" && r.Frequency != "daily" && r.Frequency != "weekly" {
		return Fail("invalid_request", "schedule.frequency", "use once, daily or weekly")
	}
	start, err := dateValue(r.StartDate)
	if err != nil {
		return err
	}
	if r.EndDate != "" {
		end, err := dateValue(r.EndDate)
		if err != nil || end.Before(start) {
			return Fail("invalid_request", "schedule.end_date", "end_date must include or follow start_date")
		}
	}
	clock, err := time.Parse("15:04", r.LocalTime)
	if err != nil || clock.Format("15:04") != r.LocalTime {
		return Fail("invalid_request", "schedule.local_time", "use HH:MM at minute precision")
	}
	if _, err := time.LoadLocation(r.TimeZone); err != nil || r.TimeZone == "" || r.TimeZone == "Local" {
		return Fail("invalid_request", "schedule.time_zone", "use an IANA time zone")
	}
	if r.DayFilter != "" && r.DayFilter != "all_days" && r.DayFilter != "weekdays" && r.DayFilter != "business_days" {
		return Fail("invalid_request", "schedule.day_filter", "use all_days, weekdays or business_days")
	}
	if r.DayFilter == "business_days" && r.WorkCalendar != "CN" || r.DayFilter != "business_days" && r.WorkCalendar != "" {
		return Fail("invalid_request", "schedule.work_calendar", "business_days requires CN; other filters omit work_calendar")
	}
	seen := map[int]bool{}
	intersects := false
	for _, day := range r.Weekdays {
		if day < 1 || day > 7 || seen[day] {
			return Fail("invalid_request", "schedule.weekdays", "use unique ISO weekdays 1-7")
		}
		seen[day] = true
		intersects = intersects || day <= 5
	}
	if r.Frequency == "weekly" && len(seen) == 0 || r.Frequency != "weekly" && len(seen) > 0 {
		return Fail("invalid_request", "schedule.weekdays", "weekdays is required only for weekly")
	}
	if r.Frequency == "weekly" && r.DayFilter == "weekdays" && !intersects {
		return Fail("invalid_request", "schedule.day_filter", "weekly weekdays do not intersect the date filter")
	}
	if len(r.ExcludedDates) > 366 {
		return Fail("invalid_request", "schedule.excluded_dates", "at most 366 excluded dates")
	}
	dates := map[string]bool{}
	for _, d := range r.ExcludedDates {
		if _, err := dateValue(d); err != nil || d < r.StartDate || r.EndDate != "" && d > r.EndDate || dates[d] {
			return Fail("invalid_request", "schedule.excluded_dates", "use unique dates inside the schedule")
		}
		dates[d] = true
	}
	return nil
}

// Resolve local wall time using the actual offsets around that date. A fold
// returns both instants in order; gaps return none. This also handles half-hour
// transitions and date-line jumps without normalizing to a different wall time.
func localMinutes(date, clock string, loc *time.Location) []time.Time {
	wall, err := time.Parse("2006-01-02 15:04", date+" "+clock)
	if err != nil {
		return nil
	}
	offsets := map[int]bool{}
	for h := -48; h <= 48; h += 6 {
		_, off := wall.Add(time.Duration(h) * time.Hour).In(loc).Zone()
		offsets[off] = true
	}
	out := []time.Time{}
	for offset := range offsets {
		v := wall.Add(-time.Duration(offset) * time.Second)
		if v.In(loc).Format("2006-01-02 15:04") == date+" "+clock {
			out = append(out, v.UTC())
		}
	}
	slices.SortFunc(out, func(a, b time.Time) int { return a.Compare(b) })
	return out
}
func Once(r Schedule) (time.Time, error) {
	if err := validateSchedule(r); err != nil {
		return time.Time{}, err
	}
	if r.Frequency != "once" {
		return time.Time{}, Fail("invalid_request", "schedule.frequency", "expected once")
	}
	loc, _ := time.LoadLocation(r.TimeZone)
	v := localMinutes(r.StartDate, r.LocalTime, loc)
	if len(v) == 0 {
		return time.Time{}, Fail("invalid_request", "schedule.local_time", "nonexistent local minute; choose a valid time")
	}
	if len(v) > 1 {
		return time.Time{}, Fail("invalid_request", "schedule.local_time", "ambiguous local minute; choose an unambiguous time")
	}
	return v[0], nil
}

type Candidate struct {
	DueAt               time.Time `json:"due_at"`
	LocalDate           string    `json:"local_date"`
	WorkCalendarVersion string    `json:"work_calendar_version,omitempty"`
	Reason              string    `json:"reason,omitempty"`
}

// candidateOn is shared by preview, dispatch and skip_once. An unknown workday
// stays a candidate with an explicit block; a known holiday is filtered out.
func candidateOn(r Schedule, date string, book *WorkCalendars, includeExcluded bool) *Candidate {
	if date < r.StartDate || r.EndDate != "" && date > r.EndDate || r.Frequency == "once" && date != r.StartDate {
		return nil
	}
	day, err := dateValue(date)
	if err != nil {
		return nil
	}
	iso := (int(day.Weekday())+6)%7 + 1
	if r.Frequency == "weekly" && !slices.Contains(r.Weekdays, iso) || r.DayFilter == "weekdays" && iso > 5 {
		return nil
	}
	work := book.Day(date)
	if r.DayFilter == "business_days" && (work.Kind == "holiday" || work.Kind == "weekend") {
		return nil
	}
	excluded := slices.Contains(r.ExcludedDates, date)
	if excluded && !includeExcluded {
		return nil
	}
	loc, _ := time.LoadLocation(r.TimeZone)
	times := localMinutes(date, r.LocalTime, loc)
	if len(times) == 0 {
		return nil
	} // recurring gap: skip, never shift
	c := &Candidate{DueAt: times[0], LocalDate: date}
	if r.DayFilter == "business_days" {
		c.WorkCalendarVersion = work.Version
		if work.Kind == "unknown" {
			c.Reason = "work_calendar_uncovered"
		}
	}
	if excluded {
		c.Reason = "user_skipped"
	}
	return c
}

// NextCandidate returns the first nominal occurrence strictly after after. It
// does not invent a future working day when coverage is unavailable.
func NextCandidate(r Schedule, after time.Time, book *WorkCalendars) *Candidate {
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		return nil
	}
	date := after.In(loc).Format("2006-01-02")
	if date < r.StartDate {
		date = r.StartDate
	}
	if r.Frequency == "once" {
		date = r.StartDate
	}
	d, err := dateValue(date)
	if err != nil {
		return nil
	}
	// Weekly excluded dates can span seven years; beyond known CN coverage the
	// first nominal date is explicitly unknown, so the search stays bounded.
	for i := 0; i < 366*8+14; i++ {
		date = d.Format("2006-01-02")
		if d.Year() > 9999 || r.EndDate != "" && date > r.EndDate {
			return nil
		}
		if c := candidateOn(r, date, book, false); c != nil && c.DueAt.After(after) {
			return c
		}
		if r.Frequency == "once" {
			return nil
		}
		d = d.AddDate(0, 0, 1)
	}
	return nil
}
func prepareSchedule(r *Schedule, now time.Time) {
	if r.StartDate == "" && (r.Frequency == "daily" || r.Frequency == "weekly") {
		if loc, err := time.LoadLocation(r.TimeZone); err == nil {
			r.StartDate = now.In(loc).Format("2006-01-02")
		}
	}
}
func firstCandidate(d Definition, draft bool, now time.Time, book *WorkCalendars) (*Candidate, error) {
	prepareSchedule(&d.Schedule, now)
	if _, err := ValidateDefinition(d, draft); err != nil {
		return nil, err
	}
	if draft {
		return nil, nil
	}
	if d.Schedule.Frequency == "once" {
		due, err := Once(d.Schedule)
		if err != nil {
			return nil, err
		}
		if !due.After(now) {
			return nil, Fail("invalid_request", "schedule", "choose a time after commit")
		}
		if c := candidateOn(d.Schedule, d.Schedule.StartDate, book, false); c != nil {
			return c, nil
		}
		return nil, Fail("invalid_request", "schedule.day_filter", "this one-time date is excluded; choose another date or filter")
	}
	c := NextCandidate(d.Schedule, now, book)
	if c == nil {
		return nil, Fail("invalid_request", "schedule", "no future candidate within the rule")
	}
	return c, nil
}
func RuleSummary(d Definition) string {
	r := d.Schedule
	frequency := r.Frequency
	if frequency == "" {
		return "time needs information"
	}
	repeat := frequency
	if frequency == "weekly" {
		repeat = fmt.Sprintf("weekly %v (ISO 1-7)", r.Weekdays)
	}
	filter := r.DayFilter
	if filter == "" {
		filter = "all_days"
	}
	if filter == "business_days" {
		filter = "business_days CN (含调休补班)"
	}
	misfire := d.Misfire
	if misfire == "" {
		misfire = "latest_within_one_hour"
	}
	end := ""
	if r.EndDate != "" {
		end = "; through " + r.EndDate + " inclusive"
	}
	excluded := ""
	if len(r.ExcludedDates) > 0 {
		excluded = fmt.Sprintf("; excluded_dates=%v", r.ExcludedDates)
	}
	return fmt.Sprintf("%s %s %s (%s); %s; misfire=%s%s%s", repeat, r.StartDate, r.LocalTime, r.TimeZone, filter, misfire, end, excluded)
}

// Preview only expands this committed definition in the requested bounded
// range. The adapter merges materialized runs by this same occurrence identity.
func Preview(t *Task, from, to time.Time, book *WorkCalendars) []Candidate {
	out := []Candidate{}
	if t.State != "active" || !to.After(from) || to.Sub(from) > 42*24*time.Hour {
		return out
	}
	r := t.Definition.Schedule
	loc, err := time.LoadLocation(r.TimeZone)
	if err != nil {
		return out
	}
	d, err := dateValue(from.In(loc).Format("2006-01-02"))
	if err != nil {
		return out
	}
	last := to.Add(-time.Nanosecond).In(loc).Format("2006-01-02")
	for !(d.Format("2006-01-02") > last) {
		date := d.Format("2006-01-02")
		if c := candidateOn(r, date, book, true); c != nil && !c.DueAt.Before(from) && c.DueAt.Before(to) && (c.DueAt.After(taskBoundary(t)) || c.Reason == "user_skipped" && c.DueAt.After(t.CreatedAt)) {
			out = append(out, *c)
		}
		d = d.AddDate(0, 0, 1)
	}
	return out
}

// FirstScheduledAt describes the current definition's first candidate after its
// activation boundary. It ignores evaluation watermarks, using the same rules.
func FirstScheduledAt(t *Task) time.Time {
	if t.State == "needs_info" {
		return time.Time{}
	}
	if c := NextCandidate(t.Definition.Schedule, t.EffectiveAfter, bundledCalendars); c != nil {
		return c.DueAt
	}
	return time.Time{}
}
func OccurrenceID(t *Task, c Candidate) string { return runID(t, &c) }
func WorkCalendarCoverage() []WorkCoverage     { return bundledCalendars.Coverage() }
func PreviewBundled(t *Task, from, to time.Time) []Candidate {
	return Preview(t, from, to, bundledCalendars)
}
func DayAnnotations(from, to time.Time, zone string) []WorkDay {
	out := []WorkDay{}
	loc, err := time.LoadLocation(zone)
	if err != nil || !to.After(from) || to.Sub(from) > 42*24*time.Hour {
		return out
	}
	d, _ := dateValue(from.In(loc).Format("2006-01-02"))
	last := to.Add(-time.Nanosecond).In(loc).Format("2006-01-02")
	for d.Format("2006-01-02") <= last {
		out = append(out, bundledCalendars.Day(d.Format("2006-01-02")))
		d = d.AddDate(0, 0, 1)
	}
	return out
}
