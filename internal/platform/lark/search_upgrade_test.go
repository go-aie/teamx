package lark

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
)

func TestSearchUpgradePreservesIdentityProgressAndFrozenDeliveries(t *testing.T) {
	h, f, runner := newStepHarness(t)
	f.pages = func(MessagesRequest) (MessagePage, error) {
		return MessagePage{Items: []Message{sampleMessage("om_pending", "oc_chat", "pending")}, HasMore: true, PageToken: "old_history_token"}, nil
	}
	runner.Step(t.Context())
	runner.Step(t.Context())
	before := h.s.Clone()
	before.Config.Private.Scope = "all"
	before.Config.Groups = GroupConfig{Enabled: true, ChatIDs: []string{"oc_group"}, MessageMode: "mentions"} // own context only
	before.Chats["oc_group"] = ChatProgress{Chat: Chat{ID: "oc_group", Mode: "topic"}, Completed: 1005}
	raw, _ := json.Marshal(before)
	upgraded, err := UpgradeSearchState(raw, 3, before.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := (&SearchSource{}).Decode(upgraded, before.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	after := decoded.(State)
	if !after.Baseline.Equal(before.Baseline) || after.SubscriptionID != before.SubscriptionID || after.SubscriptionVersion != before.SubscriptionVersion || after.Binding != before.Binding || !reflect.DeepEqual(after.Inbox, before.Inbox) || !reflect.DeepEqual(after.Observed, before.Observed) || !reflect.DeepEqual(after.Accepted, before.Accepted) || after.Config.Groups.Scope != "specified" || after.Config.Groups.AtMe || after.Config.Groups.AtAll || after.Chats["oc_chat"].Scan.Token != "old_history_token" {
		t.Fatal("conversion changed saved evidence")
	}
	if len(after.Search.Branches) != 2 || after.Search.Branches[0].Kind != "private" || after.Search.Branches[1].Kind != "own" {
		t.Fatal("own-context compatibility", after.Search)
	}
	repeated, err := UpgradeSearchState(upgraded, SearchSchema, before.RoomIdentity)
	if err != nil || string(repeated) != string(upgraded) {
		t.Fatal("upgrade is not idempotent", err)
	}
	if _, err := (&Source{}).Decode(upgraded, before.RoomIdentity); err == nil {
		t.Fatal("old collector accepted new state")
	}
	if string(raw) == string(upgraded) {
		t.Fatal("no new schema state")
	}
}

func TestSearchUpgradePrunedEvidenceDoesNotReplayKnownChats(t *testing.T) {
	before := sampleState(t)
	before.Config.Private.Scope = "all"
	before.Chats["oc_known"] = ChatProgress{Chat: peerChat("oc_known", "p2p"), Completed: 5000}
	// The legacy worker already discarded old Observed/Accepted records. A search
	// must still start from baseline for unknown peers without redelivering knowns.
	raw, _ := json.Marshal(before)
	next, err := UpgradeSearchState(raw, 3, before.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := (&SearchSource{}).Decode(next, before.RoomIdentity)
	h, f, source := ingestionHarness(t, before.Config)
	h.s = decoded.(State)
	h.now = time.Unix(5100, 0)
	old := sampleMessage("om_old", "oc_known", "delivered long ago")
	newPeer := sampleMessage("om_unknown", "oc_unknown", "undiscovered backlog")
	f.chats[old.ChatID], f.chats[newPeer.ChatID] = peerChat(old.ChatID, "p2p"), peerChat(newPeer.ChatID, "p2p")
	f.messages[old.ID], f.messages[newPeer.ID] = old, newPeer
	f.search = func(q MessageSearchRequest) (MessageSearchPage, error) {
		p := MessageSearchPage{Items: []MessageSearchItem{}}
		for _, m := range []Message{old, newPeer} {
			item := discoveryItem(m, true)
			if validSearchCandidate(item, q) {
				p.Items = append(p.Items, item)
			}
		}
		return p, nil
	}
	ingestionSteps(t, h, source, 90)
	if len(h.s.Inbox) != 1 || h.s.Inbox[RevisionKey(h.s.Binding, newPeer)].Message.ID != newPeer.ID || !h.s.Baseline.Equal(before.Baseline) || !f.requests[0].Start.Equal(before.Baseline) {
		t.Fatal("unsafe legacy progress conversion", h.s.Inbox)
	}
	// A new selected chat also starts at baseline; narrowing back to known peers
	// uses the preserved progress instead of globally resetting it.
	cfg := h.s.Config
	cfg.Private.Scope = "specified"
	cfg.Private.ChatIDs = []string{"oc_known", "oc_new"}
	h.s, err = ConfigureSearch(h.s.RoomIdentity, &h.s, h.s.Binding, cfg, h.now)
	if err != nil {
		t.Fatal(err)
	}
	if h.s.searchStart(h.s.searchRequests()[0].q) != before.Baseline.Unix() {
		t.Fatal("new selected chat skipped backlog")
	}
}

func TestSearchUpgradeRejectsUnsafeOrUnknownSnapshots(t *testing.T) {
	s := sampleState(t)
	s.Chats["oc_chat"] = ChatProgress{Chat: peerChat("oc_chat", "p2p"), Completed: 5000, Scan: &Scan{Start: 1000, End: 5100}}
	raw, _ := json.Marshal(s)
	copy := string(raw)
	if _, err := UpgradeSearchState(raw, 3, s.RoomIdentity); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("ambiguous pruned interval accepted", err)
	}
	if string(raw) != copy {
		t.Fatal("failed conversion mutated input")
	}
	for _, version := range []int{0, 1, 2, 5, 99} {
		if _, err := UpgradeSearchState(raw, version, s.RoomIdentity); err == nil {
			t.Fatal("unknown schema accepted", version)
		}
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	fields["unexpected"] = json.RawMessage(`true`)
	raw, _ = json.Marshal(fields)
	if _, err := UpgradeSearchState(raw, 3, s.RoomIdentity); err == nil {
		t.Fatal("unknown field silently rewritten")
	}
}

func TestSearchCodecAndLegacyScopeAreFenced(t *testing.T) {
	s := sampleState(t)
	cfg := s.Config
	cfg.Groups = GroupConfig{Enabled: true, Scope: "all", MessageMode: "all"}
	if _, err := Configure(s.RoomIdentity, nil, s.Binding, cfg, time.Unix(2000, 0)); err == nil {
		t.Fatal("legacy adapter saved unsupported all-groups scope")
	}
	if _, err := ConfigureSearch(s.RoomIdentity, &s, s.Binding, cfg, time.Unix(2000, 0)); !errors.Is(err, notification.ErrConflict) {
		t.Fatal("implicit state migration", err)
	}
	raw, _ := json.Marshal(s)
	if _, err := (&SearchSource{}).Decode(raw, s.RoomIdentity); err == nil {
		t.Fatal("search adapter loaded old state")
	}
}
