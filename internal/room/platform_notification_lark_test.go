package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const l2Fixture = `#!/usr/bin/env python3
import sys,json,os
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[3:]
if a==['--version']:print('lark-cli version 1.0.99');sys.exit()
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':'ou_reader'}}));sys.exit()
if a[:2]==['im','+chat-list']:
 data=json.load(open(base+'/directory.json'))
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
path=a[2]
if path=='/open-apis/authen/v1/user_info':data={'open_id':'ou_reader','tenant_key':'account_tenant','name':'测试用户'}
elif path=='/open-apis/contact/v3/users/basic_batch':
 assert a[1]=='POST' and json.loads(a[4])=={'user_id_type':'open_id'}
 data={'users':[{'user_id':id,'name':'测试发送者'} for id in json.loads(a[6])['user_ids']]}
elif path.startswith('/open-apis/im/v1/chats/'):data={'chat_mode':'p2p','name':'脱敏会话','tenant_key':'external_tenant'}
else:
 messages=json.load(open(base+'/messages.json'))
 if path.startswith('/open-apis/im/v1/messages/'):data={'items':[m for m in messages if m['message_id']==path.split('/')[-1]]}
 else:
  params=json.loads(a[4]);messages=[m for m in messages if m['chat_id']==params['container_id'] and int(params['start_time'])<=int(m['create_time'])//1000<=int(params['end_time'])]
  offset=int(params.get('page_token','0'));size=params['page_size'];page=messages[offset:offset+size];more=len(messages)>offset+size
  data={'items':page,'has_more':more,'page_token':str(offset+size) if more else ''}
print(json.dumps({'ok':True,'identity':'user','data':data}))
`

type larkHarness struct {
	t       *testing.T
	m       *roomManager
	room    roomSummary
	svc     *service
	members []MemberState
	c       platformConnection
	dir     string
	clock   *eventTestClock
}

func newLarkHarness(t *testing.T) *larkHarness {
	t.Helper()
	m, r, svc, _, members := cliToolFixture(t, "exit 0")
	m.toolCatalog = append(m.toolCatalog, tool.Definition{Name: "lark", Provider: "lark", InternalOnly: true})
	m.bindPlatformReferences(svc)
	dir := t.TempDir()
	bin := filepath.Join(dir, "lark-cli")
	if e := os.WriteFile(bin, []byte(l2Fixture), 0700); e != nil {
		t.Fatal(e)
	}
	c := platformTestSave(t, m, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("c", 32), Name: "飞书", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &platform.CLIProfile{Version: 1, Executable: bin, Profile: "fixture", Brand: "feishu", CLIVersion: lark.CLIVersion}})
	c, e := m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version})
	if e != nil || c.Validation.Status != "valid" {
		t.Fatal(e)
	}
	clock := &eventTestClock{now: time.Unix(99, 0).UTC()}
	m.notifications.Clock = clock.get
	h := &larkHarness{t, m, r, svc, members, c, dir, clock}
	h.messages(1)
	return h
}
func (h *larkHarness) messages(count int) {
	h.t.Helper()
	ms := []lark.Message{}
	for i := 0; i < count; i++ {
		m := lark.Message{ID: fmt.Sprintf("om_%03d", i), ChatID: "oc_chat", Type: "text", Created: "100000", Updated: "100000", Sender: lark.Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}}
		m.Body.Content = `{"text":"请确认这条脱敏测试消息"}`
		ms = append(ms, m)
	}
	b, _ := json.Marshal(ms)
	if e := os.WriteFile(filepath.Join(h.dir, "messages.json"), b, 0600); e != nil {
		h.t.Fatal(e)
	}
}
func (h *larkHarness) state() lark.State {
	h.t.Helper()
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	s, e := h.m.currentLarkLocked(h.room.RoomIdentity)
	if e != nil || s == nil {
		h.t.Fatal(e)
	}
	return *s
}
func (h *larkHarness) configure(c lark.Config) lark.State {
	h.t.Helper()
	h.m.mu.Lock()
	s, _ := h.m.currentLarkLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	key := notification.Key{Owner: h.room.RoomIdentity, Source: lark.MessagesSourceID}
	v := uint64(0)
	if s != nil {
		key = s.Metadata().Key
		v = s.SubscriptionVersion
	}
	next, e := h.m.configureLarkSubscription(context.Background(), key, v, c)
	if e != nil {
		h.t.Fatal(e)
	}
	return next
}
func (h *larkHarness) start() {
	h.t.Helper()
	member := h.members[0]
	h.configure(lark.Config{Enabled: true, ConnectionID: h.c.ID, Chats: []string{"oc_chat"}, Target: notification.Target{Mode: "inbox", AgentID: member.AgentID, MembershipID: member.MembershipID}, Private: true, GroupMentions: true})
	h.clock.add(3 * time.Second)
	s := h.state()
	h.m.mu.Lock()
	r := h.m.notifications.NewRunnerLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	r.Step(context.Background())
	r.Step(context.Background())
}
func (h *larkHarness) deliver() error {
	s := h.state()
	return h.m.notifications.Deliver(context.Background(), s.Metadata().Key, s.SubscriptionVersion)
}

