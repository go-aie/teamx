package notification

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"
)

// DeliveryID includes the complete subscription identity and opaque item key.
func DeliveryID(subscription Key, itemKey string) string {
	data, _ := json.Marshal(struct {
		Subscription Key
		ItemKey      string
	}{subscription, itemKey})
	hash := sha256.Sum256(data)
	return fmt.Sprintf("notification-%x", hash)
}
func Backoff(failures int, jitter func(time.Duration) time.Duration, retryAfter int) time.Duration {
	delay := min(jitter(min(5*time.Minute, time.Second<<min(max(failures-1, 0), 9))), 5*time.Minute)
	return max(delay, time.Duration(max(0, min(86400, retryAfter)))*time.Second)
}

// Deliver reserves a single attempt for the exact subscription. Target receipt
// and source acknowledgement are independent durable commits. Queries may run
// during receipt; acknowledgement transforms the latest committed source state.
func (m *Manager) Deliver(ctx context.Context, key Key, version uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current := m.projection(key.Owner)[key]
	if current.state == nil {
		return ErrIdentity
	}
	platformID := current.state.Metadata().PlatformID
	m.mu.Lock()
	if m.stopping || m.retired[key.Owner] {
		m.mu.Unlock()
		return ErrClosing
	}
	if m.deliveries[key] != nil || m.platformPauses[platformID] > 0 {
		m.mu.Unlock()
		return ErrBusy
	}
	flight := &deliveryFlight{platformID: platformID, done: make(chan struct{})}
	m.deliveries[key] = flight
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.deliveries, key); close(flight.done); m.mu.Unlock() }()
	ctx = context.WithoutCancel(ctx)
	if err := m.mutate(key.Owner, func(s *sourceStore) error {
		current, ok := s.states[key]
		if !ok || current.state.Metadata().Version != version || !current.state.Metadata().Enabled {
			return ErrIdentity
		}
		return s.expireKey(key)
	}); err != nil {
		return err
	}
	current = m.projection(key.Owner)[key]
	scope, err := m.host.Admit(context.WithValue(ctx, deliveryAdmissionKey{}, true), current.state)
	if err != nil {
		if errors.Is(err, ErrIdentity) && current.state.Metadata().Pending > 0 {
			_ = m.deliveryError(key, "target_removed")
		}
		return err
	}
	if !scope.Current {
		if current.state.Metadata().Pending > 0 {
			if err := m.deliveryError(key, "target_unavailable"); err != nil {
				return err
			}
		}
		return ErrBusy
	}
	items := current.state.PendingItems()
	if len(items) == 0 {
		if m.host.Maintain != nil {
			return m.host.Maintain(ctx, current.state.Metadata())
		}
		return nil
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.Order < b.Order
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	item, ok := current.state.Notification(items[0].Key)
	if !ok {
		return ErrInvalid
	}
	if m.host.Maintain != nil {
		meta := current.state.Metadata()
		meta.Target = item.Route.Target
		if err := m.host.Maintain(ctx, meta); err != nil && !errors.Is(err, ErrBusy) {
			return err
		}
	}
	if err := m.host.Deliver(ctx, current.state.Metadata(), item); err != nil {
		code := "target_unavailable"
		if errors.Is(err, ErrCapacity) {
			code = "inbox_capacity"
		}
		if errors.Is(err, ErrIdentity) {
			code = "target_removed"
		}
		if saveErr := m.deliveryError(key, code); saveErr != nil {
			return saveErr
		}
		return err
	}
	err = m.mutate(key.Owner, func(s *sourceStore) error {
		latest, ok := s.states[key]
		if !ok {
			return ErrIdentity
		}
		pending, ok := latest.state.Notification(item.Key)
		if !ok {
			return nil
		} // A committed expiration already settled this identity.
		if !reflect.DeepEqual(pending, item) {
			return ErrConflict
		}
		return s.SaveLocked(latest.state.Acknowledge(item.Key))
	})
	if err == nil && m.host.Maintain != nil {
		meta := current.state.Metadata()
		meta.Target = item.Route.Target
		_ = m.host.Maintain(ctx, meta)
	}
	return err
}
func (m *Manager) deliveryError(key Key, code string) error {
	return m.mutate(key.Owner, func(s *sourceStore) error {
		current, ok := s.states[key]
		if !ok {
			return ErrIdentity
		}
		if current.state.Metadata().DeliveryError == code {
			return nil
		}
		return s.SaveLocked(current.state.WithDeliveryError(code))
	})
}

// DeliveryAdmission distinguishes receipt recovery from admitting a new source
// query. A target with no current receivers can still replay a published ID.
type deliveryAdmissionKey struct{}

func DeliveryAdmission(ctx context.Context) bool {
	v, _ := ctx.Value(deliveryAdmissionKey{}).(bool)
	return v
}
