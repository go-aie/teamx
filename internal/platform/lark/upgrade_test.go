package lark

import (
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform"
	"reflect"
	"testing"
)

func legacyStateJSON(t *testing.T, s State) []byte {
	t.Helper()
	b, _ := json.Marshal(s)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(b, &fields)
	old := legacyConfig{Enabled: s.Config.Enabled, PlatformID: s.Binding.PlatformID, Chats: s.Config.Private.ChatIDs, Target: s.Config.Target, Private: true, GroupMentions: true, IntervalSeconds: 60, BackfillSeconds: s.Config.BackfillSeconds, BackfillStart: s.Config.BackfillStart}
	fields["config"], _ = json.Marshal(old)
	b, _ = json.Marshal(fields)
	return b
}
func TestLarkUpgradePreservesProgressFrozenRoutesAndIdentity(t *testing.T) {
	h, f, r := newStepHarness(t)
	f.pages = func(MessagesRequest) (MessagePage, error) {
		return MessagePage{Items: []Message{sampleMessage("om_one", "oc_chat", "hello")}, HasMore: true, PageToken: "original_cursor"}, nil
	}
	r.Step(t.Context())
	r.Step(t.Context())
	s := h.s
	raw := legacyStateJSON(t, s)
	next, err := UpgradeState(raw, s.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(raw, &before)
	_ = json.Unmarshal(next, &after)
	delete(before, "config")
	delete(after, "config")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("upgrade changed business evidence")
	}
	saved, err := (&Source{}).Decode(next, s.RoomIdentity)
	if err != nil || saved.(State).Config.Private.Scope != "specified" || saved.(State).Config.Groups.Enabled {
		t.Fatal("upgrade applied new defaults", err)
	}
}
func TestLarkUpgradeRejectsUnprovenAndUnsupportedScopes(t *testing.T) {
	s := sampleState(t)
	s.Chats["oc_chat"] = ChatProgress{Chat: peerChat("oc_chat", "p2p")}
	for _, mode := range []string{"bots", "all", "unverified", "self", "private_off", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			raw := legacyStateJSON(t, s)
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(raw, &fields)
			var cfg legacyConfig
			_ = json.Unmarshal(fields["config"], &cfg)
			switch mode {
			case "bots":
				cfg.Bots = true
			case "all":
				cfg.ScopeMode = "all"
				cfg.Chats = nil
			case "unverified":
				fields["chats"] = json.RawMessage(`{}`)
			case "self":
				p := s.Chats["oc_chat"]
				p.Chat.Relation.Kind = "self"
				p.Chat.Relation.Peer = platform.ExternalID{}
				fields["chats"], _ = json.Marshal(map[string]ChatProgress{"oc_chat": p})
			case "private_off":
				cfg.Private = false
			case "unknown":
				cfg.ScopeMode = "invalid"
			}
			fields["config"], _ = json.Marshal(cfg)
			raw, _ = json.Marshal(fields)
			copy := append([]byte(nil), raw...)
			if _, err := UpgradeState(raw, s.RoomIdentity); err == nil {
				t.Fatal("lossy upgrade accepted")
			}
			if string(copy) != string(raw) {
				t.Fatal("rejection mutated source")
			}
		})
	}
}

func TestLarkUpgradeSplitsVerifiedPrivateAndGroupChoices(t *testing.T) {
	s := sampleState(t)
	s.Chats["oc_chat"] = ChatProgress{Chat: peerChat("oc_chat", "p2p")}
	s.Chats["oc_group"] = ChatProgress{Chat: Chat{ID: "oc_group", Mode: "topic"}}
	raw := legacyStateJSON(t, s)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	var old legacyConfig
	_ = json.Unmarshal(fields["config"], &old)
	old.Chats = []string{"oc_chat", "oc_group"}
	old.GroupAll = true
	old.AtAll = true
	fields["config"], _ = json.Marshal(old)
	raw, _ = json.Marshal(fields)
	next, err := UpgradeState(raw, s.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	state, err := (&Source{}).Decode(next, s.RoomIdentity)
	if err != nil {
		t.Fatal(err)
	}
	cfg := state.(State).Config
	if !cfg.Private.Enabled || !reflect.DeepEqual(cfg.Private.ChatIDs, []string{"oc_chat"}) || !cfg.Groups.Enabled || !reflect.DeepEqual(cfg.Groups.ChatIDs, []string{"oc_group"}) || cfg.Groups.MessageMode != "all" || !cfg.Groups.AtMe || !cfg.Groups.AtAll {
		t.Fatal("mixed choices changed", cfg)
	}
	// Group-only bot-authored messages belong to the existing group policy;
	// they do not imply a private-bot subscription.
	old.Chats = []string{"oc_group"}
	old.Bots = true
	fields["config"], _ = json.Marshal(old)
	raw, _ = json.Marshal(fields)
	if _, err := UpgradeState(raw, s.RoomIdentity); err != nil {
		t.Fatal("group policy mistaken for private-bot behavior", err)
	}
}