func TestLarkVerificationResumesCredentialPausedSubscription(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	before := h.state()
	h.m.mu.Lock()
	err := h.m.notifications.InvalidateConnectionLocked(h.c.ID)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	paused := h.state()
	if paused.Status != "credentials_invalid" {
		t.Fatal("fixture did not pause subscription")
	}
	c, err := h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: h.c.ID, Version: h.c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal("verification failed", err)
	}
	resumed := h.state()
	if resumed.QueryPaused() || resumed.ErrorCode != "" || resumed.SubscriptionVersion != paused.SubscriptionVersion+1 || resumed.SubscriptionID != before.SubscriptionID || resumed.Binding != before.Binding || !reflect.DeepEqual(resumed.Config, before.Config) {
		t.Fatal("subscription did not resume under its original identity/configuration")
	}
	if !resumed.Baseline.Equal(before.Baseline) || !resumed.LastFull.Equal(before.LastFull) || !reflect.DeepEqual(resumed.Chats, before.Chats) || !reflect.DeepEqual(resumed.Inbox, before.Inbox) || !reflect.DeepEqual(resumed.Observed, before.Observed) || !reflect.DeepEqual(resumed.Accepted, before.Accepted) {
		t.Fatal("verification reset progress, deduplication or pending delivery")
	}
	// Re-verification of a running source is a no-op for subscription state.
	if _, err = h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.state(), resumed) {
		t.Fatal("verification restarted an already-running subscription")
	}
	if err := h.deliver(); err != nil {
		t.Fatal("resumed backlog was not delivered", err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if page := inboxRoutingPreview(t, h.svc, id); page.SnapshotSeq != 1 {
		t.Fatal("backlog delivery missing", page)
	}
	h.clock.add(time.Minute)
	h.messages(2)
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(resumed.Metadata().Key)
	h.m.mu.Unlock()
	if _, stop := runner.Step(context.Background()); stop {
		t.Fatal("resumed source did not query")
	}
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	if page := inboxRoutingPreview(t, h.svc, id); page.SnapshotSeq != 2 {
		t.Fatal("new message was not delivered exactly once", page)
	}
}

func TestLarkVerificationPreservesOtherPauses(t *testing.T) {
	for _, mode := range []string{"disabled", "retired", "capacity", "binding_changed", "different_account", "different_generation", "failed_verification", "other_connection"} {
		t.Run(mode, func(t *testing.T) {
			h := newLarkHarness(t)
			h.start()
			s := h.state()
			s.RuntimeState.InvalidateCredential(true)
			verify := h.c
			switch mode {
			case "disabled", "retired":
				s.Config.Enabled = false
				s.RuntimeState.Reset(false, false)
				s.Retired = mode == "retired"
			case "capacity", "binding_changed":
				s.Status, s.ErrorCode = mode, mode
			case "different_account", "failed_verification":
				script := strings.ReplaceAll(l2Fixture, "ou_reader", "ou_other")
				if mode == "failed_verification" {
					script = "#!/usr/bin/env python3\nimport sys\nsys.exit(1)\n"
				}
				if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			case "different_generation":
				s.Binding.Generation = strings.Repeat("f", 32)
			case "other_connection":
				verify = platformTestSave(t, h.m, platformConnectionRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "other", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
			}
			h.m.mu.Lock()
			err := h.m.notifications.SaveLocked(s)
			h.m.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: verify.ID, Version: verify.Version}); err != nil {
				t.Fatal(err)
			}
			h.m.mu.Lock()
			got, ok := h.m.notifications.StateLocked(s.Metadata().Key)
			h.m.mu.Unlock()
			if !ok || !reflect.DeepEqual(got, s) {
				t.Fatal("verification changed an ineligible subscription")
			}
		})
	}
}

