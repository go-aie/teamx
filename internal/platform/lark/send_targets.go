package lark

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

// SendReader verifies recipients independently of notification evidence.
// DirectUser must prove the exact peer/chat mapping; SendGroup must prove
// current membership and speaking permission, not just public visibility.
type SendReader interface {
	MessageReader
	DirectUser(context.Context, platform.AccountIdentity, string) (Chat, string, error)
	SendGroup(context.Context, platform.AccountIdentity, string) (Chat, error)
	FindSendTargets(context.Context, platform.AccountIdentity, platform.TargetQuery) ([]platform.TargetCandidate, bool, error)
}

func validSendTarget(t platform.TargetIdentity, a platform.AccessContext) bool {
	return a.Valid() && validSendIdentity(t, a.Account)
}
func validSendIdentity(t platform.TargetIdentity, account platform.AccountIdentity) bool {
	if !account.Valid() || account.Provider != "lark" || !t.Valid() || t.Provider != "lark" || t.Instance != account.Instance || t.Tenant != account.Tenant || t.Message != (platform.ExternalID{}) || t.Discussion != (platform.ExternalID{}) {
		return false
	}
	switch t.Object.Type {
	case "user":
		return t.Object.ID.Namespace == account.ID.Namespace && nativeID(t.Object.ID.Value, "ou_") && len(t.Containers) <= 1 && (len(t.Containers) == 0 || t.Containers[0].Type == "chat" && t.Containers[0].ID.Namespace == nativeNamespace(account, "chat_id") && nativeID(t.Containers[0].ID.Value, "oc_"))
	case "chat":
		return len(t.Containers) == 0 && t.Object.ID.Namespace == nativeNamespace(account, "chat_id") && nativeID(t.Object.ID.Value, "oc_")
	}
	return false
}

func verifySendTarget(ctx context.Context, t platform.TargetIdentity, a platform.AccessContext, reader SendReader) (Chat, string, error) {
	if reader == nil || !validSendTarget(t, a) {
		return Chat{}, "", failure("target_mismatch")
	}
	var chat Chat
	var name string
	var err error
	if t.Object.Type == "user" {
		// Native user self-delivery passed a bounded real probe. Managed self-target
		// and owner-evidence integration still needs implementation/acceptance;
		// never infer Room account ownership or substitute a bot notification.
		if t.Object.ID == a.Account.ID {
			return Chat{}, "", failure("self_send_unavailable")
		}
		chat, name, err = reader.DirectUser(ctx, a.Account, t.Object.ID.Value)
		if err == nil && (chat.Mode != "p2p" || len(t.Containers) == 1 && chat.ID != t.Containers[0].ID.Value) {
			err = failure("peer_mismatch")
		}
	} else {
		chat, err = reader.SendGroup(ctx, a.Account, t.Object.ID.Value)
		name = chat.Name
		if err == nil && (chat.Mode != "group" || chat.ID != t.Object.ID.Value) {
			err = failure("group_target_required")
		}
	}
	if err != nil {
		return Chat{}, "", err
	}
	if !chat.Valid() || chat.TenantKey != t.Tenant.Value {
		return Chat{}, "", failure("target_tenant_mismatch")
	}
	return chat, name, nil
}

