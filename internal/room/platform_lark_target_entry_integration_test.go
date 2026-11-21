//go:build integration || e2e

package room

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestLarkSavedTargetExactEntryRead(t *testing.T) {
	parallelFixtureTest(t)
	h := newLarkHarnessWithMembers(t, 2)
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id)
	var inbox agentReferenceInboxPage
	if err := cli([]string{"inbox", "read"}, &inbox); err != nil || len(inbox.Followups) != 1 {
		t.Fatal(err, inbox)
	}
	object := inbox.Followups[0].ObjectRef
	read := func(entry, scope, cursor string) (larkTargetPage, error) {
		args := []string{"tools", "run", entry, "read", "--object", object, "--target", object, "--scope", scope}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		var page larkTargetPage
		err := cli(args, &page)
		return page, err
	}
	if _, err := read(h.c.ToolEntryID, "content", ""); err == nil {
		t.Fatal("notification evidence granted remote access without a binding")
	}
	bindCLITool(t, h.m, h.room.RoomIdentity, member, h.c, "lark", 0)
	h.messages(30)
	// The retired root CLI used this saved-target endpoint. Keep comparing its
	// response with the retained CLI -> tools/run -> Runtime -> Host path.
	compare := func(scope, cursor string) larkTargetPage {
		t.Helper()
		var original larkTargetPage
		client := cliClient{endpoint: server.URL, http: server.Client()}
		err := client.request(t.Context(), "/v1/platform/lark/target", larkReadRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Target: &larkTargetRequest{ObjectRef: object, TargetRef: object, Scope: scope, Cursor: cursor}}, &original)
		page, entryErr := read(h.c.ToolEntryID, scope, cursor)
		if err != nil || entryErr != nil || !reflect.DeepEqual(original, page) {
			t.Fatal("saved-target transports disagree", err, entryErr, original, page)
		}
		if h.m.platformCalls.Len() != 0 {
			t.Fatal("read returned before calls settled")
		}
		return page
	}
	content := compare("content", "")
	if content.Status != platform.ResolutionResolved || !content.Complete || len(content.Contents) != 1 {
		t.Fatal(content)
	}
	var defaultScope larkTargetPage
	if err := cli([]string{"tools", "run", h.c.ToolEntryID, "read", "--object", object, "--target", object, "--json"}, &defaultScope); err != nil || !reflect.DeepEqual(content, defaultScope) {
		t.Fatal("default content scope/JSON compatibility changed", err, defaultScope)
	}
	for _, caller := range []MemberIdentity{
		{RoomIdentity: id.RoomIdentity, AgentID: h.members[1].AgentID, MembershipID: h.members[1].MembershipID},
		{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: strings.Repeat("e", 32)},
	} {
		otherCLI := contractCLI(t, server.URL, h.svc, caller)
		if err := otherCLI([]string{"tools", "run", h.c.ToolEntryID, "read", "--object", object, "--target", object}, nil); err == nil {
			t.Fatal("saved target crossed member identity")
		}
	}
	if w := managerHTTP(h.m, "/v1/tools/run", toolsRequest{ServiceInfo: h.svc.info, Caller: humanCaller, Tool: h.c.ToolEntryID, Args: []string{"read", "--object", object, "--target", object}}); w.Code == 200 {
		t.Fatal("human bypassed member admission")
	}
	first := compare("context", "")
	if first.Complete || first.Next == "" || len(first.Contents) != 25 || first.ReasonCode != "page_incomplete" {
		t.Fatal(first)
	}
	last := compare("context", first.Next)
	if !last.Complete || last.Next != "" || len(last.Contents) != 5 {
		t.Fatal(last)
	}
	for _, q := range [][2]string{{"content", first.Next}, {"context", "bad-cursor"}, {"unsupported", ""}} {
		if _, err := read(h.c.ToolEntryID, q[0], q[1]); err == nil {
			t.Fatal("invalid scope/cursor accepted", q[0])
		}
	}
	// Preserve an explicit unavailable result rather than silently returning no input.
	h.messages(0)
	missing := compare("content", "")
	if missing.Status != platform.ResolutionUnavailable || missing.ReasonCode == "" || missing.Complete {
		t.Fatal(missing)
	}
	h.messages(30)
	// Rotate the fixture credential revision as native login does, then recover
	// the same account. Neither the saved target nor the binding is recreated.
	h.m.mu.Lock()
	snapshot, err := h.m.platforms.Load()
	if err != nil {
		h.m.mu.Unlock()
		t.Fatal(err)
	}
	invalidated, err := h.m.platforms.InvalidateCLIProfile(snapshot.Platforms)
	h.m.mu.Unlock()
	if err != nil || len(invalidated) != 1 {
		t.Fatal(err)
	}
	if _, err := read(h.c.ToolEntryID, "content", ""); err == nil {
		t.Fatal("invalidated credentials still allowed reads")
	}
	checked, err := h.m.verifyPlatform(t.Context(), managedPlatformRequest{ID: h.c.ID, Version: invalidated[0].Version})
	if err != nil || checked.Validation.Status != "valid" {
		t.Fatal(err)
	}
	compare("content", "")
	if _, err := read(h.c.ToolEntryID, "context", first.Next); err == nil {
		t.Fatal("old access cursor survived re-verification")
	}
	// Another explicit connection must not override the source account, even
	// when both profiles authenticate the same native user.
	other := platformTestSave(t, h.m, managedPlatformRequest{ServiceEpoch: h.m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "other", Provider: "lark", URL: h.c.URL, CLIProfile: h.c.CLIProfile})
	other, err = h.m.verifyPlatform(t.Context(), managedPlatformRequest{ID: other.ID, Version: other.Version})
	if err != nil || other.Validation.Status != "valid" {
		t.Fatal(err)
	}
	bindCLITool(t, h.m, h.room.RoomIdentity, member, other, "lark", 1)
	if _, err := read(other.ToolEntryID, "content", ""); err == nil {
		t.Fatal("explicit entry switched saved-target connection")
	}
	compare("content", "")
	if _, err := read("lark", "content", ""); err == nil {
		t.Fatal("provider alias accepted instead of exact entry")
	}
	// Native account switching must discard remote candidate content.
	script := strings.ReplaceAll(larkFixtureScript(), "ou_reader", "ou_other")
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	page, err := read(h.c.ToolEntryID, "content", "")
	if err != nil || page.Status != platform.ResolutionUnavailable || page.ReasonCode != "identity_mismatch" || len(page.Contents) != 0 {
		t.Fatal("account mismatch leaked candidate content", err, page)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(larkFixtureScript()), 0700); err != nil {
		t.Fatal(err)
	}
	// Restore the original saved target and exact entry through a fresh manager.
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	h.m = testManager(t, root, managerTestProfiles(t))
	h.svc = eventLiveService(t, &eventHarness{m: h.m, room: h.room})
	h.m.bindPlatformReferences(h.svc)
	server.Close()
	server = httptest.NewServer(h.m)
	defer server.Close()
	cli = contractCLI(t, server.URL, h.svc, id)
	compare("content", "")
	// A large body remains explicitly incomplete with the same bounded result.
	m := lark.Message{ID: "om_000", ChatID: "oc_chat", Type: "text", Created: "100000", Updated: "100000", Sender: lark.Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}}
	body, _ := json.Marshal(map[string]string{"text": strings.Repeat("正文", 5000)})
	m.Body.Content = string(body)
	data, _ := json.Marshal([]lark.Message{m})
	if err := os.WriteFile(filepath.Join(h.dir, "messages.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	truncated := compare("content", "")
	if truncated.Complete || truncated.ReasonCode != "body_truncated" || len(truncated.Contents) != 1 || truncated.Contents[0].Complete {
		t.Fatal(truncated)
	}
	t.Log("saved-target HTTP/entry equivalence: content, context 25+5, unavailable, truncation, same-account verification and restart; wrong entry/member/account/cursor rejected")
}
