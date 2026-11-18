package notification

import (
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// ExpiryPolicy is service-wide, never frozen into a Room's configuration.
// Zero is the default policy; a positive AfterDays preserves an explicit false.
type ExpiryPolicy struct {
	AfterDays   int
	SkipExpired bool
}

func (p ExpiryPolicy) Resolved() ExpiryPolicy {
	if p.AfterDays == 0 {
		return ExpiryPolicy{3, true}
	}
	return p
}
func (p ExpiryPolicy) Validate() error {
	// Avoid duration overflow while accepting every representable day count.
	if p.AfterDays <= 0 || int64(p.AfterDays) > int64((1<<63-1)/(24*time.Hour)) {
		return ErrRequest
	}
	return nil
}
func (p ExpiryPolicy) Cutoff(now time.Time) time.Time {
	return now.UTC().Add(-time.Duration(p.Resolved().AfterDays) * 24 * time.Hour)
}
func Expired(created, cutoff time.Time) bool {
	return !cutoff.IsZero() && !created.IsZero() && !created.After(cutoff)
}

type expiringState interface{ Expire(time.Time) State }

// ApplyExpiry also enforces the provider's durable floor when cutoff is zero.
func ApplyExpiry(s State, cutoff time.Time) State {
	if e, ok := s.(expiringState); ok {
		return e.Expire(cutoff)
	}
	return s
}
func (m *Manager) expiryCutoff() time.Time {
	if m.host.Expiry != nil {
		p := m.host.Expiry().Resolved()
		if p.SkipExpired {
			return p.Cutoff(m.Now())
		}
	}
	return time.Time{}
}

// outstandingItems includes unverified conversations; they still own receipts.
func outstandingItems(s State) []Item {
	if inv, ok := s.(interface{ OutstandingItems() []Item }); ok {
		return inv.OutstandingItems()
	}
	return s.PendingItems()
}

// PendingDeliveriesLocked checks frozen item routes, including historical
// subscriptions whose current destination differs from an outstanding route.
func (m *sourceStore) PendingDeliveriesLocked(owner platform.RoomIdentity, target Target) map[string]bool {
	out := map[string]bool{}
	for key, saved := range m.states {
		if key.Owner != owner {
			continue
		}
		for _, i := range outstandingItems(saved.state) {
			if i.Route.Target == target {
				out[i.DeliveryID] = true
			}
		}
	}
	return out
}

// ExpireTargetLocked includes disabled/retired subscriptions and frozen routes.
type expirySchedule struct {
	revision uint64
	policy   ExpiryPolicy
	due      time.Time
}

// Repeated idle checks do not rescan a subscription's pending inventory. Any
// source installation or policy change invalidates this bounded schedule.
func (m *sourceStore) expireKey(key Key) error {
	saved, ok := m.states[key]
	if !ok {
		return ErrIdentity
	}
	policy := ExpiryPolicy{}
	if m.host.Expiry != nil {
		policy = m.host.Expiry().Resolved()
	}
	if !policy.SkipExpired {
		return nil
	}
	if m.expiry == nil {
		m.expiry = map[Key]expirySchedule{}
	}
	cached, exists := m.expiry[key]
	if !exists || cached.revision != saved.revision || cached.policy != policy {
		cached = expirySchedule{revision: saved.revision, policy: policy}
		for _, item := range outstandingItems(saved.state) {
			if item.CreatedAt.IsZero() {
				continue
			}
			due := item.CreatedAt.Add(time.Duration(policy.AfterDays) * 24 * time.Hour)
			if cached.due.IsZero() || due.Before(cached.due) {
				cached.due = due
			}
		}
		m.expiry[key] = cached
	}
	if cached.due.IsZero() || m.Now().Before(cached.due) {
		return nil
	}
	next := ApplyExpiry(saved.state, m.expiryCutoff())
	if reflect.DeepEqual(next, saved.state) {
		return nil
	}
	return m.SaveLocked(next)
}
func (m *sourceStore) ExpireTargetLocked(owner platform.RoomIdentity, target Target) error {
	for key, saved := range m.states {
		if key.Owner != owner {
			continue
		}
		matches := saved.state.Metadata().Target == target
		if !matches {
			for _, item := range outstandingItems(saved.state) {
				if item.Route.Target == target {
					matches = true
					break
				}
			}
		}
		if matches {
			if err := m.expireKey(key); err != nil {
				return err
			}
		}
	}
	return nil
}

// PendingInputDeliveriesLocked retains source/channel namespaces for partition
// reclamation. Frozen historical routes are checked just like ordinary delivery.
func (m *sourceStore) PendingInputDeliveriesLocked(owner platform.RoomIdentity, target Target) map[SourceID]map[string]bool {
	out := map[SourceID]map[string]bool{}
	for key, saved := range m.states {
		if key.Owner != owner {
			continue
		}
		if out[key.Source] == nil {
			out[key.Source] = map[string]bool{}
		}
		for _, item := range outstandingItems(saved.state) {
			if item.Route.Target == target {
				out[key.Source][item.DeliveryID] = true
			}
		}
	}
	return out
}
