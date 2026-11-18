package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/inputstreamtest/frozen"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestUnifiedSchedulerLocalExclusionAndIndependentSourceReadiness(t *testing.T) {
	state := historyRoom(0, "")
	state.InputLog = &roomLogRoot{Version: 1}
	h := newRoomHarness(t, state, nil)
	id := h.memberID("A", "generation-A")
	p, err := h.store.openPlatformInputStorage(id, storageAdapters())
	if err != nil {
		t.Fatal(err)
	}
	svc := &service{unifiedInputs: true, inputAdapters: storageAdapters(), store: h.store, room: h.addr}
	h.store.notificationAdapters = func(provider string) notification.Capabilities {
		c := notificationCapabilities(provider)
		if provider == "gitlab" {
			c.Admission = &notification.AdmissionCapability{Inspect: func(item notification.Item, _ json.RawMessage) (notification.Admission, error) {
				if item.Key == "excluded" {
					return notification.Admission{Verified: true, Decision: notification.AdmissionExcluded, ReasonCode: "saved_exclusion"}, nil
				}
				return notification.Admission{Verified: true, Decision: notification.AdmissionAllow}, nil
			}}
		}
		return c
	}
	receivePlatform(t, p, storedGitLab(id, "excluded", "events", "7", "42", "excluded"))
	status, err := svc.inputStatus(h.ctx, id)
	if err != nil || status.Ready || status.Pending != 0 {
		t.Fatal(status, err)
	}
	// No model execution or legacy Inbox actor/file is needed to exclude locally.
	if _, exists, err := p.repository.CurrentState(); err != nil || exists {
		t.Fatal(exists, err)
	}
	receivePlatform(t, p, storedLark(id, "unverified", "oc_direct", "待核验私聊", "omt_direct", "blocked"))
	status, err = svc.inputStatus(h.ctx, id)
	if err != nil || status.Ready || status.Pending != 0 {
		t.Fatal(status, err)
	}
	receivePlatform(t, p, storedGitLab(id, "readable", "events", "7", "56", "allowed"))
	status, err = svc.inputStatus(h.ctx, id)
	if err != nil || !status.Ready || status.Pending != 1 {
		t.Fatal(status, err)
	}
	for _, name := range []string{"inbox.json", "consumption.json"} {
		if _, err := os.Stat(filepath.Join(h.store.dir, "inboxes", id.MembershipID, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(name, err)
		}
	}
	// Incoming Room work remains independently runnable when a platform is blocked.
	m := &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Inputs: svc.inputStatusFor(id), inputStatus: &unifiedInputStatus{Pending: 1}, latest: &UnreadChanged{NewestUnreadSeq: 1, UnreadCount: 1}, turnLimit: 10}
	if !m.roomReady() || m.inboxReady() {
		t.Fatal("Room scheduling coupled to blocked platform")
	}
}

func (s *service) inputStatusFor(id MemberIdentity) func(ctx context.Context) (unifiedInputStatus, error) {
	return func(ctx context.Context) (unifiedInputStatus, error) { return s.inputStatus(ctx, id) }
}

func TestUnifiedRecoveryStatusAndContinuationKeepLifecycleGates(t *testing.T) {
	h, s := unifiedReaderFixture(t, 0)
	id := s.storage.id
	receivePlatform(t, s.storage, storedGitLab(id, "pending", "events", "7", "42", "pending"))
	// A fresh execution freezes the saved platform range, then closes without consumption.
	a, _ := h.store.LoadAgent(id)
	a.TurnNumber = 2
	a.Turn = &TurnRecord{Number: 2, Status: "running"}
	if err := h.store.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	s = startPlatformReader(t, h, s.storage, s.service, 2)
	if err := s.storage.repository.Seal(s.execution); err != nil {
		t.Fatal(err)
	}
	a.Turn.Status = "completed"
	if err := h.store.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	status, err := s.service.inputStatus(h.ctx, id)
	if err != nil || status.Recovery == nil || status.Ready {
		t.Fatal(status, err)
	}
	m := &MemberAgent{Options: SessionOptions{MemberIdentity: id}, Store: h.store, inputStatus: &status, state: a, turnLimit: 10, now: time.Now}
	v := m.status()
	if v.InputRecovery == nil || v.InboxRecovery != nil || v.Notice == nil || strings.Contains(v.Notice.Message, "#") {
		t.Fatal(v)
	}
	m.paused = true
	if err := m.continueInput(); !errors.Is(err, ErrBusy) {
		t.Fatal("pause bypass", err)
	}
	m.paused = false
	status.Blocked = "unknown action outcome"
	if err := m.continueInput(); !errors.Is(err, ErrConflict) {
		t.Fatal("unknown bypass", err)
	}
	status.Blocked = ""
	if err := m.continueInput(); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.storage.repository.CurrentState()
	if err != nil || !status.Ready || current.Stall.Attempts != 0 || time.Until(current.Stall.Next) > time.Second {
		t.Fatal(current.Stall, err)
	}
}

func TestUnifiedCorrectiveGuidanceChargesDurableDiscoveryBudget(t *testing.T) {
	h, s := unifiedReaderFixture(t, 1)
	_, err := s.legacyDiscover(h.ctx, "inbox_list", []byte(`{"turn_number":1,"filter":{"event_type":"selection"}}`))
	if !errors.Is(err, frozen.ErrScopeRequired) {
		t.Fatal(err)
	}
	state, err := s.storage.repository.State(s.execution)
	if err != nil || state.DiscoveryBytes == 0 || state.Used != 0 {
		t.Fatal(state, err)
	}
	if err := s.mutate(func(r *inputstream.Repository) error {
		return r.ChargeDiscovery(s.execution, (128<<10)-state.DiscoveryBytes)
	}); err != nil {
		t.Fatal(err)
	}
	v, err := s.legacyDiscover(h.ctx, "inbox_list", []byte(`{"turn_number":1,"filter":{"event_type":"selection"}}`))
	if err != nil || v.(map[string]string)["stop_reason"] != "discovery_budget_exhausted" {
		t.Fatal(v, err)
	}
}
