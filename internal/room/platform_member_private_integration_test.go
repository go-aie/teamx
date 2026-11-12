//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Native metadata, member lists and P2P mappings are independent evidence. The
// fixture rejects every message send; search never creates missing conversations.
func installMemberPrivateFixture(t *testing.T, h *larkHarness) {
	t.Helper()
	bin := filepath.Join(h.dir, "lark-cli")
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	fixture := `mode=open(base+'/private-mode').read() if os.path.exists(base+'/private-mode') else ''
open(base+'/private-calls','a').write(json.dumps(a)+'\n')
assert a[:2]!=['contact','+search-bot'], 'personnel search must not query bots'
assert a[:2]!=['im','+message-send'] and not (a[:2]==['api','POST'] and 'messages' in a[2]), 'must not send messages'
def reply(data):
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
if a[:2]==['contact','+search-user']:
 assert a[a.index('--query')+1]=='同事' and '--has-chatted' in a and '--page-token' not in a
 if mode=='search_permission':
  print(json.dumps({'ok':False,'error':{'type':'authorization'}}),file=sys.stderr);sys.exit(1)
 reply({'users':[{'open_id':'ou_one','localized_name':'同事','p2p_chat_id':'oc_private_one'},{'open_id':'ou_two','localized_name':'同事','p2p_chat_id':'oc_private_two'},{'open_id':'ou_missing','localized_name':'未聊过','p2p_chat_id':''}],'has_more':True})
if a[:2]==['im','+chat-list']:
 if '--page-token' not in a:
  reply({'chats':[{'chat_id':'oc_group_one','chat_mode':'group','name':'群'}],'has_more':True,'page_token':'private-next'})
 assert a[a.index('--page-token')+1]=='private-next'
 reply({'chats':[{'chat_id':'oc_private_one','chat_mode':'p2p','name':'同事'},{'chat_id':'oc_bot','chat_mode':'p2p','name':'机器人'},{'chat_id':'oc_private_two','chat_mode':'p2p','name':'同事'}],'has_more':False,'page_token':''})
path=a[2]
if path.startswith('/open-apis/im/v1/chats/'):
 chat=path.split('/')[5]
 if chat=='oc_private_one' and not path.endswith('/members') and os.path.exists(base+'/block-private'):
  import time
  open(base+'/private-read','w').write('ready')
  while not os.path.exists(base+'/release-private'):time.sleep(0.01)
 if chat=='oc_inaccessible':raise SystemExit('unmodified or cleared old private chat must not be read')
 if path.endswith('/members'):
  if mode=='member_permission':
   print(json.dumps({'ok':False,'error':{'type':'authorization'}}),file=sys.stderr);sys.exit(1)
  items=[{'member_id':'ou_reader','member_id_type':'open_id','tenant_key':'account_tenant'}]
  if chat!='oc_self':items.append({'member_id':'ou_one' if chat=='oc_private_one' else 'ou_two','member_id_type':'open_id','tenant_key':'external_tenant'})
  if chat=='oc_bot':items=[]
  if chat=='oc_foreign':items[0]['member_id']='ou_someone_else'
  reply({'items':items,'has_more':mode=='partial_members'})
 reply({'chat_mode':'group' if chat=='oc_group_one' else 'p2p','name':'原生会话','tenant_key':'account_tenant' if chat=='oc_self' else 'external_tenant'})
if path=='/open-apis/im/v1/chat_p2p/batch_query':
 peer=json.loads(a[a.index('--data')+1])['chatter_ids'][0]
 reply({'p2p_chats':[{'chat_id':{'ou_reader':'oc_self','ou_one':'oc_private_one','ou_two':'oc_private_two'}[peer]}]})
`
	script = bytes.Replace(script, []byte("if a[:2]==['im','+chat-list']:"), []byte(fixture+"\nif a[:2]==['im','+chat-list']:"), 1)
	if err := os.WriteFile(bin, script, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformMemberPrivateCLIQueriesAndReplacesVerifiedPersonnel(t *testing.T) {
	parallelFixtureTest(t)
	h, req := platformMemberHarness(t)
	installMemberPrivateFixture(t, h)
	server := httptest.NewServer(h.m)
	defer server.Close()
	binary := productionFixtureCLI(t)
	env := cliToolEnv(server.URL, h.svc, h.members[0])
	base := []string{"--platform-id", h.c.ID, "--platform-version", strconv.FormatUint(h.c.Version, 10)}
	run := func(args []string, result any) []byte {
		t.Helper()
		command := exec.Command(binary, append([]string{"platforms"}, args...)...)
		command.Env = os.Environ()
		for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
			command.Env = append(command.Env, key+"="+env(key))
		}
		output, err := command.CombinedOutput()
		if err != nil || json.Unmarshal(output, result) != nil {
			t.Fatal(args, err, string(output))
		}
		return output
	}
	var page lark.ChatPage
	run(append([]string{"chats", "--type", "p2p", "--query", "同事"}, base...), &page)
	if len(page.Chats) != 2 || page.Chats[0].Name != page.Chats[1].Name || page.Chats[0].ID == page.Chats[1].ID || !validMemberPrivateChat(page.Chats[0], h.c.Validation.Account, "oc_private_one") || len(page.SearchWarnings) != 1 || page.SearchWarnings[0].Code != "refine_query" {
		t.Fatal("same-name candidates or truncation warning lost", page)
	}
	run(append([]string{"chats", "--type", "p2p"}, base...), &page)
	if len(page.Chats) != 0 || !page.HasMore || page.PageToken != "private-next" {
		t.Fatal("empty filtered page lost continuation", page)
	}
	run(append([]string{"chats", "--type", "p2p", "--page-token", page.PageToken}, base...), &page)
	if len(page.Chats) != 2 || page.HasMore || len(page.SearchWarnings) != 1 || page.SearchWarnings[0].Code != "conversation_unverified" {
		t.Fatal("directory admitted a bot or hid partial evidence", page)
	}
	for _, mode := range []string{"search_permission", "member_permission"} {
		if err := os.WriteFile(filepath.Join(h.dir, "private-mode"), []byte(mode), 0600); err != nil {
			t.Fatal(err)
		}
		run(append([]string{"chats", "--type", "p2p", "--query", "同事"}, base...), &page)
		if len(page.Chats) != 0 || len(page.SearchWarnings) == 0 || page.SearchWarnings[0].Code != "permission_denied" && page.SearchWarnings[len(page.SearchWarnings)-1].Code != "permission_denied" {
			t.Fatal("permission failure presented as absence", page)
		}
	}
	_ = os.Remove(filepath.Join(h.dir, "private-mode"))
	h.m.mu.Lock()
	states := h.m.notifications.StatesLocked(req.RoomIdentity)
	h.m.mu.Unlock()
	if len(states) != 0 {
		t.Fatal("browse changed subscriptions")
	}
	// Establish a baseline before testing paused configuration replacement.
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Target = platformMemberTarget(req.member())
	cfg.Groups = lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_group_one"}, MessageMode: "mentions", AtMe: true}
	cfg.Private = lark.PrivateConfig{Scope: "all", ChatIDs: []string{"oc_inaccessible"}}
	cfg.IntervalSeconds = 61
	initial, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	version := initial.SubscriptionVersion
	apply := func(flags ...string) platformMemberOperation {
		t.Helper()
		source, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "private-change-" + strconv.FormatUint(version, 10), Text: "按明确人员列表调整私聊接收范围"})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := newOpaqueID()
		args := append([]string{"member", "notifications", "--operation-id", id, "--source-seq", strconv.FormatUint(source.Seq, 10), "--notification-version", strconv.FormatUint(version, 10)}, base...)
		args = append(args, flags...)
		var op, replay platformMemberOperation
		before := run(args, &op)
		if after := run(args, &replay); !bytes.Equal(before, after) || !reflect.DeepEqual(op, replay) || op.Status != "completed" {
			t.Fatal("private receipt replay changed", op, replay)
		}
		version = op.Result.Version
		var view platformMemberView
		run(append([]string{"member", "get"}, base...), &view)
		if view.Config == nil || !reflect.DeepEqual(view.Config, op.Config) || view.Baseline != initial.Baseline || view.Config.BackfillStart != initial.Config.BackfillStart || view.Config.IntervalSeconds != 61 || view.ToolEnabled || view.ToolRevision != 0 {
			t.Fatal("private replacement changed baseline, tools or authoritative result", view, op)
		}
		return op
	}
	op := apply("--private-scope", "specified", "--private-chat-id", "oc_private_two", "--private-chat-id", "oc_private_one", "--private-enabled", "true", "--group-chat-id", "oc_group_one", "--group-enabled", "false", "--group-at-all", "false")
	if len(op.Chats) != 3 || op.Chats[0].ID != "oc_group_one" || op.Chats[1].ID != "oc_private_two" || op.Chats[2].ID != "oc_private_one" || !op.Config.Private.Enabled || !reflect.DeepEqual(op.Config.Private.ChatIDs, []string{"oc_private_one", "oc_private_two"}) || op.Config.Groups.Enabled || !reflect.DeepEqual(op.Config.Groups.ChatIDs, initial.Config.Groups.ChatIDs) {
		t.Fatal(op)
	}
	// Frozen private evidence must not be accepted under a different account or ID.
	bad := op
	bad.Chats = append([]lark.Chat{}, op.Chats...)
	bad.Chats[1].Relation.Account.ID.Value = "ou_other"
	if validPlatformMemberOperation(bad) {
		t.Fatal("receipt accepted cross-account private evidence")
	}
	op = apply("--private-chat-id", "")
	if op.Config.Enabled || op.Config.Private.Enabled || len(op.Config.Private.ChatIDs) != 0 || len(op.Chats) != 0 || op.Input.Private.ChatIDs == nil {
		t.Fatal("clearing last private chat did not close reception", op)
	}
	h.m.mu.Lock()
	selections, err := h.m.platformSelectionsLocked()
	sibling, siblingErr := h.m.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, platformMemberTarget(notificationMemberIdentity(req.RoomIdentity, h.members[1])))
	h.m.mu.Unlock()
	if err != nil || len(selections) != 0 || siblingErr != nil || sibling != nil {
		t.Fatal("private configuration affected selectors or sibling", selections, sibling, err, siblingErr)
	}
}

