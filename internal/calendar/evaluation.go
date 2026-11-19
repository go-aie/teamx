package calendar

import (
	"slices"
	"time"
)

// MissedRange compacts consecutive old occurrences without losing their frozen
// definition, calendar provenance or count. It never represents runnable work.
type MissedRange struct {
	Task                Task      `json:"task"`
	FromAt              time.Time `json:"from_at"`
	UntilAt             time.Time `json:"until_at"`
	Count               int       `json:"count"`
	Reason              string    `json:"reason"`
	WorkCalendarVersion string    `json:"work_calendar_version,omitempty"`
}

func containsDate(dates []string, date string) bool { return slices.Contains(dates, date) }
func runID(t *Task, c *Candidate) string {
	return ID("run_", t.ID, t.DefinitionVersion, c.LocalDate, t.Definition.Schedule.LocalTime)
}
func newRun(t *Task, c *Candidate) *Run {
	return &Run{ID: runID(t, c), Version: 1, Task: *t, DueAt: c.DueAt, Deadline: c.DueAt.Add(time.Hour), LocalDate: c.LocalDate, Status: "queued", Attempt: 1, Authorization: t.Source, WorkCalendarVersion: c.WorkCalendarVersion}
}
func taskBoundary(t *Task) time.Time {
	boundary := t.CreatedAt
	if t.EffectiveAfter.After(boundary) {
		boundary = t.EffectiveAfter
	}
	if t.EvaluatedThrough.After(boundary) {
		boundary = t.EvaluatedThrough
	}
	return boundary
}
func refreshNext(t *Task, book *WorkCalendars) {
	c := NextCandidate(t.Definition.Schedule, taskBoundary(t), book)
	t.NextAt = time.Time{}
	t.Evaluated = c == nil
	if t.BlockedReason == "work_calendar_uncovered" {
		t.BlockedReason = ""
	}
	if c != nil {
		t.NextAt = c.DueAt
		if c.Reason != "" {
			t.BlockedReason = c.Reason
		}
	}
}
func (s *State) addMissed(t *Task, c *Candidate, reason string) {
	for i := len(s.MissedRanges) - 1; i >= 0; i-- {
		v := &s.MissedRanges[i]
		if v.Task.ID != t.ID {
			continue
		}
		if v.Task.DefinitionVersion == t.DefinitionVersion && v.Reason == reason && v.WorkCalendarVersion == c.WorkCalendarVersion && v.UntilAt.Before(c.DueAt) {
			v.UntilAt = c.DueAt
			v.Count++
			return
		}
		break
	}
	s.MissedRanges = append(s.MissedRanges, MissedRange{Task: *t, FromAt: c.DueAt, UntilAt: c.DueAt, Count: 1, Reason: reason, WorkCalendarVersion: c.WorkCalendarVersion})
}
func (s *State) Evaluate(now time.Time) bool { return s.EvaluateWithCalendars(now, bundledCalendars) }
func (s *State) EvaluateWithCalendars(now time.Time, book *WorkCalendars) bool {
	before := Digest(s.Tasks)
	changed := false
	// Expire old queued work before evaluating a new period. Started work remains
	// occupied until its original execution has settled, even after timeout.
	for _, r := range s.Runs {
		if r.Status == "queued" && r.Task.Definition.Schedule.DayFilter == "business_days" {
			work := book.Day(r.LocalDate)
			if work.Kind == "unknown" {
				if r.Reason != "work_calendar_uncovered" {
					r.Version++
					changed = true
				}
				r.Reason = "work_calendar_uncovered"
				s.Tasks[r.Task.ID].BlockedReason = r.Reason
			} else if work.Kind == "holiday" || work.Kind == "weekend" {
				r.Status = "skipped"
				r.Reason = "work_calendar_changed"
				r.Settled = true
				r.Version++
				changed = true
			} else {
				if r.WorkCalendarVersion != work.Version {
					r.WorkCalendarVersion = work.Version
					r.Version++
					changed = true
				}
				if r.Reason == "work_calendar_uncovered" {
					r.Reason = ""
					r.Version++
					changed = true
				}
			}
		}
		if r.Status == "queued" && !now.Before(r.Deadline) {
			r.Status = "skipped"
			r.Reason = "skipped_late"
			r.Settled = true
			r.Version++
			changed = true
		}
	}
	for _, t := range s.Tasks {
		if t.State != "active" {
			continue
		}
		// Old once cursors were a boolean. Never recreate their retained/compacted run.
		if t.Definition.Schedule.Frequency == "once" && t.Evaluated && t.EffectiveAfter.IsZero() {
			continue
		}
		refreshNext(t, book)
		for batch := 0; batch < 512 && !t.NextAt.IsZero() && !t.NextAt.After(now); batch++ {
			c := NextCandidate(t.Definition.Schedule, taskBoundary(t), book)
			if c == nil {
				break
			}
			next := NextCandidate(t.Definition.Schedule, c.DueAt, book)
			laterDue := next != nil && !next.DueAt.After(now)
			if c.Reason == "work_calendar_uncovered" && now.Before(c.DueAt.Add(time.Hour)) && !laterDue {
				t.BlockedReason = c.Reason
				break // keep cursor: a new bundle may fill this date
			}
			if laterDue {
				reason := "skipped_late"
				for _, previous := range s.Runs {
					if previous.Task.ID != t.ID {
						continue
					}
					if previous.Status == "unknown" || !previous.Settled && previous.Binding.Turn != 0 && previous.Status != "running" {
						reason = "previous_result_unverified"
						break
					}
					if previous.Status == "running" {
						reason = "skipped_overlap"
					}
				}
				if c.Reason != "" {
					reason = c.Reason
				}
				s.addMissed(t, c, reason)
			} else {
				id := runID(t, c)
				if s.Runs[id] == nil {
					r := newRun(t, c)
					if c.Reason != "" {
						r.Status = "skipped"
						r.Reason = c.Reason
						r.Settled = true
					} else if !now.Before(r.Deadline) || t.Definition.Misfire == "skip" && now.Sub(c.DueAt) > 5*time.Second {
						r.Status = "skipped"
						r.Reason = "skipped_late"
						r.Settled = true
					}
					if r.Status == "queued" {
						for _, previous := range s.Runs {
							if previous.Task.ID != t.ID {
								continue
							}
							if previous.Status == "unknown" || !previous.Settled && previous.Binding.Turn != 0 && previous.Status != "running" {
								r.Status = "blocked"
								r.Reason = "previous_result_unverified"
								r.Settled = true
								t.BlockedReason = r.Reason
								break
							}
							if previous.Status == "running" {
								r.Status = "skipped"
								r.Reason = "skipped_overlap"
								r.Settled = true
							}
						}
					}
					s.Runs[id] = r
				}
			}
			t.EvaluatedThrough = c.DueAt
			refreshNext(t, book)
			changed = true
		}
		for _, r := range s.Runs {
			if r.Task.ID == t.ID && r.Status == "queued" && r.Reason == "work_calendar_uncovered" {
				t.BlockedReason = r.Reason
			}
		}
		if t.Evaluated {
			// Ended schedules finish after their accepted work has settled.
			pending := false
			for _, r := range s.Runs {
				if r.Task.ID == t.ID && !r.Settled {
					pending = true
				}
			}
			if !pending {
				t.State = "completed"
			}
		}
	}
	return changed || before != Digest(s.Tasks)
}
