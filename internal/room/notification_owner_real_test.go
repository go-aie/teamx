//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Uses an explicitly selected existing access grant and rejects refresh, revoke
// and business writes at the transport. Every Room and target Store is isolated.
func TestNotificationOwnersRealGitLabReadOnly(t *testing.T) {
	inputFile := os.Getenv("TEAMX_NOTIFICATION_GITLAB_INPUT")
	if inputFile == "" {
		t.Skip("explicit private GitLab sample configuration required")
	}
	var input conversationGitLabSettingsInput
	if jsonFile(inputFile, &input) != nil || !filepath.IsAbs(input.OAuthSnapshot) || input.ProjectID <= 0 {
		t.Fatal("invalid isolated GitLab input")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "external_writes": 0, "transport": "real GitLab HTTPS GET; isolated source and member owners"}
	defer imageRealReport(t, out, report)
	t.Setenv("TEAMX_REAL_OAUTH_SNAPSHOT", input.OAuthSnapshot)
	t.Setenv("TEAMX_REAL_OAUTH_CONNECTION", input.ConnectionID)
	file, _ := selectedRealOAuthSnapshot(t, input.Origin)
	if !file.OAuthCredentials[0].ExpiresAt.After(time.Now().Add(4 * time.Minute)) {
		t.Fatal("existing access grant expires before bounded run; refresh disabled")
	}
	m := testManager(t, filepath.Join(out, "rooms"), managerTestProfiles(t))
	m.config.unifiedInputs = true
	m.platformHTTP = workspaceHTTPFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Scheme != "https" || r.URL.Scheme+"://"+r.URL.Host != input.Origin {
			return nil, platform.ErrCredential
		}
		return http.DefaultTransport.RoundTrip(r)
	})
	c := realOAuthPlatform(t, m, input.Origin)
	room := managerCreate(t, m, "notification-owner-real")
	target := fixtureInboxTarget(t, m, room.RoomIdentity)
	h := &eventHarness{t: t, m: m, room: room}
	svc := eventLiveService(t, h)
	m.bindPlatformReferences(svc)
	cfg := gitlab.NotificationConfig{Enabled: true, PlatformID: c.ID, Target: target, IntervalSeconds: 60,
		BackfillStart: time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second),
		Events:        gitlab.EventSettings{Enabled: true, Project: input.ProjectPath},
		Todos:         gitlab.TodoSettings{Enabled: true}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if _, err := m.configureGitLabNotifications(ctx, room.RoomIdentity, 0, cfg); err != nil {
		t.Fatal("normal configuration", err)
	}
	m.notifications.Limits.SlicePages = 4
	evidence := map[string]any{}
	for _, source := range []notification.SourceID{gitlab.EventsSourceID, gitlab.TodosSourceID} {
		current, err := m.notifications.CurrentLocked(room.RoomIdentity, source, c.ID, target)
		if err != nil || current == nil {
			t.Fatal("source unavailable", source, err)
		}
		runner := m.notifications.NewRunnerLocked(current.Metadata().Key)
		for range 4 {
			_, stop := runner.Step(ctx)
			if stop || ctx.Err() != nil {
				t.Fatal("bounded source stopped", source)
			}
		}
		current, _ = m.notifications.StateLocked(current.Metadata().Key)
		accepted := current.Metadata().Pending
		for range min(accepted, 3) {
			if err := m.notifications.Deliver(ctx, current.Metadata().Key, current.Metadata().Version); err != nil {
				t.Fatal("real source delivery", source, err)
			}
		}
		current, _ = m.notifications.StateLocked(current.Metadata().Key)
		switch s := current.(type) {
		case gitlab.EventsState:
			if s.ErrorCode != "" {
				t.Fatal("events query", s.ErrorCode)
			}
			evidence[source.Channel] = map[string]any{"accepted": accepted, "pending_after": len(s.Inbox), "metrics": s.Metrics}
		case gitlab.TodosState:
			if s.ErrorCode != "" && s.ErrorCode != "scan_incomplete" {
				t.Fatal("todos query", s.ErrorCode)
			}
			evidence[source.Channel] = map[string]any{"accepted": accepted, "pending_after": len(s.Inbox), "metrics": s.Metrics}
		}
	}
	report["status"], report["sources"] = "passed", evidence
}

func TestNotificationOwnersRealLarkReadOnly(t *testing.T) {
	if os.Getenv("TEAMX_NOTIFICATION_LARK_REAL") != "1" {
		t.Skip("explicit private Lark sample required")
	}
	input := realLarkInput(t)
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "external_writes": 0, "transport": "real Lark search; isolated notification and member owners"}
	defer imageRealReport(t, out, report)
	m := testManager(t, filepath.Join(out, "rooms"), managerTestProfiles(t))
	m.config.unifiedInputs = true
	room := managerCreate(t, m, "notification-owner-lark-real")
	target := fixtureInboxTarget(t, m, room.RoomIdentity)
	svc := eventLiveService(t, &eventHarness{t: t, m: m, room: room})
	m.bindPlatformReferences(svc)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: target.AgentID, MembershipID: target.MembershipID}
	c := realLarkPlatform(t, m, input)
	state := collectRealLarkSearch(t, m, svc, id, c, input, false)
	report["status"], report["pending"], report["metrics"] = "passed", len(state.Inbox), state.Metrics
}
