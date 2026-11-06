package lark

import (
	"context"
	"encoding/json"
	"github.com/go-aie/teamx/internal/platform"
	"strconv"
	"strings"
	"time"
)

// Times retain the API's native millisecond strings. Requests use Unix seconds.
type Sender struct {
	ID        string `json:"id"`
	IDType    string `json:"id_type"`
	Type      string `json:"sender_type"`
	TenantKey string `json:"tenant_key"`
	// Name is optional display evidence resolved through the same CLI account.
	Name string `json:"name,omitempty"`
}
type Mention struct {
	Key       string `json:"key"`
	ID        string `json:"id"`
	IDType    string `json:"id_type"`
	Name      string `json:"name"`
	TenantKey string `json:"tenant_key"`
}
type Message struct {
	ID        string `json:"message_id"`
	ChatID    string `json:"chat_id"`
	RootID    string `json:"root_id,omitempty"`
	ParentID  string `json:"parent_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
	Type      string `json:"msg_type"`
	Created   string `json:"create_time"`
	Updated   string `json:"update_time"`
	Deleted   bool   `json:"deleted"`
	IsUpdated bool   `json:"updated"`
	Sender    Sender `json:"sender"`
	Body      struct {
		Content string `json:"content"`
	} `json:"body"`
	Mentions []Mention `json:"mentions,omitempty"`
}
type MessagePage struct {
	Items     []Message `json:"items"`
	HasMore   bool      `json:"has_more"`
	PageToken string    `json:"page_token"`
}
type MessagesRequest struct {
	ChatID    string `json:"chat_id"`
	Start     int64  `json:"start_time"`
	End       int64  `json:"end_time"`
	PageSize  int    `json:"page_size"`
	PageToken string `json:"page_token,omitempty"`
}

func nativeID(s, prefix string) bool {
	return strings.HasPrefix(s, prefix) && len(s) > len(prefix) && len(s) <= 256 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}
func (r MessagesRequest) Valid() bool {
	return nativeID(r.ChatID, "oc_") && r.Start > 0 && r.End >= r.Start && r.PageSize >= 1 && r.PageSize <= 50 && len(r.PageToken) <= 8192 && !strings.ContainsAny(r.PageToken, "\x00\r\n")
}
func (m Message) valid() bool {
	created, e1 := strconv.ParseInt(m.Created, 10, 64)
	updated, e2 := strconv.ParseInt(m.Updated, 10, 64)
	return nativeID(m.ID, "om_") && nativeID(m.ChatID, "oc_") && m.Type != "" && e1 == nil && created > 0 && (m.Updated == "" || e2 == nil && updated >= created) && len(m.Body.Content) <= 1<<20 && (m.ParentID == "" || nativeID(m.ParentID, "om_")) && (m.RootID == "" || nativeID(m.RootID, "om_")) && (m.ThreadID == "" || nativeID(m.ThreadID, "omt_"))
}

func (c *Client) Messages(ctx context.Context, account platform.AccountIdentity, r MessagesRequest) (MessagePage, error) {
	var page MessagePage
	if !r.Valid() {
		return page, failure("invalid_request")
	}
	err := c.guarded(ctx, account, func(ctx context.Context) error {
		params := map[string]any{"container_id_type": "chat", "container_id": r.ChatID, "start_time": strconv.FormatInt(r.Start, 10), "end_time": strconv.FormatInt(r.End, 10), "sort_type": "ByCreateTimeAsc", "page_size": r.PageSize}
		if r.PageToken != "" {
			params["page_token"] = r.PageToken
		}
		// Require the completeness flag and items even for an empty page.
		var wire struct {
			Items     *[]Message `json:"items"`
			HasMore   *bool      `json:"has_more"`
			PageToken string     `json:"page_token"`
		}
		if err := c.api(ctx, "/open-apis/im/v1/messages", params, &wire); err != nil {
			return err
		}
		if wire.Items == nil || wire.HasMore == nil || len(*wire.Items) > r.PageSize || *wire.HasMore && (wire.PageToken == "" || wire.PageToken == r.PageToken) || len(wire.PageToken) > 8192 {
			return failure("invalid_response")
		}
		seen := map[string]bool{}
		last := int64(0)
		for _, m := range *wire.Items {
			n, _ := strconv.ParseInt(m.Created, 10, 64)
			// API request precision is seconds; retain all messages in boundary seconds.
			if !m.valid() || m.ChatID != r.ChatID || n/1000 < r.Start || n/1000 > r.End || n < last || seen[m.ID] {
				return failure("invalid_response")
			}
			seen[m.ID] = true
			last = n
		}
		c.senderNames(ctx, *wire.Items)
		page = MessagePage{Items: *wire.Items, HasMore: *wire.HasMore, PageToken: wire.PageToken}
		return nil
	})
	if err != nil {
		return MessagePage{}, err
	}
	return page, nil
}
func (c *Client) Message(ctx context.Context, account platform.AccountIdentity, id string) (Message, error) {
	var message Message
	if !nativeID(id, "om_") {
		return message, failure("invalid_request")
	}
	err := c.guarded(ctx, account, func(ctx context.Context) error {
		var wire struct {
			Items []Message `json:"items"`
		}
		if err := c.api(ctx, "/open-apis/im/v1/messages/"+id, struct{}{}, &wire); err != nil {
			return err
		}
		if len(wire.Items) != 1 || wire.Items[0].ID != id || !wire.Items[0].valid() {
			return failure("invalid_response")
		}
		c.senderNames(ctx, wire.Items)
		message = wire.Items[0]
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	return message, nil
}

// Text retains structured evidence in Message.Body; rich text, attachment
// understanding and notification filtering belong to L2.
func (m Message) Text() string { return m.Content() }

type senderAccountContextKey struct{}

// senderNames runs inside guarded, so lookup uses the same verified app/account
// and request budget as the messages. Names never establish Room membership or
// authority. At most five batches per page, with one shared timeout; optional
// lookup failures leave native sender IDs intact.
func (c *Client) senderNames(ctx context.Context, messages []Message) {
	account, _ := ctx.Value(senderAccountContextKey{}).(platform.Validation)
	names := map[string]string{}
	if account.Status == "valid" && validSenderName(account.Username) {
		names[account.Account.ID.Value] = account.Username
	}
	ids := []string{}
	requested := map[string]bool{}
	for i := range messages {
		s := &messages[i].Sender
		s.Name = ""
		if s.Type == "user" && s.IDType == "open_id" && names[s.ID] != "" {
			s.Name = names[s.ID]
			continue
		}
		if s.Type == "user" && s.IDType == "open_id" && nativeID(s.ID, "ou_") && !requested[s.ID] && len(ids) < 50 {
			ids = append(ids, s.ID)
			requested[s.ID] = true
		}
	}
	if len(ids) == 0 {
		return
	}
	lookup, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// basic_batch accepts at most 10 IDs; message pages contain at most 50.
	for start := 0; start < len(ids); start += 10 {
		batch, err := c.senderNameBatch(lookup, ids[start:min(start+10, len(ids))])
		if err != nil {
			break
		}
		for id, name := range batch {
			names[id] = name
		}
	}
	for i := range messages {
		s := &messages[i].Sender
		if s.Type == "user" && s.IDType == "open_id" {
			s.Name = names[s.ID]
		}
	}
}

// This endpoint returns only names, without fetching full contact profiles.
func (c *Client) senderNameBatch(ctx context.Context, ids []string) (map[string]string, error) {
	body, _ := json.Marshal(struct {
		IDs []string `json:"user_ids"`
	}{ids})
	var wire struct {
		Users []struct {
			ID   string `json:"user_id"`
			Name string `json:"name"`
		} `json:"users"`
	}
	if err := c.apiJSON(ctx, []string{"api", "POST", "/open-apis/contact/v3/users/basic_batch", "--params", `{"user_id_type":"open_id"}`, "--data", string(body)}, &wire); err != nil {
		return nil, err
	}
	if len(wire.Users) > len(ids) {
		return nil, failure("invalid_response")
	}
	requested := map[string]bool{}
	for _, id := range ids {
		requested[id] = true
	}
	names := map[string]string{}
	seen := map[string]bool{}
	for _, user := range wire.Users {
		if !requested[user.ID] || seen[user.ID] {
			return nil, failure("invalid_response")
		}
		seen[user.ID] = true
		if validSenderName(user.Name) {
			names[user.ID] = user.Name
		}
	}
	return names, nil
}
