package gitlab

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"reflect"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

var TodosSourceID = notification.SourceID{Provider: "gitlab", Channel: "todos"}

type TodosPrepare func(context.Context, string, platform.RoomIdentity, TodosQueryScope) (TodoFetcher, error)
type TodosSource struct{ Prepare TodosPrepare }

func (*TodosSource) ID() notification.SourceID { return TodosSourceID }
func (*TodosSource) Schema() int               { return 2 }
func (*TodosSource) Decode(data []byte, owner platform.RoomIdentity) (notification.State, error) {
	var s TodosState
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(&s) != nil || dec.Decode(new(any)) != io.EOF || s.Validate(owner) != nil {
		return nil, notification.ErrInvalid
	}
	return s, nil
}
func (s *TodosSource) NewRunner(h notification.StepHost) notification.Runner {
	return &TodosRunner{Host: h, Source: s}
}

// Account changes are handled by the owner as retire-before-create. This
// function never mutates an existing subscription into a different account.
func ConfigureTodos(owner platform.RoomIdentity, current *TodosState, b TodosBinding, cfg TodosConfig, now time.Time) (TodosState, error) {
	cfg, err := NormalizeTodosConfig(cfg)
	if err != nil {
		return TodosState{}, err
	}
	var next TodosState
	if current != nil {
		next = current.Clone()
		if next.Retired {
			return next, notification.ErrConflict
		}
	}
	if current == nil || cfg.Enabled {
		if !b.Valid() || cfg.PlatformID != b.PlatformID {
			return next, notification.ErrRequest
		}
		if current != nil && !next.Binding.SameAccount(b) {
			return next, notification.ErrConflict
		}
	}
	if current != nil && (cfg.BackfillSeconds != current.Config.BackfillSeconds || !cfg.BackfillStart.Equal(current.Config.BackfillStart)) {
		return next, notification.ErrConflict
	}
	if current == nil {
		if !cfg.Enabled {
			return next, notification.ErrRequest
		}
		baseline, err := notification.BackfillBaseline(cfg.BackfillSeconds, cfg.BackfillStart, now)
		if err != nil {
			return next, err
		}
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return next, err
		}
		next = TodosState{RoomIdentity: owner, SubscriptionID: hex.EncodeToString(id[:]), Binding: b, Baseline: baseline, Observed: map[string]TodosObservation{}, Inbox: map[string]TodosInboxEntry{}}
	}
	if !cfg.Enabled {
		cfg.PlatformID = next.Binding.PlatformID
	} else {
		next.Binding = b
	}
	if current == nil || !reflect.DeepEqual(cfg.Filter, next.Config.Filter) {
		if len(next.Filters) >= 256 {
			return next, notification.ErrCapacity
		}
		if len(next.Filters) > 0 && now.Before(next.Filters[len(next.Filters)-1].Since) {
			return next, notification.ErrConflict
		}
		since := now
		if current == nil {
			since = next.Baseline
		}
		next.Filters = append(next.Filters, TodosFilterRevision{Since: since, Filter: cfg.Filter})
	}
	if next.SubscriptionVersion == ^uint64(0) {
		return next, notification.ErrCapacity
	}
	next.SubscriptionVersion++
	next.Config = cfg
	next.Scan = nil
	next.RuntimeState.Reset(cfg.Enabled, next.LastFull.IsZero())
	return next, next.Validate(owner)
}
func RetireTodos(s TodosState) (TodosState, error) {
	next := s.Clone()
	if next.SubscriptionVersion == ^uint64(0) {
		return next, notification.ErrCapacity
	}
	next.SubscriptionVersion++
	next.Retired = true
	next.Config.Enabled = false
	next.RuntimeState.Reset(false, false)
	return next, nil
}
