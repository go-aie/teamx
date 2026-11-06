package room

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/lark"
)

func TestLarkMentionIdentityInAgentInboxWithoutReadBinding(t *testing.T) {
	h := newLarkHarness(t)
	m := lark.Message{ID: "om_mention", ChatID: "oc_chat", Type: "text", Created: "100000", Updated: "100000", Sender: lark.Sender{ID: "ou_sender", IDType: "open_id", Type: "user"}, Mentions: []lark.Mention{
		{Key: "@_user_1", ID: "ou_reader", IDType: "open_id", Name: "接收者"},
		{Key: "@_user_2", ID: "ou_other", IDType: "open_id", Name: "同事"},
	}}
	m.Body.Content = `{"text":"@_user_1 请和 @_user_2 确认发版"}`
	raw, err := json.Marshal([]lark.Message{m})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(h.dir, "messages.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	h.start()
	if err = h.deliver(); err != nil {
		t.Fatal(err)
	}
	member := h.members[0]
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	if err = h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedInboxSeq: 1, Turn: &TurnRecord{Number: 1, Status: "running", InboxSnapshotSeq: 1}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	previewBefore := inboxRoutingPreview(t, h.svc, id)
	if previewBefore.Entries[0].Source.Status != "not_checked" || previewBefore.Status.ConsumedThrough != 0 {
		t.Fatal("preview resolved or consumed a source", previewBefore)
	}
	cli := contractCLI(t, server.URL, h.svc, id)
	var inbox agentReferenceInboxPage
	if err = cli([]string{"inbox", "read"}, &inbox); err != nil || len(inbox.Entries) != 1 {
		t.Fatal("cannot read notification without member Lark binding", inbox, err)
	}
	text := inbox.Entries[0].Item.Text
	for _, want := range []string{"@接收者（open_id: ou_reader；当前接收账号）", "@同事（open_id: ou_other）", "确认发版"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Agent Inbox lost %q: %q", want, text)
		}
	}
	item := inbox.Entries[0].Item
	if !item.Structured || item.Sender == nil || item.Sender.Name != "测试发送者" || item.Sender.ID.Value != "ou_sender" || len(item.Mentions) != 2 || !item.Mentions[0].IsReceiver || item.Mentions[1].IsReceiver {
		t.Fatal("structured identities missing", item)
	}

	if strings.Contains(text, "@_user_") {
		t.Fatal("native mention placeholders reached Agent Inbox", text)
	}
	previewAfter := inboxRoutingPreview(t, h.svc, id)
	if !reflect.DeepEqual(previewBefore.Entries[0].Item, item) || !reflect.DeepEqual(previewAfter.Entries[0], inbox.Entries[0]) {
		t.Fatal("human and Agent notification views differ", previewAfter, inbox)
	}
	data, _ := json.Marshal(inbox.Entries[0])
	for _, key := range []string{`"subscription_id":`, `"item_key":`, `"delivery_id":`, `"route":`, `"order":`, `"version":`, `"key":`} {
		if strings.Contains(string(data), key) {
			t.Fatalf("internal field leaked: %s", key)
		}
	}
}
