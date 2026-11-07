package lark

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ReplyText uses only the fixed reply endpoint and a pinned user. No shortcut
// file expansion, mention rewriting, markdown conversion or send fallback.
// guard runs after all blocking admission work, immediately before dispatch.
func (p *Pinned) ReplyText(ctx context.Context, messageID, body, key string, guard func(context.Context) error) (string, error) {
	input, _ := json.Marshal(ReplyInput{Body: body, ReplyMode: "chat"})
	if p == nil || p.client == nil || guard == nil || !utf8.ValidString(body) || !nativeID(messageID, "om_") || len(key) != 32 || strings.Trim(key, "0123456789abcdef") != "" {
		return "", ErrReplyNotSent
	}
	if _, err := replyInput(input); err != nil {
		return "", ErrReplyNotSent
	}
	permission, err := p.SendPermission(ctx)
	if err != nil || permission.UserScopes != "granted" || permission.ApplicationScopes != "granted" {
		return "", ErrReplyNotSent
	}
	c := p.client
	unlock, err := c.lock(ctx)
	if err != nil {
		return "", ErrReplyNotSent
	}
	defer unlock()
	v, err := c.verify(ctx)
	if err != nil || v.Account != p.Account {
		return "", ErrReplyNotSent
	}
	ctx = context.WithValue(ctx, accountContextKey{}, p.Account.Instance+"|"+p.Account.ID.Namespace+"|"+p.Account.ID.Value)
	release, err := c.requests.acquire(ctx)
	if err != nil {
		return "", ErrReplyNotSent
	}
	defer release()
	content, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{body})
	payload, _ := json.Marshal(struct {
		Type    string `json:"msg_type"`
		Content string `json:"content"`
		UUID    string `json:"uuid"`
		Thread  bool   `json:"reply_in_thread"`
	}{"text", string(content), key, false})
	if ctx.Err() != nil || guard(ctx) != nil {
		return "", ErrReplyNotSent
	}
	// run has no business retry. Any error from this point may have applied;
	// never turn a CLI exit, timeout, cancellation or rate limit into not_applied.
	data, err := c.run(ctx, "api", "POST", "/open-apis/im/v1/messages/"+messageID+"/reply", "--data", string(payload), "--as", "user", "--format", "json")
	if err != nil {
		return "", err
	}
	var response struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Data     struct {
			ID string `json:"message_id"`
		} `json:"data"`
	}
	if decode(data, &response) != nil || !response.OK || response.Identity != "user" || !nativeID(response.Data.ID, "om_") {
		return "", failure("invalid_response")
	}
	return response.Data.ID, nil
}

func (p *Pinned) MessageReader() MessageReader { return p.client }
