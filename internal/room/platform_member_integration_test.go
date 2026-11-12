//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func platformMemberHarness(t *testing.T) (*larkHarness, platformMemberRequest) {
	t.Helper()
	h := newLarkSearchHarness(t)
	bin := filepath.Join(h.dir, "lark-cli")
	script, _ := os.ReadFile(bin)
	text := strings.Replace(string(script), "'chat_mode':'p2p','name':'脱敏会话'", "'chat_mode':('p2p' if path.endswith('oc_private') else 'group'),'name':'产品讨论群'", 1)
	// The search fixture already specializes its private chat. Keep this harness's
	// exact-ID type contract when that newer fixture form is used as well.
	text = strings.Replace(text, "'chat_mode':'p2p' if path.endswith('/oc_chat') else 'group'", "'chat_mode':('p2p' if path.endswith('oc_private') else 'group')", 1)
	search := `if a[:2]==['im','+chat-search']:
 assert a[a.index('--query')+1]=='产品'
 assert a[a.index('--chat-modes')+1]=='group,topic'
 if '--page-token' in a:
  assert a[a.index('--page-token')+1]=='next-page'
  data={'chats':[{'chat_id':'oc_group_three','chat_mode':'THREAD','name':'第三群'}],'has_more':False,'page_token':''}
 else:
  data={'chats':[{'chat_id':'oc_group_one','chat_mode':'DEFAULT','name':'产品讨论群'},{'chat_id':'oc_group_two','chat_mode':'DEFAULT','name':'产品讨论群'}],'has_more':True,'page_token':'next-page'}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
`
	text = strings.Replace(text, "if a[:2]==['im','+chat-list']:", search+"\nif a[:2]==['im','+chat-list']:", 1)
	if err := os.WriteFile(bin, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	page := lark.ChatPage{Chats: []lark.Chat{{ID: "oc_private", Mode: "p2p", Name: "同事"}, {ID: "oc_group_one", Mode: "group", Name: "产品讨论群"}}, HasMore: true, PageToken: "next-page"}
	raw, _ := json.Marshal(page)
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ClientMessageID: "member-platform-source", Text: "连接飞书，关注指定的产品讨论群；按我的选择启用工具"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := newOpaqueID()
	return h, platformMemberRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), SourceSeq: source.Seq, OperationID: id, Input: &platformMemberInput{PlatformID: h.c.ID, PlatformVersion: h.c.Version, AddChatIDs: []string{"oc_group_two"}}}
}

