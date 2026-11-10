package room

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNotificationPreviewLocationPreservesSourceAndDoesNotConsume(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	item := ownLarkItem("navigation", "om_navigation", false)
	inboxCall[InboxReceipt](t, h, ReceiveInbox{MemberIdentity: h.id, Item: item})
	before := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	page := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	preview, err := h.store.projectInboxPreview(page, before)
	if err != nil || len(preview.Entries) != 1 {
		t.Fatal(preview, err)
	}
	location := preview.Entries[0].NotificationSource
	if location == nil || location.Provider != item.Source.Provider || location.Channel != item.Source.Channel || location.SubscriptionID != item.Source.SubscriptionID || location.Instance != item.Source.Instance {
		t.Fatal("preview lost exact source", location, item.Source)
	}
	// The shared Agent projection retains its existing source and consumption contract.
	shared := projectInboxEntry(page.Entries[0], preview.Entries[0].Source)
	data, err := json.Marshal(shared)
	if err != nil || shared.NotificationSource != nil || strings.Contains(string(data), "notification_source") {
		t.Fatal("navigation leaked into Agent input", string(data), err)
	}
	after := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	if after.ConsumedThrough != before.ConsumedThrough || after.LatestSeq != before.LatestSeq || after.Batch != nil {
		t.Fatal("preview consumed input", before, after)
	}
}
