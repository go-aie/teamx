//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func onboardingRequest(t *testing.T, m *roomManager, room roomSummary, member MemberState, c managedPlatform) platformOnboardingRequest {
	t.Helper()
	id, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	return platformOnboardingRequest{ServiceEpoch: m.epoch, Caller: humanCaller, OperationID: id, platformOnboardingSpec: platformOnboardingSpec{MemberIdentity: MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}, PlatformID: c.ID, Provider: c.Provider, Origin: c.URL}}
}
func onboardingHTTP(t *testing.T, m *roomManager, action string, req platformOnboardingRequest) platformOnboarding {
	t.Helper()
	w := managerHTTP(m, "/v1/platforms/onboarding/"+action, req)
	var op platformOnboarding
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &op) != nil {
		t.Fatal(action, w.Code, w.Body.String())
	}
	for _, secret := range []string{"test-platform-secret", "private-client-secret", "candidate-access", "candidate-refresh"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("secret exposed")
		}
	}
	return op
}
func onboardingApply(t *testing.T, m *roomManager, req platformOnboardingRequest) platformOnboarding {
	req.platformOnboardingSpec = platformOnboardingSpec{}
	return onboardingHTTP(t, m, "apply", req)
}
func TestOnboardingGitLabDefaultsAndRetryPreserveProgress(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	req := onboardingRequest(t, m, room, members[0], c)
	prepared := onboardingHTTP(t, m, "prepare", req)
	if prepared.Connection.Status != "waiting" || prepared.Notifications.Status != "pending" {
		t.Fatal("prepare applied defaults")
	}
	repeated := onboardingHTTP(t, m, "prepare", req)
	if repeated.PlatformID != c.ID {
		t.Fatal("retry changed identity")
	}
	op := onboardingApply(t, m, req)
	if op.Connection.Status != "connected" || op.Notifications.Status != "applied" || op.Tool.Status != "applied" {
		t.Fatal(op.Connection, op.Notifications, op.Tool)
	}
	m.mu.Lock()
	before, err := m.gitlabNotificationViewLocked(room.RoomIdentity, c.ID)
	f, fe := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || fe != nil || !before.Config.Enabled || !before.Config.Todos.Enabled || before.Config.Events.Enabled || !before.Config.Target.Inbox() || before.Config.Target.AgentID != members[0].AgentID || before.Config.BackfillStart.IsZero() || len(f.Bindings) != 1 {
		t.Fatal("wrong defaults", err, fe)
	}
	again := onboardingApply(t, m, req)
	m.mu.Lock()
	after, err := m.gitlabNotificationViewLocked(room.RoomIdentity, c.ID)
	next, _ := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(f, next) || again.Notifications.Version != op.Notifications.Version {
		t.Fatal("retry rewrote progress")
	}
	// Ordinary user changes survive another first-use operation.
	cfg := after.Config
	cfg.Enabled = false
	if _, err = m.configureGitLabNotifications(t.Context(), room.RoomIdentity, after.Version, cfg); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	_, _, err = m.changeToolBindingLocked(t.Context(), toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: f.Revision, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "gitlab", PlatformID: c.ID}, Enabled: false})
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	newReq := onboardingRequest(t, m, room, members[0], c)
	saved := onboardingHTTP(t, m, "prepare", newReq)
	if saved.Notifications.Status != "preserved" || saved.Tool.Status != "preserved" {
		t.Fatal("lost saved preferences", saved.Notifications, saved.Tool)
	}
	onboardingApply(t, m, newReq)
	m.mu.Lock()
	last, _ := m.gitlabNotificationViewLocked(room.RoomIdentity, c.ID)
	lastTools, _ := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if last.Config.Enabled || len(lastTools.Bindings) != 0 {
		t.Fatal("reopened user opt-out")
	}
}
func TestOnboardingPartialFailureRecoveryAndStaleSettings(t *testing.T) {
	parallelFixtureTest(t)
	for _, scenario := range []string{"recover", "later_edit"} {
		t.Run(scenario, func(t *testing.T) {
			m, room, _, c, members := cliToolFixture(t, "exit 0")
			req := onboardingRequest(t, m, room, members[0], c)
			{
				cfg := gitlab.DefaultNotificationConfig(c.ID)
				cfg.Events.Enabled, cfg.Events.Project = true, "group/project"
				req.GitLab = &cfg
			}
			onboardingHTTP(t, m, "prepare", req)
			verify := m.platformProject
			m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 0, ErrRequest }
			op := onboardingApply(t, m, req)
			if op.Notifications.Status != "failed" || op.Tool.Status != "applied" {
				t.Fatal("did not report partial result", op.Notifications, op.Tool)
			}
			m.platformProject = verify
			if scenario == "recover" {
				m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
			}
			if scenario == "later_edit" {
				m.mu.Lock()
				v, _ := m.gitlabNotificationViewLocked(room.RoomIdentity, c.ID)
				m.mu.Unlock()
				cfg := v.Config
				cfg.Enabled = false
				if _, err := m.configureGitLabNotifications(t.Context(), room.RoomIdentity, v.Version, cfg); err != nil {
					t.Fatal(err)
				}
			}
			retry := onboardingApply(t, m, req)
			switch scenario {
			case "recover":
				if retry.Notifications.Status != "applied" || retry.Tool.Status != "applied" {
					t.Fatal(retry.Notifications, retry.Tool)
				}
			case "later_edit":
				if retry.Notifications.ErrorCode != "settings_changed" {
					t.Fatal("overwrote newer settings", retry.Notifications)
				}

			}
		})
	}
}

