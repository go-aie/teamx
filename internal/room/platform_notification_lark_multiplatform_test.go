//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func larkStateFor(t *testing.T, m *roomManager, owner RoomIdentity, id string) lark.State {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.currentLarkLocked(owner, id)
	if err != nil || s == nil {
		t.Fatal("platform state", id, err)
	}
	return *s
}
func secondLarkPlatform(t *testing.T, h *larkHarness) managedPlatform {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	fixture := strings.NewReplacer("cli_fixture", "cli_second", "ou_reader", "ou_second", "'fixture'", "'second'").Replace(l2Fixture)
	if err := os.WriteFile(bin, []byte(fixture), 0700); err != nil {
		t.Fatal(err)
	}
	messages, err := os.ReadFile(filepath.Join(h.dir, "messages.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "messages.json"), messages, 0600); err != nil {
		t.Fatal(err)
	}
	c := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "第二飞书", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &platform.CLIProfile{Version: 1, Executable: bin, Profile: "second", Brand: "feishu", CLIVersion: lark.CLIVersion}})
	c, err = h.m.verifyPlatform(t.Context(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal("second profile verification", err)
	}
	return c
}
func TestLarkMultiplePlatformsCollectPauseAndRecoverIndependently(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	a := h.state()
	c := secondLarkPlatform(t, h)
	cfg := a.Config
	cfg.PlatformID = c.ID
	cfg.BackfillStart = a.Baseline
	key := notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}
	b, err := h.m.configureLarkSubscription(t.Context(), key, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if b.SubscriptionID == a.SubscriptionID || b.Binding.Account == a.Binding.Account {
		t.Fatal("merged profile/account identity")
	}
	h.clock.add(3 * time.Second)
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(b.Metadata().Key)
	h.m.mu.Unlock()
	runner.Step(t.Context())
	runner.Step(t.Context())
	b = larkStateFor(t, h.m, h.room.RoomIdentity, c.ID)
	if len(a.Inbox) != 1 || len(b.Inbox) != 1 {
		t.Fatal("independent reception", len(a.Inbox), len(b.Inbox))
	}
	for _, item := range b.Inbox {
		if item.Item.Source.Reference.RecipientID != b.Binding.Account.ID {
			t.Fatal("wrong native recipient")
		}
	}
	cfg = a.Config
	cfg.Enabled = false
	paused, err := h.m.configureLarkSubscription(t.Context(), a.Metadata().Key, a.SubscriptionVersion, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if paused.Config.Enabled || !reflect.DeepEqual(larkStateFor(t, h.m, h.room.RoomIdentity, c.ID), b) {
		t.Fatal("pause crossed platform")
	}
	cfg = b.Config
	cfg.PlatformID = a.Binding.PlatformID
	if _, err := h.m.configureLarkSubscription(t.Context(), b.Metadata().Key, b.SubscriptionVersion, cfg); !errors.Is(err, ErrConflict) {
		t.Fatal("subscription moved between platforms", err)
	}
	if _, err := h.m.configureLarkSubscription(t.Context(), b.Metadata().Key, b.SubscriptionVersion-1, b.Config); !errors.Is(err, ErrConflict) {
		t.Fatal("stale version accepted", err)
	}
	h.m.mu.Lock()
	_, err = h.m.currentLarkLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if !errors.Is(err, ErrConflict) {
		t.Fatal("ambiguous platform silently chosen", err)
	}
	views, err := h.m.notificationViewsLocked(h.room.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, v := range views {
		if v.Provider == "lark" {
			found[v.PlatformID] = true
		}
	}
	if !found[a.Binding.PlatformID] || !found[b.Binding.PlatformID] {
		t.Fatal("missing public platform context")
	}
	// Source snapshots and profile bindings must survive a service restart intact.
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	for _, want := range []lark.State{paused, b} {
		got := larkStateFor(t, restored, h.room.RoomIdentity, want.Binding.PlatformID)
		if !reflect.DeepEqual(got, want) {
			t.Fatal("restart changed isolated state")
		}
	}
}

func TestLarkPlatformBRemainsConfigurableWhileAQuerySettles(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	a := h.state()
	c := secondLarkPlatform(t, h)
	cfg := a.Config
	cfg.PlatformID = c.ID
	b, err := h.m.configureLarkSubscription(t.Context(), notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// A delayed worker query must settle outside the shared Room manager gate.
	started := make(chan struct{})
	release := make(chan struct{})
	h.m.notifications.Source(lark.MessagesSourceID).(*lark.Source).Reader = func(q lark.QueryScope) (lark.MessageReader, error) {
		client, err := lark.New(q.Profile)
		if q.Binding.PlatformID != a.Binding.PlatformID {
			return client, err
		}
		return &blockingLarkReader{MessageReader: client, started: started, release: release}, err
	}
	h.clock.add(time.Minute)
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("query not started")
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	pauseDone := make(chan error, 1)
	cfg = a.Config
	cfg.Enabled = false
	go func() {
		_, err := h.m.configureLarkSubscription(context.Background(), a.Metadata().Key, a.SubscriptionVersion, cfg)
		pauseDone <- err
	}()
	// Await the durable version fence before checking the settlement wait.
	deadline := time.Now().Add(5 * time.Second)
	for larkStateFor(t, h.m, h.room.RoomIdentity, a.Binding.PlatformID).Config.Enabled && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-pauseDone:
		t.Fatal("A returned before worker settlement", err)
	default:
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	bcfg := b.Config
	bcfg.IntervalSeconds = 90
	if _, err := h.m.configureLarkSubscription(ctx, b.Metadata().Key, b.SubscriptionVersion, bcfg); err != nil {
		t.Fatal("A settlement blocked B", err)
	}
	close(release)
	select {
	case err := <-pauseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("query did not settle")
	}

	if got := larkStateFor(t, h.m, h.room.RoomIdentity, a.Binding.PlatformID); got.Config.Enabled || got.SubscriptionVersion != a.SubscriptionVersion+1 {
		t.Fatal("late A query crossed version fence")
	}
}

type blockingLarkReader struct {
	lark.MessageReader
	started chan struct{}
	release chan struct{}
}

func (r *blockingLarkReader) Messages(ctx context.Context, a platform.AccountIdentity, q lark.MessagesRequest) (lark.MessagePage, error) {
	close(r.started)
	<-r.release
	return r.MessageReader.Messages(ctx, a, q)
}

// Ensure platform IDs also accompany the public notification JSON, without
// exposing credential/profile selection or private message content.
func TestLarkPublicViewsContainAccuratePlatformContext(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	s := h.state()
	v, err := h.m.larkNotificationViewLocked(h.room.RoomIdentity, &s)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if v.PlatformID != h.c.ID || v.Account != s.Binding.Account || strings.Contains(string(raw), "fingerprint") || strings.Contains(string(raw), `"profile"`) {
		t.Fatal("public platform context", string(raw))
	}
}
