//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func writeLegacyGitLabSettings(t *testing.T, m *roomManager, owner RoomIdentity, cfg gitlab.NotificationConfig) []byte {
	t.Helper()
	cfg, err := gitlab.NormalizeNotificationConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	record := gitlabNotificationRecord{RoomIdentity: owner, Schema: 2, Version: 7, Config: cfg, BackfillLocked: true, Errors: map[string]string{"events": "apply_failed"}}
	raw, _ := json.Marshal(record)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	var config map[string]json.RawMessage
	if err := json.Unmarshal(fields["config"], &config); err != nil {
		t.Fatal(err)
	}
	delete(config, "enabled")
	fields["config"], _ = json.Marshal(config)
	raw, _ = json.Marshal(fields)
	path := m.gitlabNotificationPath(owner)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return raw
}

func notificationSourceBytes(t *testing.T, m *roomManager, owner RoomIdentity) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, s := range m.notifications.StatesLocked(owner) {
		path, err := m.notifications.StatePathLocked(s.Metadata().Key)
		if err != nil {
			t.Fatal(err)
		}
		out[path], err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}
func assertNotificationSourceBytes(t *testing.T, want map[string][]byte) {
	t.Helper()
	for path, before := range want {
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("source snapshot rewritten", path, err)
		}
	}
}

func TestNotificationsUpgradePreservesLegacySettingsAndProgress(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	seedEventInbox(t, h, 1)
	cfg := unifiedGitLabConfig(t, h)
	before := writeLegacyGitLabSettings(t, h.m, h.room.RoomIdentity, cfg)
	sourceBytes := notificationSourceBytes(t, h.m, h.room.RoomIdentity)
	path := h.m.gitlabNotificationPath(h.room.RoomIdentity)
	root := h.m.store.dir
	args := []string{"upgrade", "--rooms-dir", root}
	var out, diagnostic bytes.Buffer
	if err := runNotificationsUpgrade(args, &out, &diagnostic); err == nil {
		t.Fatal("upgraded running service")
	}
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	// Runtime readers refuse the old settings instead of silently reinterpreting them.
	if m, err := newRoomManager(root, 7); err == nil {
		m.close()
		t.Fatal("legacy settings opened without upgrade")
	}
	if err := runNotificationsUpgrade(args, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".v2.bak")
	if err != nil || !bytes.Equal(bytes.TrimSpace(backup), bytes.TrimSpace(before)) {
		t.Fatal("backup changed preimage", err)
	}
	info, err := os.Stat(path + ".v2.bak")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup permissions", err)
	}
	assertNotificationSourceBytes(t, sourceBytes)
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runNotificationsUpgrade(args, &out, &diagnostic); err != nil {
		t.Fatal("repeat upgrade", err)
	}
	repeated, _ := os.ReadFile(path)
	if !bytes.Equal(current, repeated) {
		t.Fatal("repeat upgraded current file")
	}
	restored := testManager(t, root, managerTestProfiles(t))
	v := platformGitLabView(t, restored, h.room.RoomIdentity, c.ID)
	if v.Version != 7 || !v.Config.Enabled || !reflect.DeepEqual(v.Config, cfg) || v.Todos.Config.(gitlab.TodosConfig).Enabled || v.Todos.SubscriptionID != "" || v.Events.Pending != 1 || v.Errors["events"] != "apply_failed" {
		t.Fatal("upgrade changed legacy preferences/progress", v)
	}
	if err := restored.close(); err != nil {
		t.Fatal(err)
	}
	// Restore the exact preimage, then re-upgrade against the existing backup.
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runNotificationsUpgrade(args, &out, &diagnostic); err != nil {
		t.Fatal("rollback/re-upgrade", err)
	}
	assertNotificationSourceBytes(t, sourceBytes)
}