func TestOnboardingLarkDefaultsAndRestart(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	req := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	onboardingHTTP(t, h.m, "prepare", req)
	op := onboardingApply(t, h.m, req)
	if op.Notifications.Status != "applied" || op.Tool.Status != "applied" {
		t.Fatal(op.Notifications, op.Tool)
	}
	before := larkStateFor(t, h.m, h.room.RoomIdentity, h.c.ID)
	if !before.Config.DiscoverPrivate() || !before.Config.Groups.Enabled || before.Config.Groups.Scope != "all" || before.Config.Groups.MessageMode != "mentions" || !before.Config.Groups.AtMe || !before.Config.Groups.AtAll || before.Config.Target.MembershipID != h.members[0].MembershipID {
		t.Fatal("wrong Lark defaults")
	}
	// New manager, new epoch; only the new epoch authorizes resuming local work.
	dir := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restarted := testManager(t, dir, managerTestProfiles(t))
	req.ServiceEpoch = restarted.epoch
	op = onboardingApply(t, restarted, req)
	after := larkStateFor(t, restarted, h.room.RoomIdentity, h.c.ID)
	if op.Notifications.Status != "applied" || after.SubscriptionID != before.SubscriptionID || !after.Baseline.Equal(before.Baseline) || after.SubscriptionVersion != before.SubscriptionVersion {
		t.Fatal("restart reapplied defaults")
	}
	info, err := os.Stat(restarted.onboardingPath(req.OperationID))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("unsafe receipt mode", err)
	}
}
func TestOnboardingFrozenSettingsAndIdentity(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	req := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", req)
	changed := req
	changed.MembershipID = members[1].MembershipID
	if w := managerHTTP(m, "/v1/platforms/onboarding/prepare", changed); w.Code != http.StatusConflict {
		t.Fatal("operation retargeted", w.Code)
	}
	changed = req
	changed.Caller.ID = "someone-else"
	if w := managerHTTP(m, "/v1/platforms/onboarding/prepare", changed); w.Code != http.StatusForbidden {
		t.Fatal("operation owner ignored", w.Code)
	}
	// An explicit opt-out during authentication must stop an old default intent.
	bindCLITool(t, m, room.RoomIdentity, members[1], c, "gitlab", 0)
	op := onboardingApply(t, m, req)
	if op.Tool.ErrorCode != "settings_changed" || op.Notifications.Status != "applied" {
		t.Fatal(op.Notifications, op.Tool)
	}
	// A changed platform generation cannot inherit an unfinished operation.
	m.mu.Lock()
	f, _ := m.platforms.Load()
	for i := range f.Platforms {
		if f.Platforms[i].ID == c.ID {
			f.Platforms[i].Generation = strings.Repeat("f", 32)
			f.Platforms[i].IdentityBinding.Generation = f.Platforms[i].Generation
		}
	}
	err := m.store.save(m.platformsPathForTest(), f, 2<<20)
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	next := onboardingApply(t, m, req)
	if next.Connection.Status == "connected" {
		t.Fatal("recreated identity accepted")
	}
}
func TestOnboardingOnlyPlatformAndExplicitOptions(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	req := onboardingRequest(t, m, room, members[0], c)
	req.MemberIdentity = MemberIdentity{}
	req.OnlyPlatform = true
	onboardingHTTP(t, m, "prepare", req)
	op := onboardingApply(t, m, req)
	if op.Connection.Status != "connected" || op.Notifications.Status != "skipped" || op.Tool.Status != "skipped" {
		t.Fatal(op.Connection, op.Notifications, op.Tool)
	}
	if _, err := os.Stat(filepath.Join(m.roomDir(room.RoomID), "gitlab-notifications.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("only-platform configured notifications")
	}
	req = onboardingRequest(t, m, room, members[0], c)
	no := false
	req.Notifications = &no
	onboardingHTTP(t, m, "prepare", req)
	op = onboardingApply(t, m, req)
	if op.Notifications.Status != "applied" || op.Tool.Status != "applied" {
		t.Fatal(op.Notifications, op.Tool)
	}
}

func TestOnboardingLarkExplicitOptOutKeepsUnstartedBaseline(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	req := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	no := false
	req.Notifications, req.Tool = &no, &no
	onboardingHTTP(t, h.m, "prepare", req)
	op := onboardingApply(t, h.m, req)
	state := larkStateFor(t, h.m, h.room.RoomIdentity, h.c.ID)
	if op.Notifications.Status != "applied" || op.Tool.Status != "applied" || state.Config.Enabled || !state.Baseline.IsZero() {
		t.Fatal("opt-out lost or baseline started", op.Notifications, op.Tool)
	}
	fresh := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	preserved := onboardingHTTP(t, h.m, "prepare", fresh)
	if preserved.Notifications.Status != "preserved" || preserved.Tool.Status != "preserved" {
		t.Fatal("opt-out was reapplied as default")
	}
	onboardingApply(t, h.m, fresh)
	// The first explicit enable starts at its save time, retaining the same subscription.
	h.clock.add(30 * time.Second)
	cfg := state.Config
	cfg.Enabled = true
	enabled, err := h.m.configureLarkSubscription(t.Context(), state.Metadata().Key, state.SubscriptionVersion, cfg)
	if err != nil || enabled.SubscriptionID != state.SubscriptionID || !enabled.Baseline.Equal(h.clock.get().Truncate(time.Second)) {
		t.Fatal("wrong first enable baseline", err, enabled.Baseline)
	}
}
func TestOnboardingInterruptedJournalSaveRecoversAfterRestart(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	req := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", req)
	original := m.store.ops
	saves := 0
	m.store.ops.rename = func(old, next string) error {
		if next == m.onboardingPath(req.OperationID) {
			saves++
			if saves == 2 {
				return errors.New("injected receipt loss")
			}
		}
		return os.Rename(old, next)
	}
	_, err := m.applyOnboarding(t.Context(), req.OperationID, humanCaller.ID)
	if err == nil || saves != 2 {
		t.Fatal("did not interrupt receipt save", err, saves)
	}
	m.store.ops = original
	m.mu.Lock()
	before, e := m.gitlabNotificationViewLocked(room.RoomIdentity, c.ID)
	m.mu.Unlock()
	if e != nil || before.Version != 1 || before.Todos.SubscriptionID == "" {
		t.Fatal("settings did not commit before interruption", e, before.Version, before.Pending, before.Errors)
	}
	root := m.store.dir
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restarted := testManager(t, root, managerTestProfiles(t))
	req.ServiceEpoch = restarted.epoch
	op := onboardingApply(t, restarted, req)
	restarted.mu.Lock()
	after, e := restarted.gitlabNotificationViewLocked(room.RoomIdentity, c.ID)
	bindings, be := restarted.readToolBindingsLocked(room.RoomIdentity)
	restarted.mu.Unlock()
	if e != nil || be != nil || op.Notifications.Status != "applied" || op.Tool.Status != "applied" || after.Pending["todos"] || after.Version != before.Version || after.Todos.SubscriptionID != before.Todos.SubscriptionID || !after.Config.BackfillStart.Equal(before.Config.BackfillStart) || len(bindings.Bindings) != 1 {
		t.Fatal("restart changed settings identity or did not resume", e, be, op.Notifications, op.Tool)
	}
}
func TestOnboardingCancellationAndEpochFence(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	req := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", req)
	m.mu.Lock()
	refs, err := m.managedPlatformReferencesLocked(c.ID)
	m.mu.Unlock()
	if err != nil || len(refs) != 1 || !refs[0].Pending {
		t.Fatal("pending target missing from references", err)
	}
	minimal := req
	minimal.platformOnboardingSpec = platformOnboardingSpec{}
	stale := minimal
	stale.ServiceEpoch = "old"
	if w := managerHTTP(m, "/v1/platforms/onboarding/apply", stale); w.Code != 403 {
		t.Fatal("stale epoch accepted", w.Code)
	}
	cancelled := onboardingHTTP(t, m, "cancel", minimal)
	if !cancelled.Cancelled {
		t.Fatal("cancel not durable")
	}
	if w := managerHTTP(m, "/v1/platforms/onboarding/apply", minimal); w.Code != 409 {
		t.Fatal("cancelled operation resumed", w.Code)
	}
	m.mu.Lock()
	refs, err = m.managedPlatformReferencesLocked(c.ID)
	m.mu.Unlock()
	if err != nil || len(refs) != 0 {
		t.Fatal("cancelled operation retained pending permission", err)
	}
}

func TestOnboardingProfileVerificationHandoffAndMissingDependency(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	req := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	op := onboardingHTTP(t, h.m, "prepare", req)
	// A single verified-platform handoff applies the two independent settings.
	response := managerHTTP(h.m, "/v1/platforms/verify", managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: h.c.ID, Version: h.c.Version, OnboardingID: op.ID})
	var c managedPlatform
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &c) != nil || c.Onboarding == nil || c.Onboarding.Notifications.Status != "applied" || c.Onboarding.Tool.Status != "applied" || c.Version <= h.c.Version {
		t.Fatal("profile handoff did not return independent outcomes", response.Code, response.Body.String())
	}
	m, room, _, git, members := cliToolFixture(t, "exit 0")
	m.toolCatalog[0].Executable = filepath.Join(t.TempDir(), "not-installed")
	r := onboardingRequest(t, m, room, members[0], git)
	onboardingHTTP(t, m, "prepare", r)
	result := onboardingApply(t, m, r)
	if result.Tool.Status != "applied" || result.Tool.ErrorCode == "" || result.Notifications.Status != "applied" {
		t.Fatal("dependency erased saved intent or affected notifications", result.Tool, result.Notifications)
	}
	m.mu.Lock()
	bindings, err := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(bindings.Bindings) != 1 {
		t.Fatal("missing dependency discarded binding", err)
	}
}

