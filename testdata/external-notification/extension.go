// Package localnotification is a deterministic, accountless notification source.
// It uses only public TeamX contracts and preserves its own saved progress.
package localnotification

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/go-aie/teamx/extension"
	"github.com/go-aie/teamx/platform"
	"github.com/go-aie/teamx/platform/notification"
)

var sourceID = notification.SourceID{Provider: "local-feed", Channel: "updates"}

func Extension() extension.Extension {
	return extension.Extension{Name: "local-notification", Notifications: []notification.Provider{{
		Provider: sourceID.Provider,
		Sources:  []notification.Declaration{{ID: sourceID, New: func() notification.Source { return source{} }, Bind: bind}},
		Input:    &notification.InputCapability{Codec: codec{}, EncodeStoredInput: encode, DecodeStoredInput: decode},
	}}}
}

type state struct {
	Owner         platform.RoomIdentity `json:"owner"`
	Target        notification.Target   `json:"target"`
	Subscription  string                `json:"subscription"`
	Version       uint64                `json:"version"`
	Emitted       bool                  `json:"emitted"`
	Pending       *notification.Item    `json:"pending,omitempty"`
	DeliveryError string                `json:"delivery_error,omitempty"`
	Paused        bool                  `json:"paused,omitempty"`
}

func (s state) Metadata() notification.Metadata {
	pending := 0
	if s.Pending != nil {
		pending = 1
	}
	return notification.Metadata{Key: notification.Key{Owner: s.Owner, Source: sourceID, SubscriptionID: s.Subscription}, Target: s.Target, Version: s.Version, Enabled: true, Pending: pending, DeliveryError: s.DeliveryError}
}
func (s state) Validate(owner platform.RoomIdentity) error {
	if s.Owner != owner || owner.RoomID == "" || owner.RoomInstance == "" || !s.Target.Valid() || s.Subscription != "11111111111111111111111111111111" || s.Version == 0 || s.Pending != nil && (!s.Emitted || s.Pending.Source.Provider != sourceID.Provider || s.Pending.Source.Channel != sourceID.Channel || s.Pending.Source.SubscriptionID != s.Subscription || s.Pending.Key != "local-item" || s.Pending.DeliveryID != notification.DeliveryID(s.Metadata().Key, "local-item") || s.Pending.Route.Target != s.Target || !s.Pending.Route.Valid(s.Version)) {
		return notification.ErrInvalid
	}
	return nil
}
func (s state) CloneState() notification.State {
	if s.Pending != nil {
		item := *s.Pending
		s.Pending = &item
	}
	return s
}
func (s state) PendingItems() []notification.Item {
	if s.Pending == nil {
		return nil
	}
	return []notification.Item{*s.Pending}
}
func (s state) Notification(key string) (notification.Item, bool) {
	if s.Pending == nil || s.Pending.Key != key {
		return notification.Item{}, false
	}
	return *s.Pending, true
}
func (s state) Acknowledge(key string) notification.State {
	if s.Pending != nil && s.Pending.Key == key {
		s.Pending = nil
		s.DeliveryError = ""
	}
	return s
}
func (s state) WithDeliveryError(code string) notification.State { s.DeliveryError = code; return s }
func (s state) InvalidateCredential() (notification.State, error) {
	s.Version++
	s.Paused = true
	return s, nil
}
func (s state) ScopeFailed(error) notification.State { s.Paused = true; return s }
func (s state) QueryPaused() bool                    { return s.Paused }

type source struct{}

func (source) ID() notification.SourceID { return sourceID }
func (source) Schema() int               { return 1 }
func (source) Decode(b []byte, owner platform.RoomIdentity) (notification.State, error) {
	var s state
	if err := strictDecode(b, &s); err != nil {
		return nil, err
	}
	return s, s.Validate(owner)
}
func (source) NewRunner(h notification.StepHost) notification.Runner { return runner{h} }

