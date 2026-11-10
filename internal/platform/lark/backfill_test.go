package lark

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestCustomBackfillFreezesAbsoluteStartAcrossSaveAndReload(t *testing.T) {
	s := sampleState(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	start := now.Add(-7 * 24 * time.Hour).In(time.FixedZone("local", 8*3600))
	cfg := s.Config
	cfg.BackfillStart = start
	first, err := Configure(s.RoomIdentity, nil, s.Binding, cfg, now)
	if err != nil || !first.Baseline.Equal(start) || first.Config.BackfillStart.Location() != time.UTC {
		t.Fatal("absolute start was not normalized and frozen", err)
	}
	raw, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := (&Source{}).Decode(raw, s.RoomIdentity)
	if err != nil {
		t.Fatal("custom start did not survive reload", err)
	}
	first = decoded.(State)
	cfg = first.Config
	cfg.Enabled = false
	paused, err := Configure(s.RoomIdentity, &first, s.Binding, cfg, now.Add(time.Hour))
	if err != nil || paused.Baseline != first.Baseline {
		t.Fatal("pause changed baseline", err)
	}
	cfg.Enabled = true
	cfg.IntervalSeconds = 120
	cfg.Private.ChatIDs = []string{"oc_new"}
	resumed, err := Configure(s.RoomIdentity, &paused, s.Binding, cfg, now.Add(2*time.Hour))
	if err != nil || resumed.Baseline != first.Baseline {
		t.Fatal("resume or new chat changed baseline", err)
	}
	cfg.BackfillStart = start.Add(-time.Hour)
	if _, err := Configure(s.RoomIdentity, &resumed, s.Binding, cfg, now); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("existing custom start could be moved earlier", err)
	}
	cfg.BackfillStart = start.Add(time.Hour)
	if _, err := Configure(s.RoomIdentity, &resumed, s.Binding, cfg, now); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("existing custom start could be moved later", err)
	}
	cfg.BackfillStart = time.Time{}
	if _, err := Configure(s.RoomIdentity, &resumed, s.Binding, cfg, now); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("custom start could be removed", err)
	}
	corrupt := first.Clone()
	corrupt.Baseline = corrupt.Baseline.Add(-time.Hour)
	if corrupt.Validate(s.RoomIdentity) == nil {
		t.Fatal("snapshot accepted mismatched custom start and baseline")
	}
}

func TestBackfillLockedPerSubscriptionAndIndependentInNewRoom(t *testing.T) {
	original := sampleState(t)
	now := time.Unix(2010, 0).UTC()
	for _, seconds := range []int{3600, 86400} {
		cfg := original.Config
		cfg.BackfillSeconds = seconds
		if _, err := Configure(original.RoomIdentity, &original, original.Binding, cfg, now); !errors.Is(err, notification.ErrConflict) {
			t.Fatal("existing preset could be changed", err)
		}
	}
	owner := original.RoomIdentity
	owner.RoomID, owner.RoomInstance = "history-room", "history-instance"
	cfg := original.Config
	cfg.BackfillStart = time.Unix(500, 0).UTC()
	history, err := Configure(owner, nil, original.Binding, cfg, now)
	if err != nil || !history.Baseline.Equal(cfg.BackfillStart) || history.SubscriptionID == original.SubscriptionID || history.RoomIdentity == original.RoomIdentity {
		t.Fatal("new room could not choose an independent historical start", err)
	}
	if !original.Baseline.Equal(time.Unix(1000, 0)) || !original.Config.BackfillStart.IsZero() {
		t.Fatal("new room changed original subscription")
	}
}

func TestBackfillDefaultsPresetsAndInvalidTimes(t *testing.T) {
	s := sampleState(t)
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	for _, seconds := range []int{0, 3600, 86400} {
		cfg := s.Config
		cfg.BackfillSeconds = seconds
		got, err := Configure(s.RoomIdentity, nil, s.Binding, cfg, now)
		if err != nil || !got.Baseline.Equal(now.Add(-time.Duration(seconds)*time.Second)) || !got.Config.BackfillStart.IsZero() {
			t.Fatal("default or preset changed", seconds, err)
		}
		// Old snapshots omit the optional absolute timestamp.
		raw, _ := json.Marshal(got)
		if _, err := (&Source{}).Decode(raw, s.RoomIdentity); err != nil {
			t.Fatal("legacy preset snapshot rejected", err)
		}
	}
	for _, tt := range []struct {
		name    string
		start   time.Time
		seconds int
	}{
		{"future", now.Add(time.Hour), 0},
		{"unix epoch", time.Unix(0, 0).UTC(), 0},
		{"before unix epoch", time.Unix(-1, 0).UTC(), 0},
		{"conflicting modes", now.Add(-time.Hour), 3600},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := s.Config
			cfg.BackfillStart, cfg.BackfillSeconds = tt.start, tt.seconds
			if _, err := Configure(s.RoomIdentity, nil, s.Binding, cfg, now); !errors.Is(err, notification.ErrRequest) {
				t.Fatal("invalid backfill accepted", err)
			}
		})
	}
}

func TestCustomBackfillReadsOlderMessagesWithoutWaitingForNewOnes(t *testing.T) {
	h, f, r := newStepHarness(t)
	h.now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	start := h.now.Add(-7 * 24 * time.Hour)
	cfg := h.s.Config
	cfg.BackfillStart = start
	var err error
	h.s, err = Configure(h.s.RoomIdentity, nil, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	f.pages = func(q MessagesRequest) (MessagePage, error) {
		if q.Start != start.Unix() || q.End != h.now.Unix()-1 {
			t.Fatalf("unexpected historical window: %+v", q)
		}
		m := sampleMessage("om_history", "oc_chat", "historical message")
		m.Created = strconv.FormatInt(start.Add(time.Hour).UnixMilli(), 10)
		m.Updated = m.Created
		return MessagePage{Items: []Message{m}}, nil
	}
	r.Step(context.Background())
	r.Step(context.Background())
	if len(h.s.Inbox) != 1 || h.s.Chats["oc_chat"].Completed != h.now.Unix()-1 {
		t.Fatal("historical message was not buffered")
	}
}