func TestNotificationsUpgradeSplitsDifferentLegacyChannelsByPlatform(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "split-legacy")
	a, b := sharedGitLabPlatforms(t, m)
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
	events, err := m.configureEventsSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.EventsSourceID}, 0, gitlab.EventsConfig{Enabled: true, PlatformID: a, Project: "group/project", IntervalSeconds: 15, BackfillSeconds: 3600})
	if err != nil {
		t.Fatal(err)
	}
	todos, err := m.configureTodosSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.TodosSourceID}, 0, gitlab.TodosConfig{Enabled: true, PlatformID: b, IntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	before := notificationSourceBytes(t, m, room.RoomIdentity)
	path := m.gitlabNotificationPath(room.RoomIdentity)
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	var out, diagnostic bytes.Buffer
	if err := runNotificationsUpgrade([]string{"upgrade", "--rooms-dir", root}, &out, &diagnostic); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	va, vb := platformGitLabView(t, restored, room.RoomIdentity, a), platformGitLabView(t, restored, room.RoomIdentity, b)
	if va.Config.Todos.Enabled || !va.Config.Events.Enabled || vb.Config.Events.Enabled || !vb.Config.Todos.Enabled || va.Config.IntervalSeconds != 15 || vb.Config.IntervalSeconds != 60 || va.Events.SubscriptionID != events.SubscriptionID || vb.Todos.SubscriptionID != todos.SubscriptionID || !va.Events.Baseline.Equal(events.Baseline) || !vb.Todos.Baseline.Equal(todos.Baseline) {
		t.Fatal("legacy channels were merged", va, vb)
	}
	marker, err := os.ReadFile(path + ".v0.bak")
	if err != nil || string(bytes.TrimSpace(marker)) != "null" {
		t.Fatal("missing absent-settings rollback marker", err)
	}
	assertNotificationSourceBytes(t, before)
}

func TestNotificationsUpgradeFailureBoundaries(t *testing.T) {
	for _, stage := range []string{"backup-conflict", "backup-symlink", "backup-write", "commit-write", "commit-unknown"} {
		t.Run(stage, func(t *testing.T) {
			h, _ := newIndependentEventHarness(t)
			original := writeLegacyGitLabSettings(t, h.m, h.room.RoomIdentity, unifiedGitLabConfig(t, h))
			path := h.m.gitlabNotificationPath(h.room.RoomIdentity)
			backup := path + ".v2.bak"
			sources := notificationSourceBytes(t, h.m, h.room.RoomIdentity)
			if stage == "backup-conflict" {
				if err := os.WriteFile(backup, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if stage == "backup-symlink" {
				if err := os.Symlink(path, backup); err != nil {
					t.Fatal(err)
				}
			}
			// Use the production Store and writer fault hooks with a fresh offline
			// loader; the outer service lock is already held by the test fixture.
			offline := &roomManager{store: h.m.store, index: h.m.index}
			offline.initPlatform()
			ops := offline.store.ops
			injected := errors.New("notification upgrade write failure")
			// Track durable files so injected failures follow named boundaries.
			journalWritten, backupWritten := false, false
			offline.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
				if stage == "backup-write" && journalWritten || stage == "commit-write" && backupWritten {
					return nil, injected
				}
				return ops.createTemp(dir, pattern)
			}
			offline.store.ops.rename = func(from, to string) error {
				if err := ops.rename(from, to); err != nil {
					return err
				}
				if filepath.Base(to) == offline.notificationUpgradeJournal() {
					journalWritten = true
				}
				if to == backup {
					backupWritten = true
				}
				if stage == "commit-unknown" && to == path {
					return injected
				}
				return nil
			}
			_, err := offline.upgradeMemberNotificationsLocked()
			if err == nil {
				t.Fatal("fault not observed")
			}
			if stage == "commit-unknown" && !errors.Is(err, ErrCommitUnknown) {
				t.Fatal("unknown commit not latched", err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if stage != "commit-unknown" && !bytes.Equal(original, after) {
				t.Fatal("failed upgrade replaced source settings")
			}
			assertNotificationSourceBytes(t, sources)
			if stage == "commit-write" || stage == "commit-unknown" {
				data, err := os.ReadFile(backup)
				if err != nil || !bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(original)) {
					t.Fatal("commit failure lost backup", err)
				}
			}
			offline.store.ops = ops
		})
	}
}

func TestNotificationsUpgradeRefusesIncompatibleLegacyScope(t *testing.T) {
	parallelFixtureTest(t)
	h, c := newIndependentEventHarness(t)
	_, err := h.m.configureTodosSubscription(context.Background(), notification.Key{Owner: h.room.RoomIdentity, Source: gitlab.TodosSourceID}, 0, gitlab.TodosConfig{Enabled: true, PlatformID: c.ID, IntervalSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	before := notificationSourceBytes(t, h.m, h.room.RoomIdentity)
	offline := &roomManager{store: h.m.store, index: h.m.index}
	offline.initPlatform()
	if _, err := offline.upgradeMemberNotificationsLocked(); !errors.Is(err, ErrConflict) {
		t.Fatal("incompatible settings silently changed", err)
	}
	path := h.m.gitlabNotificationPath(h.room.RoomIdentity)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid conversion saved settings", err)
	}
	matches, _ := filepath.Glob(path + ".v*.bak")
	if len(matches) != 0 {
		t.Fatal("invalid conversion wrote backup")
	}
	assertNotificationSourceBytes(t, before)
}
