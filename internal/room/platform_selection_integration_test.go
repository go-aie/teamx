//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exercise the same access/Space admission as the browser server. Bare manager
// component requests have a different synthetic Space and cannot enter it.
func platformOnboardingHTTP(t *testing.T, m *roomManager, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, m.config.endpoint+path, bytes.NewReader(data))
	r.RemoteAddr = "127.0.0.1:45000"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	} else {
		r.Header.Set("Origin", m.config.endpoint)
	}
	w := httptest.NewRecorder()
	m.config.access.wrap(m).ServeHTTP(w, r)
	return w
}

// Real TeamX CLI/HTTP; official Feishu CLI is synthetic.
func TestPlatformSelectionConversationAndMemberConfiguration(t *testing.T) {
	t.Setenv("USER", humanCaller.ID)
	h, member := platformMemberHarness(t)
	platformSelectionConversation(t, h, member, "inbox")
}

func TestDefaultAssistantPlatformOnboarding(t *testing.T) {
	for _, mode := range []string{"wait", "inbox", "human"} {
		t.Run(mode, func(t *testing.T) { defaultAssistantPlatformOnboarding(t, mode) })
	}
}

func defaultAssistantPlatformOnboarding(t *testing.T, mode string, choose ...platformSelectionBrowserDriver) {
	t.Helper()
	t.Setenv("USER", humanCaller.ID)
	h, member := platformMemberHarness(t)
	// Reuse the verified global account, but start the default DM with no
	// member settings or inherited authority from the fixture's ordinary Room.
	h.m.defaultRuntime = testTeamDefinition(t).Runtime
	ensureAssistant(t, h.m)
	h.room = assistantSummary(t, h.m)
	_, snapshot := readManagedRoom(t, h.m, h.room)
	h.members = snapshot.Members
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	source, err := h.svc.room.Call[PostReceipt](t.Context(), PostMessage{
		RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ClientMessageID: "assistant-onboarding",
		Text: "连接飞书，关注产品讨论群，需要我处理时在这里提醒我",
	})
	if err != nil {
		t.Fatal(err)
	}
	member.ServiceInfo, member.Caller, member.SourceSeq = h.svc.info, agentCaller(h.members[0].AgentID, h.members[0].MembershipID), source.Seq
	// Another DM can reuse the global account without inheriting settings,
	// receipts or the source message of the default assistant.
	other := createDirectConversation(t, h.m, directSelection("other-onboarding-assistant"))
	_, state := readManagedRoom(t, h.m, other)
	svc := eventLiveService(t, &eventHarness{m: h.m, room: other})
	platformSelectionConversation(t, h, member, mode, choose...)
	foreign := member
	foreign.ServiceInfo, foreign.Caller = svc.info, agentCaller(state.Members[0].AgentID, state.Members[0].MembershipID)
	foreign.OperationID, foreign.SourceSeq = "", 0
	foreign.Input = &platformMemberInput{PlatformID: h.c.ID, PlatformVersion: h.c.Version}
	token, revoke, err := h.m.config.access.issueExecution(executionBinding{ServiceInfo: svc.info, Caller: foreign.Caller})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	w := platformOnboardingHTTP(t, h.m, "/v1/platforms/member/get", foreign, token)
	var current platformMemberView
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &current) != nil || current.Config != nil || current.ToolEnabled {
		t.Fatal("independent DM inherited member settings", w.Code, w.Body.String())
	}
	if w := platformOnboardingHTTP(t, h.m, "/v1/platforms/member/status", platformMemberRequest{ServiceInfo: svc.info, Caller: foreign.Caller, OperationID: member.OperationID}, token); w.Code != http.StatusForbidden {
		t.Fatal("independent DM inherited operation receipt")
	}
}

// Browser callers supply a driver from an e2e-only file. Integration never
// consults browser environment variables or starts Chromium.
type platformSelectionBrowserDriver func(*testing.T, string, string, string, *larkHarness)

