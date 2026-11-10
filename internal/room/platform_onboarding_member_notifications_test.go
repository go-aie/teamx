//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func larkOnboardingSubscriptionForTest(t *testing.T, m *roomManager, req platformOnboardingRequest) lark.State {
	t.Helper()
	m.mu.Lock()
	s, err := m.currentLarkForTargetLocked(req.RoomIdentity, req.PlatformID, inboxTarget(req.MemberIdentity))
	m.mu.Unlock()
	if err != nil || s == nil {
		t.Fatal("missing exact member subscription", err)
	}
	return *s
}

func TestMemberNotificationGitLabOnboardingMembersAndRoomRemainIndependent(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	clock := &eventTestClock{now: time.Unix(1000, 0).UTC()}
	m.notifications.Clock = clock.get
	aReq := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", aReq)
	onboardingApply(t, m, aReq)
	a := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(aReq.MemberIdentity))
	rCfg := gitlab.DefaultNotificationConfig(c.ID)
	rCfg.Enabled = false
	r := saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, 0, rCfg)
	bReq := onboardingRequest(t, m, room, members[1], c)
	p := onboardingHTTP(t, m, "prepare", bReq)
	if p.Schema != platformOnboardingSchema || p.BeforeNotificationVersion != 0 || p.Notifications.Status != "pending" || p.GitLab.Target != inboxTarget(bReq.MemberIdentity) {
		t.Fatal("A or Room settings mistaken for B", p)
	}
	// A's intervening edit changes only A's version, not B's admission.
	a.Config.IntervalSeconds = 60
	a = saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, a.Version, a.Config)
	clock.add(time.Minute)
	op := onboardingApply(t, m, bReq)
	if op.Notifications.Status != "applied" || op.Tool.Status != "applied" {
		t.Fatal(op.Notifications, op.Tool)
	}
	// The response alone locates B's exact settings, independently of UI state.
	b := getGitLabNotificationConfigHTTP(t, m, op.Input.RoomIdentity, op.PlatformID, op.GitLab.Target)
	if b.Version != 1 || !b.Config.Enabled || !b.Config.Todos.Enabled || b.Config.Events.Enabled || b.Config.Target != inboxTarget(bReq.MemberIdentity) || b.Todos.SubscriptionID == a.Todos.SubscriptionID {
		t.Fatal("wrong B initialization", b)
	}
	if !b.Config.BackfillStart.Equal(clock.get()) || b.Config.BackfillStart.Equal(a.Config.BackfillStart) {
		t.Fatal("B inherited A's reception baseline")
	}
	if got := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, a.Config.Target); !reflect.DeepEqual(a, got) {
		t.Fatal("B changed A")
	}
	if got := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, r.Config.Target); !reflect.DeepEqual(r, got) {
		t.Fatal("B changed Room")
	}
	b.Config.Enabled = false
	b = saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, b.Version, b.Config)
	fresh := onboardingRequest(t, m, room, members[1], c)
	p = onboardingHTTP(t, m, "prepare", fresh)
	if p.Notifications.Status != "preserved" || p.BeforeNotificationVersion != b.Version || p.GitLab.Enabled {
		t.Fatal("B opt-out lost", p)
	}
	onboardingApply(t, m, fresh)
	if got := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, b.Config.Target); !reflect.DeepEqual(b, got) {
		t.Fatal("preserving B rewrote settings or progress")
	}
}

