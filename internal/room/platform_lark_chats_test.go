package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestLarkChatScopeBrowser(t *testing.T) {
	if os.Getenv("TEAMX_LARK_SCOPE_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	h := newLarkHarness(t)
	page := lark.ChatPage{Chats: []lark.Chat{{ID: "oc_private", Mode: "p2p", Name: "同事"}, {ID: "oc_group", Mode: "group", Name: "项目群"}}}
	raw, _ := json.Marshal(page)
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "testdata/lark_scope_browser.cjs", server.URL)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestLarkChatPickerListsMetadataBeforeSubscription(t *testing.T) {
	h := newLarkHarness(t)
	page := lark.ChatPage{Chats: []lark.Chat{{ID: "oc_private", Mode: "p2p", Name: "同事"}, {ID: "oc_group", Mode: "group", Name: "项目群"}}, HasMore: true, PageToken: "next-page"}
	raw, _ := json.Marshal(page)
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	req := larkChatsRequest{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ConnectionID: h.c.ID, ConnectionVersion: h.c.Version}
	response := managerHTTP(h.m, "/v1/platform/lark/chats", req)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var got lark.ChatPage
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil || len(got.Chats) != 2 || got.Chats[0].Name != "同事" || !got.HasMore || got.PageToken != "next-page" {
		t.Fatal(got, err)
	}
	for _, forbidden := range []string{"executable", "cli_profile", "fingerprint", "脱敏测试消息"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatal("unexpected response content", forbidden)
		}
	}
	h.m.mu.Lock()
	states := h.m.notifications.StatesLocked(h.room.RoomIdentity)
	calls := len(h.m.platformCalls)
	h.m.mu.Unlock()
	if len(states) != 0 || calls != 0 {
		t.Fatal("metadata browse created a subscription or leaked a call")
	}
	// The existing CLI client forwards the exact next-page token.
	bin := filepath.Join(h.dir, "lark-cli")
	script, _ := os.ReadFile(bin)
	script = []byte(strings.Replace(string(script), "data=json.load(open(base+'/directory.json'))", "assert a[a.index('--page-token')+1]=='next-page'\n data={'chats':[{'chat_id':'oc_second','chat_mode':'group','name':'下一页'}],'has_more':False,'page_token':''}", 1))
	if err := os.WriteFile(bin, script, 0700); err != nil {
		t.Fatal(err)
	}
	req.PageToken = got.PageToken
	response = managerHTTP(h.m, "/v1/platform/lark/chats", req)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "oc_second") {
		t.Fatal("pagination failed", response.Code, response.Body.String())
	}
	for _, mutate := range []func(*larkChatsRequest){
		func(r *larkChatsRequest) { r.Caller = agentCaller(h.members[0].AgentID, h.members[0].MembershipID) },
		func(r *larkChatsRequest) { r.ServiceEpoch = "stale" },
		func(r *larkChatsRequest) { r.RoomInstance = "stale" },
		func(r *larkChatsRequest) { r.ConnectionVersion++ },
		func(r *larkChatsRequest) { r.PageToken = "invalid\npage" },
		func(r *larkChatsRequest) { r.Query = strings.Repeat("字", 51) },
		func(r *larkChatsRequest) { r.Query = "bad\nquery" },
		func(r *larkChatsRequest) { r.Query = " " },
		func(r *larkChatsRequest) { r.Type = "unknown" },
	} {
		bad := req
		mutate(&bad)
		if response := managerHTTP(h.m, "/v1/platform/lark/chats", bad); response.Code == 200 {
			t.Fatal("invalid browsing identity or parameters accepted")
		}
	}
	w := httptest.NewRecorder()
	h.m.ServeHTTP(w, httptest.NewRequest("GET", "/v1/platform/lark/chats", nil))
	if w.Code != 405 {
		t.Fatal(w.Code)
	}
}

