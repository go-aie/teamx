package room

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/calendar"
)

type calendarOccurrence struct {
	ID                  string                `json:"occurrence_id"`
	TaskID              string                `json:"task_id"`
	DueAt               time.Time             `json:"due_at"`
	LocalDate           string                `json:"local_date,omitempty"`
	Status              string                `json:"status"`
	Reason              string                `json:"reason,omitempty"`
	WorkCalendarVersion string                `json:"work_calendar_version,omitempty"`
	Run                 *calendar.Run         `json:"run,omitempty"`
	MissedRange         *calendar.MissedRange `json:"missed_range,omitempty"`
}

func calendarManagementList(state calendar.State, q calendarQuery, zone string) (calendarList, error) {
	if q.TaskID != "" || q.RunID != "" || q.History || q.Result {
		return calendarList{}, ErrRequest
	}
	ranged := !q.From.IsZero() || !q.To.IsZero()
	if ranged && (q.From.IsZero() || !q.To.After(q.From) || q.To.Sub(q.From) > 42*24*time.Hour) {
		return calendarList{}, calendar.Fail("invalid_request", "range", "use from/to with a positive range of at most 42 days")
	}
	switch q.State {
	case "", "active", "paused", "needs_info", "completed", "cancelled":
	default:
		return calendarList{}, ErrRequest
	}
	v := calendarList{TimeZone: zone, MessageLinks: []calendarMessageLink{}, Revision: state.Revision, Tasks: []*calendar.Task{}, Runs: []*calendar.Run{}, Occurrences: []calendarOccurrence{}, WorkCalendars: calendar.WorkCalendarCoverage(), Complete: true, HistoryRetentionDays: 90}
	selected := map[string]bool{}
	entries := map[string]calendarOccurrence{}
	for _, t := range state.Tasks {
		if q.State != "" && q.State != t.State {
			continue
		}
		v.Tasks = append(v.Tasks, t)
		selected[t.ID] = true
		if t.CreationSource.MessageSeq != 0 {
			v.MessageLinks = append(v.MessageLinks, calendarMessageLink{TaskID: t.ID, MessageSeq: t.CreationSource.MessageSeq})
		}
		if t.BlockedReason == "work_calendar_uncovered" {
			v.CoverageIncomplete = true
		}
		if ranged {
			for _, c := range calendar.PreviewBundled(t, q.From, q.To) {
				status := "planned"
				if c.Reason == "user_skipped" {
					status = "skipped"
				} else if c.Reason != "" {
					status = "blocked"
					v.CoverageIncomplete = true
				}
				id := calendar.OccurrenceID(t, c)
				entries[id] = calendarOccurrence{ID: id, TaskID: t.ID, DueAt: c.DueAt, LocalDate: c.LocalDate, Status: status, Reason: c.Reason, WorkCalendarVersion: c.WorkCalendarVersion}
			}
		}
	}
	sort.Slice(v.Tasks, func(i, j int) bool {
		if v.Tasks[i].CreatedAt.Equal(v.Tasks[j].CreatedAt) {
			return v.Tasks[i].ID < v.Tasks[j].ID
		}
		return v.Tasks[i].CreatedAt.Before(v.Tasks[j].CreatedAt)
	})
	for _, r := range state.Runs {
		if selected[r.Task.ID] && r.Publication.Status == "published" && r.Publication.Seq != 0 {
			v.MessageLinks = append(v.MessageLinks, calendarMessageLink{TaskID: r.Task.ID, RunID: r.ID, MessageSeq: r.Publication.Seq})
		}
		if !selected[r.Task.ID] || ranged && (r.DueAt.Before(q.From) || !r.DueAt.Before(q.To)) {
			continue
		}
		entries[r.ID] = calendarOccurrence{ID: r.ID, TaskID: r.Task.ID, DueAt: r.DueAt, LocalDate: r.LocalDate, Status: calendarBusinessStatus(r), Reason: r.Reason, WorkCalendarVersion: r.WorkCalendarVersion, Run: r}
	}
	for i, m := range state.MissedRanges {
		if selected[m.Task.ID] && (!ranged || !m.UntilAt.Before(q.From) && m.FromAt.Before(q.To)) {
			entry := m
			id := fmt.Sprintf("missed_%s_%d", m.Task.ID, i)
			entries[id] = calendarOccurrence{ID: id, TaskID: m.Task.ID, DueAt: m.FromAt, Status: "skipped", Reason: m.Reason, MissedRange: &entry}
		}
	}
	sort.Slice(v.MessageLinks, func(i, j int) bool {
		a, b := v.MessageLinks[i], v.MessageLinks[j]
		if a.MessageSeq == b.MessageSeq {
			return a.TaskID < b.TaskID
		}
		return a.MessageSeq < b.MessageSeq
	})
	all := make([]calendarOccurrence, 0, len(entries))
	for _, o := range entries {
		all = append(all, o)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].DueAt.Equal(all[j].DueAt) {
			return all[i].ID < all[j].ID
		}
		if ranged {
			return all[i].DueAt.Before(all[j].DueAt)
		}
		return all[i].DueAt.After(all[j].DueAt)
	})
	filter := calendar.ID("", state.RoomID, state.RoomInstance, q.From, q.To, q.State, zone)
	start := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		if len(parts) != 3 || parts[0] != strconv.FormatUint(state.Revision, 10) || parts[2] != filter {
			return v, calendar.Fail("version_conflict", "cursor", "calendar snapshot or query changed; restart list")
		}
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 || n >= len(all) {
			return v, ErrRequest
		}
		start = n
	}
	end := min(start+200, len(all))
	v.Occurrences = all[start:end]
	for _, o := range v.Occurrences {
		if o.Run != nil {
			v.Runs = append(v.Runs, o.Run)
		}
	}
	if end < len(all) {
		v.NextCursor = fmt.Sprintf("%d:%d:%s", state.Revision, end, filter)
		v.Complete = false
	}
	if ranged {
		v.Days = calendar.DayAnnotations(q.From, q.To, zone)
		for _, day := range v.Days {
			if day.Kind == "unknown" {
				v.CoverageIncomplete = true
			}
		}
	}
	return v, nil
}
