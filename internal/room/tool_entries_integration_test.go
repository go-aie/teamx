//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func sharedToolAccount(t *testing.T, m *roomManager) managedPlatform {
	t.Helper()
	m.mu.Lock()
	file, err := m.platforms.Load()
	if err == nil {
		_, err = m.platforms.CommitOAuthLogin(nil, strings.Repeat("d", 32), "Personal GitLab", file.OAuthApplications[0], "", humanCaller.ID, platform.OAuthTokens{AccessToken: platform.NewSecret("second-access"), RefreshToken: platform.NewSecret("second-refresh"), ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"api"}}, platform.Validation{Status: "valid", CheckedAt: time.Now().UTC(), UserID: 202, Username: "user202"})
	}
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range platformTestList(t, m) {
		if p.Validation.UserID == 202 {
			return p
		}
	}
	t.Fatal("missing second account")
	return managedPlatform{}
}

func TestPlatformEntriesSharedGitLabAccountsRoutingAndDependencies(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, a, members := cliToolFixture(t, `if test "$1" = --help; then printf 'help'; else printf '%s|%s' "$GITLAB_TOKEN" "$1"; fi`)
	m.toolCatalog[0].Skill = tool.Catalog()[0].Skill
	b := sharedToolAccount(t, m)
	bindCLITool(t, m, room.RoomIdentity, members[0], a, "gitlab", 0)
	bindCLITool(t, m, room.RoomIdentity, members[0], b, "gitlab", 1)
	client, endpoint := toolPipeServer(t, m)
	call := func(args ...string) (string, error) {
		var out, stderr bytes.Buffer
		err := runToolsWithClient(t.Context(), args, bytes.NewReader(nil), &out, &stderr, cliToolEnv(endpoint, svc, members[0]), client)
		return out.String() + stderr.String(), err
	}
	list := func() []toolSelection {
		out, err := call("list")
		var selections []toolSelection
		if err != nil || json.Unmarshal([]byte(out), &selections) != nil || strings.Contains(out, "second-access") {
			t.Fatal(err, out)
		}
		return selections
	}
	if got := list(); len(got) != 2 || got[0].Name != a.ToolEntryID || got[1].Name != b.ToolEntryID || got[0].PlatformName != a.Name || got[1].ToolType != "gitlab" {
		t.Fatal(got)
	}
	for _, p := range []managedPlatform{a, b} {
		want := "test-platform-secret|api"
		if p.ID == b.ID {
			want = "second-access|api"
		}
		if out, err := call("run", p.ToolEntryID, "api"); err != nil || out != want {
			t.Fatal(err, out)
		}
		if out, err := call("run", p.ToolEntryID, "--help"); err != nil || out != "help" {
			t.Fatal(err, out)
		}
		if out, err := call("skill", p.ToolEntryID); err != nil || !strings.Contains(out, "tools run "+p.ToolEntryID+" --help") || strings.Contains(out, "tools run gitlab ") {
			t.Fatal("skill entry", err)
		}
	}
	if _, err := call("run", "gitlab", "api"); err == nil {
		t.Fatal("provider alias admitted")
	}
	m.mu.Lock()
	file, err := m.readToolBindingsLocked(room.RoomIdentity)
	m.mu.Unlock()
	if err != nil || len(file.Bindings) != 2 {
		t.Fatal("bindings did not reload", err)
	}
	// A rename preserves the invocation identifier and both bindings.
	a, err = m.updatePlatform(managedPlatformRequest{ID: a.ID, Version: a.Version, Name: "Renamed", Provider: a.Provider, URL: a.URL}, false)
	if err != nil || list()[0].Name != a.ToolEntryID || list()[0].PlatformName != "Renamed" {
		t.Fatal("rename", err)
	}
	// Missing CLI affects run only; HTTP reads/actions remain discoverable.
	bin := m.toolCatalog[0].Executable
	m.toolCatalog[0].Executable = filepath.Join(t.TempDir(), "missing")
	for _, s := range list() {
		if slices.Contains(s.Capabilities, "run") || !slices.Contains(s.Capabilities, "read") || s.UnavailableReason != "executable_unavailable" {
			t.Fatal(s)
		}
	}
	m.toolCatalog[0].Executable = bin
	for _, s := range list() {
		if !slices.Contains(s.Capabilities, "run") {
			t.Fatal("dependency repair not detected", s)
		}
	}
	// Admission and settlement use the selected grant, never its app owner.
	m.mu.Lock()
	_, ca, xa, errA := m.prepareToolLocked(t.Context(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: a.ToolEntryID, Args: []string{"api"}})
	_, cb, xb, errB := m.prepareToolLocked(t.Context(), toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: b.ToolEntryID, Args: []string{"api"}})
	m.mu.Unlock()
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	defer func() {
		if cb != nil {
			m.finishPlatformCall(cb)
		}
	}()
	done := make(chan int, 1)
	go func() {
		done <- managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: 2, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "gitlab", PlatformID: a.ID}}).Code
	}()
	select {
	case <-xa.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("A not canceled")
	}
	if xb.Err() != nil {
		t.Fatal("B canceled with A")
	}
	if out, err := call("run", b.ToolEntryID, "api"); err != nil || out != "second-access|api" {
		t.Fatal("B blocked by A settlement", err, out)
	}
	select {
	case <-done:
		t.Fatal("revocation did not join A")
	default:
	}
	m.finishPlatformCall(ca)
	select {
	case code := <-done:
		if code != 200 {
			t.Fatal(code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not settle")
	}
	if _, err := call("run", a.ToolEntryID, "api"); err == nil {
		t.Fatal("stale entry admitted")
	}
	if got := list(); len(got) != 1 || got[0].Name != b.ToolEntryID {
		t.Fatal(got)
	}
	if _, err := call("run", "gitlab", "api"); err == nil {
		t.Fatal("single-account provider alias admitted")
	}
	m.finishPlatformCall(cb)
	cb = nil
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, m.store.dir, managerTestProfiles(t))
	restored.toolCatalog = m.toolCatalog
	restored.mu.Lock()
	f, err := restored.readToolBindingsLocked(room.RoomIdentity)
	entries, selectionErr := restored.selectionsLocked(f, agentCaller(members[0].AgentID, members[0].MembershipID))
	restored.mu.Unlock()
	if err != nil || selectionErr != nil || len(f.Bindings) != 1 || len(entries) != 1 || entries[0].Name != b.ToolEntryID || entries[0].Username != b.Validation.Username {
		t.Fatal("restart changed binding or account", err, selectionErr, entries)
	}
}