func TestPlatformMemberPrivateRejectsUnverifiableTargets(t *testing.T) {
	for _, tc := range []struct{ id, mode string }{
		{"oc_group_one", ""}, {"oc_bot", ""}, {"oc_self", ""}, {"oc_foreign", ""},
		{"oc_private_one", "partial_members"}, {"oc_private_one", "member_permission"}, {"ou_one", ""},
	} {
		t.Run(tc.id+tc.mode, func(t *testing.T) {
			parallelFixtureTest(t)
			h, req := platformMemberHarness(t)
			installMemberPrivateFixture(t, h)
			if err := os.WriteFile(filepath.Join(h.dir, "private-mode"), []byte(tc.mode), 0600); err != nil {
				t.Fatal(err)
			}
			req.Input.AddChatIDs = nil
			req.Input.Private = &platformMemberPrivatePatch{Scope: "specified", ChatIDs: memberPatchIDs(tc.id), Enabled: memberPatchBool(true)}
			if r := managerHTTP(h.m, "/v1/platforms/member/notifications", req); r.Code == 200 {
				t.Fatal("unverifiable target saved", r.Body.String())
			}
			if _, err := os.Stat(h.m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
				t.Fatal("invalid private target reserved an operation", err)
			}
			h.m.mu.Lock()
			states := h.m.notifications.StatesLocked(req.RoomIdentity)
			h.m.mu.Unlock()
			if len(states) != 0 {
				t.Fatal("rejected private target changed configuration")
			}
		})
	}
}