func installChatSearchFixture(t *testing.T, h *larkHarness) {
	t.Helper()
	bin := filepath.Join(h.dir, "lark-cli")
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	search := `if a[:2]==['im','+chat-search']:
 assert a[a.index('--query')+1]=='yanfa'
 assert '--disable-search-by-user' in a
 data={'chats':[{'chat_id':'oc_search_group','chat_mode':'DEFAULT','name':'研发群'}],'has_more':False,'page_token':''}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
if a[:2]==['contact','+search-user']:
 assert '--has-chatted' in a
 data={'users':[{'open_id':'ou_peer','localized_name':'研发同事','p2p_chat_id':'oc_search_peer'}],'has_more':True}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
if a[:2]==['contact','+search-bot']:
 print(json.dumps({'ok':False,'error':{'type':'authorization'}}),file=sys.stderr);sys.exit(1)
`
	script = []byte(strings.Replace(string(script), "if a[:2]==['im','+chat-list']:", search+"\nif a[:2]==['im','+chat-list']:", 1))
	if err := os.WriteFile(bin, script, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestLarkChatPickerSearchesMetadataWithPartialPermissions(t *testing.T) {
	h := newLarkHarness(t)
	installChatSearchFixture(t, h)
	req := larkChatsRequest{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ConnectionID: h.c.ID, ConnectionVersion: h.c.Version, Query: "yanfa", Type: "all"}
	response := managerHTTP(h.m, "/v1/platform/lark/chats", req)
	var page lark.ChatPage
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &page) != nil || len(page.Chats) != 2 || len(page.SearchWarnings) != 2 || page.HasMore {
		t.Fatal(response.Code, response.Body.String())
	}
	if page.Chats[0].Mode != "group" || page.Chats[1].ID != "oc_search_peer" {
		t.Fatal(page)
	}
	for _, c := range platformTestList(t, h.m) {
		if c.ID == h.c.ID && (c.Version != h.c.Version || c.Validation.Status != "valid") {
			t.Fatal("optional permission changed connection", c)
		}
	}
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	if len(h.m.notifications.StatesLocked(h.room.RoomIdentity)) != 0 || len(h.m.platformCalls) != 0 {
		t.Fatal("search changed subscription or leaked calls")
	}
}

func TestLarkChatSearchBrowser(t *testing.T) {
	if os.Getenv("TEAMX_LARK_SCOPE_BROWSER") != "1" {
		t.Skip("optional local Chromium acceptance")
	}
	h := newLarkHarness(t)
	installChatSearchFixture(t, h)
	raw, _ := json.Marshal(lark.ChatPage{Chats: []lark.Chat{{ID: "oc_private", Mode: "p2p", Name: "同事"}, {ID: "oc_group", Mode: "group", Name: "项目群"}}})
	if err := os.WriteFile(filepath.Join(h.dir, "directory.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "node", "testdata/lark_search_browser.cjs", server.URL).CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, out)
	}
	t.Log(strings.TrimSpace(string(out)))
}

func TestLarkChatPickerRejectsConnectionChangedDuringRead(t *testing.T) {
	h := newLarkHarness(t)
	req := larkChatsRequest{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ConnectionID: h.c.ID, ConnectionVersion: h.c.Version}
	h.m.mu.Lock()
	a, err := h.m.checkLarkChatsLocked(context.Background(), req, nil)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.m.verifyPlatformConnection(context.Background(), platformConnectionRequest{ID: h.c.ID, Version: h.c.Version}); err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	_, err = h.m.checkLarkChatsLocked(context.Background(), req, &a)
	h.m.mu.Unlock()
	if err == nil {
		t.Fatal("late result survived connection change")
	}
}

func TestLarkChatPickerDiscardsCLIIdentitySwitch(t *testing.T) {
	h := newLarkHarness(t)
	bin := filepath.Join(h.dir, "lark-cli")
	script, _ := os.ReadFile(bin)
	text := strings.Replace(string(script), "'openId':'ou_reader'", "'openId':('ou_other' if os.path.exists(base+'/switched') else 'ou_reader')", 1)
	text = strings.Replace(text, "data=json.load(open(base+'/directory.json'))", "open(base+'/switched','w').write('yes')\n data={'chats':[{'chat_id':'oc_private','chat_mode':'p2p','name':'discard-this-name'}],'has_more':False,'page_token':''}", 1)
	if err := os.WriteFile(bin, []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	req := larkChatsRequest{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ConnectionID: h.c.ID, ConnectionVersion: h.c.Version}
	response := managerHTTP(h.m, "/v1/platform/lark/chats", req)
	if response.Code == 200 || !strings.Contains(response.Body.String(), "identity_mismatch") || strings.Contains(response.Body.String(), "discard-this-name") {
		t.Fatal(response.Code, response.Body.String())
	}
	for _, c := range platformTestList(t, h.m) {
		if c.ID == h.c.ID && c.Validation.Status != "failed" {
			t.Fatal("switched account was not marked for verification")
		}
	}
}
