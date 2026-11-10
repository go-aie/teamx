package gitlab

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

var EventsSourceID = notification.SourceID{Provider: "gitlab", Channel: "events"}

type EventsPrepare func(context.Context, string, Binding) (EventFetcher, int64, error)

// EventsSource owns only provider code and its injected bounded query capability.
// It creates no worker and owns no Room, Session, directory lock or writer.
type EventsSource struct{ Prepare EventsPrepare }

func (*EventsSource) ID() notification.SourceID { return EventsSourceID }
func (*EventsSource) Schema() int               { return 3 }
func (*EventsSource) Decode(data []byte, owner platform.RoomIdentity) (notification.State, error) {
	var state EventsState
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&state) != nil || dec.Decode(new(any)) != io.EOF || state.Validate(owner) != nil {
		return nil, notification.ErrInvalid
	}
	return state, nil
}
func (s *EventsSource) NewRunner(host notification.StepHost) notification.Runner {
	return &EventsRunner{Host: host, Source: s, NeedFull: true}
}

// Configure preserves the first opt-in baseline, history and identity, while
// allowing only an explicit retry to adopt a replacement credential reference.
func ConfigureEvents(owner platform.RoomIdentity, current *EventsState, binding *platform.ProjectBinding, cfg EventsConfig, now time.Time) (EventsState, error) {
	cfg, err := NormalizeEventsConfig(cfg)
	if err != nil {
		return EventsState{}, err
	}
	next := EventsState{}
	if current != nil {
		next = current.Clone()
		if next.Retired {
			return next, notification.ErrConflict
		}
	}
	if current == nil || cfg.Enabled {
		if binding == nil || !validEventsProjectBinding(*binding) {
			return EventsState{}, notification.ErrRequest
		}
		if current != nil && (next.Binding.URL != binding.URL || next.Binding.Project != binding.Project) {
			return EventsState{}, notification.ErrConflict
		}
	}
	if current != nil && (cfg.BackfillSeconds != current.Config.BackfillSeconds || !cfg.BackfillStart.Equal(current.Config.BackfillStart)) {
		return next, notification.ErrConflict
	}
	if current == nil {
		if !cfg.Enabled {
			return EventsState{}, notification.ErrRequest
		}
		baseline, err := notification.BackfillBaseline(cfg.BackfillSeconds, cfg.BackfillStart, now)
		if err != nil {
			return next, err
		}
		var opaque [16]byte
		if _, err := rand.Read(opaque[:]); err != nil {
			return EventsState{}, err
		}
		next = EventsState{RoomIdentity: owner, SubscriptionID: hex.EncodeToString(opaque[:]), Binding: *binding,
			Baseline: baseline, Watermark: baseline, IgnoreBefore: baseline, Filters: []EventsFilterRevision{{Since: baseline, Filter: cfg.Filter}},
			Observed: map[string]EventsObservation{}, Inbox: map[string]EventsInboxEntry{}}
	} else if !reflect.DeepEqual(cfg.Filter, next.Config.Filter) {
		next.Compact()
		if len(next.Filters) >= MaxEventsFilters {
			return EventsState{}, notification.ErrCapacity
		}
		if now.Before(next.Filters[len(next.Filters)-1].Since) {
			return EventsState{}, notification.ErrConflict
		}
		next.Filters = append(next.Filters, EventsFilterRevision{Since: now, Filter: cfg.Filter})
	}
	if next.SubscriptionVersion == ^uint64(0) {
		return EventsState{}, notification.ErrCapacity
	}
	next.SubscriptionVersion++
	if cfg.Enabled {
		next.Binding = *binding
	}
	next.Config = cfg
	next.QuickCache = nil
	next.RuntimeState.Reset(cfg.Enabled, next.ProjectID == 0)
	return next, nil
}

func RetireEvents(s EventsState) (EventsState, error) {
	next := s.Clone()
	if next.SubscriptionVersion == ^uint64(0) {
		return next, notification.ErrCapacity
	}
	next.SubscriptionVersion++
	next.Retired = true
	next.Config.Enabled = false
	next.QuickCache = nil
	next.RuntimeState.Reset(false, false)
	return next, nil
}
func SyncEvents(s EventsState, binding platform.ProjectBinding) (EventsState, bool, error) {
	if s.Retired || !s.Config.Enabled {
		return s, false, notification.ErrRequest
	}
	if binding.URL != s.Binding.URL || binding.Project != s.Binding.Project || s.ProjectID != 0 && binding.ProjectID != 0 && s.ProjectID != binding.ProjectID {
		return s, false, notification.ErrConflict
	}
	next := s.Clone()
	changed := binding != s.Binding
	if changed {
		if next.SubscriptionVersion == ^uint64(0) {
			return s, false, notification.ErrCapacity
		}
		next.SubscriptionVersion++
		next.Binding = binding
		next.QuickCache = nil
	}
	next.RuntimeState.Reset(true, false)
	return next, changed, nil
}

func (s EventsState) Metadata() notification.Metadata {
	return notification.Metadata{Target: s.Config.Target.Canonical(), Key: notification.Key{Owner: s.RoomIdentity, Source: EventsSourceID, SubscriptionID: s.SubscriptionID}, Version: s.SubscriptionVersion, Enabled: s.Config.Enabled, Retired: s.Retired, PlatformID: s.Binding.PlatformID, Observed: len(s.Observed), Pending: len(s.Inbox), DeliveryError: s.DeliveryErrorCode}
}
func (s EventsState) CloneState() notification.State { return s.Clone() }
func (s EventsState) PendingItems() []notification.Item {
	items := make([]notification.Item, 0, len(s.Inbox))
	for key, entry := range s.Inbox {
		items = append(items, notification.Item{Route: entry.Route, Key: key, DeliveryID: entry.Route.DeliveryID(entry.DeliveryID), CreatedAt: entry.Event.CreatedAt, Order: fmt.Sprintf("%020d", entry.Event.ID)})
	}
	return items
}
func (s EventsState) Notification(key string) (notification.Item, bool) {
	entry, ok := s.Inbox[key]
	if !ok {
		return notification.Item{}, false
	}
	return EventsMessage(s, entry), true
}
func (s EventsState) Acknowledge(key string) notification.State {
	next := s.Clone()
	delete(next.Inbox, key)
	next.RuntimeState.ResumeCapacity()
	next.DeliveryErrorCode = ""
	next.Compact()
	return next
}
func (s EventsState) WithDeliveryError(code string) notification.State {
	next := s.Clone()
	next.DeliveryErrorCode = code
	return next
}
func (s EventsState) InvalidateCredential() (notification.State, error) {
	next := s.Clone()
	if next.SubscriptionVersion == ^uint64(0) {
		return nil, notification.ErrCapacity
	}
	next.SubscriptionVersion++
	next.QuickCache = nil
	next.RuntimeState.InvalidateCredential(next.Config.Enabled)
	if next.Scan != nil {
		next.Scan.NextPage = 1
		next.Scan.Pages = 0
		next.Scan.Bytes = 0
	}
	return next, nil
}
func (s EventsState) ScopeFailed(err error) notification.State {
	next := s.Clone()
	next.RuntimeState.ScopeFailed(err)
	return next
}
