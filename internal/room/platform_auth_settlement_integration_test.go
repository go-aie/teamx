//go:build integration || e2e

package room

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestGitLabCallbackSettlesActiveNotificationsBeforeAdoptingAuthorization(t *testing.T) {
	parallelFixtureTest(t)
	g := newGitlabLoginHarness(t)
	initial, cookie := g.login(t)
	g.callback(initial, cookie, nil)
	m := g.h.m
	fixtures := testManager(t, t.TempDir(), managerTestProfiles(t))
	m.teams = fixtures.teams
	room := managerCreate(t, m, "authorization-settlement")
	c := platformTestList(t, m)[0]
	before, err := m.configureTodosSubscription(context.Background(), notification.Key{Owner: room.RoomIdentity, Source: gitlab.TodosSourceID}, 0,
		gitlab.TodosConfig{Target: fixtureInboxTarget(t, m, room.RoomIdentity), Enabled: true, PlatformID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	entered, canceled, release, recovered := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{}, 8)
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var calls atomic.Int32
	m.notifications.Source(gitlab.TodosSourceID).(*gitlab.TodosSource).Prepare = func(context.Context, string, RoomIdentity, gitlab.TodosQueryScope) (gitlab.TodoFetcher, error) {
		return todoFetchFunc(func(ctx context.Context, _ gitlab.TodoQuery) (gitlab.TodoBatch, error) {
			if calls.Add(1) == 1 {
				close(entered)
				<-ctx.Done()
				close(canceled)
				<-release
				return gitlab.TodoBatch{}, ctx.Err()
			}
			select {
			case recovered <- struct{}{}:
			default:
			}
			return gitlab.TodoBatch{Version: 1, Todos: []gitlab.Todo{}, Complete: true}, nil
		}), nil
	}
	if err := m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, entered, "notification query did not start")
	g.req.Version = c.Version
	g.req.Affected = []gitlabAuthPlatform{{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{room.RoomIdentity}}}
	next, nextCookie := g.login(t)
	finished := make(chan *httptest.ResponseRecorder, 1)
	go func() { finished <- g.callback(next, nextCookie, nil) }()
	awaitEventSignal(t, canceled, "callback did not cancel old query")
	for range 3 {
		m.notifications.EnsureLocked(before.Metadata().Key)
		m.notifications.WakeLocked(before.Metadata().Key)
	}
	if calls.Load() != 1 {
		t.Fatal("callback admitted a replacement before settlement")
	}
	if platformTestList(t, m)[0].Version != c.Version {
		t.Fatal("authorization saved before old work settled")
	}
	unblock()
	select {
	case response := <-finished:
		if response.Code != 303 {
			t.Fatal("callback did not complete", response.Code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback waited on a replacement worker")
	}
	result := g.status(t, next, nextCookie)
	if result.Status != "succeeded" || g.posts.Load() != 2 || g.revokes.Load() != 0 {
		t.Fatal("authorization not adopted", result.Status, result.ErrorCode)
	}
	awaitEventSignal(t, recovered, "verified subscription did not resume")
	m.mu.Lock()
	after, ok := m.todosSubscriptionLocked(before.Metadata().Key)
	m.mu.Unlock()
	if !ok || !after.Baseline.Equal(before.Baseline) || after.Binding.UserID != before.Binding.UserID || after.QueryPaused() {
		t.Fatal("recovery lost subscription progress or identity")
	}
}

func TestGitLabAuthorizationSeparatesSavedGrantFromOnboardingOutcome(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"completed", "cancelled_settings"} {
		t.Run(mode, func(t *testing.T) {
			g := newGitlabLoginHarness(t)
			m := g.h.m
			c := platformTestList(t, m)[0]
			req := platformOnboardingRequest{ServiceEpoch: m.epoch, Caller: humanCaller, OperationID: strings.Repeat("9", 32), platformOnboardingSpec: platformOnboardingSpec{PlatformID: c.ID, Provider: c.Provider, Origin: c.URL, OnlyPlatform: true}}
			onboardingHTTP(t, m, "prepare", req)
			g.req.OnboardingID = req.OperationID
			v, cookie := g.login(t)
			if mode == "cancelled_settings" {
				m.mu.Lock()
				op, err := m.readOnboardingLocked(req.OperationID, humanCaller.ID)
				if err == nil {
					op.Cancelled = true
					err = m.saveOnboardingLocked(op)
				}
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			}
			g.callback(v, cookie, nil)
			result := g.status(t, v, cookie)
			if result.Status != "succeeded" || result.Followup == nil || g.posts.Load() != 1 || g.revokes.Load() != 0 {
				t.Fatal("local setup changed authorization outcome", result.Status, result.ErrorCode)
			}
			want := "completed"
			if mode == "cancelled_settings" {
				want = "pending"
			}
			if result.Followup.Onboarding != want {
				t.Fatal("onboarding outcome was hidden", result.Followup)
			}
			if saved := platformTestList(t, m)[0]; saved.AuthorizationStatus != "active" {
				t.Fatal("adopted grant was lost")
			}
		})
	}
}

func TestLarkLoginHoldsNotificationsUntilAccountVerification(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarnessWithMembers(t, 1)
	h.start()
	before := h.state()
	script := strings.Replace(larkFixtureScript(), "import sys,json,os", "import sys,json,os,time", 1)
	script = strings.Replace(script, "if a==['--version']:", roomAuthCommands+"\nif a==['--version']:", 1)
	if err := os.WriteFile(h.c.CLIProfile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	view := authHTTP(t, h.m, "login", larkAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, Profile: h.c.CLIProfile, Affected: authImpact(t, h.m, "fixture")})
	req := larkAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, SessionID: view.SessionID}
	authWait(t, h.m, req, "waiting")
	for range 3 {
		h.m.notifications.EnsureLocked(before.Metadata().Key)
		h.m.notifications.WakeLocked(before.Metadata().Key)
	}
	if h.m.notifications.DoneLocked(before.Metadata().Key) != nil {
		t.Fatal("notification restarted while CLI authorization was pending")
	}
	if err := os.WriteFile(filepath.Join(h.dir, "auth-release"), []byte("yes"), 0600); err != nil {
		t.Fatal(err)
	}
	authWait(t, h.m, req, "succeeded")
	after := h.state()

	if after.QueryPaused() || !after.Baseline.Equal(before.Baseline) || after.SubscriptionID != before.SubscriptionID || after.Binding.Account != before.Binding.Account {
		t.Fatalf("verified authorization: paused=%v status=%s error=%s baseline=%v id=%v account=%v", after.QueryPaused(), after.Status, after.ErrorCode, after.Baseline.Equal(before.Baseline), after.SubscriptionID == before.SubscriptionID, after.Binding.Account == before.Binding.Account)
	}
}

