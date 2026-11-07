package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/gitlab"
)

type gitlabProjectsRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	RoomIdentity
	Caller            Caller `json:"caller"`
	ConnectionID      string `json:"connection_id"`
	ConnectionVersion uint64 `json:"connection_version"`
	Search            string `json:"search,omitempty"`
	Page              int    `json:"page,omitempty"`
}

func (m *roomManager) checkGitLabProjectsLocked(ctx context.Context, req gitlabProjectsRequest, expected *connection.Account) (connection.Account, platform.Secret, error) {
	if err := ctx.Err(); err != nil {
		return connection.Account{}, platform.Secret{}, err
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		return connection.Account{}, platform.Secret{}, ErrIdentity
	}
	if m.busy || m.roomBusyLocked(req.RoomIdentity) {
		return connection.Account{}, platform.Secret{}, ErrBusy
	}
	a, secret, err := m.resolvePlatformAccountLocked(req.RoomIdentity, req.ConnectionID, "gitlab")
	if err != nil {
		return a, secret, err
	}
	if a.ConnectionVersion != req.ConnectionVersion || expected != nil && a != *expected {
		return a, secret, ErrConflict
	}
	return a, secret, nil
}

func (m *roomManager) listGitLabProjects(ctx context.Context, req gitlabProjectsRequest) (gitlab.ProjectPage, error) {
	if !gitlab.ValidProjectSearch(req.Search) || req.Page < 0 || req.Page > 1000 {
		return gitlab.ProjectPage{}, ErrRequest
	}
	if req.Page == 0 {
		req.Page = 1
	}
	req.Search = strings.TrimSpace(req.Search)
	m.mu.Lock()
	a, secret, err := m.checkGitLabProjectsLocked(ctx, req, nil)
	if err != nil {
		m.mu.Unlock()
		return gitlab.ProjectPage{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
	call := &platformCall{owner: req.RoomIdentity, connectionID: a.ConnectionID, cancel: cancel, done: make(chan struct{})}
	if m.platformCalls == nil {
		m.platformCalls = map[*platformCall]bool{}
	}
	m.platformCalls[call] = true
	m.mu.Unlock()
	defer m.finishPlatformCall(call)
	page, err := gitlab.ListProjects(callCtx, m.gitlabAccountRead(req.RoomIdentity, a, secret), req.Search, req.Page)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, _, checkErr := m.checkGitLabProjectsLocked(callCtx, req, &a); checkErr != nil {
		return gitlab.ProjectPage{}, checkErr
	}
	return page, err
}

func (m *roomManager) serveGitLabProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req gitlabProjectsRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	page, err := m.listGitLabProjects(r.Context(), req)
	if err != nil {
		if errors.Is(err, platform.ErrCredential) {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(struct {
				Code string `json:"code"`
			}{"credential"})
			return
		}
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(page)
}
