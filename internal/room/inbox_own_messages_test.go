package room

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func ownLarkItem(key, message string, own bool) notification.Item {
	accountID := platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}
	chat := platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_chat"}
	sender := accountID
	if !own {
		sender.Value = "ou_other"
	}
	return notification.Item{Key: key, DeliveryID: "own-" + key, Text: "飞书\n发送者：我\n已处理", CreatedAt: time.Now().UTC(), Source: platform.SourceRef{
		Provider: "lark", Channel: "messages", Instance: lark.Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "external_tenant"}, SubscriptionID: strings.Repeat("a", 32), ItemKey: key,
		Reference: platform.NotificationReference{Version: 2, RecipientID: accountID, AccountTenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "account_tenant"}, ChatID: chat,
			Target: platform.ObjectReference{Type: "message", ID: platform.ExternalID{Namespace: "lark:message_id:cli_fixture", Value: message}}}},
		Content: &notification.Content{Structured: true, Text: "已处理", Sender: &notification.Participant{ID: sender, IsReceiver: own, Type: "user"}, Conversation: &notification.Context{ID: chat, Type: "group"}}}
}

func TestOwnLarkInboxPreservesContextWithoutOpeningWork(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	items := []notification.Item{
		ownLarkItem("own-first", "om_root", true),
		ownLarkItem("incoming", "om_incoming", false),
		ownLarkItem("own-reply", "om_reply", true),
		ownLarkItem("own-standalone", "om_standalone", true),
	}
	for i := 1; i <= 2; i++ {
		items[i].Source.Reference.RootID = items[0].Source.Reference.Target.ID
		items[i].Source.Reference.ParentID = items[0].Source.Reference.Target.ID
	}
	items[3].Source.Reference.DiscussionID = platform.ExternalID{Namespace: "lark:thread_id:cli_fixture", Value: "omt_own"}
	for _, item := range items {
		q := ReceiveInbox{MemberIdentity: h.id, Item: item}
		first := inboxCall[InboxReceipt](t, h, q)
		if replay := inboxCall[InboxReceipt](t, h, q); replay != first {
			t.Fatal("own message was duplicated", first, replay)
		}
	}
	inboxRunning(t, h, 1, 4)
	svc := referenceTestService(h)
	resolve := func(_ context.Context, _ MemberIdentity, source platform.SourceRef) (platform.TargetResolution, platform.AccessContext, error) {
		return lark.ResolveNotification(source, time.Now().UTC()), platform.AccessContext{}, nil
	}
	page, err := svc.readReferenceInbox(h.ctx, h.id, 1, 25, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Entries) != 4 || len(page.Sources) != 4 || len(page.Followups) != 1 {
		t.Fatal("context lost or own messages opened work", page)
	}
	projected, err := projectAgentInboxRead(h.id, page)
	if err != nil || !projected.Entries[0].Item.ContextOnly || projected.Entries[1].Item.ContextOnly || !projected.Entries[2].Item.ContextOnly || projected.Entries[0].Origin.Kind != "account" {
		t.Fatal("human/agent message contexts not distinguished", projected, err)
	}
	if page.Sources[0].ObjectRef != page.Sources[1].ObjectRef || page.Sources[1].ObjectRef != page.Sources[2].ObjectRef {
		t.Fatal("native reply linkage lost", page.Sources)
	}
	if _, err := h.store.ensureDiscussionFollowup(h.id, 4, 1, page.Sources[3].TargetRef); !errors.Is(err, ErrConflict) {
		t.Fatal("own thread created independent work", err)
	}
	if _, err := h.store.readFollowup(h.id, page.Sources[3].ObjectRef, referenceDigest("unit_", page.Sources[3].ObjectRef)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("own standalone message created a unit", err)
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	h.reload(t, true)
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if status.LatestSeq != 4 || status.ConsumedThrough != 4 {
		t.Fatal("context-only consumption did not survive restart", status)
	}
	preview := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	human, err := h.store.projectInboxPreview(preview, status)
	if err != nil || len(human.Entries) != 4 || !human.Entries[3].Item.ContextOnly || human.Entries[0].Source.ObjectRef != page.Sources[0].ObjectRef {
		t.Fatal("human preview or source linkage lost after restart", human, err)
	}
}