func ResolveSendTarget(ctx context.Context, l platform.TargetLocator, a platform.AccessContext, reader SendReader) (platform.TargetResolution, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r := platform.TargetResolution{Status: platform.ResolutionUnsupported, ReasonCode: "send_locator_required", Account: a.Account, CheckedAt: time.Now().UTC()}
	if l.URL != "" || l.Source != nil || l.TargetRef != "" || (l.Candidate == nil) == (l.Query == nil) || reader == nil || !a.Valid() {
		return r, nil
	}
	if l.Query != nil {
		candidates, more, err := reader.FindSendTargets(ctx, a.Account, *l.Query)
		if err != nil {
			r.Status = platform.ResolutionUnavailable
			r.ReasonCode = ErrorCode(err)
			if r.ReasonCode == "permission_denied" {
				r.Reason = "姓名查询为可选能力，用户查询需 contact:user:search；可改用准确的原生 ID，或为该查询单独授权。群查询需要当前账号可见范围。"
			}
			return r, nil
		}
		r.Status = platform.ResolutionPartial
		r.ReasonCode = "choose_exact_target"
		r.Candidates = candidates
		r.CandidatesIncomplete = more
		return r, nil
	}
	t := *l.Candidate
	chat, name, err := verifySendTarget(ctx, t, a, reader)
	if err != nil {
		r.Status = platform.ResolutionUnavailable
		r.ReasonCode = ErrorCode(err)
		if r.ReasonCode == "permission_denied" {
			r.Reason = "无法核验目标访问；私聊对端／群成员核验需 im:chat.members:read，普通群发言范围通过群信息 moderation_permission 核验；仍需原生访问权限。可在连接设置中使用授权目标核验；读取或发送授权不代替目标核验。"
		}
		if r.ReasonCode == "self_send_unavailable" {
			r.Reason = "用户身份向本人原生投递已通过单次探测；self 目标与本人关联的受控 Agent 链路尚未接入，当前模式未开放，不会回退机器人。"
		}
		return r, nil
	}
	if t.Object.Type == "user" {
		t.Containers = []platform.NativeIdentity{{Type: "chat", ID: platform.ExternalID{Namespace: nativeNamespace(a.Account, "chat_id"), Value: chat.ID}}}
	}
	r.Status = platform.ResolutionResolved
	r.ReasonCode = ""
	r.Target = &t
	r.TargetLabel = name
	r.Complete = true
	r.ObjectVerified = true
	return r, nil
}

func (c *Client) DirectUser(ctx context.Context, a platform.AccountIdentity, id string) (Chat, string, error) {
	if !nativeID(id, "ou_") || id == a.ID.Value {
		return Chat{}, "", failure("user_target_required")
	}
	var chat Chat
	var name string
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		// This read independently checks the exact user's current P2P relationship.
		var wire struct {
			Chats []struct {
				ID string `json:"chat_id"`
			} `json:"p2p_chats"`
		}
		body, _ := json.Marshal(map[string]any{"chatter_ids": []string{id}})
		if err := c.apiJSON(ctx, []string{"api", "POST", "/open-apis/im/v1/chat_p2p/batch_query", "--params", `{"chatter_id_type":"open_id"}`, "--data", string(body)}, &wire); err != nil {
			return err
		}
		if len(wire.Chats) != 1 || !nativeID(wire.Chats[0].ID, "oc_") {
			return failure("peer_mismatch")
		}
		chat = Chat{ID: wire.Chats[0].ID}
		// Names are optional display data; exact member identities prove the peer.
		var members struct {
			Items []struct {
				ID     string `json:"member_id"`
				Type   string `json:"member_id_type"`
				Tenant string `json:"tenant_key"`
				Name   string `json:"name"`
			} `json:"items"`
			More *bool `json:"has_more"`
		}
		if err := c.api(ctx, "/open-apis/im/v1/chats/"+chat.ID+"/members", map[string]any{"member_id_type": "open_id", "page_size": 100}, &members); err != nil {
			return err
		}
		if members.More == nil || *members.More || len(members.Items) != 2 {
			return failure("peer_unverified")
		}
		seen := map[string]bool{}
		for _, u := range members.Items {
			if u.Type != "open_id" || u.Tenant != a.Tenant.Value || (u.ID != a.ID.Value && u.ID != id) || seen[u.ID] {
				return failure("peer_mismatch")
			}
			seen[u.ID] = true
			if u.ID == id {
				name = u.Name
			}
		}
		if !validSenderName(name) {
			name = id
		}
		return nil
	})
	if err != nil {
		return Chat{}, "", err
	}
	chat, err = c.Chat(ctx, a, chat.ID)
	if err == nil && (chat.Mode != "p2p" || chat.TenantKey != a.Tenant.Value) {
		err = failure("target_tenant_mismatch")
	}
	return chat, name, err
}

