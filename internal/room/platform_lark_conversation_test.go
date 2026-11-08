package room

import (
	"path/filepath"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestLarkHistoricalRelationshipSnapshotUsesAdapterCodec(t *testing.T) {
	h := newInboxHarness(t, InboxLimits{})
	item := ownLarkItem("historical", "om_historical", true)
	item.Content.Conversation.Type = "p2p"
	source := item.Source
	r := source.Reference
	proof := lark.ConversationRelation{Account: platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: source.Provider, Instance: source.Instance, Tenant: r.AccountTenant}, ID: r.RecipientID}, ChatID: r.ChatID.Value, Kind: "self"}
	path := filepath.Join(h.store.dir, "lark-conversations", referenceDigest("conversation_", source.Instance, r.RecipientID, r.AccountTenant, r.ChatID)+".json")
	h.store.mu.Lock()
	err := h.store.save(path, proof, maxPrivateFileBytes)
	var admission notification.Admission
	if err == nil {
		admission, err = h.store.inboxAdmissionLocked(InboxEntry{Item: item})
	}
	h.store.mu.Unlock()
	if err != nil || !admission.Verified || admission.Decision != notification.AdmissionExcluded {
		t.Fatal("existing native proof was lost during boundary refactoring", admission, err)
	}
}
