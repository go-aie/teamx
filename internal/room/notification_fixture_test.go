package room

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"maps"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// A notification-only provider: opaque keys and an offset, no GitLab IDs,
// dates, project, connection, Workspace or tool. It stays out of production.
var fixtureNotificationSourceID = notification.SourceID{Provider: "fixture", Channel: "updates"}

type fixtureNotificationState struct {
	Format  int
	Key     notification.Key
	Version uint64
	Enabled bool
	Offset  int
	Seen    map[string]bool
	Inbox   map[string]notification.Item
	notification.RuntimeState
}

func (s fixtureNotificationState) Metadata() notification.Metadata {
	return notification.Metadata{Key: s.Key, Version: s.Version, Enabled: s.Enabled, Observed: len(s.Seen), Pending: len(s.Inbox), DeliveryError: s.DeliveryErrorCode}
}
func (s fixtureNotificationState) Validate(owner platform.RoomIdentity) error {
	if s.Format != 53 || s.Key.Owner != owner || s.Key.Source != fixtureNotificationSourceID || !validRestartID(s.Key.SubscriptionID) || s.Version == 0 || s.Offset < 0 || s.Seen == nil || s.Inbox == nil {
		return notification.ErrInvalid
	}
	return s.RuntimeState.Validate(s.Enabled)
}
func (s fixtureNotificationState) copy() fixtureNotificationState {
	s.Seen, s.Inbox = maps.Clone(s.Seen), maps.Clone(s.Inbox)
	return s
}
func (s fixtureNotificationState) CloneState() notification.State { return s.copy() }
func (s fixtureNotificationState) PendingItems() []notification.Item {
	items := make([]notification.Item, 0, len(s.Inbox))
	for _, item := range s.Inbox {
		items = append(items, item)
	}
	return items
}
func (s fixtureNotificationState) Notification(key string) (notification.Item, bool) {
	item, ok := s.Inbox[key]
	return item, ok
}
func (s fixtureNotificationState) Acknowledge(key string) notification.State {
	s = s.copy()
	delete(s.Inbox, key)
	s.DeliveryErrorCode = ""
	return s
}
func (s fixtureNotificationState) WithDeliveryError(code string) notification.State {
	s.DeliveryErrorCode = code
	return s
}
func (s fixtureNotificationState) InvalidateCredential() (notification.State, error) {
	s.Version++
	s.RuntimeState.InvalidateCredential(s.Enabled)
	return s, nil
}
func (s fixtureNotificationState) ScopeFailed(err error) notification.State {
	s.RuntimeState.ScopeFailed(err)
	return s
}

type fixtureNotificationSource struct{ records []string }

func (fixtureNotificationSource) ID() notification.SourceID { return fixtureNotificationSourceID }
func (fixtureNotificationSource) Schema() int               { return 53 }
func (fixtureNotificationSource) Decode(data []byte, owner platform.RoomIdentity) (notification.State, error) {
	var s fixtureNotificationState
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || s.Validate(owner) != nil {
		return nil, notification.ErrInvalid
	}
	return s, nil
}
func (s fixtureNotificationSource) NewRunner(host notification.StepHost) notification.Runner {
	return fixtureNotificationRunner{source: s, host: host}
}

type fixtureNotificationRunner struct {
	source fixtureNotificationSource
	host   notification.StepHost
}

func (r fixtureNotificationRunner) Step(ctx context.Context) (time.Duration, bool) {
	state, _, err := r.host.Read(ctx)
	if err != nil {
		return 0, true
	}
	next := state.(fixtureNotificationState)
	if next.Offset == len(r.source.records) {
		return 0, true
	}
	key := r.source.records[next.Offset]
	next.Offset++
	if !next.Seen[key] {
		next.Seen[key] = true
		next.Inbox[key] = notification.Item{Key: key, DeliveryID: notification.DeliveryID(next.Key, key), Text: "Fixture update: " + key,
			Source: platform.SourceRef{Provider: fixtureNotificationSourceID.Provider, Channel: fixtureNotificationSourceID.Channel, Instance: "https://fixture.invalid", SubscriptionID: next.Key.SubscriptionID, ItemKey: key}}
	}
	err = r.host.Commit(next)
	return 0, err != nil
}

func fixtureNotificationAdapter(source fixtureNotificationSource) notificationAdapter {
	return notificationAdapter{Source: source, Admit: func(_ context.Context, state notification.State) (notification.Scope, error) {
		if _, ok := state.(fixtureNotificationState); !ok {
			return notification.Scope{}, notification.ErrInvalid
		}
		return notification.Scope{Revision: "offset-feed-v1"}, nil
	}}
}
