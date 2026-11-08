package lark

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/go-aie/teamx/internal/platform"
)

// ConversationRelation is native relationship evidence, scoped to the actual
// account (including app namespace, tenant and instance), never to a sender.
// P2P relationships are immutable; incomplete evidence is never a peer proof.
type ConversationRelation struct {
	Account platform.AccountIdentity `json:"account"`
	ChatID  string                   `json:"chat_id"`
	Kind    string                   `json:"kind"` // self, peer, group
	Peer    platform.ExternalID      `json:"peer,omitzero"`
}

func (r ConversationRelation) ValidFor(a platform.AccountIdentity, id string) bool {
	if r.Account != a || !a.Valid() || a.Provider != "lark" || (a.Instance != Origin("feishu") && a.Instance != Origin("lark")) || a.Tenant.Namespace != "lark:tenant_key" || !strings.HasPrefix(a.ID.Namespace, "lark:open_id:cli_") || !nativeID(a.ID.Value, "ou_") || !nativeID(id, "oc_") || r.ChatID != id {
		return false
	}
	switch r.Kind {
	case "self", "group":
		return r.Peer == (platform.ExternalID{})
	case "peer":
		return r.Peer.Namespace == a.ID.Namespace && nativeID(r.Peer.Value, "ou_") && r.Peer != a.ID
	}
	return false
}

// NotificationChat verifies P2P membership before admitting automatic input.
// Ordinary Chat/Messages reads remain available without this extra permission.
func (c *Client) NotificationChat(ctx context.Context, a platform.AccountIdentity, id string) (Chat, error) {
	chat, err := c.Chat(ctx, a, id)
	if err != nil {
		return Chat{}, err
	}
	r, err := c.conversation(ctx, a, chat)
	if err != nil {
		return Chat{}, err
	}
	chat.Relation = r
	return chat, nil
}

func (c *Client) conversation(ctx context.Context, a platform.AccountIdentity, chat Chat) (ConversationRelation, error) {
	r := ConversationRelation{Account: a, ChatID: chat.ID}
	if !chat.Valid() {
		return ConversationRelation{}, failure("invalid_request")
	}
	if chat.Mode != "p2p" {
		r.Kind = "group"
		return r, nil // Chat was independently read under this account.
	}
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		var members struct {
			Items *[]struct {
				ID     string `json:"member_id"`
				Type   string `json:"member_id_type"`
				Tenant string `json:"tenant_key"`
			} `json:"items"`
			More *bool `json:"has_more"`
		}
		if err := c.api(ctx, "/open-apis/im/v1/chats/"+chat.ID+"/members", map[string]any{"member_id_type": "open_id", "page_size": 100}, &members); err != nil {
			return err
		}
		if members.Items == nil || members.More == nil || *members.More || len(*members.Items) < 1 || len(*members.Items) > 2 {
			return failure("conversation_unverified")
		}
		seen := map[string]bool{}
		for _, u := range *members.Items {
			if u.Type != "open_id" || !nativeID(u.ID, "ou_") || u.Tenant == "" || seen[u.ID] {
				return failure("conversation_unverified")
			}
			seen[u.ID] = true
			if u.ID == a.ID.Value {
				if u.Tenant != a.Tenant.Value {
					return failure("conversation_unverified")
				}
			} else {
				r.Peer = platform.ExternalID{Namespace: a.ID.Namespace, Value: u.ID}
			}
		}
		if !seen[a.ID.Value] {
			return failure("conversation_unverified")
		}
		peer := r.Peer.Value
		if len(*members.Items) == 1 {
			if chat.TenantKey != a.Tenant.Value {
				return failure("conversation_unverified")
			}
			r.Kind, peer = "self", a.ID.Value
		} else {
			r.Kind = "peer"
		}
		// Independently bind the exact native P2P chat to self/the other member.
		// A one-member list alone is not proof of a self conversation.
		var wire struct {
			Chats []struct {
				ID string `json:"chat_id"`
			} `json:"p2p_chats"`
		}
		body, _ := json.Marshal(map[string]any{"chatter_ids": []string{peer}})
		if err := c.apiJSON(ctx, []string{"api", "POST", "/open-apis/im/v1/chat_p2p/batch_query", "--params", `{"chatter_id_type":"open_id"}`, "--data", string(body)}, &wire); err != nil {
			return err
		}
		if len(wire.Chats) != 1 || wire.Chats[0].ID != chat.ID {
			return failure("conversation_unverified")
		}
		return nil
	})
	if err != nil || !r.ValidFor(a, chat.ID) {
		if err == nil {
			err = failure("conversation_unverified")
		}
		return ConversationRelation{}, err
	}
	return r, nil
}

func notificationChat(ctx context.Context, reader MessageReader, a platform.AccountIdentity, id string) (Chat, error) {
	var chat Chat
	var err error
	if r, ok := reader.(interface {
		NotificationChat(context.Context, platform.AccountIdentity, string) (Chat, error)
	}); ok {
		chat, err = r.NotificationChat(ctx, a, id)
	} else {
		chat, err = reader.Chat(ctx, a, id)
	}
	if err == nil && chat.Mode == "p2p" && (!chat.Relation.ValidFor(a, id) || chat.Relation.Kind != "self" && chat.Relation.Kind != "peer") {
		err = failure("conversation_unverified")
	}
	return chat, err
}

// SelfChat discovers the current account's native self mapping, then independently
// proves its complete membership using the same contract as notification exclusion.
func (c *Client) SelfChat(ctx context.Context, a platform.AccountIdentity) (Chat, error) {
	var wire struct {
		Chats []struct {
			ID string `json:"chat_id"`
		} `json:"p2p_chats"`
	}
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		body, _ := json.Marshal(map[string]any{"chatter_ids": []string{a.ID.Value}})
		return c.apiJSON(ctx, []string{"api", "POST", "/open-apis/im/v1/chat_p2p/batch_query", "--params", `{"chatter_id_type":"open_id"}`, "--data", string(body)}, &wire)
	})
	if err != nil {
		return Chat{}, err
	}
	if len(wire.Chats) != 1 || !nativeID(wire.Chats[0].ID, "oc_") {
		return Chat{}, failure("conversation_unverified")
	}
	chat, err := c.NotificationChat(ctx, a, wire.Chats[0].ID)
	if err != nil {
		return Chat{}, err
	}
	if !chat.Relation.ValidFor(a, chat.ID) || chat.Relation.Kind != "self" {
		return Chat{}, failure("conversation_unverified")
	}
	return chat, nil
}