func TestPlatformMemberCLISelectedGroupAndSeparateTool(t *testing.T) {
	parallelFixtureTest(t)
	h, req := platformMemberHarness(t)
	// An existing sibling binding must survive all current-member changes.
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[1], h.c, "lark", 0)
	server := httptest.NewServer(h.m)
	defer server.Close()
	binary := productionFixtureCLI(t)
	env := cliToolEnv(server.URL, h.svc, h.members[0])
	run := func(args []string, result any) {
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
	}
	base := []string{"--platform-id", h.c.ID, "--platform-version", strconv.FormatUint(h.c.Version, 10)}
	var reused platformConnectOperation
	id, _ := newOpaqueID()
	run(append([]string{"connect", "start", "--provider", "lark", "--name", h.c.Name, "--operation-id", id, "--source-seq", strconv.FormatUint(req.SourceSeq, 10)}, base...), &reused)
	if !connectTerminal(reused.Status) {
		reused = waitConnectionResult(t, h.m, platformConnectRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), OperationID: id})
	}
	if reused.Status != "completed" {
		t.Fatal(reused)
	}
	var view platformMemberView
	run(append([]string{"member", "get"}, base...), &view)
	if view.ToolEnabled || view.NotificationVersion != 0 || view.ToolRevision != 1 {
		t.Fatal(view)
	}
	var page struct {
		lark.ChatPage
		PlatformVersion uint64 `json:"platform_version"`
	}
	run(append([]string{"chats", "--query", "产品"}, base...), &page)
	if len(page.Chats) != 2 || page.Chats[0].Name != page.Chats[1].Name || !page.HasMore || page.PlatformVersion != h.c.Version {
		t.Fatal(page)
	}
	run(append([]string{"chats", "--query", "产品", "--page-token", page.PageToken}, base...), &page)
	if len(page.Chats) != 1 || page.Chats[0].ID != "oc_group_three" || page.HasMore {
		t.Fatal(page)
	}
	run(append([]string{"chats"}, base...), &page)
	if len(page.Chats) != 1 || page.Chats[0].Mode != "group" || !page.HasMore {
		t.Fatal("directory group filter lost continuation", page)
	}
	var configured platformMemberOperation
	args := append([]string{"member", "notifications", "--operation-id", req.OperationID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10), "--notification-version", "0", "--add-chat-id", "oc_group_two"}, base...)
	run(args, &configured)
	if configured.Status != "completed" || configured.Config == nil || !reflect.DeepEqual(configured.Config.Groups.ChatIDs, []string{"oc_group_two"}) || configured.Config.Private.Enabled || configured.Config.Groups.AtMe || configured.Config.Groups.AtAll {
		t.Fatal(configured)
	}
	var replay platformMemberOperation
	run(args, &replay)
	if !reflect.DeepEqual(replay, configured) {
		t.Fatal("duplicate CLI submission changed receipt")
	}
	run(append([]string{"member", "get"}, base...), &view)
	if view.NotificationVersion != 1 || view.Baseline.IsZero() || view.ToolEnabled || view.ToolRevision != 1 {
		t.Fatal(view)
	}
	toolID, _ := newOpaqueID()
	run(append([]string{"member", "tool", "--tool-revision", "1", "--enabled", "true", "--operation-id", toolID, "--source-seq", strconv.FormatUint(req.SourceSeq, 10)}, base...), &configured)
	if configured.Status != "completed" || configured.Kind != "tool" {
		t.Fatal(configured)
	}
	baseline := view.Baseline
	run(append([]string{"member", "get"}, base...), &view)
	if !view.ToolEnabled || view.ToolRevision != 2 || view.NotificationVersion != 1 || view.Baseline != baseline || view.Tool == nil || (view.Tool.ActionAuthorization == nil || len(view.Tool.ActionAuthorization.Grants) != 3) {
		t.Fatal(view)
	}
	var status struct {
		Operations []platformMemberOperation `json:"operations"`
	}
	run([]string{"member", "status", "--source-seq", strconv.FormatUint(req.SourceSeq, 10)}, &status)
	if len(status.Operations) != 2 {
		t.Fatal("lost independent steps", status)
	}
	h.m.mu.Lock()
	f, err := h.m.readToolBindingsLocked(req.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil || len(f.Bindings) != 2 || f.Bindings[0].MembershipID != h.members[1].MembershipID {
		t.Fatal(f, err)
	}
	source, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: humanCaller, ClientMessageID: "disable-selected", Text: "关闭我的飞书通知和工具，保留群选择"})
	if err != nil {
		t.Fatal(err)
	}
	notificationID, _ := newOpaqueID()
	run(append([]string{"member", "notifications", "--notification-version", "1", "--enabled", "false", "--operation-id", notificationID, "--source-seq", strconv.FormatUint(source.Seq, 10)}, base...), &configured)
	toolID, _ = newOpaqueID()
	run(append([]string{"member", "tool", "--tool-revision", "2", "--enabled", "false", "--operation-id", toolID, "--source-seq", strconv.FormatUint(source.Seq, 10)}, base...), &configured)
	run(append([]string{"member", "get"}, base...), &view)
	if view.ToolEnabled || view.Config.Enabled || view.Baseline != baseline || view.NotificationVersion != 2 || view.ToolRevision != 3 || !reflect.DeepEqual(view.Config.Groups.ChatIDs, []string{"oc_group_two"}) {
		t.Fatal("disable lost selections/baseline", view)
	}
}

