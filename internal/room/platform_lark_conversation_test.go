package room

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestLarkSelfConversationSharedAccountSourceAndInboxRecovery(t *testing.T) {
	h := newLarkHarness(t)
	r2 := managerCreate(t, h.m, "self-second-room")
	_, snap := readManagedRoom(t, h.m, r2)
	svc2 := eventLiveService(t, &eventHarness{m: h.m, room: r2})
	h.m.bindPlatformReferences(svc2)
	other := &larkHarness{t, h.m, r2, svc2, snap.Members, h.c, h.dir, h.clock}
	// Seed pre-S1 snapshots using ordinary collection, then remove the new
	// relationship evidence. Preserve each frozen source/delivery identity.
	for _, room := range []*larkHarness{h, other} {
		if room == other {
			h.clock.add(-3 * time.Second)
		}
		room.start()
		s := room.state()
		p := s.Chats["oc_chat"]
		p.Chat.Relation = lark.ConversationRelation{}
		p.Chat.TenantKey = s.Binding.Account.Tenant.Value
		p.NeedsVerification = true
		s.Chats["oc_chat"] = p
		for key, e := range s.Inbox {
			e.Item.Source.Tenant = s.Binding.Account.Tenant
			s.Inbox[key] = e
		}
		room.m.mu.Lock()
		err := room.m.notifications.SaveLocked(s)
		room.m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if room == other {
			for _, e := range s.Inbox {
				if err := svc2.receiveInbox(t.Context(), e.Item.Route.Target, e.Item); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	script := strings.Replace(l2Fixture, "'tenant_key':'external_tenant'}\nelse:", "'tenant_key':'account_tenant'}\nelse:", 1)
	script = strings.Replace(script, ",{'member_id':'ou_sender','member_id_type':'open_id','tenant_key':'external_tenant'}", "", 1)
	// A self conversation never needs automatic body reads. Explicit lark read
	// still uses the separate Messages API; this guard covers only this test run.
	script = strings.Replace(script, " messages=json.load(open(base+'/messages.json'))", " raise Exception('self body scan')\n messages=json.load(open(base+'/messages.json'))", 1)
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s := h.state()
	h.m.mu.Lock()
	runner := h.m.notifications.NewRunnerLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	runner.Step(t.Context())
	runner.Step(t.Context())
	if got := h.state(); got.Chats["oc_chat"].Chat.Relation.Kind != "self" || len(got.PendingItems()) != 0 || len(got.Inbox) != 1 {
		t.Fatal("source self history leaked or vanished", got.Chats["oc_chat"])
	}
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	// Room 2 has not verified its old source yet. It must reuse the native proof
	// for the same account from Room 1, even without a member read-tool binding.
	member := other.members[0]
	id := MemberIdentity{RoomIdentity: r2.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	status, err := svc2.memberInboxStatus(t.Context(), id)
	if err != nil || status.AutomaticReady || status.AutomaticBlocked || status.LatestSeq != 1 || (&MemberAgent{inbox: &status}).inboxReady() {
		t.Fatal("cross-Room history scheduled work", status, err)
	}
	root := h.m.store.dir
	if err := h.m.close(); err != nil {
		t.Fatal(err)
	}
	restored := testManager(t, root, managerTestProfiles(t))
	for _, room := range []*larkHarness{h, other} {
		room.m = restored
		room.svc = eventLiveService(t, &eventHarness{m: restored, room: room.room})
		restored.bindPlatformReferences(room.svc)
		if err := room.deliver(); err != nil {
			t.Fatal(err)
		}
	}
	status, err = other.svc.memberInboxStatus(context.Background(), id)
	if err != nil || status.AutomaticReady || status.AutomaticBlocked || status.LatestSeq != 1 {
		t.Fatal("independent manager/Store reopen lost self exclusion", status, err)
	}
}

func TestLarkHistoricalConversationUsesCurrentCredentialForSameAccount(t *testing.T) {
	h := newLarkHarness(t)
	h.start()
	s := h.state()
	var source platform.SourceRef
	for _, e := range s.Inbox {
		source = e.Item.Source
	}
	p := s.Chats["oc_chat"]
	p.Chat.Relation = lark.ConversationRelation{}
	s.Chats["oc_chat"] = p
	// Historical credentials are evidence, never credentials to execute with.
	s.Binding.CredentialVersion++
	s.Binding.Fingerprint = strings.Repeat("e", 64)
	s, err := lark.Retire(s)
	if err != nil {
		t.Fatal(err)
	}
	h.m.mu.Lock()
	err = h.m.notifications.SaveLocked(s)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.m.inboxLarkConversation(t.Context(), h.svc, source)
	if err != nil || r.Kind != "peer" || r.Account != s.Binding.Account {
		t.Fatal("retired history could not be classified under current verified account", r, err)
	}
	h.m.mu.Lock()
	state, ok := h.m.notifications.StateLocked(s.Metadata().Key)
	h.m.mu.Unlock()
	if !ok {
		t.Fatal("retired evidence lost")
	}
	if got := state.(lark.State); !got.Retired || got.Config.Enabled {
		t.Fatal("classification resumed retired subscription")
	}
}