func bind(source notification.Source, host notification.RuntimeHost) (notification.Runtime, error) {
	return notification.Runtime{Source: source,
		Admit: func(ctx context.Context, s notification.State) (notification.Scope, error) {
			if err := ctx.Err(); err != nil {
				return notification.Scope{}, err
			}
			if err := s.Validate(s.Metadata().Key.Owner); err != nil {
				return notification.Scope{}, err
			}
			return notification.Scope{Revision: "local-feed/v1"}, nil
		},
		CheckLoaded: func(owner platform.RoomIdentity) error {
			for _, s := range host.States(owner) {
				if err := s.Validate(owner); err != nil {
					return err
				}
			}
			return nil
		},
	}, nil
}

type runner struct{ host notification.StepHost }

func (r runner) Step(ctx context.Context) (time.Duration, bool) {
	current, _, err := r.host.Read(ctx)
	if err != nil {
		return time.Second, false
	}
	s := current.(state)
	if s.Emitted {
		return time.Hour, false
	}
	key := "local-item"
	s.Pending = &notification.Item{Key: key, DeliveryID: notification.DeliveryID(s.Metadata().Key, key),
		CreatedAt: r.host.Now(), Order: key, Text: "A notification from the external module",
		Route:  notification.Route{Target: s.Target, Version: s.Version},
		Source: platform.SourceRef{Provider: sourceID.Provider, Channel: sourceID.Channel, Instance: "local://feed", SubscriptionID: s.Subscription, ItemKey: key}}
	s.Emitted = true
	// No source-side acknowledgement occurs here. TeamX first persists this
	// pending item, then clears it only after the Inbox's durable receipt.
	if err := r.host.Commit(s); err != nil {
		return time.Second, false
	}
	return time.Hour, false
}

type codec struct{}

func (codec) Descriptor() notification.InputDescriptor {
	return notification.InputDescriptor{Provider: sourceID.Provider, Codec: "local-feed.notification", Schema: 1, GroupingVersion: 1, Channels: []string{sourceID.Channel}, Policy: notification.DefaultInputPolicy()}
}
func (codec) Decode(schema int, data json.RawMessage) (notification.InputProjection, error) {
	var item notification.Item
	if schema != 1 {
		return notification.InputProjection{}, notification.ErrInvalid
	}
	if err := strictDecode(data, &item); err != nil {
		return notification.InputProjection{}, err
	}
	if item.Source.Provider != sourceID.Provider || item.Source.Channel != sourceID.Channel || item.Source.Instance != "local://feed" || item.Source.SubscriptionID == "" || !item.Route.Target.Valid() {
		return notification.InputProjection{}, notification.ErrIdentity
	}
	return notification.InputProjection{Group: notification.StoredInputGroup(item), Label: "Local updates", SourceLabel: "External notification fixture"}, nil
}
func encode(item notification.Item) (notification.AppendRequest, error) {
	data, err := json.Marshal(item)
	if err != nil {
		return notification.AppendRequest{}, err
	}
	if _, err := (codec{}).Decode(1, data); err != nil {
		return notification.AppendRequest{}, err
	}
	return notification.AppendRequest{DeliveryID: item.DeliveryID, Channel: item.Source.Channel, CreatedAt: item.CreatedAt, Payload: data, DeliveryDigest: notification.InputDigest(data)}, nil
}
func decode(record notification.StoredRecord) (notification.Item, error) {
	var item notification.Item
	if err := strictDecode(record.Payload, &item); err != nil {
		return item, err
	}
	request, err := encode(item)
	if err != nil {
		return item, err
	}
	if record.Schema != 1 || record.Codec != (codec{}).Descriptor().Codec || record.Channel != request.Channel || record.DeliveryID != request.DeliveryID || !record.CreatedAt.Equal(request.CreatedAt) || record.DeliveryDigest != request.DeliveryDigest {
		return item, notification.ErrInvalid
	}
	return item, nil
}
func strictDecode(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("%w: %v", notification.ErrInvalid, err)
	}
	if d.Decode(new(any)) != io.EOF {
		return notification.ErrInvalid
	}
	return nil
}
