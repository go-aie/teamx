//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func oldLarkSnapshot(t *testing.T, m *roomManager, s lark.State, bots bool) (string, []byte) {
	t.Helper()
	raw, _ := json.Marshal(s)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	cfg := map[string]any{"enabled": s.Config.Enabled, "platform_id": s.Binding.PlatformID, "chats": s.Config.Private.ChatIDs, "target": s.Config.Target, "private": true, "group_mentions": true, "at_all": false, "bots": bots, "interval_seconds": s.Config.IntervalSeconds, "backfill_seconds": s.Config.BackfillSeconds}
	fields["config"], _ = json.Marshal(cfg)
	raw, _ = json.Marshal(fields)
	env := struct {
		notification.SourceID
		Schema int             `json:"schema"`
		State  json.RawMessage `json:"state"`
	}{lark.MessagesSourceID, 1, raw}
	raw, _ = json.Marshal(env)
	path, err := m.notifications.StatePathLocked(s.Metadata().Key)
	if err != nil {
		t.Fatal(err)
	}
	return path, raw
}
func TestLarkNotificationsOfflineUpgradePreservesEvidenceAndIsRepeatable(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	want := h.state()
	path, before := oldLarkSnapshot(t, h.m, want, false)
	root := h.m.store.dir
	args := []string{"upgrade-lark", "--rooms-dir", root}
	var out bytes.Buffer
	if err := runNotificationsUpgrade(args, &out, &out); err == nil {
		t.Fatal("upgraded running service")
	}
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if m, err := newRoomManager(root, 7); err == nil {
		m.close()
		t.Fatal("legacy source loaded without explicit conversion")
	}
	if err := runNotificationsUpgrade(args, &out, &out); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil || !equalPlatformJSON(backup, before) {
		t.Fatal("original backup changed", err)
	}
	info, err := os.Stat(path + ".v1.bak")
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("backup mode", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := runNotificationsUpgrade(args, &out, &out); err != nil {
		t.Fatal("repeat", err)
	}
	repeated, _ := os.ReadFile(path)
	if !bytes.Equal(after, repeated) {
		t.Fatal("repeat changed snapshot")
	}
	restored := testManager(t, root, managerTestProfiles(t))
	got := larkStateFor(t, restored, h.room.RoomIdentity, want.Binding.PlatformID)
	// NormalizeConfig adds the explicit category representation only. Runtime
	// identity, progress, pending items and frozen destinations must match exactly.
	want.Config = got.Config
	if !reflect.DeepEqual(got, want) {
		t.Fatal("upgrade changed identity/progress/backlog")
	}
	if got.Config.Groups.Enabled || got.Config.Private.Scope != "specified" {
		t.Fatal("upgrade applied first-run defaults")
	}
}
func TestLarkNotificationsUpgradeRejectsWholeSetBeforeWriting(t *testing.T) {
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
	p1, v1 := oldLarkSnapshot(t, h.m, a, false)
	// The second snapshot lacks proven conversation metadata and enables bots.
	p2, v2 := oldLarkSnapshot(t, h.m, b, true)
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	for path, raw := range map[string][]byte{p1: v1, p2: v2} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := runNotificationsUpgrade([]string{"upgrade-lark", "--rooms-dir", root}, &out, &out); err == nil {
		t.Fatal("lossy conversion accepted")
	}
	for path, want := range map[string][]byte{p1: v1, p2: v2} {
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, want) {
			t.Fatal("failed conversion mutated original")
		}
		if _, err := os.Lstat(path + ".v1.bak"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("backup written before all candidates validated", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, larkUpgradeJournal)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected candidate journaled", err)
	}
}
func TestLarkNotificationsUpgradeInterruptedCommitRecoveryAndConflict(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	state := h.state()
	path, before := oldLarkSnapshot(t, h.m, state, false)
	root, index := h.m.store.dir, h.m.index
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{dir: root, lock: lock, ops: defaultSnapshotOps()}
	defer store.Close()
	m := &roomManager{store: store, index: index}
	rename := store.ops.rename
	store.ops.rename = func(from, to string) error {
		if to == path {
			return errors.New("injected write failure")
		}
		return rename(from, to)
	}
	if _, err := m.upgradeLarkNotificationsLocked(); err == nil {
		t.Fatal("failure hidden")
	}
	if err := checkLarkNotificationUpgrade(root); !errors.Is(err, ErrVersion) {
		t.Fatal("mixed startup allowed", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, before) {
		t.Fatal("uncommitted source changed")
	}
	store.ops.rename = rename
	// An external third value cannot be overwritten by a recovery retry.
	if err := os.WriteFile(path, []byte(`{"external":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.upgradeLarkNotificationsLocked(); !errors.Is(err, ErrConflict) {
		t.Fatal("external conflict overwritten", err)
	}
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.upgradeLarkNotificationsLocked(); err != nil {
		t.Fatal("recovery", err)
	}
	if err := checkLarkNotificationUpgrade(root); err != nil {
		t.Fatal("journal incomplete", err)
	}
	backup, _ := os.ReadFile(path + ".v1.bak")
	if !equalPlatformJSON(backup, before) {
		t.Fatal("recovery replaced preimage")
	}
}

func TestLarkNotificationsUpgradeRejectsBackupConflictsAndSymlinks(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	state := h.state()
	path, before := oldLarkSnapshot(t, h.m, state, false)
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"upgrade-lark", "--rooms-dir", root}
	var out bytes.Buffer
	backup := path + ".v1.bak"
	if err := os.WriteFile(backup, []byte(`{"foreign":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runNotificationsUpgrade(args, &out, &out); !errors.Is(err, ErrConflict) {
		t.Fatal("backup conflict", err)
	}
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, before, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, backup); err != nil {
		t.Fatal(err)
	}
	if err := runNotificationsUpgrade(args, &out, &out); !errors.Is(err, ErrInvalid) {
		t.Fatal("backup symlink", err)
	}
	if err := os.Remove(backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := runNotificationsUpgrade(args, &out, &out); !errors.Is(err, ErrInvalid) {
		t.Fatal("source symlink", err)
	}
	after, err := os.ReadFile(outside)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatal("symlink target modified", err)
	}
}

func TestLarkNotificationsUpgradeCommitUnknownRequiresReload(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	state := h.state()
	path, before := oldLarkSnapshot(t, h.m, state, false)
	root, index := h.m.store.dir, h.m.index
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := lockDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{dir: root, lock: lock, ops: defaultSnapshotOps()}
	defer store.Close()
	rename, syncDir := store.ops.rename, store.ops.syncDir
	sourceReplaced := false
	store.ops.rename = func(from, to string) error {
		err := rename(from, to)
		if err == nil && to == path {
			sourceReplaced = true
		}
		return err
	}
	store.ops.syncDir = func(dir string) error {
		if sourceReplaced {
			return errors.New("injected directory sync failure")
		}
		return syncDir(dir)
	}
	m := &roomManager{store: store, index: index}
	if _, err := m.upgradeLarkNotificationsLocked(); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("unknown commit hidden", err)
	}
	if err := store.check(true); !errors.Is(err, ErrCommitUnknown) {
		t.Fatal("writes remained open", err)
	}
	if err := checkLarkNotificationUpgrade(root); !errors.Is(err, ErrVersion) {
		t.Fatal("incomplete startup admitted", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runNotificationsUpgrade([]string{"upgrade-lark", "--rooms-dir", root}, &out, &out); err != nil {
		t.Fatal("reload recovery", err)
	}
	if err := checkLarkNotificationUpgrade(root); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".v1.bak")
	if err != nil || !equalPlatformJSON(backup, before) {
		t.Fatal("unknown commit lost original", err)
	}
}
