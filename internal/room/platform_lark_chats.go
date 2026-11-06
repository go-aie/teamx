package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/lark"
)

// A human can browse conversation metadata before enabling a subscription or
// binding a member. This endpoint never reads messages or changes selection.
type larkChatsRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	RoomIdentity
	Caller            Caller `json:"caller"`
	ConnectionID      string `json:"connection_id"`
	ConnectionVersion uint64 `json:"connection_version"`
	PageToken         string `json:"page_token,omitempty"`
}

func (m *roomManager) checkLarkChatsLocked(ctx context.Context, req larkChatsRequest, expected *connection.Account) (connection.Account, error) {
	if err := ctx.Err(); err != nil {
		return connection.Account{}, err
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		return connection.Account{}, ErrIdentity
	}
	if m.busy || m.roomBusyLocked(req.RoomIdentity) {
		return connection.Account{}, ErrBusy
	}
	a, _, err := m.resolvePlatformAccountLocked(req.RoomIdentity, req.ConnectionID, "lark")
	if err != nil {
		return a, err
	}
	if a.ConnectionVersion != req.ConnectionVersion || a.CLIProfile == nil || !lark.ValidAccount(a.Identity, *a.CLIProfile) || expected != nil && (a.ConnectionVersion != expected.ConnectionVersion || larkBinding(a) != larkBinding(*expected) || *a.CLIProfile != *expected.CLIProfile) {
		return a, ErrConflict
	}
	return a, nil
}

func (m *roomManager) listLarkChats(ctx context.Context, req larkChatsRequest) (lark.ChatPage, error) {
	if len(req.PageToken) > 8192 || strings.ContainsAny(req.PageToken, "\x00\r\n") {
		return lark.ChatPage{}, ErrRequest
	}
	m.mu.Lock()
	a, err := m.checkLarkChatsLocked(ctx, req, nil)
	if err != nil {
		m.mu.Unlock()
		return lark.ChatPage{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	call := &platformCall{owner: req.RoomIdentity, connectionID: a.ConnectionID, cancel: cancel, done: make(chan struct{})}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	client, err := lark.New(*a.CLIProfile)
	if err != nil {
		return lark.ChatPage{}, err
	}
	page, err := client.Chats(callCtx, a.Identity, lark.ChatsRequest{PageSize: 100, PageToken: req.PageToken})
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