func TestLarkLoginPersistsRecoveryOfFailedAndUnverifiedSharedConnections(t *testing.T) {
	parallelFixtureTest(t)
	for _, mode := range []string{"restored", "recovery_save_failed"} {
		t.Run(mode, func(t *testing.T) {
			h := newLarkHarnessWithMembers(t, 1)
			h.start()
			before := h.state()
			if mode == "recovery_save_failed" {
				blocked := before.Clone()
				blocked.SubscriptionVersion = ^uint64(0) - 1
				if err := h.m.notifications.SaveLocked(blocked); err != nil {
					t.Fatal(err)
				}
			}
			h.m.mu.Lock()
			ticket, err := h.m.platforms.BeginVerification(h.c.ID, h.c.Version)
			if err == nil {
				_, err = h.m.platforms.CompleteVerification(ticket, platform.Validation{Status: "failed", CheckedAt: time.Now().UTC(), ErrorCode: "credential_unavailable"})
			}
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			// A second saved connection shares the native profile but has never been
			// verified. One explicit login must save both acknowledged targets.
			platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "shared", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
			script := strings.Replace(larkFixtureScript(), "import sys,json,os", "import sys,json,os,time", 1)
			script = strings.Replace(script, "if a==['--version']:", roomAuthCommands+"\nif a==['--version']:", 1)
			if err := os.WriteFile(h.c.CLIProfile.Executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			view := authHTTP(t, h.m, "login", larkAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, Profile: h.c.CLIProfile, Affected: authImpact(t, h.m, "fixture")})
			req := larkAuthRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, SessionID: view.SessionID}
			authWait(t, h.m, req, "waiting")
			if err := os.WriteFile(filepath.Join(h.dir, "auth-release"), []byte("yes"), 0600); err != nil {
				t.Fatal(err)
			}
			result := authWait(t, h.m, req, "succeeded")
			if len(result.Platforms) != 2 {
				t.Fatal("shared verification receipt omitted a connection")
			}
			for _, c := range result.Platforms {
				if c.Validation.Status != "valid" || c.Validation.Account != before.Binding.Account {
					t.Fatal("native login was not adopted", c.Validation.Status)
				}
			}
			after := h.state()
			if (mode == "restored" && after.QueryPaused()) || after.SubscriptionID != before.SubscriptionID || !after.Baseline.Equal(before.Baseline) || after.Binding.Account != before.Binding.Account {
				t.Fatal("failed connection recovery lost notification identity/progress")
			}
			if mode == "recovery_save_failed" && (result.Followup == nil || result.Followup.Notifications != "pending" || !after.QueryPaused()) {
				t.Fatal("notification failure hid adopted authorization", result.Followup)
			}
			root := h.m.store.dir
			if err := h.m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, root, managerTestProfiles(t))
			for _, c := range platformTestList(t, reopened) {
				if c.Provider == "lark" && (c.Validation.Status != "valid" || c.Validation.Account != before.Binding.Account) {
					t.Fatal("saved verification was lost after reopening", c.Validation.Status)
				}
			}

		})
	}
}
