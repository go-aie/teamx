package room

import (
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestOwnGitlabInboxPreservesContextWithoutOpeningWork(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	initReferenceTest(t, h)
	source := referenceTestSource("own-note")
	item := h.receive("own-note").Item
	item.Source = source
	item.Content = &notification.Content{Text: "已发布的原图评论", Structured: true,
		Sender:  &notification.Participant{ID: source.Reference.RecipientID, IsReceiver: true},
		Context: &notification.Context{URL: source.URL}}
	for _, key := range []string{"manual", "agent"} {
		q := h.receive(key)
		q.Item = item
		q.Item.Key, q.Item.DeliveryID, q.Item.Source.ItemKey = key, key, key
		q.Item.CreatedAt = time.Now().UTC()
		inboxCall[InboxReceipt](t, h, q)
	}
	inboxRunning(t, h, 1, 2)
	page, err := referenceTestService(h).readReferenceInbox(h.ctx, h.id, 1, 25, referenceTestResolver)
	if err != nil || len(page.Entries) != 2 || len(page.Followups) != 0 {
		t.Fatal("own GitLab notifications opened new work", page, err)
	}
	projected, err := projectAgentInboxRead(h.id, page)
	if err != nil || !projected.Entries[0].Item.ContextOnly || !projected.Entries[1].Item.ContextOnly || projected.Entries[0].Origin.Kind != "account" {
		t.Fatal("native own-account context or honest provenance lost", projected, err)
	}
	inboxCall[InboxStatus](t, h, ConfirmInboxBatch{MemberIdentity: h.id, TurnNumber: 1, Receipt: page.Batch.Receipt})
	h.reload(t, true)
	preview := inboxCall[InboxPage](t, h, PreviewInbox{MemberIdentity: h.id})
	status := inboxCall[InboxStatus](t, h, InspectInbox{h.id})
	human, err := h.store.projectInboxPreview(preview, status)
	if err != nil || len(human.Entries) != 2 || !human.Entries[0].Item.ContextOnly || status.ConsumedThrough != 2 {
		t.Fatal("own GitLab progress or consumption lost on restart", human, err)
	}
	if caps := gitlab.NotificationCapabilities(); caps.Project == nil || caps.MatchAction == nil {
		t.Fatal("GitLab progress adapter missing")
	}
}
