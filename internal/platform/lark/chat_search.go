package lark

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
)

// Search is separate from directory discovery: results are display metadata,
// never proof of membership, message access or sending authority.
type ChatSearchRequest struct {
	Query     string
	Type      string // all, group, p2p
	PageToken string // group search only; user/bot shortcuts cannot paginate
}

func (q ChatSearchRequest) Valid() bool {
	return utf8.ValidString(q.Query) && strings.TrimSpace(q.Query) != "" && len([]rune(q.Query)) <= 50 && !strings.ContainsAny(q.Query, "\x00\r\n") &&
		(q.Type == "all" || q.Type == "group" || q.Type == "p2p") && len(q.PageToken) <= 8192 && !strings.ContainsAny(q.PageToken, "\x00\r\n") && (q.Type != "p2p" || q.PageToken == "")
}

type ChatSearchWarning struct {
	Source string `json:"source"` // group, user, bot
	Code   string `json:"code"`   // classified error or refine_query
}

func (c *Client) SearchChats(ctx context.Context, a platform.AccountIdentity, q ChatSearchRequest) (ChatPage, error) {
	if !q.Valid() {
		return ChatPage{}, failure("invalid_request")
	}
	page := ChatPage{Chats: []Chat{}}
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		seen := map[string]bool{}
		add := func(chat Chat) error {
			if !chat.Valid() {
				return failure("invalid_response")
			}
			if !seen[chat.ID] {
				page.Chats = append(page.Chats, chat)
				seen[chat.ID] = true
			}
			return nil
		}
		// A missing optional search permission does not invalidate an otherwise
		// usable connection or hide results from other search sources.
		warn := func(source string, err error) bool {
			code := ErrorCode(err)
			if code == "permission_denied" || code == "query_failed" {
				page.SearchWarnings = append(page.SearchWarnings, ChatSearchWarning{source, code})
				return true
			}
			return false
		}
		if q.Type != "p2p" {
			var raw struct {
				Chats     []Chat `json:"chats"`
				More      *bool  `json:"has_more"`
				PageToken string `json:"page_token"`
			}
			args := []string{"im", "+chat-search", "--query", q.Query, "--disable-search-by-user", "--search-types", "private,public_joined,external", "--chat-modes", "group,topic", "--page-size", "20"}
			if q.PageToken != "" {
				args = append(args, "--page-token", q.PageToken)
			}
			if err := c.apiJSON(ctx, args, &raw); err != nil {
				if !warn("group", err) {
					return err
				}
			} else {
				if raw.More == nil || raw.Chats == nil || len(raw.Chats) > 20 || len(raw.PageToken) > 8192 || strings.ContainsAny(raw.PageToken, "\x00\r\n") || *raw.More && (raw.PageToken == "" || raw.PageToken == q.PageToken) {
					return failure("invalid_response")
				}
				for _, chat := range raw.Chats {
					// Search metadata uses wire group modes DEFAULT/THREAD.
					switch strings.ToLower(chat.Mode) {
					case "default", "group":
						chat.Mode = "group"
					case "thread", "topic":
						chat.Mode = "topic"
					default:
						return failure("invalid_response")
					}
					if err := add(chat); err != nil {
						return err
					}
				}
				page.HasMore, page.PageToken = *raw.More, raw.PageToken
			}
		}
		if q.Type == "group" || q.PageToken != "" {
			return nil
		}
		var users struct {
			Users []struct {
				ID   string `json:"open_id"`
				Name string `json:"localized_name"`
				Chat string `json:"p2p_chat_id"`
			} `json:"users"`
			More *bool `json:"has_more"`
		}
		if err := c.apiJSON(ctx, []string{"contact", "+search-user", "--query", q.Query, "--has-chatted", "--page-size", "20"}, &users); err != nil {
			if !warn("user", err) {
				return err
			}
		} else {
			if users.More == nil || users.Users == nil || len(users.Users) > 20 {
				return failure("invalid_response")
			}
			for _, u := range users.Users {
				if !nativeID(u.ID, "ou_") {
					return failure("invalid_response")
				}
				if u.Chat != "" { // Never create a conversation during search.
					if err := add(Chat{ID: u.Chat, Mode: "p2p", Name: u.Name}); err != nil {
						return err
					}
				}
			}
			if *users.More {
				page.SearchWarnings = append(page.SearchWarnings, ChatSearchWarning{"user", "refine_query"})
			}
		}
		var bots struct {
			Bots []struct {
				ID   string `json:"open_id"`
				Name string `json:"name"`
				Chat string `json:"chat_id"`
			} `json:"bots"`
			More *bool `json:"has_more"`
		}
		if err := c.apiJSON(ctx, []string{"contact", "+search-bot", "--query", q.Query, "--has-chatted", "--page-size", "20"}, &bots); err != nil {
			if !warn("bot", err) {
				return err
			}
		} else {
			if bots.More == nil || bots.Bots == nil || len(bots.Bots) > 20 {
				return failure("invalid_response")
			}
			for _, b := range bots.Bots {
				if !nativeID(b.ID, "ou_") {
					return failure("invalid_response")
				}
				if b.Chat != "" {
					if err := add(Chat{ID: b.Chat, Mode: "p2p", Name: b.Name}); err != nil {
						return err
					}
				}
			}
			if *bots.More {
				page.SearchWarnings = append(page.SearchWarnings, ChatSearchWarning{"bot", "refine_query"})
			}
		}
		return nil
	})
	if err != nil {
		return ChatPage{}, err
	}
	return page, nil
}
