package gitlab

import (
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func (s EventsState) Expire(cutoff time.Time) notification.State {
	effective := cutoff
	if s.ExpiredBefore.After(effective) {
		effective = s.ExpiredBefore
	}
	found := false
	for _, e := range s.Inbox {
		if notification.Expired(e.Event.CreatedAt, effective) {
			found = true
			break
		}
	}
	if !found {
		return s
	}
	n := s.Clone()
	if cutoff.After(n.ExpiredBefore) {
		n.ExpiredBefore = cutoff
	}
	removed := false
	for k, e := range n.Inbox {
		if notification.Expired(e.Event.CreatedAt, n.ExpiredBefore) {
			delete(n.Inbox, k)
			delete(n.Observed, k)
			n.Metrics.ExpiredSkipped++
			removed = true
		}
	}
	if removed {
		n.RuntimeState.ResumeCapacity()
		if n.Metadata().Pending == 0 {
			n.DeliveryErrorCode = ""
		}
	}
	return n
}
func (s TodosState) Expire(cutoff time.Time) notification.State {
	effective := cutoff
	if s.ExpiredBefore.After(effective) {
		effective = s.ExpiredBefore
	}
	found := false
	for _, e := range s.Inbox {
		if notification.Expired(e.Todo.CreatedAt, effective) {
			found = true
			break
		}
	}
	if !found {
		return s
	}
	n := s.Clone()
	if cutoff.After(n.ExpiredBefore) {
		n.ExpiredBefore = cutoff
	}
	removed := false
	for k, e := range n.Inbox {
		if notification.Expired(e.Todo.CreatedAt, n.ExpiredBefore) {
			delete(n.Inbox, k)
			delete(n.Observed, k)
			n.Metrics.ExpiredSkipped++
			removed = true
		}
	}
	if removed {
		n.RuntimeState.ResumeCapacity()
		if n.Metadata().Pending == 0 {
			n.DeliveryErrorCode = ""
		}
	}
	return n
}

// SourceTerminal proves an excluded or acknowledged identity from a retained
// observation or an admission floor that permanently prevents re-entry.
func (s EventsState) SourceTerminal(item notification.Item) (string, bool) {
	if item.Source.Instance != s.Binding.URL || item.Source.ProjectID != s.ProjectID || item.Source.Project != s.Binding.Project {
		return "", false
	}
	if o, ok := s.Observed[item.Key]; ok && o.CreatedAt.Equal(item.CreatedAt) {
		return "committed_observation", true
	}
	if notification.Expired(item.CreatedAt, s.ExpiredBefore) || !item.CreatedAt.IsZero() && item.CreatedAt.Before(s.IgnoreBefore) {
		return "committed_admission_floor", true
	}
	return "", false
}
func (s TodosState) SourceTerminal(item notification.Item) (string, bool) {
	if item.Source.Instance != s.Binding.URL {
		return "", false
	}
	if o, ok := s.Observed[item.Key]; ok && o.CreatedAt.Equal(item.CreatedAt) {
		return "committed_observation", true
	}
	if notification.Expired(item.CreatedAt, s.ExpiredBefore) || !item.CreatedAt.IsZero() && item.CreatedAt.Before(s.Baseline) {
		return "committed_admission_floor", true
	}
	return "", false
}
