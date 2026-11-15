//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestNotificationReplacementRechecksExactObservationAfterRemotePreparation(t *testing.T) {
	for _, change := range []string{"target", "progress", "sibling", "replacement"} {
		t.Run(change, func(t *testing.T) {
			parallelFixtureTest(t)
			h, req := platformMemberHarness(t)
			cfg := h.m.defaultLarkConfig(h.c.ID)
			cfg.Target = platformMemberTarget(req.member())
			cfg.Enabled, cfg.Private.Enabled = false, false
			cfg.Groups.Scope, cfg.Groups.ChatIDs = "specified", []string{"oc_group_one"}
			initial, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
			if err != nil {
				t.Fatal(err)
			}
			req.Input.AddChatIDs = nil
			req.Input.NotificationVersion = initial.SubscriptionVersion
			req.Input.Groups = &platformMemberGroupPatch{ChatIDs: memberPatchIDs("oc_group_one")}
			bin := filepath.Join(h.dir, "lark-cli")
			script, _ := os.ReadFile(bin)
			block := "if path.endswith('/oc_group_one'):\n import time\n open(base+'/observed-read','w').write('ready')\n while not os.path.exists(base+'/release-observation'):time.sleep(0.01)\n"
			script = []byte(strings.Replace(string(script), "path=a[2]\n", "path=a[2]\n"+block, 1))
			if err := os.WriteFile(bin, script, 0700); err != nil {
				t.Fatal(err)
			}
			release := filepath.Join(h.dir, "release-observation")
			defer os.WriteFile(release, []byte("ok"), 0600)
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() { response <- managerHTTP(h.m, "/v1/platforms/member/notifications", req) }()
			deadline := time.Now().Add(8 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(h.dir, "observed-read")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("remote preparation not reached")
				}
				time.Sleep(10 * time.Millisecond)
			}
			updated := initial
			switch change {
			case "target":
				cfg.Groups.ChatIDs = []string{"oc_group_one", "oc_group_two"}
				// Human full settings entry appends B while the Agent is preparing its
				// older complete replacement containing only A.
				body := map[string]any{"service_epoch": h.m.epoch, "room_id": req.RoomID, "room_instance": req.RoomInstance, "caller": humanCaller, "provider": "lark", "channel": "messages", "subscription_id": initial.SubscriptionID, "subscription_version": initial.SubscriptionVersion, "config": cfg}
				w := managerHTTP(h.m, "/v1/platform/notifications/configure", body)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				// A second stale full human form must be rejected as well.
				body["config"] = initial.Config
				w = managerHTTP(h.m, "/v1/platform/notifications/configure", body)
				if w.Code != 409 {
					t.Fatal("old full form overwrote B", w.Code, w.Body.String())
				}
			case "progress":
				// Ordinary collector bookkeeping changes no subscription version.
				h.m.mu.Lock()
				current, e := h.m.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, cfg.Target)
				if e == nil {
					updated = *current
					updated.LastCheck = time.Now().UTC()
					updated.Metrics.Calls++
					e = h.m.notifications.SaveLocked(updated)
				}
				h.m.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
			case "sibling":
				other := cfg
				other.Target = inboxTarget(notificationMemberIdentity(req.RoomIdentity, h.members[1]))
				other.Groups.ChatIDs = []string{"oc_group_two"}
				if _, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, other); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				// Exercise same-version ABA: retire old subscription and replace its ID.
				h.m.mu.Lock()
				retired, e := lark.Retire(initial)
				if e == nil {
					e = h.m.notifications.SaveLocked(retired)
				}
				h.m.mu.Unlock()
				if e != nil {
					t.Fatal(e)
				}
				next, e := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
				if e != nil || next.SubscriptionVersion != initial.SubscriptionVersion || next.SubscriptionID == initial.SubscriptionID {
					t.Fatal(next, e)
				}
			}
			if err := os.WriteFile(release, []byte("ok"), 0600); err != nil {
				t.Fatal(err)
			}
			var w *httptest.ResponseRecorder
			select {
			case w = <-response:
			case <-time.After(8 * time.Second):
				t.Fatal("preparation did not settle")
			}
			if change == "target" || change == "replacement" {
				var failure apiError
				_ = json.Unmarshal(w.Body.Bytes(), &failure)
				want := "notification_version"
				if change == "replacement" {
					want = "subscription_id"
				}
				if w.Code != 409 || failure.Field != want || failure.Outcome != "not_applied" {
					t.Fatal(w.Code, w.Body.String())
				}
				if _, err := os.Stat(h.m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
					t.Fatal("rejected preparation reserved ID", err)
				}
			} else if w.Code != 200 {
				t.Fatal("unrelated progress rejected", w.Code, w.Body.String())
			}
			h.m.mu.Lock()
			current, err := h.m.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, cfg.Target)
			h.m.mu.Unlock()
			if err != nil || current == nil {
				t.Fatal(current, err)
			}
			if change == "target" && !reflect.DeepEqual(current.Config.Groups.ChatIDs, []string{"oc_group_one", "oc_group_two"}) {
				t.Fatal("lost B", current.Config)
			}
			if change == "progress" && (!current.LastCheck.Equal(updated.LastCheck) || current.Metrics.Calls != updated.Metrics.Calls || current.SubscriptionVersion != initial.SubscriptionVersion) {
				t.Fatal("lost ordinary progress", current, updated)
			}
		})
	}
}
