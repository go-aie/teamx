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
func (m *Manager) PendingDeliveriesLocked(owner platform.RoomIdentity, target Target) map[string]bool {
	out := map[string]bool{}
	for key, saved := range m.states {
		if key.Owner != owner {
			continue
		}
		for _, i := range outstandingItems(saved.state) {
			if i.Route.Target.Canonical() == target.Canonical() {
				out[i.DeliveryID] = true
			}
		}
	}
	return out
}

// ExpireTargetLocked includes disabled/retired subscriptions and frozen routes.
func (m *Manager) ExpireTargetLocked(owner platform.RoomIdentity, target Target) error {
	for key, saved := range m.states {
		if key.Owner != owner {
			continue
		}
		matches := saved.state.Metadata().Target == target
		if !matches {
			for _, i := range outstandingItems(saved.state) {
				if i.Route.Target.Canonical() == target.Canonical() {
					matches = true
					break
				}
			}
		}
		if !matches {
			continue
		}
		next := ApplyExpiry(saved.state, m.expiryCutoff())
		if !reflect.DeepEqual(next, saved.state) {
			if err := m.SaveLocked(next); err != nil {
				return err
			}
		}
	}
	return nil
}
