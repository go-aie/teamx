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
	"strings"
	"sync"
	"testing"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestMemberNotificationGitLabReceiverStorageVersionsAndApplyLocks(t *testing.T) {
	parallelFixtureTest(t)
	m := testManager(t, t.TempDir(), managerTestProfiles(t))
	room := managerCreate(t, m, "receiver-storage")
	a, _ := sharedGitLabPlatforms(t, m)
	_, snapshot := readManagedRoom(t, m, room)
	targets := []notification.Target{{}, {Mode: "inbox", AgentID: snapshot.Members[0].AgentID, MembershipID: snapshot.Members[0].MembershipID}, {Mode: "inbox", AgentID: snapshot.Members[1].AgentID, MembershipID: snapshot.Members[1].MembershipID}}
	configs := make([]gitlab.NotificationConfig, len(targets))
	for i, target := range targets {
		cfg := gitlab.DefaultNotificationConfig(a)
		cfg.Target = target
		cfg.Enabled = false
		v, err := m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 0, cfg)
		if err != nil || v.Version != 1 || v.Config.Target != target {
			t.Fatal(v, err)
		}
		configs[i] = v.Config
	}
	m.mu.Lock()
	if _, err := m.gitlabNotificationViewLocked(room.RoomIdentity, a); !errors.Is(err, ErrConflict) {
		t.Fatal("ambiguous legacy read selected a receiver", err)
	}
	m.gitlabConfiguring[notificationApplyKey(room.RoomIdentity, a, targets[1])] = true
	m.mu.Unlock()
	cfg := configs[2]
	cfg.IntervalSeconds = 60
	vb, err := m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 1, cfg)
	if err != nil || vb.Version != 2 {
		t.Fatal("A's apply lock blocked B", vb, err)
	}
	if _, err := m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 1, configs[1]); !errors.Is(err, ErrBusy) {
		t.Fatal("A did not hold its apply lock", err)
	}
	m.mu.Lock()
	delete(m.gitlabConfiguring, notificationApplyKey(room.RoomIdentity, a, targets[1]))
	m.mu.Unlock()
	// Two concurrent writes with the same receiver version admit exactly one.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, interval := range []int{60, 120} {
		wg.Add(1)
		go func(interval int) {
			defer wg.Done()
			cfg := configs[1]
			cfg.IntervalSeconds = interval
			_, err := m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 1, cfg)
			results <- err
		}(interval)
	}
	wg.Wait()
	close(results)
	success, conflicts := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) || errors.Is(err, ErrBusy) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflicts != 1 {
		t.Fatal(success, conflicts)
	}
	bad := configs[1]
	bad.Target.AgentID = "impostor"
	if _, err := m.configureGitLabNotifications(context.Background(), room.RoomIdentity, 0, bad); !errors.Is(err, ErrIdentity) {
		t.Fatal("same membership acquired another agent", err)
	}
	m.mu.Lock()
	before := map[notificationScope]gitlabNotificationRecord{}
	for key, record := range m.gitlabNotifications[room.RoomIdentity] {
		before[key] = record
	}
	m.mu.Unlock()
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	beforeJSON, _ := json.Marshal(gitlabNotificationFileFor(room.RoomIdentity, before))
	afterJSON, _ := json.Marshal(gitlabNotificationFileFor(room.RoomIdentity, restored.gitlabNotifications[room.RoomIdentity]))
	if !equalPlatformJSON(beforeJSON, afterJSON) {
		t.Fatal("reopen overwrote a receiver")
	}
	var saved gitlabNotificationFile
	if err := readSnapshot(restored.gitlabNotificationPath(room.RoomIdentity), &saved, maxGitLabConfigBytes); err != nil {
		t.Fatal(err)
	}
	if len(saved.Platforms[a]) != 3 {
		t.Fatal("persisted target collection", saved)
	}
	saved.Platforms[a] = append(saved.Platforms[a], saved.Platforms[a][0])
	if err := validateGitLabNotificationFile(room.RoomIdentity, saved); !errors.Is(err, ErrConflict) {
		t.Fatal("duplicate silently overwritten", err)
	}
	saved.Platforms[a][3].Config.Target.AgentID = "impostor"
	if err := validateGitLabNotificationFile(room.RoomIdentity, saved); !errors.Is(err, ErrIdentity) {
		t.Fatal("membership impostor silently loaded", err)
	}
}

