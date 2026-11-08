package lark

import (
	"context"
	"github.com/go-aie/teamx/internal/platform"
	"strings"
)

// Chat is verified independently from the sending account's tenant.
type Chat struct {
	ID        string               `json:"chat_id"`
	Mode      string               `json:"chat_mode"`
	Name      string               `json:"name"`
	TenantKey string               `json:"tenant_key"`
	Relation  ConversationRelation `json:"relation,omitzero"`
}

func (c Chat) Valid() bool {
	return nativeID(c.ID, "oc_") && (c.Mode == "p2p" || c.Mode == "group" || c.Mode == "topic") && len(c.Name) <= 4096 && len(c.TenantKey) <= 256 && !strings.ContainsAny(c.Name+c.TenantKey, "\x00\r\n")
}
func (c *Client) Chat(ctx context.Context, account platform.AccountIdentity, id string) (Chat, error) {
	var chat Chat
	if !nativeID(id, "oc_") {
		return chat, failure("invalid_request")
	}
	err := c.guarded(ctx, account, func(ctx context.Context) error {
		if err := c.api(ctx, "/open-apis/im/v1/chats/"+id, map[string]string{"user_id_type": "open_id"}, &chat); err != nil {
			return err
		}
		chat.ID = id // get is addressed by the exact path and does not return chat_id.
		if !chat.Valid() {
			return failure("invalid_response")
		}
		return nil
	})
	if err != nil {
		return Chat{}, err
	}
	return chat, nil
}

type MessageReader interface {
	Messages(context.Context, platform.AccountIdentity, MessagesRequest) (MessagePage, error)
	Message(context.Context, platform.AccountIdentity, string) (Message, error)
	Chat(context.Context, platform.AccountIdentity, string) (Chat, error)
}