func TestOnboardingMissingMemberPreservesConnectedPlatform(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 0")
	req := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", req)
	if _, err := svc.remove(apiRequest{ServiceInfo: svc.info, Caller: humanCaller, AgentID: members[0].AgentID, MembershipID: members[0].MembershipID}); err != nil {
		t.Fatal(err)
	}
	result := onboardingApply(t, m, req)
	if result.Connection.Status != "connected" || result.Notifications.ErrorCode != "target_changed" || result.Tool.ErrorCode != "target_changed" {
		t.Fatal("lost connection or rebound missing member", result.Connection, result.Notifications, result.Tool)
	}
	m.mu.Lock()
	bindings, err := m.readToolBindingsLocked(room.RoomIdentity)
	_, configured := m.gitlabNotifications[room.RoomIdentity][notificationScope{c.ID, "inbox:" + members[0].MembershipID}]
	m.mu.Unlock()
	if err != nil || len(bindings.Bindings) != 0 || configured {
		t.Fatal("missing member acquired settings", err)
	}
}

func TestOnboardingUncommittedFirstAttemptDoesNotFreezeReceptionStart(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	clock := &eventTestClock{now: time.Unix(1000, 0).UTC()}
	m.notifications.Clock = clock.get
	req := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", req)
	original := m.store.ops
	fail := true
	m.store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
		if dir == m.roomDir(room.RoomID) && fail {
			fail = false
			return nil, errors.New("before notification commit")
		}
		return original.createTemp(dir, pattern)
	}
	result := onboardingApply(t, m, req)
	if result.Notifications.Status != "failed" || result.Notifications.Version != 0 {
		t.Fatal("first save did not fail before commit", result.Notifications)
	}
	m.store.ops = original
	clock.add(30 * time.Second)
	result = onboardingApply(t, m, req)
	m.mu.Lock()
	saved, err := m.gitlabNotificationViewLocked(room.RoomIdentity, c.ID)
	m.mu.Unlock()
	if err != nil || result.Notifications.Status != "applied" || !saved.Config.BackfillStart.Equal(clock.get()) {
		t.Fatal("uncommitted attempt froze reception start", err, result.Notifications, saved.Config.BackfillStart)
	}
}