func TestPlatformMemberSubscriptionPatchVersionsAndHumanEvidence(t *testing.T) {
	parallelFixtureTest(t)
	h, req := platformMemberHarness(t)
	cfg := h.m.defaultLarkConfig(h.c.ID)
	cfg.Target = inboxTarget(req.member())
	cfg.Groups.Scope, cfg.Groups.ChatIDs, cfg.Groups.MessageMode = "specified", []string{"oc_old"}, "mentions"
	cfg.Private.Scope, cfg.Private.ChatIDs = "specified", []string{"oc_private"}
	cfg.BackfillSeconds, cfg.IntervalSeconds = 20, 61
	initial, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Stale notification/config and account versions are rejected before mutation.
	for _, mutate := range []func(*platformMemberRequest){
		func(r *platformMemberRequest) { r.Input.NotificationVersion = 0 },
		func(r *platformMemberRequest) { r.Input.PlatformVersion++ },
		func(r *platformMemberRequest) { r.Caller.Kind = "human" },
		func(r *platformMemberRequest) { r.SourceSeq = 9999 },
		func(r *platformMemberRequest) { r.Input.AddChatIDs = []string{"oc_private"} },
		func(r *platformMemberRequest) { r.Input.AddChatIDs = []string{"oc_group_two", "oc_group_two"} },
	} {
		bad := req
		in := *req.Input
		in.NotificationVersion = initial.SubscriptionVersion
		bad.Input = &in
		mutate(&bad)
		if r := managerHTTP(h.m, "/v1/platforms/member/notifications", bad); r.Code == 200 {
			t.Fatal("bad configuration admitted", r.Body.String())
		}
	}
	h.clock.add(30 * time.Second)
	req.Input.NotificationVersion = initial.SubscriptionVersion
	r := managerHTTP(h.m, "/v1/platforms/member/notifications", req)
	var op platformMemberOperation
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil || op.Status != "completed" {
		t.Fatal(r.Code, r.Body.String())
	}
	h.m.mu.Lock()
	current, err := h.m.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, cfg.Target)
	h.m.mu.Unlock()
	if err != nil || current == nil || current.Baseline != initial.Baseline || current.Config.BackfillStart != initial.Config.BackfillStart || !reflect.DeepEqual(current.Config.Private, initial.Config.Private) || current.Config.IntervalSeconds != initial.Config.IntervalSeconds || current.Config.Groups.MessageMode != initial.Config.Groups.MessageMode || !reflect.DeepEqual(current.Config.Groups.ChatIDs, []string{"oc_group_two", "oc_old"}) {
		t.Fatal("existing reception settings lost", current, err)
	}
	// Search metadata and exact-ID read do not open another member's subscription.
	h.m.mu.Lock()
	other, err := h.m.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, inboxTarget(notificationMemberIdentity(req.RoomIdentity, h.members[1])))
	h.m.mu.Unlock()
	if err != nil || other != nil {
		t.Fatal("sibling notification changed", err)
	}
}

