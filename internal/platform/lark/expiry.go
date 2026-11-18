package lark

import (
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func (s State) Expire(cutoff time.Time) notification.State {
	effective := cutoff
	if s.ExpiredBefore.After(effective) {
		effective = s.ExpiredBefore
	}
	found := false
	for _, e := range s.Inbox {
		if notification.Expired(e.Item.CreatedAt, effective) {
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
		if notification.Expired(e.Item.CreatedAt, n.ExpiredBefore) {
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
func (s State) OutstandingItems() []notification.Item {
	out := make([]notification.Item, 0, len(s.Inbox))
	for _, e := range s.Inbox {
		out = append(out, e.Item)
	}
	return out
}

func (s State) SourceTerminal(item notification.Item) (string, bool) {
	if item.Source.Instance != s.Binding.Account.Instance || item.Source.Reference.RecipientID != s.Binding.Account.ID || item.Source.Reference.AccountTenant != s.Binding.Account.Tenant {
		return "", false
	}
	if o, ok := s.Observed[item.Key]; ok && o.Route == item.Route {
		return "committed_observation", true
	}
	if notification.Expired(item.CreatedAt, s.ExpiredBefore) {
		return "committed_expiry_floor", true
	}
	// Pruning uses this same conversation and branch floor. Pending items are
	// retained independently, so only already terminal revisions reach this path.
	p, ok := s.Chats[item.Source.Reference.ChatID.Value]
	if !ok || p.Completed == 0 || item.CreatedAt.IsZero() {
		return "", false
	}
	cutoff := p.Completed - 300
	if p.Scan != nil {
		cutoff = min(cutoff, p.Scan.Start)
	}
	if s.Search != nil {
		for _, b := range s.Search.Branches {
			if (p.Chat.Mode == "p2p") != (b.Kind == "private") {
				continue
			}
			if b.Completed == 0 {
				cutoff = min(cutoff, s.Baseline.Unix())
			} else {
				cutoff = min(cutoff, b.Completed-searchOverlap)
			}
			if b.Scan != nil {
				cutoff = min(cutoff, b.Scan.Start)
			}
		}
	}
	return "committed_conversation_floor", item.CreatedAt.Unix() < cutoff
}
