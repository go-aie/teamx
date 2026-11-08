package lark

import (
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestNotificationProjectionOwnContextRequiresExactReceiverIdentity(t *testing.T) {
	id := platform.ExternalID{Namespace: "lark:open_id:cli_a", Value: "ou_reader"}
	item := notification.Item{Source: platform.SourceRef{Provider: "lark", Reference: platform.NotificationReference{RecipientID: id}}, Content: &notification.Content{Sender: &notification.Participant{ID: id, IsReceiver: true}}}
	project := NotificationCapabilities().Project
	if !project(item).ContextOnly {
		t.Fatal("historical own-message evidence was lost")
	}
	item.Content.Sender.ID.Namespace = "lark:open_id:cli_b"
	if project(item).ContextOnly {
		t.Fatal("same raw ID in another application counted as the receiver")
	}
	item.Content.Sender.ID, item.Content.Sender.IsReceiver = id, false
	if project(item).ContextOnly {
		t.Fatal("unverified sender counted as the receiver")
	}
}
