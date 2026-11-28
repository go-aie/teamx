//go:build e2e

package room

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// Opt-in read-only platform, real Chromium and durable service restart acceptance.
// No Issue/Review is created, and the formal service directory is never modified.
func TestScionNotificationRealReadOnly(t *testing.T) {
	if os.Getenv("TEAMX_SCION_NOTIFICATION_REAL") != "1" {
		t.Skip("explicit read-only native platform and browser acceptance")
	}
	out := os.Getenv("TEAMX_SCION_NOTIFICATION_OUTPUT")
	if !filepath.IsAbs(out) {
		t.Fatal("fresh absolute output required")
	}
	if e := os.Mkdir(out, 0700); e != nil {
		t.Fatal(e)
	}
	credentials, e := os.ReadFile(os.Getenv("TEAMX_SCION_TEST_CREDENTIALS"))
	if e != nil {
		t.Fatal("credential snapshot unavailable")
	}
	bin := os.Getenv("TEAMX_SCION_BIN")
	if !filepath.IsAbs(bin) {
		t.Fatal("absolute native CLI required")
	}
	profiles := managerTestProfiles(t)
	root := filepath.Join(out, "rooms")
	setup := func() *roomManager {
		m := testManager(t, root, profiles)
		m.config.unifiedInputs = true
		m.config.endpoint = "http://127.0.0.1:1"
		m.config.cliPath = os.Args[0]
		m.factory = controlledActivationFactory(nil, nil)
		return m
	}
	m := setup()
	room := managerCreate(t, m, "scion-issue-read-only")
	svc := openRoomOK(t, m, room, "before")
	p := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), Executable: bin, CLIVersion: scion.CLIVersion}
	profileDir := filepath.Join(root, "scion-profiles", p.Profile)
	if os.MkdirAll(profileDir, 0700) != nil || os.WriteFile(filepath.Join(profileDir, "credentials.json"), credentials, 0600) != nil {
		t.Fatal("profile copy failed")
	}
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "Scion", Provider: "scion", URL: "https://scion.opencsg.com", CLIProfile: p})
	c = verifyScion(t, m, c)
	snap, e := svc.store.LoadRoom()
	if e != nil {
		t.Fatal(e)
	}
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: snap.Members[0].AgentID, MembershipID: snap.Members[0].MembershipID}
	runBrowser := func(mode string) {
		server := browserTestServer(t, m)
		defer server.Close()
		input, _ := json.Marshal(map[string]any{"room_id": room.RoomID, "room_instance": room.RoomInstance, "platform_id": c.ID, "mode": mode})
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "node", "testdata/scion_notifications_browser.cjs", server.URL, string(input), out)
		b, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatal("browser", e, string(b))
		}
		t.Log(string(b))
	}
	runBrowser("initial")
	m.mu.Lock()
	state, e := m.notifications.CurrentLocked(room.RoomIdentity, scion.WorkitemsSourceID, c.ID, inboxTarget(id))
	if e != nil || state == nil {
		m.mu.Unlock()
		t.Fatal("UI did not save subscription", e)
	}
	s := state.(scion.State)
	m.mu.Unlock()
	if e = m.notifications.Start(); e != nil {
		t.Fatal(e)
	}
	waitCollected := func(after time.Time) {
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			m.mu.Lock()
			state, _ = m.notifications.StateLocked(s.Metadata().Key)
			m.mu.Unlock()
			s = state.(scion.State)
			if s.Discovery.LastFull.After(after) && len(s.Inbox) == 0 && len(s.Observed) > 0 {
				return
			}
			if s.QueryPaused() {
				t.Fatal("collection paused", s.ErrorCode)
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("collection/delivery did not complete", s.Status, s.ErrorCode)
	}
	waitCollected(time.Time{})
	accepted := len(s.Observed)
	preview := inboxRoutingPreview(t, svc, id)
	if preview.Status.RetainedEntries != accepted {
		t.Fatal("delivery count", preview.Status.RetainedEntries, accepted)
	}
	used := readScionInput(t, svc, id)
	if used != 1 {
		t.Fatal("formal read did not settle", used)
	}
	if len(snap.Members) > 1 {
		other := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: snap.Members[1].AgentID, MembershipID: snap.Members[1].MembershipID}
		if page := inboxRoutingPreview(t, svc, other); len(page.Entries) != 0 {
			t.Fatal("member isolation failed")
		}
	}
	key := s.Metadata().Key
	if e = m.close(); e != nil {
		t.Fatal(e)
	}
	m = setup()
	svc = openRoomOK(t, m, room, "after")
	m.mu.Lock()
	state, ok := m.notifications.StateLocked(key)
	if !ok {
		m.mu.Unlock()
		t.Fatal("restart source missing")
	}
	s = state.(scion.State)
	m.mu.Unlock()
	// Explicit sync keeps the existing subscription and frozen beginning.
	if e = m.syncScionSubscription(t.Context(), key, s.SubscriptionVersion, s.Config.Target); e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	state, _ = m.notifications.StateLocked(key)
	s = state.(scion.State)
	m.mu.Unlock()
	if e = m.notifications.Start(); e != nil {
		t.Fatal(e)
	}
	waitCollected(s.Discovery.LastFull)
	after := inboxRoutingPreview(t, svc, id)
	if after.Status.RetainedEntries != preview.Status.RetainedEntries {
		t.Fatal("restart replayed native creations", after.Status.RetainedEntries, preview.Status.RetainedEntries)
	}
	runBrowser("restored")

	if e = m.close(); e != nil {
		t.Fatal(e)
	}
	recovery := scionRecoveryInput{Room: room, Member: id, Key: key, Count: accepted}
	data, _ := json.Marshal(recovery)
	if e = os.WriteFile(filepath.Join(out, "recovery-input.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestScionNotificationRealRecoveryProcess$", "-test.v", "-test.timeout=1m")
	child.Env = append(os.Environ(), "TEAMX_SCION_RECOVERY_PROCESS_ROOT="+out)
	if b, e := child.CombinedOutput(); e != nil {
		t.Fatal("recovery subprocess", e, string(b))
	} else {
		t.Log(string(b))
	}
	report := map[string]any{"status": "passed", "scope": "open Issue creation only", "native_cli": scion.CLIVersion, "accepted": accepted, "formal_read_confirmed": used, "restarted_delivery_count": after.Status.RetainedEntries, "external_business_writes": 0, "production_subscription": false, "real_model": false, "background_worker": true, "cross_process_restart": true}
	data, _ = json.MarshalIndent(report, "", "  ")
	if e = os.WriteFile(filepath.Join(out, "report.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("read-only acceptance: %s", out)
}

type scionRecoveryInput struct {
	Room   roomSummary
	Member MemberIdentity
	Key    notification.Key
	Count  int
}

func TestScionNotificationRealRecoveryProcess(t *testing.T) {
	out := os.Getenv("TEAMX_SCION_RECOVERY_PROCESS_ROOT")
	if out == "" {
		t.Skip("only the isolated read-only acceptance subprocess")
	}
	var input scionRecoveryInput
	b, e := os.ReadFile(filepath.Join(out, "recovery-input.json"))
	if e != nil || json.Unmarshal(b, &input) != nil {
		t.Fatal("recovery input unavailable")
	}
	m := testManager(t, filepath.Join(out, "rooms"), managerTestProfiles(t))
	m.config.unifiedInputs = true
	m.config.endpoint = "http://127.0.0.1:1"
	m.config.cliPath = os.Args[0]
	m.factory = controlledActivationFactory(nil, nil)
	svc := openRoomOK(t, m, input.Room, "subprocess")
	m.mu.Lock()
	state, ok := m.notifications.StateLocked(input.Key)
	m.mu.Unlock()
	if !ok {
		t.Fatal("subprocess source missing")
	}
	s := state.(scion.State)
	before := s.Discovery.LastFull
	if e = m.syncScionSubscription(t.Context(), input.Key, s.SubscriptionVersion, s.Config.Target); e != nil {
		t.Fatal(e)
	}
	if e = m.notifications.Start(); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		state, _ = m.notifications.StateLocked(input.Key)
		m.mu.Unlock()
		s = state.(scion.State)
		if s.Discovery.LastFull.After(before) && len(s.Inbox) == 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !s.Discovery.LastFull.After(before) || len(s.Observed) != input.Count {
		t.Fatal("subprocess native rediscovery changed identity or did not finish")
	}
	page := inboxRoutingPreview(t, svc, input.Member)
	if page.Status.RetainedEntries != input.Count {
		t.Fatal("subprocess duplicated deliveries", page.Status.RetainedEntries)
	}
	storage, e := svc.inputStorage(input.Member)
	if e != nil {
		t.Fatal(e)
	}
	checkpoint, e := storage.repository.State(inputstream.Execution{Generation: "scion-read", Turn: 1})
	if e != nil || checkpoint.Used != 1 || len(checkpoint.Attempts) != 1 || checkpoint.Attempts[0].Phase != "settled" {
		t.Fatal("formal confirmation did not survive process restart", e)
	}
	report, _ := json.MarshalIndent(map[string]any{"status": "passed", "separate_process": true, "retained": input.Count, "formal_read_confirmed": checkpoint.Used, "native_full_after_restart": s.Discovery.LastFull}, "", "  ")
	if e = os.WriteFile(filepath.Join(out, "recovery-report.json"), report, 0600); e != nil {
		t.Fatal(e)
	}
}
