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
		if e.Route.Target.Inbox() && notification.Expired(e.Event.CreatedAt, effective) {
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
		if e.Route.Target.Inbox() && notification.Expired(e.Event.CreatedAt, n.ExpiredBefore) {
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
		if e.Route.Target.Inbox() && notification.Expired(e.Todo.CreatedAt, effective) {
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
		if e.Route.Target.Inbox() && notification.Expired(e.Todo.CreatedAt, n.ExpiredBefore) {
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