func TestLarkVerificationRestartsPausedWorker(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	s := h.state()
	h.m.mu.Lock()
	err := h.m.notifications.InvalidateConnectionLocked(h.c.ID)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	h.messages(2)
	h.clock.add(time.Minute)
	if err := h.m.notifications.Start(); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	oldWorker := h.m.notifications.DoneLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	if oldWorker == nil {
		t.Fatal("paused query lost its worker")
	}
	if _, err := h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: h.c.ID, Version: h.c.Version}); err != nil {
		t.Fatal(err)
	}
	awaitEventSignal(t, oldWorker, "verification did not settle the paused worker")
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if page := inboxRoutingPreview(t, h.svc, id); page.SnapshotSeq == 2 && !h.state().QueryPaused() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("verification did not restart polling and deliver the new message")
}

func TestLarkNotificationsInboxReferencesAndRoomQuestion(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	if len(h.state().Inbox) != 1 {
		t.Fatal("missing source candidate")
	}
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	// Receipt persists while member scheduling remains paused.
	before, _ := h.svc.store.LoadRoom()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	after, _ := h.svc.store.LoadRoom()
	if after.LatestSeq != before.LatestSeq {
		t.Fatal("delivery enabled member or copied to Room")
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id)
	// Collection has already captured the message. Inbox reading and followup
	// creation need neither a member binding nor an available Lark executable.
	bin := filepath.Join(h.dir, "lark-cli")
	if err := os.Rename(bin, bin+".disabled"); err != nil {
		t.Fatal(err)
	}
	if e := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); e != nil {
		t.Fatal(e)
	}
	var inbox agentReferenceInboxPage
	if e := cli([]string{"inbox", "read"}, &inbox); e != nil || len(inbox.Followups) != 1 || len(inbox.Entries) != 1 {
		t.Fatal("inbox source resolution", e)
	}
	if inbox.Entries[0].Source.EvidenceSource != lark.NotificationEvidence || !strings.Contains(inbox.Entries[0].Item.Text, "脱敏测试消息") {
		t.Fatal("lost captured body or evidence provenance", inbox)
	}
	object := inbox.Followups[0].ObjectRef
	unit := inbox.Followups[0].Ref
	u, e := h.svc.store.readFollowup(id, object, unit)
	if e != nil || len(u.Actions) != 0 {
		t.Fatal(e)
	}
	var content larkTargetPage
	if e = cli([]string{"lark", "read", "--object", object, "--target", object}, &content); e == nil {
		t.Fatal("notification identity granted remote read access")
	}
	if err := os.Rename(bin+".disabled", bin); err != nil {
		t.Fatal(err)
	}
	bindCLITool(t, h.m, h.room.RoomIdentity, member, h.c, "lark", 0)
	if e = cli([]string{"lark", "read", "--object", object, "--target", object}, &content); e != nil || content.Status != platform.ResolutionResolved || len(content.Contents) != 1 || !strings.Contains(content.Contents[0].Body, "脱敏测试消息") || !strings.Contains(content.Contents[0].Body, "发送者：测试发送者（open_id: ou_sender") {
		t.Fatal(content, e)
	}
	if len(h.state().Inbox) != 0 {
		t.Fatal("source ack missing")
	}
	again, e := h.svc.store.readFollowup(id, object, unit)
	if e != nil || again.ObjectRef != object || again.Ref != unit {
		t.Fatal("stable reference changed", e)
	}
	// The normal Room question path works without granting a Lark action.
	var question agentFollowupReceipt
	change := followupQuestionChange{followupCommand: followupCommand{ObjectRef: object, UnitRef: unit, Revision: u.Revision, TurnNumber: 1}, Text: "@operator 请确认如何处理这条脱敏消息"}
	if e = cli(entryChange(t, "matters", "notify", change), &question); e != nil {
		t.Fatal(e)
	}
	saved, _ := h.svc.store.LoadRoom()
	if saved.LatestSeq == 0 || !strings.Contains(saved.Messages[len(saved.Messages)-1].Text, "请确认") {
		t.Fatal("question not in Room")
	}
	if caps, _ := h.m.referenceCapabilities("lark"); caps.Read == nil || len(caps.Actions) != 0 || h.m.referenceExecutors["lark"] != nil {
		t.Fatal("lark writes enabled or read missing")
	}
	// Another member cannot borrow the source account to read a saved target.
	other := larkReadRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[1].AgentID, h.members[1].MembershipID), Target: &larkTargetRequest{ObjectRef: object, TargetRef: object, Scope: "content"}}
	if w := managerHTTP(h.m, "/v1/platform/lark/target", other); w.Code == 200 {
		t.Fatal("read crossed Membership")
	}
}
func TestLarkNotificationsRestartSwitchAndDeliveryCapacity(t *testing.T) {
	h := newLarkHarness(t)
	h.messages(2)
	h.start()
	old := h.state()
	if len(old.Inbox) != 2 {
		t.Fatal("missing pending")
	}
	cfg := old.Config
	cfg.Enabled = false
	h.configure(cfg)
	if e := h.deliver(); !errors.Is(e, notification.ErrIdentity) {
		t.Fatal(e)
	}
	if len(h.state().Inbox) != 2 {
		t.Fatal("pause discarded backlog")
	}
	cfg.Enabled = true
	cfg.Target = notification.Target{Mode: "inbox", AgentID: h.members[1].AgentID, MembershipID: h.members[1].MembershipID}
	next := h.configure(cfg)
	if next.Baseline != old.Baseline || next.SubscriptionID != old.SubscriptionID {
		t.Fatal("switch reset baseline")
	}
	for _, e := range next.Inbox {
		if e.Item.Route.Target != old.Config.Target {
			t.Fatal("pending rerouted")
		}
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	limits := DefaultInboxLimits()
	limits.Entries = 1
	addr, e := SpawnAgentInbox(h.svc.rt, h.svc.store, id, limits)
	if e != nil {
		t.Fatal(e)
	}
	h.svc.inboxes[id.MembershipID] = addr
	if e = h.deliver(); e != nil {
		t.Fatal(e)
	}
	if e = h.deliver(); !errors.Is(e, notification.ErrCapacity) {
		t.Fatal(e)
	}
	if h.state().DeliveryErrorCode != "inbox_capacity" || len(h.state().Inbox) != 1 {
		t.Fatal("capacity lost pending")
	}
	root := h.m.store.dir
	if e = h.m.close(); e != nil {
		t.Fatal(e)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	if len(h.state().Inbox) != 1 || h.state().Baseline != old.Baseline {
		t.Fatal("restart lost source")
	}
	if e = h.deliver(); !errors.Is(e, notification.ErrCapacity) {
		t.Fatal(e)
	}
	if inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 || len(h.state().Inbox) != 1 {
		t.Fatal("restart lost full Inbox backlog")
	}
}

func TestLarkNotificationsAckCrashReusesDeliveryID(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	old := h.state()
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	original := h.m.store.ops.createTemp
	h.m.store.ops.createTemp = func(string, string) (snapshotFile, error) { return nil, errors.New("source ack failure") }
	if err := h.deliver(); err == nil {
		t.Fatal("ack failure hidden")
	}
	h.m.store.ops.createTemp = original
	if len(h.state().Inbox) != 1 || inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 {
		t.Fatal("handoff lost")
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	if len(h.state().Inbox) != 0 || inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 || h.state().SubscriptionID != old.SubscriptionID {
		t.Fatal("crash duplicated delivery")
	}
}

func TestLarkConnectionReplacementRetainsOldSource(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	old := h.state()
	c := platformTestSave(t, h.m, platformConnectionRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "另一个显式连接", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
	c, e := h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: c.ID, Version: c.Version})
	if e != nil || c.Validation.Status != "valid" {
		t.Fatal(e)
	}
	cfg := old.Config
	cfg.ConnectionID = c.ID
	bad := cfg
	bad.Chats = []string{"invalid"}
	if _, e = h.m.configureLarkSubscription(context.Background(), old.Metadata().Key, old.SubscriptionVersion, bad); !errors.Is(e, ErrRequest) {
		t.Fatal(e)
	}
	if h.state().Retired || h.state().SubscriptionID != old.SubscriptionID {
		t.Fatal("invalid change retired source")
	}
	h.clock.add(time.Minute)
	next := h.configure(cfg)
	if next.SubscriptionID == old.SubscriptionID || len(next.Inbox) != 0 || !next.Baseline.After(old.Baseline) {
		t.Fatal("replacement adopted old account progress")
	}
	h.m.mu.Lock()
	stored, ok := h.m.notifications.StateLocked(old.Metadata().Key)
	history, e := h.m.retiredNotificationViewsLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if !ok || !stored.(lark.State).Retired || stored.Metadata().Enabled || len(stored.PendingItems()) != 1 || e != nil || len(history) != 1 || history[0].Pending != 1 {
		t.Fatal("old backlog disappeared", e)
	}
	if e = h.m.syncLarkSubscription(context.Background(), old.Metadata().Key, stored.Metadata().Version); !errors.Is(e, ErrRequest) {
		t.Fatal("retired source resumed", e)
	}
}

func TestLarkBackgroundPollingDeliversWithoutEnablingMember(t *testing.T) {
	h := newLarkHarness(t)
	h.messages(2)
	h.start()
	if e := h.m.notifications.Start(); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for {
		s := h.state()
		if len(s.Inbox) == 0 && len(s.Observed) == 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("background handoff incomplete")
		case <-time.After(20 * time.Millisecond):
		}
	}
	if e := h.m.notifications.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 2 {
		t.Fatal("background delivery missing")
	}
	snapshot, e := h.svc.store.LoadRoom()
	if e != nil || snapshot.LatestSeq != 0 {
		t.Fatal("background source posted raw messages to Room", e)
	}
}

func TestLarkAllChatDiscoveryPersistsAndProjectsSafeDiagnostics(t *testing.T) {
	h := newLarkHarness(t)
	directory := lark.ChatPage{Chats: []lark.Chat{{ID: "oc_chat", Mode: "p2p"}}, HasMore: true, PageToken: "private-page-token"}
	b, _ := json.Marshal(directory)
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := lark.Config{Enabled: true, ConnectionID: h.c.ID, ScopeMode: "all", Target: notification.Target{Mode: "inbox", AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}, Private: true, GroupMentions: true}
	s := h.configure(cfg)
	h.clock.add(3 * time.Second)
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	runner.Step(context.Background())
	s = h.state()
	if s.Directory == nil || s.Directory.Scan == nil || s.Directory.Scan.Token != "private-page-token" {
		t.Fatal("directory checkpoint missing")
	}
	h.m.mu.Lock()
	view, err := h.m.larkNotificationViewLocked(h.room.RoomIdentity, &s)
	h.m.mu.Unlock()
	encoded, _ := json.Marshal(view)
	if err != nil || view.Lark.DirectoryComplete || !view.Lark.DirectoryScanning || view.Lark.DirectoryPages != 1 || strings.Contains(string(encoded), "private-page-token") || strings.Contains(string(encoded), `"profile"`) {
		t.Fatal("inaccurate or sensitive diagnostics", err)
	}
	root := h.m.store.dir
	if err = h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.m.notifications.Clock = h.clock.get
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	if h.state().Directory.Scan.Token != s.Directory.Scan.Token || h.state().Baseline != s.Baseline {
		t.Fatal("restart lost directory checkpoint")
	}
	directory.HasMore = false
	directory.PageToken = ""
	b, _ = json.Marshal(directory)
	if err = os.WriteFile(filepath.Join(h.dir, "directory.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	runner = h.m.notifications.NewRunnerLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	for i := 0; i < 4; i++ {
		if _, stop := runner.Step(context.Background()); stop {
			t.Fatal(h.state().ErrorCode)
		}
	}
	s = h.state()
	if s.Directory.LastFull.IsZero() || s.Chats["oc_chat"].Completed == 0 || len(s.Inbox) != 1 {
		t.Fatal("directory/history resume failed")
	}
	if err = h.deliver(); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if inboxRoutingPreview(t, h.svc, id).SnapshotSeq != 1 {
		t.Fatal("all-chat source did not reach Inbox")
	}
}