func TestMemberNotificationLarkOnboardingMembersReceiptRecoveryAndEpoch(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	aReq := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	onboardingHTTP(t, h.m, "prepare", aReq)
	onboardingApply(t, h.m, aReq)
	a := larkOnboardingSubscriptionForTest(t, h.m, aReq)
	bReq := onboardingRequest(t, h.m, h.room, h.members[1], h.c)
	p := onboardingHTTP(t, h.m, "prepare", bReq)
	if p.BeforeNotificationVersion != 0 || p.Notifications.Status != "pending" || p.Lark.Target != inboxTarget(bReq.MemberIdentity) {
		t.Fatal("A mistaken for B", p)
	}
	original := h.m.store.ops
	saves := 0
	h.m.store.ops.rename = func(old, next string) error {
		if next == h.m.onboardingPath(bReq.OperationID) {
			saves++
			if saves == 2 {
				return errors.New("lost B notification receipt")
			}
		}
		return os.Rename(old, next)
	}
	_, err := h.m.applyOnboarding(t.Context(), bReq.OperationID, humanCaller.ID)
	if err == nil || saves != 2 {
		t.Fatal("receipt failure was not injected", err)
	}
	h.m.store.ops = original
	b := larkOnboardingSubscriptionForTest(t, h.m, bReq)
	if b.SubscriptionID == a.SubscriptionID || !b.Config.DiscoverPrivate() || b.Config.Groups.Enabled {
		t.Fatal("B did not get independent defaults")
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	m := testManager(t, root, managerTestProfiles(t))
	// Reopen after an uncertain store write before admitting further mutations.
	// Editing A cannot make B's committed configuration look stale on recovery.
	a.Config.Enabled = false
	a, err = m.configureLarkSubscription(t.Context(), a.Metadata().Key, a.SubscriptionVersion, a.Config)
	if err != nil {
		t.Fatal(err)
	}
	minimal := bReq
	minimal.platformOnboardingSpec = platformOnboardingSpec{}
	for _, action := range []string{"status", "apply", "cancel"} {
		if w := managerHTTP(m, "/v1/platforms/onboarding/"+action, minimal); w.Code != http.StatusForbidden {
			t.Fatal("old epoch accepted", action, w.Code)
		}
	}
	bReq.ServiceEpoch = m.epoch
	minimal.ServiceEpoch = m.epoch
	op := onboardingHTTP(t, m, "status", minimal)
	if op.Input.MemberIdentity != bReq.MemberIdentity || op.PlatformID != h.c.ID || op.Lark.Target != inboxTarget(bReq.MemberIdentity) {
		t.Fatal("status lost exact navigation identity", op)
	}
	op = onboardingApply(t, m, bReq)
	if op.Notifications.Status != "applied" || op.Tool.Status != "applied" || !reflect.DeepEqual(b, larkOnboardingSubscriptionForTest(t, m, bReq)) || !reflect.DeepEqual(a, larkOnboardingSubscriptionForTest(t, m, aReq)) {
		t.Fatal("recovery rewrote either member", op.Notifications, op.Tool)
	}
	b.Config.Enabled = false
	b, err = m.configureLarkSubscription(t.Context(), b.Metadata().Key, b.SubscriptionVersion, b.Config)
	if err != nil {
		t.Fatal(err)
	}
	fresh := onboardingRequest(t, m, h.room, h.members[1], h.c)
	p = onboardingHTTP(t, m, "prepare", fresh)
	if p.Notifications.Status != "preserved" || p.Lark.Enabled {
		t.Fatal("B disabled preference lost", p)
	}
	onboardingApply(t, m, fresh)
	if !reflect.DeepEqual(b, larkOnboardingSubscriptionForTest(t, m, fresh)) {
		t.Fatal("preserved B was changed")
	}
}

func TestMemberNotificationGitLabPartialApplyRecoversOnlyOriginalMember(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	aReq := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", aReq)
	onboardingApply(t, m, aReq)
	a := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(aReq.MemberIdentity))
	bReq := onboardingRequest(t, m, room, members[1], c)
	cfg := gitlab.DefaultNotificationConfig(c.ID)
	cfg.Events.Enabled, cfg.Events.Project = true, "group/project"
	bReq.GitLab = &cfg
	onboardingHTTP(t, m, "prepare", bReq)
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 0, ErrRequest }
	op := onboardingApply(t, m, bReq)
	if op.Notifications.Status != "failed" || op.Tool.Status != "applied" {
		t.Fatal("partial outcomes lost", op)
	}
	b := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(bReq.MemberIdentity))
	a.Config.IntervalSeconds = 60
	a = saveGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, a.Version, a.Config)
	m.platformProject = func(context.Context, string, string, workspaceSecret) (int64, error) { return 7, nil }
	op = onboardingApply(t, m, bReq)
	after := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(bReq.MemberIdentity))
	if op.Notifications.Status != "applied" || after.Version != b.Version || after.Pending["events"] || after.Pending["todos"] {
		t.Fatal("could not resume exact pending configuration", op.Notifications, after)
	}
	if got := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, a.Config.Target); !reflect.DeepEqual(a, got) {
		t.Fatal("B recovery changed A")
	}
}

func TestMemberNotificationOnboardingReplacementDoesNotInheritPreparedIntent(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members := cliToolFixture(t, "exit 0")
	aReq := onboardingRequest(t, m, room, members[0], c)
	onboardingHTTP(t, m, "prepare", aReq)
	onboardingApply(t, m, aReq)
	a := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(aReq.MemberIdentity))
	bReq := onboardingRequest(t, m, room, members[1], c)
	onboardingHTTP(t, m, "prepare", bReq)
	if _, err := svc.remove(apiRequest{AgentID: members[1].AgentID, MembershipID: members[1].MembershipID}); err != nil {
		t.Fatal(err)
	}
	replacement := inboxRoutingMember(t, svc, members[1].AgentID, "replacement-membership")
	op := onboardingApply(t, m, bReq)
	if op.Connection.Status != "connected" || op.Notifications.ErrorCode != "target_changed" || op.Tool.ErrorCode != "target_changed" {
		t.Fatal("replacement inherited operation", op)
	}
	if got := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(replacement)); got.Version != 0 {
		t.Fatal("replacement acquired notification")
	}
	if got := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, a.Config.Target); !reflect.DeepEqual(a, got) {
		t.Fatal("replacement affected A")
	}
}