func TestPlatformMemberPrivateRejectsChangesDuringVerification(t *testing.T) {
	for _, change := range []string{"account", "member", "subscription"} {
		t.Run(change, func(t *testing.T) {
			parallelFixtureTest(t)
			h, req := platformMemberHarness(t)
			installMemberPrivateFixture(t, h)
			req.Input.AddChatIDs = nil
			req.Input.Private = &platformMemberPrivatePatch{Scope: "specified", ChatIDs: memberPatchIDs("oc_private_one")}
			if err := os.WriteFile(filepath.Join(h.dir, "block-private"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- managerHTTP(h.m, "/v1/platforms/member/notifications", req) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(h.dir, "private-read")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("private metadata read did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch change {
			case "account":
				h.m.mu.Lock()
				ticket, err := h.m.platforms.BeginVerification(h.c.ID, h.c.Version)
				if err == nil {
					_, err = h.m.platforms.CompleteVerification(ticket, h.c.Validation)
				}
				h.m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
			case "member":
				if _, err := h.svc.remove(apiRequest{AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}); err != nil {
					t.Fatal(err)
				}
				inboxRoutingMember(t, h.svc, req.Caller.ID, "replacement-private-member")
			case "subscription":
				cfg := h.m.defaultLarkConfig(h.c.ID)
				cfg.Target = platformMemberTarget(req.member())
				if _, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(h.dir, "release-private"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-result:
				if r.Code == 200 {
					t.Fatal("stale private selection applied", r.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("private read did not settle")
			}
			if _, err := os.Stat(h.m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
				t.Fatal("stale private selection reserved an operation", err)
			}
		})
	}
}

func TestPlatformMemberPrivateUnknownReceiptSurvivesRestart(t *testing.T) {
	parallelFixtureTest(t)
	h, req := platformMemberHarness(t)
	installMemberPrivateFixture(t, h)
	req.Input.AddChatIDs = nil
	req.Input.Private = &platformMemberPrivatePatch{Scope: "specified", ChatIDs: memberPatchIDs("oc_private_one")}
	req.Input.Enabled = memberPatchBool(false)
	real := h.m.store.ops
	h.m.mu.Lock()
	h.m.store.ops.rename = func(from, to string) error {
		if to == h.m.platformMemberPath(req.OperationID) {
			var op platformMemberOperation
			data, _ := os.ReadFile(from)
			if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
				return os.ErrPermission
			}
		}
		return real.rename(from, to)
	}
	h.m.mu.Unlock()
	if r := managerHTTP(h.m, "/v1/platforms/member/notifications", req); r.Code == 200 {
		t.Fatal("uncertain private receipt reported success")
	}
	h.m.mu.Lock()
	h.m.store.ops = real
	h.m.mu.Unlock()
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	reopened := testManager(t, h.m.store.dir, nil)
	svc := eventLiveService(t, &eventHarness{m: reopened, room: h.room})
	req.ServiceInfo = svc.info
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	r := managerHTTP(reopened, "/v1/platforms/member/notifications", req)
	var op platformMemberOperation
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil || op.Status != "unknown" || len(op.Chats) != 1 || !validMemberPrivateChat(op.Chats[0], op.Account, "oc_private_one") || !reflect.DeepEqual(op.Input, *req.Input) {
		t.Fatal("restart re-read or lost private evidence", r.Code, r.Body.String())
	}
	reopened.mu.Lock()
	current, err := reopened.currentLarkForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
	reopened.mu.Unlock()
	if err != nil || current == nil || current.SubscriptionVersion != 1 || current.Config.Enabled || !reflect.DeepEqual(current.Config.Private.ChatIDs, []string{"oc_private_one"}) {
		t.Fatal("unknown private submission lost or replayed", current, err)
	}
	// A lost receipt cannot be bypassed by changing input or its operation ID.
	req.Input.Private.ChatIDs = memberPatchIDs("oc_private_two")
	if r := managerHTTP(reopened, "/v1/platforms/member/notifications", req); r.Code != 409 {
		t.Fatal("same private ID accepted different input", r.Code, r.Body.String())
	}
	req.Input.Private.ChatIDs = memberPatchIDs()
	req.Input.NotificationVersion = 1
	req.OperationID, _ = newOpaqueID()
	if r := managerHTTP(reopened, "/v1/platforms/member/notifications", req); r.Code != 409 {
		t.Fatal("unknown private operation bypassed under a new ID", r.Code, r.Body.String())
	}
}