func TestPlatformMemberRejectsChangesDuringGroupVerification(t *testing.T) {
	for _, change := range []string{"account", "member", "subscription"} {
		t.Run(change, func(t *testing.T) {
			parallelFixtureTest(t)
			h, req := platformMemberHarness(t)
			bin := filepath.Join(h.dir, "lark-cli")
			script, _ := os.ReadFile(bin)
			block := "if path.endswith('/oc_group_two'):\n import time\n open(base+'/selected-read','w').write('ready')\n while not os.path.exists(base+'/release-read'):time.sleep(0.01)\n"
			script = []byte(strings.Replace(string(script), "path=a[2]\n", "path=a[2]\n"+block, 1))
			if err := os.WriteFile(bin, script, 0700); err != nil {
				t.Fatal(err)
			}
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- managerHTTP(h.m, "/v1/platforms/member/notifications", req) }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(filepath.Join(h.dir, "selected-read")); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("selection read did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch change {
			case "account":
				// A new verification version for the same account also invalidates selection.
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
				inboxRoutingMember(t, h.svc, req.Caller.ID, "new-member-during-selection")
			case "subscription":
				cfg := h.m.defaultLarkConfig(h.c.ID)
				cfg.Target = inboxTarget(req.member())
				if _, err := h.m.configureLarkSubscription(context.Background(), notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}, 0, cfg); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(h.dir, "release-read"), []byte("ok"), 0600); err != nil {
				t.Fatal(err)
			}
			select {
			case r := <-result:
				if r.Code == 200 {
					t.Fatal("stale selected group applied", r.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("selected read did not settle")
			}
			if _, err := os.Stat(h.m.platformMemberPath(req.OperationID)); !os.IsNotExist(err) {
				t.Fatal("stale selection reserved or applied", err)
			}
		})
	}
}

func TestPlatformMemberNotificationUnknownReceiptAndRestart(t *testing.T) {
	for _, afterReplace := range []bool{false, true} {
		t.Run(strconv.FormatBool(afterReplace), func(t *testing.T) {
			parallelFixtureTest(t)
			h, req := platformMemberHarness(t)
			real := h.m.store.ops
			h.m.mu.Lock()
			h.m.store.ops.rename = func(from, to string) error {
				if to == h.m.platformMemberPath(req.OperationID) {
					data, _ := os.ReadFile(from)
					var op platformMemberOperation
					if json.Unmarshal(data, &op) == nil && op.Status == "completed" {
						if afterReplace {
							if err := real.rename(from, to); err != nil {
								return err
							}
						}
						return os.ErrPermission
					}
				}
				return real.rename(from, to)
			}
			h.m.mu.Unlock()
			if r := managerHTTP(h.m, "/v1/platforms/member/notifications", req); r.Code == 200 {
				t.Fatal("uncertain receipt reported success")
			}
			h.m.mu.Lock()
			h.m.store.ops = real
			current, err := h.m.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, inboxTarget(req.member()))
			h.m.mu.Unlock()
			if err != nil || current == nil || current.SubscriptionVersion != 1 {
				t.Fatal("subscription save was lost", err)
			}
			if err := h.m.close(); err != nil {
				t.Fatal(err)
			}
			reopened := testManager(t, h.m.store.dir, nil)
			svc := eventLiveService(t, &eventHarness{m: reopened, room: h.room})
			req.ServiceInfo = svc.info
			// Any accidental native re-read or resubmission would now fail.
			if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			r := managerHTTP(reopened, "/v1/platforms/member/notifications", req)
			var op platformMemberOperation
			want := "unknown"
			if afterReplace {
				want = "completed"
			}
			if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &op) != nil || op.Status != want {
				t.Fatal(r.Code, r.Body.String())
			}
			query := platformMemberRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller, OperationID: req.OperationID}
			before, _ := os.ReadFile(reopened.platformMemberPath(req.OperationID))
			if r := managerHTTP(reopened, "/v1/platforms/member/status", query); r.Code != 200 {
				t.Fatal(r.Code, r.Body.String())
			}
			after, _ := os.ReadFile(reopened.platformMemberPath(req.OperationID))
			if string(before) != string(after) {
				t.Fatal("status rewrote operation")
			}
			reopened.mu.Lock()
			saved, err := reopened.currentLarkForTargetLocked(req.RoomIdentity, h.c.ID, inboxTarget(req.member()))
			reopened.mu.Unlock()
			if err != nil || saved == nil || saved.SubscriptionVersion != current.SubscriptionVersion || saved.Baseline != current.Baseline || !reflect.DeepEqual(saved.Config, current.Config) {
				t.Fatal("receipt recovery replayed subscription", err)
			}
		})
	}
}