func TestMemberNotificationOnboardingJournalTargetValidationAndHistoricalResults(t *testing.T) {
	parallelFixtureTest(t)
	m, room, _, c, members := cliToolFixture(t, "exit 0")
	req := onboardingRequest(t, m, room, members[0], c)
	op := onboardingHTTP(t, m, "prepare", req)
	cfg := *op.GitLab
	for _, target := range []notification.Target{{Mode: "room"}, inboxTarget(notificationMemberIdentity(room.RoomIdentity, members[1]))} {
		wrong := onboardingRequest(t, m, room, members[0], c)
		changed := cfg
		changed.Target = target
		wrong.GitLab = &changed
		if w := managerHTTP(m, "/v1/platforms/onboarding/prepare", wrong); w.Code != http.StatusBadRequest {
			t.Fatal("prepare accepted another notification target", target, w.Code)
		}
	}
	for _, target := range []notification.Target{{}, inboxTarget(notificationMemberIdentity(room.RoomIdentity, members[1]))} {
		bad := op
		changed := cfg
		changed.Target = target
		bad.GitLab = &changed
		if validOnboarding(bad) {
			t.Fatal("journal accepts another target", target)
		}
	}
	// Completed schema 1 can describe a Room notification preserved during
	// member onboarding. Read it as history without creating a member setting.
	op.Schema = 1
	cfg.Target = notification.Target{}
	op.GitLab = &cfg
	op.Notifications.Status, op.Tool.Status = "preserved", "preserved"
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.onboardingPath(op.ID), data, 0600); err != nil {
		t.Fatal(err)
	}
	minimal := req
	minimal.platformOnboardingSpec = platformOnboardingSpec{}
	history := onboardingHTTP(t, m, "status", minimal)
	if history.Schema != 1 || history.GitLab.Target.Inbox() {
		t.Fatal("historical result retargeted")
	}
	onboardingApply(t, m, req)
	if got := getGitLabNotificationConfigHTTP(t, m, room.RoomIdentity, c.ID, inboxTarget(req.MemberIdentity)); got.Version != 0 {
		t.Fatal("historical apply replayed defaults")
	}
	// Unfinished old operations must finish/cancel using the previous version.
	op.Notifications.Status = "pending"
	data, err = json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(m.onboardingPath(op.ID), data, 0600); err != nil {
		t.Fatal(err)
	}
	if w := managerHTTP(m, "/v1/platforms/onboarding/apply", minimal); w.Code != http.StatusConflict {
		t.Fatal("unfinished legacy operation replayed", w.Code)
	}
}

func TestMemberNotificationOnboardingFirstEnableReceiptRecovery(t *testing.T) {
	parallelFixtureTest(t)
	for _, provider := range []string{"gitlab", "lark"} {
		t.Run(provider, func(t *testing.T) {
			m, room, _, c, members := cliToolFixture(t, "exit 0")
			if provider == "lark" {
				h := newLarkHarness(t)
				m, room, c, members = h.m, h.room, h.c, h.members
			}
			clock := &eventTestClock{now: time.Unix(1000, 0).UTC()}
			m.notifications.Clock = clock.get
			no, yes := false, true
			req := onboardingRequest(t, m, room, members[1], c)
			req.Notifications, req.Tool = &no, &no
			onboardingHTTP(t, m, "prepare", req)
			onboardingApply(t, m, req)
			clock.add(time.Minute)
			firstEnable := onboardingRequest(t, m, room, members[1], c)
			firstEnable.Notifications = &yes
			p := onboardingHTTP(t, m, "prepare", firstEnable)
			if p.BeforeNotificationVersion != 1 {
				t.Fatal("disabled subscription missing")
			}
			original := m.store.ops
			saves := 0
			m.store.ops.rename = func(old, next string) error {
				if next == m.onboardingPath(firstEnable.OperationID) {
					saves++
					if saves == 2 {
						return errors.New("first enable receipt lost")
					}
				}
				return os.Rename(old, next)
			}
			if _, err := m.applyOnboarding(t.Context(), firstEnable.OperationID, humanCaller.ID); err == nil || saves != 2 {
				t.Fatal("first enable receipt was not interrupted", err)
			}
			m.store.ops = original
			root := m.store.dir
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			restarted := testManager(t, root, managerTestProfiles(t))
			firstEnable.ServiceEpoch = restarted.epoch
			op := onboardingApply(t, restarted, firstEnable)
			if op.Notifications.Status != "applied" || op.Notifications.Version != 2 || op.Tool.Status != "preserved" {
				t.Fatal("first enable recovery changed independent choices", op.Notifications, op.Tool)
			}
			var start time.Time
			if provider == "gitlab" {
				start = getGitLabNotificationConfigHTTP(t, restarted, room.RoomIdentity, c.ID, op.GitLab.Target).Config.BackfillStart
			} else {
				start = larkOnboardingSubscriptionForTest(t, restarted, firstEnable).Baseline
			}
			if !start.Equal(clock.get()) {
				t.Fatal("recovery moved first reception time", start)
			}
		})
	}
}
