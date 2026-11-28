//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/scion"
)

const scionNotificationFixture = `#!/usr/bin/env -S python3 -S
import os,sys,json,time
args=sys.argv[1:]
base=os.path.dirname(os.path.realpath(sys.argv[0]))
if args==['--version']:
 print('scn 0.1.13');sys.exit()
assert 'SCION_TOKEN' not in os.environ
path=os.environ['SCION_CREDENTIALS_FILE']
origin=os.environ['SCION_ENDPOINT']
subject=json.load(open(path))[origin]
if args[:2]==['--json','whoami']:
 print(json.dumps({'schema':'scion.cli.whoami/v1','endpoint':origin,'subject':subject}));sys.exit()
mode=open(base+'/mode').read()
if args[:3]==['--json','repo','info']:
 print(json.dumps({'schema':'scion.cli.repo/v1','name':args[3],'created_at_ms':100}));sys.exit()
assert args[:8]==['--json','--repo','Owner/repo','issue','ls','--state','open','--limit']
open(base+'/executed','a').write(path+'\n')
if mode=='block':
 open(base+'/started','w').write(path)
 time.sleep(60)
if mode=='denied':sys.exit(5)
now=int(time.time()*1000)-1000
issue={'schema':'scion.cli.issue/v1','repo':'Owner/repo','number':1,'title':'Issue notification acceptance','body':'read only evidence','author':subject,'state':'open','created_at_ms':now,'updated_at_ms':now}
print(json.dumps({'schema':'scion.cli.issue-list/v1','repo':'Owner/repo','issues':[issue],'truncated':False}))
`