// Build an actual old envelope and schema-3 settings, including the historical
// boundary where pending Route differs from the current Config.Target.
func seedLegacyGitLabNotificationFiles(t *testing.T) (*eventHarness, gitlab.EventsState, string, string) {
	t.Helper()
	h, _ := newIndependentEventHarness(t)
	seedEventInbox(t, h, 1)
	state := h.state()
	state.Config.Target = notification.Target{Mode: "inbox", AgentID: "removed-agent", MembershipID: "removed-membership"}
	state.SubscriptionVersion++
	cfg := gitlab.NotificationConfig{Enabled: true, PlatformID: state.Config.PlatformID, Target: state.Config.Target, IntervalSeconds: state.Config.IntervalSeconds, BackfillSeconds: state.Config.BackfillSeconds, BackfillStart: state.Config.BackfillStart, Events: gitlab.EventSettingsFrom(state.Config)}
	cfg, err := gitlab.NormalizeNotificationConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	record := gitlabNotificationRecord{RoomIdentity: state.RoomIdentity, Schema: 3, Version: 9, Config: cfg, BackfillLocked: true, Errors: map[string]string{"events": "apply_failed"}}
	settings := struct {
		RoomIdentity
		Schema    int                                 `json:"schema"`
		Platforms map[string]gitlabNotificationRecord `json:"platforms"`
	}{state.RoomIdentity, 3, map[string]gitlabNotificationRecord{cfg.PlatformID: record}}
	path, err := h.m.notifications.StatePathLocked(state.Metadata().Key)
	if err != nil {
		t.Fatal(err)
	}
	settingsPath := h.m.gitlabNotificationPath(state.RoomIdentity)
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(state)
	env := notificationEnvelope{SourceID: gitlab.EventsSourceID, Schema: 2, State: raw}
	for path, value := range map[string]any{path: env, settingsPath: settings} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return h, state, path, settingsPath
}