func platformSelectionConversation(t *testing.T, h *larkHarness, member platformMemberRequest, mode string, choose ...platformSelectionBrowserDriver) {
	t.Helper()
	t.Setenv("USER", humanCaller.ID)
	server := browserTestServer(t, h.m)
	defer server.Close()
	h.m.config.endpoint = server.URL
	binary := productionFixtureCLI(t)
	env := cliToolEnv(server.URL, h.svc, h.members[0])
	token, revoke, err := h.m.config.access.issueExecution(executionBinding{ServiceInfo: h.svc.info, Caller: member.Caller})
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	cliEnv := os.Environ()
	cliEnv = append(cliEnv, "TEAMX_EXECUTION_TOKEN="+token)
	for _, key := range []string{"TEAMX_ENDPOINT", "TEAMX_ROOM_ID", "TEAMX_ROOM_INSTANCE", "TEAMX_SERVICE_EPOCH", "TEAMX_AGENT_ID", "TEAMX_MEMBERSHIP_ID"} {
		cliEnv = append(cliEnv, key+"="+env(key))
	}
	run := func(args []string, result any) {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"platforms"}, args...)...)
		cmd.Env = cliEnv
		out, err := cmd.CombinedOutput()
		if err != nil || json.Unmarshal(out, result) != nil {
			t.Fatal(args, err, string(out))
		}
	}
	source := strconv.FormatUint(member.SourceSeq, 10)
	accountID, _ := newOpaqueID()
	groupID, _ := newOpaqueID()
	var account, group platformSelectionView
	run([]string{"selection", "start", "--kind", "account", "--operation-id", accountID, "--source-seq", source, "--notify", "inbox"}, &account)
	base := []string{"--platform-id", h.c.ID, "--platform-version", strconv.FormatUint(h.c.Version, 10)}
	run(append([]string{"selection", "start", "--kind", "groups", "--operation-id", groupID, "--source-seq", source, "--query", "产品", "--notify", "inbox"}, base...), &group)
	var pending struct {
		Pending bool `json:"pending"`
	}
	run([]string{"selection", "wait", "--operation-id", groupID, "--timeout", "0s"}, &pending)
	if !pending.Pending {
		t.Fatal("wait cancelled selection")
	}
	// The browser server owns a real Space identity. Human submissions must
	// cross its access middleware, just like the CLI's bound execution token.
	submit := func(req platformSelectionRequest) platformSelectionView {
		t.Helper()
		w := platformOnboardingHTTP(t, h.m, "/v1/platforms/selection-interactions/submit", req, "")
		var view platformSelectionView
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &view) != nil {
			t.Fatal("submit", req.RoomID, req.OperationID, w.Code, w.Body.String())
		}
		return view
	}
	if len(choose) != 0 {
		choose[0](t, server.URL, accountID, groupID, h)
	} else {
		run([]string{"selection", "update", "--operation-id", accountID, "--version", "1", "--selected-platform-id", h.c.ID, "--selected-platform-version", strconv.FormatUint(h.c.Version, 10), "--name", h.c.Name}, &account)
		human := platformSelectionRequest{ServiceInfo: member.ServiceInfo, Caller: humanCaller, OperationID: accountID, Version: account.Version}
		account = submit(human)
		run([]string{"selection", "update", "--operation-id", groupID, "--version", "1", "--chat-id", "oc_group_two"}, &group)
		human.OperationID = groupID
		human.Version = group.Version
		group = submit(human)
	}
	if mode == "human" {
		if _, err := h.svc.room.Call[PostReceipt](t.Context(), PostMessage{
			RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ClientMessageID: "continue-onboarding", Text: "继续刚才的飞书群配置",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if mode == "wait" {
		var result struct {
			Pending   bool                  `json:"pending"`
			Selection platformSelectionView `json:"selection"`
		}
		run([]string{"selection", "wait", "--operation-id", groupID, "--timeout", "1s"}, &result)
		if result.Pending || result.Selection.Status != "completed" {
			t.Fatal("completed choice not observed in same-turn wait", result)
		}
	}
	run([]string{"selection", "status", "--operation-id", accountID}, &account)
	run([]string{"selection", "status", "--operation-id", groupID}, &group)
	if account.Status != "completed" || account.Draft.PlatformID != h.c.ID || account.Account != group.Account || group.Status != "completed" || !reflect.DeepEqual(group.Draft.ChatIDs, []string{"oc_group_two"}) {
		t.Fatal(account, group)
	}
	h.m.deliverConnectNotifications(t.Context())
	run([]string{"selection", "status", "--operation-id", groupID}, &group)
	if group.Continuation.Mode != "inbox" || group.Continuation.InboxSeq == 0 || group.Source.Seq != member.SourceSeq || group.MemberIdentity != member.member() {
		t.Fatal("selection result lost original source, target or Inbox handoff", group)
	}
	// Selection receipts alone have not enabled reception or tools.
	var current platformMemberView
	run(append([]string{"member", "get"}, base...), &current)
	if current.NotificationVersion != 0 || current.ToolEnabled {
		t.Fatal(current)
	}
	connectionID, _ := newOpaqueID()
	var connection platformConnectOperation
	run(append([]string{"connect", "start", "--provider", "lark", "--name", account.Draft.Name, "--operation-id", connectionID, "--source-seq", source}, base...), &connection)
	// Reuse now performs a managed capability check before completing. Observe
	// the production waiter instead of assuming start is a synchronous commit.
	var connected struct {
		Pending   bool                     `json:"pending"`
		Operation platformConnectOperation `json:"operation"`
	}
	run([]string{"connect", "wait", "--operation-id", connectionID, "--timeout", "1m"}, &connected)
	connection = connected.Operation
	if connected.Pending || connection.Status != "completed" {
		t.Fatal(connection)
	}
	var applied platformMemberOperation
	run(append([]string{"member", "notifications", "--operation-id", member.OperationID, "--source-seq", source, "--notification-version", "0", "--add-chat-id", group.Draft.ChatIDs[0]}, base...), &applied)
	if applied.Status != "completed" || len(applied.Chats) != 1 || applied.Chats[0].ID != "oc_group_two" {
		t.Fatal(applied)
	}
	run(append([]string{"member", "get"}, base...), &current)
	if current.MemberIdentity != member.member() || current.Config == nil || current.Config.Target != platformMemberTarget(member.member()) || !reflect.DeepEqual(current.Config.Groups.ChatIDs, group.Draft.ChatIDs) || current.Config.Private.Enabled || current.ToolEnabled || current.Baseline.IsZero() {
		t.Fatal(current)
	}
	for _, id := range []string{accountID, groupID} {
		var v platformSelectionView
		run([]string{"selection", "acknowledge", "--operation-id", id, "--result-version", "1"}, &v)
		if v.Continuation.State != "handled" {
			t.Fatal(v)
		}
	}
	// A late event/active observer returns the same fixed receipt, not a second save.
	run(append([]string{"member", "notifications", "--operation-id", member.OperationID, "--source-seq", source, "--notification-version", "0", "--add-chat-id", group.Draft.ChatIDs[0]}, base...), &applied)
	run(append([]string{"member", "get"}, base...), &current)
	if current.NotificationVersion != 1 {
		t.Fatal("late path replayed config", current)
	}
}

func TestPlatformSelectionRejectsDraftChangedDuringGroupVerification(t *testing.T) {
	h, member := platformMemberHarness(t)
	id, _ := newOpaqueID()
	req := platformSelectionRequest{ServiceInfo: member.ServiceInfo, Caller: member.Caller, OperationID: id, SourceSeq: member.SourceSeq, Input: &platformSelectionInput{Kind: "groups", PlatformID: h.c.ID, PlatformVersion: h.c.Version}}
	first := selectionCall(t, h.m, "start", req, false)
	q := selectionQuery(req, false)
	q.Version = first.Version
	q.Draft = &platformSelectionDraft{ChatIDs: []string{"oc_group_two"}}
	draft := selectionCall(t, h.m, "update", q, false)
	bin := filepath.Join(h.dir, "lark-cli")
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	block := "if path.endswith('/oc_group_two'):\n import time\n open(base+'/selection-read','w').write('ready')\n while not os.path.exists(base+'/selection-release'):time.sleep(0.01)\n"
	if err := os.WriteFile(bin, []byte(strings.Replace(string(script), "path=a[2]\n", "path=a[2]\n"+block, 1)), 0700); err != nil {
		t.Fatal(err)
	}
	human := selectionQuery(req, true)
	human.Version = draft.Version
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- managerHTTP(h.m, "/v1/platforms/selection-interactions/submit", human) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.dir, "selection-read")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("verification did not start")
		}
		time.Sleep(time.Millisecond)
	}
	q.Version = draft.Version
	q.Draft = &platformSelectionDraft{ChatIDs: []string{"oc_group_one"}}
	changed := selectionCall(t, h.m, "update", q, false)
	if err := os.WriteFile(filepath.Join(h.dir, "selection-release"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-result:
		if w.Code != 409 {
			t.Fatal("late verification replaced newer draft", w.Code, w.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("submission did not settle")
	}
	current := selectionCall(t, h.m, "status", selectionQuery(req, false), false)
	if current.Status != "waiting" || current.Version != changed.Version || !reflect.DeepEqual(current.Draft.ChatIDs, []string{"oc_group_one"}) || current.ResultVersion != 0 {
		t.Fatal(current)
	}
}
