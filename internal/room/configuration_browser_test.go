//go:build e2e

package room

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Real Chromium, production HTTP/UI and durable stores; isolated providers accept
// reads only. Each viewport gets a fresh Room, account and member configuration.
func TestConfigurationObservationBrowser(t *testing.T) {
	if os.Getenv("TEAMX_CONFIGURATION_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	for _, viewport := range []string{"desktop", "mobile"} {
		t.Run(viewport, func(t *testing.T) {
			t.Setenv("USER", humanCaller.ID)
			h := larkToolConfigurationHarness(t)
			g := configurationGitLab(t, h)
			id := notificationMemberIdentity(h.room.RoomIdentity, h.members[0])
			cfg := h.m.defaultLarkConfig(h.c.ID)
			cfg.Target = inboxTarget(id)
			cfg.Enabled, cfg.Private.Enabled = true, false
			cfg.Groups = lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_group_one"}, MessageMode: "mentions", AtMe: true}
			initial, err := h.m.configureLarkSubscription(t.Context(), notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
			if err != nil {
				t.Fatal(err)
			}
			// Show both existing subscriptions in the member's platform list;
			// neither tool has been bound, so both tool observations remain zero.
			if _, err := h.m.configureGitLabNotifications(t.Context(), id.RoomIdentity, 0, gitlab.NotificationConfig{PlatformID: g.ID, Target: cfg.Target}); err != nil {
				t.Fatal(err)
			}
			for _, rule := range []PolicyChange{
				{ID: "style", Status: "active", Text: "简短中文", SourceText: "以后简短中文"},
				{ID: "order", Status: "active", Text: "先给结论", SourceText: "以后先给结论"},
			} {
				if _, err := h.svc.store.changePolicy(id, humanCaller, rule); err != nil {
					t.Fatal(err)
				}
			}
			server := browserTestServer(t, h.m)
			defer server.Close()
			h.m.config.endpoint = server.URL
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, "node", "testdata/configuration_observation_browser.cjs", server.URL, h.room.RoomInstance, h.c.ID, g.ID, h.members[0].AgentID, viewport)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("browser: %v\n%s", err, out)
			}
			t.Log(string(out))
			f := configurationBindings(t, h)
			if len(f.Bindings) != 2 || f.Revision != 2 {
				t.Fatal("tool saves lost or repeated", f)
			}
			h.m.mu.Lock()
			current, err := h.m.currentLarkForTargetLocked(id.RoomIdentity, h.c.ID, cfg.Target)
			h.m.mu.Unlock()
			if err != nil || current == nil || current.SubscriptionVersion != initial.SubscriptionVersion+1 || len(current.Config.Groups.ChatIDs) != 2 || current.Baseline != initial.Baseline {
				t.Fatal("notification conflict overwrote configuration/baseline", current, err)
			}
			policy, err := h.svc.store.readPolicy(id)
			if err != nil || policy.Revision != 5 || len(policy.History) != 5 || policy.Current[0].Version != 3 || policy.Current[0].Text != "另一入口保存的最新规则" || policy.Current[1].Version != 2 {
				t.Fatal("rule conflict lost or repeated writes", policy, err)
			}
			if root := os.Getenv("TEAMX_CONFIGURATION_ARTIFACTS"); root != "" {
				writeJSONFixture(t, filepath.Join(root, viewport+"-saved.json"), map[string]any{"tools": f, "notification": current, "policy": policy})
			}
		})
	}
}
