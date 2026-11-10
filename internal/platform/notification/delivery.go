package notification

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
func (m *Manager) Deliver(ctx context.Context, key Key, version uint64) error {
	m.host.Gate.Lock()
	defer m.host.Gate.Unlock()
	current, ok := m.states[key]
	if !ok || current.state.Metadata().Version != version || !current.state.Metadata().Enabled {
		return ErrIdentity
	}
	scope, err := m.host.Admit(ctx, current.state)
	if err != nil {
		if errors.Is(err, ErrIdentity) && current.state.Metadata().Pending > 0 && current.state.Metadata().DeliveryError != "target_removed" {
			if saveErr := m.SaveLocked(current.state.WithDeliveryError("target_removed")); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	if !scope.Current {
		if current.state.Metadata().Pending > 0 && current.state.Metadata().DeliveryError != "target_unavailable" {
			if err := m.SaveLocked(current.state.WithDeliveryError("target_unavailable")); err != nil {
				return err
			}
		}
		return ErrBusy
	}
	items := current.state.PendingItems()
	if len(items) == 0 {
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
	// The owner holds this same gate through its target commit. Clear/restart keeps
	// receipts, so an ambiguous ack repeats this stable ID rather than a new post.
	if err := m.host.Deliver(context.WithoutCancel(ctx), current.state.Metadata(), item); err != nil {
		code := "target_unavailable"
		if errors.Is(err, ErrCapacity) {
			if item.Route.Target.Inbox() {
				code = "inbox_capacity"
			} else {
				code = "room_capacity"
			}
		}
		if errors.Is(err, ErrIdentity) {
			code = "target_removed"
		}
		if current.state.Metadata().DeliveryError != code {
			if saveErr := m.SaveLocked(current.state.WithDeliveryError(code)); saveErr != nil {
				return saveErr
			}
		}
		return err
	}
	return m.SaveLocked(current.state.Acknowledge(item.Key))
}
