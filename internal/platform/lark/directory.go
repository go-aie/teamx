package lark

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

const DirectoryInterval = 10 * time.Minute
const MaxChats = 10000

type ChatsRequest struct {
	PageSize  int
	PageToken string
}
type ChatPage struct {
	Chats          []Chat              `json:"chats"`
	HasMore        bool                `json:"has_more"`
	PageToken      string              `json:"page_token"`
	SearchWarnings []ChatSearchWarning `json:"search_warnings,omitempty"`
}
type DirectoryReader interface {
	Chats(context.Context, platform.AccountIdentity, ChatsRequest) (ChatPage, error)
}

// Chats is one page of joined conversations, including p2p. No search, mute
// filtering, auto-pagination or metadata enrichment is enabled.
func (c *Client) Chats(ctx context.Context, a platform.AccountIdentity, q ChatsRequest) (ChatPage, error) {
	var page ChatPage
	if q.PageSize < 1 || q.PageSize > 100 || len(q.PageToken) > 8192 || strings.ContainsAny(q.PageToken, "\x00\r\n") {
		return page, failure("invalid_request")
	}
	err := c.guarded(ctx, a, func(ctx context.Context) error {
		args := []string{"im", "+chat-list", "--types", "p2p,group", "--sort", "create_time", "--page-size", strconv.Itoa(q.PageSize), "--as", "user", "--format", "json"}
		if q.PageToken != "" {
			args = append(args, "--page-token", q.PageToken)
		}
		release, err := c.requests.acquire(ctx)
		if err != nil {
			return err
		}
		defer release()
		b, err := c.run(ctx, args...)
		if err != nil {
			return err
		}
		var env struct {
			OK       bool            `json:"ok"`
			Identity string          `json:"identity"`
			Data     json.RawMessage `json:"data"`
		}
		var raw struct {
			Chats     []Chat `json:"chats"`
			HasMore   *bool  `json:"has_more"`
			PageToken string `json:"page_token"`
		}
		if decode(b, &env) != nil || !env.OK || env.Identity != "user" || decode(env.Data, &raw) != nil || raw.HasMore == nil || raw.Chats == nil {
			return failure("invalid_response")
		}
		page = ChatPage{Chats: raw.Chats, HasMore: *raw.HasMore, PageToken: raw.PageToken}
		return validateChatPage(page, q)
	})
	if err != nil {
		return ChatPage{}, err
	}
	return page, nil
}
func validateChatPage(p ChatPage, q ChatsRequest) error {
	if p.Chats == nil || len(p.Chats) > q.PageSize || len(p.PageToken) > 8192 || strings.ContainsAny(p.PageToken, "\x00\r\n") || p.HasMore && (p.PageToken == "" || p.PageToken == q.PageToken) {
		return failure("invalid_response")
	}
	seen := map[string]bool{}
	for _, c := range p.Chats {
		if !c.Valid() || seen[c.ID] {
			return failure("invalid_response")
		}
		seen[c.ID] = true
	}
	return nil
}

type DirectoryScan struct {
	Token string          `json:"page_token,omitempty"`
	Pages int             `json:"pages"`
	Seen  map[string]bool `json:"seen"`
}
type DirectoryProgress struct {
	Scan     *DirectoryScan `json:"scan,omitempty"`
	LastFull time.Time      `json:"last_full"`
	RetryAt  time.Time      `json:"retry_at"`
	Yield    bool           `json:"yield,omitempty"`
}

func (s State) ChatIDs() []string {
	ids := []string{}
	if s.Config.Private.Enabled {
		if s.Config.DiscoverPrivate() {
			for id, p := range s.Chats {
				if p.Chat.Mode == "p2p" && !p.Unlisted {
					ids = append(ids, id)
				}
			}
		} else {
			ids = append(ids, s.Config.Private.ChatIDs...)
		}
	}
	if s.Config.Groups.Enabled {
		ids = append(ids, s.Config.Groups.ChatIDs...)
	}
	slices.Sort(ids)
	return slices.Compact(ids)
}
func (s *State) updateFull(now time.Time) {
	if s.Search != nil {
		s.LastFull = time.Time{}
		for _, b := range s.Search.Branches {
			if b.Completed == 0 {
				s.LastFull = time.Time{}
				return
			}
			t := time.Unix(b.Completed, 0).UTC()
			if s.LastFull.IsZero() || t.Before(s.LastFull) {
				s.LastFull = t
			}
		}
		return
	}
	s.LastFull = now
	ids := s.ChatIDs()
	if len(ids) == 0 || s.Config.DiscoverPrivate() && (s.Directory == nil || s.Directory.Scan != nil || s.Directory.LastFull.IsZero()) {
		s.LastFull = time.Time{}
		return
	}
	for _, id := range ids {
		p := s.Chats[id]
		if p.Completed == 0 || p.ErrorCode != "" || p.NeedsVerification {
			s.LastFull = time.Time{}
			return
		}
		if t := time.Unix(p.Completed, 0).UTC(); t.Before(s.LastFull) {
			s.LastFull = t
		}
	}
}

// Drop only evidence outside a completed conversation's next overlap window.
// Pending deliveries and their frozen routes survive; Room/Inbox own delivered
// immutable evidence. Incomplete historical scans are never trimmed past Start.
func (s *State) pruneObserved() {
	for key, o := range s.Observed {
		if _, pending := s.Inbox[key]; pending {
			continue
		}
		p, ok := s.Chats[o.Message.ChatID]
		if !ok || p.Completed == 0 {
			continue
		}
		cutoff := p.Completed - 300
		if p.Scan != nil {
			cutoff = min(cutoff, p.Scan.Start)
		}
		if s.Search != nil {
			for _, b := range s.Search.Branches {
				if (p.Chat.Mode == "p2p") != (b.Kind == "private") {
					continue
				}
				if b.Completed == 0 {
					cutoff = s.Baseline.Unix()
				} else {
					cutoff = min(cutoff, b.Completed-searchOverlap)
				}
				if b.Scan != nil {
					cutoff = min(cutoff, b.Scan.Start)
				}
			}
		}
		ms, _ := strconv.ParseInt(o.Message.Created, 10, 64)
		if ms/1000 < cutoff {
			delete(s.Observed, key)
		}
	}
	retained := map[string]bool{}
	for _, o := range s.Observed {
		if o.Accepted {
			retained[MessageKey(s.Binding, o.Message)] = true
		}
	}
	for key := range s.Accepted {
		if !retained[key] {
			delete(s.Accepted, key)
		}
	}
}