func scionNotificationHarness(t *testing.T) (*roomManager, roomSummary, *service, managedPlatform, MemberIdentity, string) {
	t.Helper()
	m, room, svc, _, members := cliToolFixtureWithInputLog(t, "exit 0", true)
	dir := t.TempDir()
	bin := filepath.Join(dir, "scn")
	if e := os.WriteFile(filepath.Join(dir, "mode"), []byte("ok"), 0600); e != nil {
		t.Fatal(e)
	}
	p := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), CLIVersion: scion.CLIVersion, Executable: bin}
	scionWriteCredential(t, m, p.Profile, "alice")
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "Scion", Provider: "scion", URL: "https://scion.invalid", CLIProfile: p})
	if e := os.WriteFile(filepath.Join(dir, "scn"), []byte(scionNotificationFixture), 0700); e != nil {
		t.Fatal(e)
	}
	c = verifyScion(t, m, c)
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: members[0].AgentID, MembershipID: members[0].MembershipID}
	return m, room, svc, c, id, dir
}
func scionHTTPConfig(t *testing.T, m *roomManager, id MemberIdentity, cfg scion.Config, current *scion.State) scion.State {
	t.Helper()
	req := map[string]any{"service_epoch": m.epoch, "room_id": id.RoomID, "room_instance": id.RoomInstance, "caller": humanCaller, "provider": "scion", "channel": "workitems", "subscription_id": "", "subscription_version": 0, "config": cfg}
	if current != nil {
		req["subscription_id"] = current.SubscriptionID
		req["subscription_version"] = current.SubscriptionVersion
	}
	w := managerHTTP(m, "/v1/platform/notifications/configure", req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var view notificationView
	if e := json.Unmarshal(w.Body.Bytes(), &view); e != nil {
		t.Fatal(e)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state, ok := m.notifications.StateLocked(notification.Key{Owner: id.RoomIdentity, Source: scion.WorkitemsSourceID, SubscriptionID: view.SubscriptionID})
	if !ok {
		t.Fatal("missing state")
	}
	return state.(scion.State)
}
func readScionInput(t *testing.T, svc *service, id MemberIdentity) int {
	t.Helper()
	storage, e := svc.inputStorage(id)
	if e != nil {
		t.Fatal(e)
	}
	agent := AgentSnapshot{Version: formatVersion, MemberIdentity: id, InputStateRef: "input-state.json", TurnNumber: 1, Turn: &TurnRecord{Number: 1, Status: "running"}}
	if e = svc.store.SaveAgent(agent); e != nil {
		t.Fatal(e)
	}
	reader, e := newInputSession(context.Background(), svc, storage, "scion-read", 1, false, func(ctx context.Context) error { return ctx.Err() })
	if e != nil {
		t.Fatal(e)
	}
	catalog, e := reader.catalog(context.Background())
	if e != nil {
		t.Fatal("catalog", e)
	}
	directory, _, e := catalog.Directory()
	if e != nil {
		t.Fatal("input directory", e)
	}
	stream := ""
	for _, v := range directory.Streams {
		if strings.Contains(v.Label, "Issue") {
			stream = v.Ref
			break
		}
	}
	if stream == "" {
		t.Fatal("Issue stream missing", directory)
	}
	q := inputstream.ReadRequest{Turn: 1, Stream: stream, ID: "issue-read"}
	args, _ := json.Marshal(q)
	var out bytes.Buffer
	if e = reader.writeCLI(context.Background(), args, &out, func() error { return nil }); e != nil {
		t.Fatal("formal read", e)
	}
	var view platformInputView
	if json.Unmarshal(out.Bytes(), &view) != nil || len(view.Entries) == 0 {
		t.Fatal("empty formal input", out.String())
	}
	if e = reader.writeCLI(context.Background(), args, &out, func() error { return nil }); e != nil {
		t.Fatal("lost response read replay", e)
	}
	checkpoint, e := storage.repository.State(reader.execution)
	if e != nil || checkpoint.Used == 0 || len(checkpoint.Attempts) != 1 || checkpoint.Attempts[0].Phase != "settled" {
		t.Fatal("unconfirmed input", e, checkpoint)
	}
	return checkpoint.Used
}
func TestScionNotificationManagementDeliveryAndRestart(t *testing.T) {
	m, _, svc, c, id, _ := scionNotificationHarness(t)
	cfg := scion.DefaultConfig(c.ID)
	cfg.Repository = "Owner/repo"
	cfg.BackfillSeconds = 86400
	cfg.Target = inboxTarget(id)
	s := scionHTTPConfig(t, m, id, cfg, nil)
	m.mu.Lock()
	r := m.notifications.NewRunnerLocked(s.Metadata().Key)
	m.mu.Unlock()
	for range 4 {
		r.Step(context.Background())
	}
	m.mu.Lock()
	saved, _ := m.notifications.StateLocked(s.Metadata().Key)
	m.mu.Unlock()
	s = saved.(scion.State)
	if len(s.Inbox) != 1 || s.Discovery.LastFull.IsZero() {
		t.Fatal("discovery missing", s.Status, s.ErrorCode)
	}
	if e := m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion); e != nil {
		t.Fatal(e)
	}
	if p := inboxRoutingPreview(t, svc, id); len(p.Entries) != 1 {
		t.Fatal("input delivery", p)
	}
	for _, item := range s.PendingItems() {
		if e := m.commitPlatformNotification(context.Background(), s.Metadata(), item); e != nil {
			t.Fatal("lost response replay", e)
		}
	}
	if p := inboxRoutingPreview(t, svc, id); len(p.Entries) != 1 {
		t.Fatal("delivery replay duplicated", p)
	}
	// The source codec and store reload independently; replay the same native Issue.
	m.mu.Lock()
	saved, _ = m.notifications.StateLocked(s.Metadata().Key)
	m.mu.Unlock()
	s = saved.(scion.State)
	cfg = s.Config
	cfg.Enabled = false
	paused := scionHTTPConfig(t, m, id, cfg, &s)
	if len(paused.Observed) != 1 || paused.SubscriptionID != s.SubscriptionID {
		t.Fatal("pause lost progress")
	}
	raw, _ := json.Marshal(paused)
	decoded, e := (&scion.Source{}).Decode(raw, id.RoomIdentity)
	if e != nil {
		t.Fatal("restart decode", e)
	}
	paused = decoded.(scion.State)
	cfg.Enabled = true
	resumed := scionHTTPConfig(t, m, id, cfg, &paused)
	m.mu.Lock()
	r = m.notifications.NewRunnerLocked(resumed.Metadata().Key)
	m.mu.Unlock()
	for range 3 {
		r.Step(context.Background())
	}
	if e = m.notifications.Deliver(context.Background(), s.Metadata().Key, resumed.SubscriptionVersion); e != nil {
		t.Fatal(e)
	}
	if p := inboxRoutingPreview(t, svc, id); len(p.Entries) != 1 {
		t.Fatal("restart duplicated", p)
	}
	if used := readScionInput(t, svc, id); used != 1 {
		t.Fatal("formal consumption", used)
	}
	// Notifications alone never create a tool grant.
	m.mu.Lock()
	bindings, e := m.readToolBindingsLocked(id.RoomIdentity)
	m.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	for _, b := range bindings.Bindings {
		if b.PlatformID == c.ID {
			t.Fatal("notification granted tool")
		}
	}
}
func TestScionNotificationRejectsSharedTargetAndRangeChanges(t *testing.T) {
	m, _, _, c, id, _ := scionNotificationHarness(t)
	cfg := scion.DefaultConfig(c.ID)
	cfg.Repository = "Owner/repo"
	cfg.Target = notification.Target{Mode: "shared_inbox", InboxID: "shared"}
	if _, e := m.configureScionSubscription(context.Background(), notification.Key{Owner: id.RoomIdentity, Source: scion.WorkitemsSourceID}, 0, cfg); e == nil {
		t.Fatal("shared destination accepted")
	}
	cfg.Target = inboxTarget(id)
	s := scionHTTPConfig(t, m, id, cfg, nil)
	cfg.Repository = "Owner/other"
	if _, e := m.configureScionSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); e == nil {
		t.Fatal("range changed")
	}
	cfg = s.Config
	cfg.ObjectTypes = []string{"review"}
	if _, e := m.configureScionSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); e == nil {
		t.Fatal("Review accepted before supported")
	}
}
func TestScionNotificationCredentialRecovery(t *testing.T) {
	m, _, _, c, id, _ := scionNotificationHarness(t)
	cfg := scion.DefaultConfig(c.ID)
	cfg.Repository = "Owner/repo"
	cfg.Target = inboxTarget(id)
	s := scionHTTPConfig(t, m, id, cfg, nil)
	m.mu.Lock()
	e := m.notifications.InvalidatePlatformLocked(c.ID)
	m.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	c = verifyScion(t, m, c)
	m.mu.Lock()
	state, _ := m.notifications.StateLocked(s.Metadata().Key)
	m.mu.Unlock()
	next := state.(scion.State)
	if next.QueryPaused() || next.SubscriptionID != s.SubscriptionID || !next.Baseline.Equal(s.Baseline) {
		t.Fatal("same account recovery lost progress")
	}
	scionWriteCredential(t, m, c.CLIProfile.Profile, "bob")
	_ = verifyScion(t, m, c)
	m.mu.Lock()
	state, _ = m.notifications.StateLocked(s.Metadata().Key)
	m.mu.Unlock()
	if state.(scion.State).Binding.Account.ID.Value != "alice" {
		t.Fatal("changed account inherited")
	}
}
func TestScionNotificationCancelSettlesPrivateCredentials(t *testing.T) {
	for _, action := range []string{"cancel", "pause", "logout", "revoke"} {
		t.Run(action, func(t *testing.T) {
			m, room, _, c, id, dir := scionNotificationHarness(t)
			cfg := scion.DefaultConfig(c.ID)
			cfg.Repository = "Owner/repo"
			cfg.Target = inboxTarget(id)
			s := scionHTTPConfig(t, m, id, cfg, nil)
			if e := os.WriteFile(filepath.Join(dir, "mode"), []byte("block"), 0600); e != nil {
				t.Fatal(e)
			}
			m.mu.Lock()
			r := m.notifications.NewRunnerLocked(s.Metadata().Key)
			m.mu.Unlock()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r.Step(ctx)
			done := make(chan struct{})
			go func() { defer close(done); r.Step(ctx) }()
			deadline := time.Now().Add(5 * time.Second)
			var credential string
			for time.Now().Before(deadline) {
				b, e := os.ReadFile(filepath.Join(dir, "started"))
				if e == nil {
					credential = strings.TrimSpace(string(b))
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if credential == "" {
				t.Fatal("query did not start")
			}
			switch action {
			case "cancel":
				cancel()
			case "pause":
				cfg.Enabled = false
				if _, e := m.configureScionSubscription(context.Background(), s.Metadata().Key, s.SubscriptionVersion, cfg); e != nil {
					t.Fatal(e)
				}
			case "logout":
				w := managerHTTP(m, "/v1/platforms/scion/logout", scionAuthBody(m, map[string]any{"url": c.URL, "cli_profile": c.CLIProfile, "affected": []scionAuthPlatform{{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{room.RoomIdentity}}}}))
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
			case "revoke":
				profile := *c.CLIProfile
				profile.Profile = strings.Repeat("e", 32)
				scionWriteCredential(t, m, profile.Profile, "alice")
				if _, e := m.updatePlatform(managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version, Name: c.Name, Provider: c.Provider, URL: c.URL, CLIProfile: &profile, ReferencedRooms: []RoomIdentity{room.RoomIdentity}}, false); e != nil {
					t.Fatal(e)
				}
			}

			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("query did not settle")
			}
			if _, e := os.Stat(credential); !os.IsNotExist(e) {
				t.Fatal("private credential remains", e)
			}
			m.mu.Lock()
			calls := m.platformCalls.Len()
			m.mu.Unlock()
			if calls != 0 {
				t.Fatal("pending calls", calls)
			}
		})
	}
}