func TestMemberNotificationUpgradePreservesFrozenRoutesAndWholeState(t *testing.T) {
	parallelFixtureTest(t)
	h, want, path, settingsPath := seedLegacyGitLabNotificationFiles(t)
	before, _ := os.ReadFile(path)
	root := h.m.store.dir
	if m, err := newRoomManager(root, 7); err == nil {
		m.close()
		t.Fatal("old formats opened without upgrade")
	}
	args := []string{"upgrade", "--rooms-dir", root}
	var out bytes.Buffer
	if err := runNotificationsUpgrade(args, &out, &out); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(path + ".v2.bak")
	if err != nil || !equalPlatformJSON(before, backup) {
		t.Fatal("source backup lost", err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	got, ok := restored.notifications.StateLocked(want.Metadata().Key)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatal("conversion changed source identity, progress or frozen routes", got)
	}
	records := restored.gitlabNotifications[want.RoomIdentity]
	saved := records[notificationScopeFor(want.Config.PlatformID, want.Config.Target)]
	if saved.Version != 9 || saved.Config.Target != want.Config.Target || saved.Errors["events"] != "apply_failed" {
		t.Fatal("desired intent lost", saved)
	}
	// Simulate an old binary: every source rejects the new envelope version.
	var env notificationEnvelope
	raw, _ := os.ReadFile(path)
	_ = json.Unmarshal(raw, &env)
	if env.Schema != 3 {
		t.Fatal("old source codec still accepts envelope", env.Schema)
	}
	if restored.notifications.Source(gitlab.TodosSourceID).Schema() != 3 || restored.notifications.Source(lark.MessagesSourceID).Schema() != 3 {
		t.Fatal("incomplete format fence")
	}
	if err := restored.close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if err := runNotificationsUpgrade(args, &out, &out); err != nil {
		t.Fatal("repeat upgrade", err)
	}
	repeated, _ := os.ReadFile(path)
	if !bytes.Equal(after, repeated) {
		t.Fatal("repeat upgrade rewrote progress")
	}
	// A rollback restores the complete group; the exact old group can be safely
	// upgraded again against its immutable journal and backups.
	for _, p := range []string{path, settingsPath} {
		backup := notificationBackupPath(p, json.RawMessage(`{"schema":2}`))
		if p == settingsPath {
			backup = p + ".v3.bak"
		}
		raw, err := os.ReadFile(backup)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runNotificationsUpgrade(args, &out, &out); err != nil {
		t.Fatal("whole group rollback/re-upgrade", err)
	}
}

func TestMemberNotificationUpgradeInterruptedGroupBlocksStartupAndResumes(t *testing.T) {
	for _, point := range []string{"source", "settings", "complete"} {
		t.Run(point, func(t *testing.T) {
			h, want, sourcePath, settingsPath := seedLegacyGitLabNotificationFiles(t)
			lock, err := lockDirectory(h.m.store.dir)
			if err != nil {
				t.Fatal(err)
			}
			store := &Store{dir: h.m.store.dir, lock: lock, ops: defaultSnapshotOps()}
			defer store.Close()
			offline := &roomManager{store: store, index: h.m.index}
			offline.initPlatform()
			real := store.ops.rename
			injected := errors.New("injected post-rename failure")
			store.ops.rename = func(from, to string) error {
				if err := real(from, to); err != nil {
					return err
				}
				if point == "source" && to == sourcePath || point == "settings" && to == settingsPath {
					return injected
				}
				return nil
			}
			if point == "complete" {
				store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					if dir == store.dir {
						var j platformUpgrade
						if readLarkUpgradeJournal(filepath.Join(store.dir, memberNotificationsJournal), &j) == nil && !j.Complete {
							var env notificationEnvelope
							data, _ := os.ReadFile(sourcePath)
							_ = json.Unmarshal(data, &env)
							var cfg gitlabNotificationFile
							_ = readSnapshot(settingsPath, &cfg, maxGitLabConfigBytes)
							if env.Schema == 3 && cfg.Schema == 4 {
								return nil, injected
							}
						}
					}
					return os.CreateTemp(dir, pattern)
				}
			}
			if _, err := offline.upgradeMemberNotificationsLocked(); err == nil {
				t.Fatal("fault not observed")
			}
			if err := checkMemberNotificationsUpgrade(store.dir); err == nil {
				t.Fatal("partial upgrade admitted startup")
			}
			for _, p := range []string{sourcePath + ".v2.bak", settingsPath + ".v3.bak"} {
				if info, err := os.Stat(p); err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("full private backup set missing", err)
				}
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if m, err := newRoomManager(store.dir, 7); err == nil {
				m.close()
				t.Fatal("partially committed group opened")
			}
			var out bytes.Buffer
			if err := runNotificationsUpgrade([]string{"upgrade", "--rooms-dir", store.dir}, &out, &out); err != nil {
				t.Fatal("resume", err)
			}
			restored := testManager(t, store.dir, managerTestProfiles(t))
			got, ok := restored.notifications.StateLocked(want.Metadata().Key)
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatal("resume changed state")
			}
		})
	}
}