func TestPlatformEntriesLarkIndependentProfilesAndBuiltinDispatch(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	h.m.toolCatalog[len(h.m.toolCatalog)-1].Skill = tool.Catalog()[1].Skill
	script := strings.Replace(l2Fixture, "path=a[2]", `if a==['config','show']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','dpopMode':'required'}));sys.exit()
path=a[2]`, 1)
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}

	b := secondLarkPlatform(t, h)
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], b, "lark", 1)
	client, endpoint := toolPipeServer(t, h.m)
	for _, p := range []managedPlatform{h.c, b} {
		var out, stderr bytes.Buffer
		if err := runToolsWithClient(t.Context(), []string{"run", p.ToolEntryID, "--help"}, bytes.NewReader(nil), &out, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err != nil || !strings.Contains(out.String(), "tools run "+p.ToolEntryID+" read") {
			t.Fatal(err, out.String())
		}
		out.Reset()
		if err := runToolsWithClient(t.Context(), []string{"skill", p.ToolEntryID}, nil, &out, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err != nil || !strings.Contains(out.String(), "tools run "+p.ToolEntryID+" read") {
			t.Fatal(err, out.String())
		}
		// The raw bounded endpoint shares the same entry admission and pinned
		// profile as tools run. Each fixture rejects the other profile identity.
		result, err := h.m.readLark(t.Context(), larkReadRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), Tool: p.ToolEntryID, MessageID: "om_000"})
		if err != nil || result.(lark.Message).ID != "om_000" {
			t.Fatal(err, result)
		}
		var discard bytes.Buffer
		if err := runToolsWithClient(t.Context(), []string{"run", p.ToolEntryID, "api", "POST", "/open-apis/im/v1/messages"}, bytes.NewReader(nil), &discard, &discard, cliToolEnv(endpoint, h.svc, h.members[0]), client); err == nil {
			t.Fatal("arbitrary CLI admitted")
		}
	}
	// No arbitrary default is introduced for direct reads with two accounts.
	if _, err := h.m.readLark(t.Context(), larkReadRequest{ServiceInfo: h.svc.info, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), MessageID: "om_000"}); err == nil {
		t.Fatal("ambiguous native read admitted")
	}
	// Exercise the actual tools-run dispatcher against a captured Inbox target.
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if err := h.svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	page, err := h.m.readReferenceInbox(t.Context(), apiRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), TurnNumber: 1, Limit: 25})
	if err != nil || len(page.Followups) != 1 {
		t.Fatal("capture reference", err, page)
	}
	ref := page.Followups[0].ObjectRef
	var output, stderr bytes.Buffer
	args := []string{"run", h.c.ToolEntryID, "read", "--object", ref, "--target", ref}
	if err := runToolsWithClient(t.Context(), args, bytes.NewReader(nil), &output, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err != nil || !strings.Contains(output.String(), "脱敏测试消息") {
		t.Fatal("built-in read", err, output.String(), stderr.String())
	}
	args[1] = b.ToolEntryID
	if err := runToolsWithClient(t.Context(), args, bytes.NewReader(nil), &output, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err == nil {
		t.Fatal("read switched away from source platform")
	}
	if runtime.GOOS == "darwin" {
		q, _ := json.Marshal(actionResolveRequest{Locator: platform.TargetLocator{TargetRef: page.Sources[0].TargetRef}, TurnNumber: 1})

		args = []string{"run", h.c.ToolEntryID, "actions", "resolve", "--request", string(q)}
		output.Reset()
		if err := runToolsWithClient(t.Context(), args, bytes.NewReader(nil), &output, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err != nil || !strings.Contains(output.String(), "target_ref") {
			t.Fatal("built-in action resolve", err, output.String(), stderr.String())
		}
		args[1] = b.ToolEntryID
		if err := runToolsWithClient(t.Context(), args, bytes.NewReader(nil), &output, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err == nil {
			t.Fatal("action resolve crossed original platform")
		}
	}
	// Dependency failure preserves intent, hides only the affected entry and
	// restores it on the next check after the executable is repaired.
	bin := b.CLIProfile.Executable
	if err := os.Rename(bin, bin+".bak"); err != nil {
		t.Fatal(err)
	}
	members := agentCaller(h.members[0].AgentID, h.members[0].MembershipID)
	h.m.mu.Lock()
	f, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
	s, listErr := h.m.selectionsLocked(f, members)
	h.m.mu.Unlock()
	if err != nil || listErr != nil || len(f.Bindings) != 2 || len(s) != 2 || len(s[0].Capabilities) == 0 || len(s[1].Capabilities) != 0 {
		t.Fatal("dependency isolation", err, listErr, s)
	}
	if err := os.Rename(bin+".bak", bin); err != nil {
		t.Fatal(err)
	}
	// Explicit entry and captured platform evidence must agree, including for
	// native managed actions that restore a saved access context.
	h.m.mu.Lock()
	account, _, err := h.m.resolvePlatformAccountLocked(id.RoomIdentity, b.ID, "lark")
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := h.m.withToolEntry(context.Background(), toolsRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Tool: h.c.ToolEntryID})
	if err != nil {
		t.Fatal(err)
	}
	if session, err := h.m.prepareLarkReferenceAction(ctx, id, larkReferenceAccess(id, account, platform.SourceRef{})); err == nil {
		session.Close()
		t.Fatal("entry crossed reference account")
	}
}

func TestPlatformEntryBuiltinActionExecutionRetainsReceiptAndAccount(t *testing.T) {
	parallelFixtureTest(t)
	h := selfSendHarness(t)
	id, _ := enableSelfSend(t, h, h.members[0], 0)
	client, endpoint := toolPipeServer(t, h.m)
	run := func(entry, op, payload string, out any) error {
		var stdout, stderr bytes.Buffer
		if err := runToolsWithClient(t.Context(), []string{"run", entry, "actions", op, "--request", payload}, bytes.NewReader(nil), &stdout, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client); err != nil {
			return err
		}
		return json.Unmarshal(stdout.Bytes(), out)
	}
	var target actionTargetView
	if err := run(h.c.ToolEntryID, "resolve", `{"locator":{"self":true},"turn_number":1}`, &target); err != nil || len(target.Actions) != 1 || !target.Actions[0].Executable {
		t.Fatal("self target", err, target)
	}
	q := agentActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: target.TargetRef, ID: "entry-self-send", Operation: platform.OperationMessageSendSelf, Input: ActionInput{Body: "P4 isolated fixture content"}}
	data, _ := json.Marshal(q)
	var receipt referenceActionReceipt
	if err := run(h.c.ToolEntryID, "execute", string(data), &receipt); err != nil || receipt.Status != "succeeded" || receipt.Attempts != 1 {
		t.Fatal("built-in action", err, receipt)
	}
	if err := run(h.c.ToolEntryID, "execute", string(data), &receipt); err != nil || receipt.Status != "succeeded" || len(selfSendPosts(t, h.dir)) != 1 {
		t.Fatal("receipt replayed write", err, receipt)
	}
	stored, err := h.svc.store.queryActions(id, q.ID, "", "")
	if err != nil || len(stored) != 1 || stored[0].Access.PlatformID != h.c.ID {
		t.Fatal("receipt account", err)
	}
	b := secondLarkPlatform(t, h)
	bindCLITool(t, h.m, id.RoomIdentity, h.members[0], b, "lark", 1)
	if err := run(b.ToolEntryID, "execute", string(data), &receipt); err == nil || len(selfSendPosts(t, h.dir)) != 1 {
		t.Fatal("other entry restored or replayed an action")
	}
}

func TestPlatformEntryBuiltinSlowReaderRevocationSettles(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarness(t)
	m := lark.Message{ID: "om_000", ChatID: "oc_chat", Type: "text", Created: "100000", Updated: "100000", Sender: lark.Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}}
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("x", 7000)})
	m.Body.Content = string(body)
	data, _ := json.Marshal([]lark.Message{m})
	if err := os.WriteFile(filepath.Join(h.dir, "messages.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	if err := h.svc.store.initReferenceFollowups(id); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	page, err := h.m.readReferenceInbox(t.Context(), apiRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), TurnNumber: 1, Limit: 25})
	if err != nil || len(page.Followups) != 1 {
		t.Fatal(err)
	}
	bindCLITool(t, h.m, id.RoomIdentity, h.members[0], h.c, "lark", 0)
	client, endpoint := toolPipeServer(t, h.m)
	ref := page.Followups[0].ObjectRef
	data, _ = json.Marshal(toolsRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Tool: h.c.ToolEntryID, Args: []string{"read", "--object", ref, "--target", ref}})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, endpoint+"/v1/tools/run", bytes.NewReader(append(data, '\n')))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal(response.Status)
	}
	// Deliberately leave the response body unread while its large frame blocks.
	done := make(chan int, 1)
	go func() {
		done <- managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Revision: 1, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", PlatformID: h.c.ID}}).Code
	}()
	select {
	case code := <-done:
		if code != 200 {
			t.Fatal("slow reader blocked settlement", code)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("slow reader retained builtin call")
	}
}
