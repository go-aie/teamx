package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// A human can browse conversation metadata before enabling a subscription or
// binding a member. This endpoint never reads messages or changes selection.
type larkChatsRequest struct {
	member       *toolsRequest // Internal admission only; human HTTP cannot populate it.
	verifyIDs    []string      // Exact selected IDs, re-read through the same guarded client.
	ServiceEpoch string        `json:"service_epoch"`
	RoomIdentity
	Caller          Caller `json:"caller"`
	PlatformID      string `json:"platform_id"`
	PlatformVersion uint64 `json:"platform_version"`
	PageToken       string `json:"page_token,omitempty"`
	Query           string `json:"query,omitempty"`
	Type            string `json:"type,omitempty"`
}

func (m *roomManager) checkLarkChatsLocked(ctx context.Context, req larkChatsRequest, expected *registry.Account) (registry.Account, error) {
	if err := ctx.Err(); err != nil {
		return registry.Account{}, err
	}
	if req.member != nil {
		if req.member.RoomIdentity != req.RoomIdentity || req.member.ServiceEpoch != req.ServiceEpoch || req.member.Caller != req.Caller {
			return registry.Account{}, ErrIdentity
		}
		if err := m.checkToolMemberLocked(ctx, *req.member); err != nil {
			return registry.Account{}, err
		}
	} else {
		if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
			return registry.Account{}, ErrIdentity
		}
	}
	if m.busy || m.roomBusyLocked(req.RoomIdentity) {
		return registry.Account{}, ErrBusy
	}
	a, _, err := m.resolvePlatformAccountLocked(req.RoomIdentity, req.PlatformID, "lark")
	if err != nil {
		return a, err
	}
	if a.PlatformVersion != req.PlatformVersion || a.CLIProfile == nil || !lark.ValidAccount(a.Identity, *a.CLIProfile) || expected != nil && (a.PlatformVersion != expected.PlatformVersion || larkBinding(a) != larkBinding(*expected) || *a.CLIProfile != *expected.CLIProfile) {
		return a, ErrConflict
	}
	return a, nil
}

func (m *roomManager) listLarkChats(ctx context.Context, req larkChatsRequest) (lark.ChatPage, error) {
	if len(req.PageToken) > 8192 || strings.ContainsAny(req.PageToken, "\x00\r\n") {
		return lark.ChatPage{}, ErrRequest
	}
	if req.Type == "" {
		req.Type = "all"
	}
	search := lark.ChatSearchRequest{Query: req.Query, Type: req.Type, PageToken: req.PageToken}
	if req.Type != "all" && req.Type != "group" && req.Type != "p2p" || req.Query != "" && !search.Valid() {
		return lark.ChatPage{}, ErrRequest
	}
	m.mu.Lock()
	a, err := m.checkLarkChatsLocked(ctx, req, nil)
	if err != nil {
		m.mu.Unlock()
		return lark.ChatPage{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	call := &platformCall{owner: req.RoomIdentity, platformID: a.PlatformID, cancel: cancel, done: make(chan struct{})}
	if req.member != nil {
		call.agentID, call.membershipID = req.Caller.ID, req.Caller.MembershipID
	}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	client, err := m.larkClients.New(*a.CLIProfile)
	if err != nil {
		return lark.ChatPage{}, err
	}
	var page lark.ChatPage
	if len(req.verifyIDs) > 0 {
		page.Chats = []lark.Chat{}
		for _, id := range req.verifyIDs {
			var chat lark.Chat
			chat, err = client.Chat(callCtx, a.Identity, id)
			if err != nil {
				break
			}
			if chat.Mode != "group" && chat.Mode != "topic" {
				err = ErrRequest
				break
			}
			page.Chats = append(page.Chats, chat)
		}
	} else if req.Query != "" {
		page, err = client.SearchChats(callCtx, a.Identity, search)
	} else {
		page, err = client.Chats(callCtx, a.Identity, lark.ChatsRequest{PageSize: 100, PageToken: req.PageToken})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, checkErr := m.checkLarkChatsLocked(callCtx, req, &a); checkErr != nil {
		return lark.ChatPage{}, checkErr
	}
	if err != nil {
		if saveErr := m.recordLarkReadFailureLocked(a, err); saveErr != nil {
			return lark.ChatPage{}, saveErr
		}
		return lark.ChatPage{}, err
	}
	return page, nil
}

func (m *roomManager) serveLarkChats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req larkChatsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	page, err := m.listLarkChats(r.Context(), req)
	if err != nil {
		writeLarkReadError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(page)
}