func TestOnboardingToolDisableSettlesWithoutBlockingAnotherPlatform(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "gitlab", 0)
	secondReq := platformTestRequest(m)
	secondReq.ID = strings.Repeat("d", 32)
	secondReq.Name = "Another"
	second := verifyTodoAccount(t, m, platformTestSave(t, m, secondReq), 202)
	req := onboardingRequest(t, m, room, members[0], c)
	no := false
	req.Tool = &no
	onboardingHTTP(t, m, "prepare", req)
	callCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	m.mu.Lock()
	call := &platformCall{owner: room.RoomIdentity, agentID: members[0].AgentID, membershipID: members[0].MembershipID, tool: "gitlab", platformID: c.ID, cancel: cancel, done: done}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	m.mu.Unlock()
	finished := make(chan error, 1)
	go func() { _, err := m.applyOnboarding(t.Context(), req.OperationID, humanCaller.ID); finished <- err }()
	select {
	case <-callCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("disable did not cancel exact call")
	}
	m.mu.Lock()
	busy := m.busy
	f, err := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || busy || len(f.Bindings) != 0 || len(f.Disabled) != 1 {
		t.Fatal("disable did not commit before settling", err, busy)
	}
	// B can save/use its own binding while A's canceled call has not returned.
	bindCLITool(t, m, room.RoomIdentity, members[0], second, "gitlab", f.Revision)
	m.mu.Lock()
	delete(m.platformCalls, call)
	close(done)
	m.mu.Unlock()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("disable did not settle")
	}
}

func TestOnboardingLegacyRemovedBindingRequiresExplicitChoice(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	bindCLITool(t, m, room.RoomIdentity, members[1], c, "gitlab", 0)
	path := m.toolBindingsPath(room.RoomIdentity)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]any
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "disabled")
	data, err = json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	req := onboardingRequest(t, m, room, members[0], c)
	op := onboardingHTTP(t, m, "prepare", req)
	if op.Tool.Status != "preserved" || op.Tool.ErrorCode != "legacy_tool_choice_unknown" {
		t.Fatal("legacy absence became a fresh authorization", op.Tool)
	}
	onboardingApply(t, m, req)
	m.mu.Lock()
	f, err := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(f.Bindings) != 1 {
		t.Fatal("legacy disable was reopened", err)
	}
	yes := true
	explicit := onboardingRequest(t, m, room, members[0], c)
	explicit.Tool = &yes
	onboardingHTTP(t, m, "prepare", explicit)
	op = onboardingApply(t, m, explicit)
	if op.Tool.Status != "applied" {
		t.Fatal("explicit choice could not initialize legacy binding", op.Tool)
	}
}
