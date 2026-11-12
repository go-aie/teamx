//go:build integration || e2e

package room

import (
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestMemberPlatformAddChoices(t *testing.T) {
	parallelFixtureTest(t)
	for _, notifications := range []bool{false, true} {
		for _, tools := range []bool{false, true} {
			t.Run(map[bool]string{false: "notifications_off", true: "notifications_on"}[notifications]+"/"+map[bool]string{false: "tools_off", true: "tools_on"}[tools], func(t *testing.T) {
				m, r, _, c, members := cliToolFixture(t, "exit 0")
				req := onboardingRequest(t, m, r, members[0], c)
				req.PreserveExisting = true
				req.Notifications = &notifications
				req.Tool = &tools
				prepared := onboardingHTTP(t, m, "prepare", req)
				if prepared.BeforeNotificationVersion != 0 || prepared.Notifications.Status != "pending" || prepared.Tool.Status != "pending" {
					t.Fatal(prepared)
				}
				op := onboardingApply(t, m, req)
				cfg := getGitLabNotificationConfigHTTP(t, m, r.RoomIdentity, c.ID, inboxTarget(req.MemberIdentity))
				m.mu.Lock()
				f, err := m.readToolBindingsLocked(r.RoomIdentity)
				m.mu.Unlock()
				if err != nil || op.Notifications.Status != "applied" || op.Tool.Status != "applied" || cfg.Config.Enabled != notifications || len(f.Bindings) != map[bool]int{true: 1, false: 0}[tools] || len(f.Disabled) != map[bool]int{true: 0, false: 1}[tools] {
					t.Fatal(op, cfg, f, err)
				}
				if !cfg.Config.Todos.Enabled || cfg.Config.Events.Enabled || cfg.Config.IntervalSeconds != gitlab.DefaultIntervalSeconds {
					t.Fatal("defaults changed", cfg.Config)
				}
				again := onboardingApply(t, m, req)
				if !reflect.DeepEqual(op, again) {
					t.Fatal("replayed completed steps")
				}
			})
		}
	}
}

func TestMemberPlatformAddPreservesClosedChoices(t *testing.T) {
	parallelFixtureTest(t)
	m, r, _, c, members := cliToolFixture(t, "exit 0")
	off, on := false, true
	first := onboardingRequest(t, m, r, members[0], c)
	first.Notifications = &off
	first.Tool = &off
	onboardingHTTP(t, m, "prepare", first)
	onboardingApply(t, m, first)
	before := getGitLabNotificationConfigHTTP(t, m, r.RoomIdentity, c.ID, inboxTarget(first.MemberIdentity))
	m.mu.Lock()
	bindings, _ := m.readToolBindingsLocked(r.RoomIdentity)
	m.mu.Unlock()
	add := onboardingRequest(t, m, r, members[0], c)
	add.PreserveExisting = true
	add.Notifications = &on
	add.Tool = &on
	// An add panel can become stale before prepare; neither checked default may
	// overwrite a closed choice found by the server under its own lock.
	cfg := gitlab.DefaultNotificationConfig(c.ID)
	cfg.IntervalSeconds = 90
	add.GitLab = &cfg
	op := onboardingHTTP(t, m, "prepare", add)
	if op.Notifications.Status != "preserved" || op.Tool.Status != "preserved" || !reflect.DeepEqual(*op.GitLab, before.Config) {
		t.Fatal(op)
	}
	onboardingApply(t, m, add)
	after := getGitLabNotificationConfigHTTP(t, m, r.RoomIdentity, c.ID, inboxTarget(first.MemberIdentity))
	m.mu.Lock()
	next, _ := m.readToolBindingsLocked(r.RoomIdentity)
	m.mu.Unlock()
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(bindings, next) {
		t.Fatal("add changed saved choices")
	}
}

func TestMemberPlatformNotificationOnlyDefaultsAndReceiptRecovery(t *testing.T) {
	parallelFixtureTest(t)
	m, r, _, c, members := cliToolFixture(t, "exit 0")
	clock := &eventTestClock{now: time.Unix(10000, 0).UTC()}
	m.notifications.Clock = clock.get
	req := onboardingRequest(t, m, r, members[0], c)
	req.SkipTool = true
	req.Backfill = &onboardingBackfill{Seconds: 3600}
	prepared := onboardingHTTP(t, m, "prepare", req)
	if prepared.Tool.Status != "skipped" || prepared.GitLab.BackfillSeconds != 3600 {
		t.Fatal(prepared)
	}
	op := onboardingApply(t, m, req)
	before := getGitLabNotificationConfigHTTP(t, m, r.RoomIdentity, c.ID, inboxTarget(req.MemberIdentity))
	if !before.Config.BackfillStart.Equal(clock.get().Add(-time.Hour)) || !before.Config.Enabled || !before.Config.Todos.Enabled {
		t.Fatal(before)
	}
	m.mu.Lock()
	f, _ := m.readToolBindingsLocked(r.RoomIdentity)
	op.Notifications.Status = "pending"
	err := m.saveOnboardingLocked(op)
	m.mu.Unlock()
	if err != nil || f.Revision != 0 || len(f.Bindings) != 0 {
		t.Fatal("notification toggle changed tools", f, err)
	}
	clock.add(time.Minute)
	recovered := onboardingApply(t, m, req)
	after := getGitLabNotificationConfigHTTP(t, m, r.RoomIdentity, c.ID, inboxTarget(req.MemberIdentity))
	if recovered.Notifications.Status != "applied" || recovered.Tool.Status != "skipped" || !reflect.DeepEqual(before, after) {
		t.Fatal("recovery reset first start", recovered)
	}
}

func TestMemberPlatformLarkChoicesAndToolOnly(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	req := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	off, on := false, true
	req.PreserveExisting = true
	req.Notifications = &off
	req.Tool = &off
	onboardingHTTP(t, h.m, "prepare", req)
	onboardingApply(t, h.m, req)
	before := larkOnboardingSubscriptionForTest(t, h.m, req)
	add := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	add.PreserveExisting = true
	add.Notifications = &on
	add.Tool = &on
	p := onboardingHTTP(t, h.m, "prepare", add)
	if p.Notifications.Status != "preserved" || p.Tool.Status != "preserved" {
		t.Fatal(p)
	}
	onboardingApply(t, h.m, add)
	if after := larkOnboardingSubscriptionForTest(t, h.m, req); !reflect.DeepEqual(before, after) {
		t.Fatal("Lark closed subscription changed")
	}
	// Tool-only application can enable an exact binding without touching the
	// notification version, baseline or progress.
	only := onboardingRequest(t, h.m, h.room, h.members[0], h.c)
	only.SkipNotifications = true
	only.Tool = &on
	onboardingHTTP(t, h.m, "prepare", only)
	op := onboardingApply(t, h.m, only)
	if op.Notifications.Status != "skipped" || op.Tool.Status != "applied" {
		t.Fatal(op)
	}
	if after := larkOnboardingSubscriptionForTest(t, h.m, req); !reflect.DeepEqual(before, after) {
		t.Fatal("tool-only write changed notification")
	}
	h.m.mu.Lock()
	f, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	expected := tool.Binding{AgentID: req.AgentID, MembershipID: req.MembershipID, Tool: "lark", PlatformID: h.c.ID, PlatformGeneration: op.Generation}
	if err != nil || len(f.Bindings) != 1 {
		t.Fatal(f, err)
	}
	authorization := f.Bindings[0].ActionAuthorization
	if authorization == nil || len(authorization.Grants) != 3 {
		t.Fatal("explicit tool enable did not save default actions", f)
	}
	expected.ActionAuthorization = authorization
	expected.CapabilityEvidence = f.Bindings[0].CapabilityEvidence
	if !reflect.DeepEqual(f.Bindings[0], expected) {
		t.Fatal("tool enable changed the exact binding identity", f)
	}
}