func TestMemberNotificationUpgradeRejectsUnassignableOldDataBeforeWriting(t *testing.T) {
	for _, mode := range []string{"target-mismatch", "account-mismatch", "duplicate", "pending-onboarding", "candidate-tamper", "backup-symlink"} {
		t.Run(mode, func(t *testing.T) {
			h, state, path, settingsPath := seedLegacyGitLabNotificationFiles(t)
			lock, err := lockDirectory(h.m.store.dir)
			if err != nil {
				t.Fatal(err)
			}
			offline := &roomManager{store: &Store{dir: h.m.store.dir, lock: lock, ops: defaultSnapshotOps()}, index: h.m.index}
			defer offline.store.Close()
			offline.initPlatform()
			switch mode {
			case "target-mismatch":
				state.Config.Target = notification.Target{}
			case "account-mismatch":
				state.Binding.PlatformGeneration = strings.Repeat("f", 32)
			case "duplicate":
				state.SubscriptionID = strings.Repeat("d", 32)
				for key, entry := range state.Inbox {
					entry.SubscriptionID = state.SubscriptionID
					entry.DeliveryID = state.DeliveryID(key)
					state.Inbox[key] = entry
				}
				path = filepath.Join(filepath.Dir(path), "gitlab.events."+state.SubscriptionID+".json")
			case "pending-onboarding":
				op := platformOnboarding{Schema: 1, ID: strings.Repeat("e", 32), Owner: "human", PlatformID: state.Config.PlatformID, Input: platformOnboardingSpec{OnlyPlatform: true, PlatformID: state.Config.PlatformID, Provider: "gitlab", Origin: state.Binding.URL}, Connection: onboardingStep{Status: "waiting"}, Notifications: onboardingStep{Status: "pending"}, Tool: onboardingStep{Status: "pending"}}
				if err := offline.saveOnboardingLocked(op); err != nil {
					t.Fatal(err)
				}
			case "candidate-tamper":
				j, err := offline.planMemberNotificationsUpgrade(nil)
				if err != nil {
					t.Fatal(err)
				}
				for i := range j.Files {
					if filepath.Base(j.Files[i].Path) == "gitlab-notifications.json" {
						var file gitlabNotificationFile
						_ = json.Unmarshal(j.Files[i].After, &file)
						record := file.Platforms[state.Config.PlatformID][0]
						record.Version++
						file.Platforms[state.Config.PlatformID][0] = record
						j.Files[i].After, _ = json.Marshal(file)
					}
				}
				if err := offline.commitMemberNotificationsUpgrade(j); err == nil {
					t.Fatal("tampered recovery accepted")
				}
				return
			case "backup-symlink":
				if err := os.Symlink(path, path+".v2.bak"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "target-mismatch" || mode == "account-mismatch" || mode == "duplicate" {
				raw, _ := json.Marshal(state)
				data, _ := json.Marshal(notificationEnvelope{SourceID: gitlab.EventsSourceID, Schema: 2, State: raw})
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(settingsPath)
			if _, err := offline.upgradeMemberNotificationsLocked(); err == nil {
				t.Fatal("bad legacy data accepted")
			}
			after, _ := os.ReadFile(settingsPath)
			if !bytes.Equal(before, after) {
				t.Fatal("failed preflight changed settings")
			}
			if _, err := os.Stat(filepath.Join(offline.store.dir, memberNotificationsJournal)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failed preflight wrote journal", err)
			}
		})
	}
}

func TestMemberNotificationLarkOnlyOldEnvelopeRequiresAndReceivesFormatFence(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.start()
	want := h.state()
	path, err := h.m.notifications.StatePathLocked(want.Metadata().Key)
	if err != nil {
		t.Fatal(err)
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(want)
	before, _ := json.Marshal(notificationEnvelope{SourceID: lark.MessagesSourceID, Schema: 2, State: raw})
	if err := os.WriteFile(path, before, 0600); err != nil {
		t.Fatal(err)
	}
	if m, err := newRoomManager(root, 7); err == nil {
		m.close()
		t.Fatal("old Lark-only directory opened")
	}
	var out bytes.Buffer
	if err := runNotificationsUpgrade([]string{"upgrade", "--rooms-dir", root}, &out, &out); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	got, ok := restored.notifications.StateLocked(want.Metadata().Key)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatal("Lark migration changed evidence")
	}
	data, _ := os.ReadFile(path)
	var env notificationEnvelope
	_ = json.Unmarshal(data, &env)
	if env.Schema != 3 {
		t.Fatal("Lark old binary not fenced")
	}
	if _, err := os.Stat(restored.gitlabNotificationPath(want.RoomIdentity)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Lark migration synthesized GitLab settings")
	}
}

func TestMemberNotificationGitLabSourceScopesReopenWithoutSharingProgress(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	seedEventInbox(t, h, 1)
	original := h.state()
	targets := []notification.Target{{}, {Mode: "inbox", AgentID: "assistant", MembershipID: "member-a"}, {Mode: "inbox", AgentID: "reviewer", MembershipID: "member-b"}}
	want := []gitlab.EventsState{original}
	for i, target := range targets[1:] {
		state := original.Clone()
		state.SubscriptionID = strings.Repeat(string(rune('a'+i)), 32)
		state.Config.Target = target
		for key, entry := range state.Inbox {
			entry.SubscriptionID = state.SubscriptionID
			entry.DeliveryID = state.DeliveryID(key)
			state.Inbox[key] = entry
		}
		if i == 0 {
			if _, _, err := state.Accept([]gitlab.Event{h.event(2, 0)}, h.clock.get()); err != nil {
				t.Fatal(err)
			}
		}
		h.m.mu.Lock()
		err := h.m.notifications.SaveLocked(state)
		h.m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, state)
	}
	h.m.mu.Lock()
	for _, state := range want {
		if got, err := h.m.currentEventsForTargetLocked(state.RoomIdentity, state.Config.PlatformID, state.Config.Target); err != nil || got == nil || !reflect.DeepEqual(*got, state) {
			t.Fatal("exact source lookup", err)
		}
	}
	h.m.mu.Unlock()
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	for _, state := range want {
		got, ok := restored.notifications.StateLocked(state.Metadata().Key)
		if !ok || !reflect.DeepEqual(got, state) {
			t.Fatal("source scope lost progress")
		}
	}
	if len(want[1].Inbox) != 2 || len(want[2].Inbox) != 1 || want[1].Inbox[gitlab.EventsKey(want[1].Binding, 7, 1)].DeliveryID == want[2].Inbox[gitlab.EventsKey(want[2].Binding, 7, 1)].DeliveryID {
		t.Fatal("source progress or delivery identity shared")
	}
}

func TestMemberNotificationTodosUpgradePreservesActiveAndRetiredProgress(t *testing.T) {
	parallelFixtureTest(t)
	h := newTodoHarness(t)
	h.pages.pending = []gitlab.Todo{h.todo(10, 0, "pending")}
	h.pages.done = []gitlab.Todo{h.todo(11, 0, "done")}
	h.full()
	active := h.state()
	retired := active.Clone()
	retired.SubscriptionID = strings.Repeat("f", 32)
	for key, entry := range retired.Inbox {
		entry.SubscriptionID = retired.SubscriptionID
		entry.DeliveryID = notification.DeliveryID(retired.Metadata().Key, key)
		retired.Inbox[key] = entry
	}
	var err error
	retired, err = gitlab.RetireTodos(retired)
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	err = h.m.notifications.SaveLocked(retired)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]gitlab.TodosState{}
	for _, state := range []gitlab.TodosState{active, retired} {
		path, err := h.m.notifications.StatePathLocked(state.Metadata().Key)
		if err != nil {
			t.Fatal(err)
		}
		paths[path] = state
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	for path, state := range paths {
		raw, _ := json.Marshal(state)
		data, _ := json.Marshal(notificationEnvelope{SourceID: gitlab.TodosSourceID, Schema: 2, State: raw})
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := runNotificationsUpgrade([]string{"upgrade", "--rooms-dir", root}, &out, &out); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	for _, want := range paths {
		got, ok := restored.notifications.StateLocked(want.Metadata().Key)
		if !ok || !reflect.DeepEqual(got, want) {
			t.Fatal("Todo upgrade changed active or retired evidence")
		}
	}
}

func TestMemberNotificationGitLabAppliedConfigUsesCanonicalRoomTarget(t *testing.T) {
	parallelFixtureTest(t)
	h, _ := newIndependentEventHarness(t)
	state := h.state()
	state.Config.Target = notification.Target{Mode: "room"}
	if err := state.Validate(state.RoomIdentity); err != nil {
		t.Fatal("invalid legacy room target fixture", err)
	}
	cfg := unifiedGitLabConfig(t, h)
	if !gitlabConfigApplied(cfg, state) {
		t.Fatal("room alias unnecessarily blocked restored subscription")
	}
}