// A bounded exact-member scan avoids treating a public group as joined. If the
// scan cannot prove membership, report incomplete instead of granting access.
func (c *Client) SendGroup(ctx context.Context, a platform.AccountIdentity, id string) (Chat, error) {
	if !nativeID(id, "oc_") {
		return Chat{}, failure("invalid_request")
	}
	var chat Chat
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		var info struct {
			Chat
			Status               string `json:"chat_status"`
			External             *bool  `json:"external"`
			MessageType          string `json:"group_message_type"`
			ModerationPermission string `json:"moderation_permission"`
		}
		if err := c.api(ctx, "/open-apis/im/v1/chats/"+id, map[string]string{"user_id_type": "open_id"}, &info); err != nil {
			return err
		}
		info.Chat.ID = id
		chat = info.Chat
		if !chat.Valid() || chat.Mode != "group" || chat.TenantKey != a.Tenant.Value || info.Status != "normal" || info.External == nil || *info.External || info.MessageType != "chat" {
			return failure("group_send_unavailable")
		}
		// The existing chat GET provides the speaking policy. First-batch
		// all-members groups need no extra moderation-list scope or request.
		if info.ModerationPermission != "all_members" {
			return failure("speaking_permission_unverified")
		}
		token := ""
		joined := false
		seen := map[string]bool{}
		for page := 0; page < 20; page++ {
			var wire struct {
				Items []struct {
					ID     string `json:"member_id"`
					Type   string `json:"member_id_type"`
					Tenant string `json:"tenant_key"`
				} `json:"items"`
				More  *bool  `json:"has_more"`
				Token string `json:"page_token"`
			}
			if err := c.api(ctx, "/open-apis/im/v1/chats/"+id+"/members", map[string]any{"member_id_type": "open_id", "page_size": 100, "page_token": token}, &wire); err != nil {
				return err
			}
			if wire.More == nil || wire.Items == nil || len(wire.Items) > 1000 {
				return failure("invalid_response")
			}
			for _, u := range wire.Items {
				if u.ID == a.ID.Value && u.Type == "open_id" && u.Tenant == a.Tenant.Value {
					joined = true
				}
			}
			if joined || !*wire.More {
				break
			}
			if wire.Token == "" || len(wire.Token) > 8192 || seen[wire.Token] {
				return failure("invalid_response")
			}
			seen[wire.Token] = true
			token = wire.Token
		}
		if !joined {
			return failure("membership_unverified")
		}
		return nil
	})
	return chat, err
}

func (c *Client) FindSendTargets(ctx context.Context, a platform.AccountIdentity, q platform.TargetQuery) ([]platform.TargetCandidate, bool, error) {
	if (q.ObjectType != "user" && q.ObjectType != "chat") || !utf8.ValidString(q.Text) || len([]rune(q.Text)) > 50 || strings.TrimSpace(q.Text) == "" || strings.ContainsAny(q.Text, "\x00\r\n") {
		return nil, false, failure("invalid_target_query")
	}
	candidates := []platform.TargetCandidate{}
	more := false
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		if q.ObjectType == "user" {
			var wire struct {
				Users []struct {
					ID    string `json:"open_id"`
					Name  string `json:"localized_name"`
					Cross *bool  `json:"is_cross_tenant"`
				} `json:"users"`
				More *bool `json:"has_more"`
			}
			if err := c.apiJSON(ctx, []string{"contact", "+search-user", "--query", q.Text, "--has-chatted", "--exclude-external-users", "--page-size", "20"}, &wire); err != nil {
				return err
			}
			if wire.More == nil || wire.Users == nil || len(wire.Users) > 20 {
				return failure("invalid_response")
			}
			more = *wire.More
			for _, u := range wire.Users {
				if !nativeID(u.ID, "ou_") || !validSenderName(u.Name) || u.Cross == nil || *u.Cross {
					return failure("invalid_response")
				}
				candidates = append(candidates, platform.TargetCandidate{Target: platform.TargetIdentity{IdentityScope: a.IdentityScope, Object: platform.NativeIdentity{Type: "user", ID: platform.ExternalID{Namespace: a.ID.Namespace, Value: u.ID}}}, Label: u.Name})
			}
		} else {
			var wire struct {
				Chats []Chat `json:"chats"`
				More  *bool  `json:"has_more"`
			}
			if err := c.apiJSON(ctx, []string{"im", "+chat-search", "--query", q.Text, "--disable-search-by-user", "--search-types", "private,public_joined", "--chat-modes", "group", "--page-size", "20"}, &wire); err != nil {
				return err
			}
			if wire.More == nil || wire.Chats == nil || len(wire.Chats) > 20 {
				return failure("invalid_response")
			}
			more = *wire.More
			for _, chat := range wire.Chats {
				if !nativeID(chat.ID, "oc_") || !validSenderName(chat.Name) {
					return failure("invalid_response")
				}
				candidates = append(candidates, platform.TargetCandidate{Target: platform.TargetIdentity{IdentityScope: a.IdentityScope, Object: platform.NativeIdentity{Type: "chat", ID: platform.ExternalID{Namespace: nativeNamespace(a, "chat_id"), Value: chat.ID}}}, Label: chat.Name})
			}
		}
		return nil
	})
	return candidates, more, err
}
