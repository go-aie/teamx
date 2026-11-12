//go:build integration || e2e

package room

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLarkAuthorizationPreservesNotificationProgressAndClosedChoices(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "enabled", false: "disabled"}[enabled], func(t *testing.T) {
			h := newLarkHarness(t)
			h.start()
			if !enabled {
				cfg := h.state().Config
				cfg.Enabled = false
				h.configure(cfg)
			}
			before := h.state()
			bindings := bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
			script := strings.Replace(larkFixtureScript(), "import sys,json,os", "import sys,json,os,time", 1)
			script = strings.Replace(script, "if a==['--version']:", roomAuthCommands+"\nif a==['--version']:", 1)
			if err := os.WriteFile(h.c.CLIProfile.Executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			source, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ClientMessageID: "authorize-existing", Text: "补全原飞书账号的授权，保留我的通知和工具选择"})
			if err != nil {
				t.Fatal(err)
			}
			id, _ := newOpaqueID()
			req := platformConnectRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), OperationID: id, SourceSeq: source.Seq, Input: &platformConnectInput{Intent: "authorize", Provider: "lark", PlatformID: h.c.ID, PlatformVersion: h.c.Version}}
			connectHTTP(t, h.m, "authorize", req)
			waitConnectionObservation(t, h.m, req, func(op platformConnectOperation) bool { return op.Interaction.URL != "" })
			if err := os.WriteFile(filepath.Join(h.dir, "auth-release"), []byte("yes"), 0600); err != nil {
				t.Fatal(err)
			}
			if done := waitConnectionResult(t, h.m, req); done.Status != "completed" {
				t.Fatal(done)
			}
			after := h.state()
			// Fingerprints bind the new credential; business branch progress is unchanged.
			oldSearch, newSearch := before.Clone().Search, after.Clone().Search
			for i := range oldSearch.Branches {
				oldSearch.Branches[i].Fingerprint = ""
				oldSearch.Branches[i].RetryAt = time.Time{}
			}
			for i := range newSearch.Branches {
				newSearch.Branches[i].Fingerprint = ""
				newSearch.Branches[i].RetryAt = time.Time{}
			}
			if !reflect.DeepEqual(before.Config, after.Config) || before.SubscriptionID != after.SubscriptionID || !before.Baseline.Equal(after.Baseline) || !before.LastFull.Equal(after.LastFull) || !reflect.DeepEqual(oldSearch, newSearch) || !reflect.DeepEqual(before.Observed, after.Observed) || !reflect.DeepEqual(before.Accepted, after.Accepted) || !reflect.DeepEqual(before.Inbox, after.Inbox) {
				t.Fatal("OAuth changed reception choices, branch progress or backlog")
			}
			if enabled && (after.QueryPaused() || after.Binding.CredentialVersion != before.Binding.CredentialVersion+1) {
				t.Fatal("same-account subscription did not resume")
			}
			if !enabled && after.Config.Enabled {
				t.Fatal("OAuth reopened disabled subscription")
			}
			h.m.mu.Lock()
			current, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
			h.m.mu.Unlock()
			if err != nil || !reflect.DeepEqual(bindings, current) {
				t.Fatal("OAuth expanded or repaired member intent/evidence", err)
			}
		})
	}
}
