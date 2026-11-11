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
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

type gitlabProjectsRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	RoomIdentity
	Caller          Caller `json:"caller"`
	PlatformID      string `json:"platform_id"`
	PlatformVersion uint64 `json:"platform_version"`
	Search          string `json:"search,omitempty"`
	Page            int    `json:"page,omitempty"`
	member          *toolsRequest
	verifyID        int64
	verifyPath      string
}

func (m *roomManager) checkGitLabProjectsLocked(ctx context.Context, req gitlabProjectsRequest, expected *registry.Account) (registry.Account, platform.Secret, error) {
	if err := ctx.Err(); err != nil {
		return registry.Account{}, platform.Secret{}, err
	}
	if req.member != nil {
		if req.member.RoomIdentity != req.RoomIdentity || req.member.ServiceEpoch != req.ServiceEpoch || req.member.Caller != req.Caller {
			return registry.Account{}, platform.Secret{}, ErrIdentity
		}
		if err := m.checkToolMemberLocked(ctx, *req.member); err != nil {
			return registry.Account{}, platform.Secret{}, err
		}
	} else if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		return registry.Account{}, platform.Secret{}, ErrIdentity
	}
	if m.busy || m.roomBusyLocked(req.RoomIdentity) {
		return registry.Account{}, platform.Secret{}, ErrBusy
	}
	a, secret, err := m.resolvePlatformAccountLocked(req.RoomIdentity, req.PlatformID, "gitlab")
	if err != nil {
		return a, secret, err
	}
	if a.PlatformVersion != req.PlatformVersion || expected != nil && a != *expected {
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
	var page gitlab.ProjectPage
	if req.verifyID != 0 {
		var p gitlab.Project
		p, err = gitlab.GetProject(callCtx, m.gitlabAccountRead(req.RoomIdentity, a, secret), req.verifyID)
		if err == nil && p.Path != req.verifyPath {
			err = ErrConflict
		}
		if err == nil {
			page.Projects = []gitlab.Project{p}
		}
	} else {
		page, err = gitlab.ListProjects(callCtx, m.gitlabAccountRead(req.RoomIdentity, a, secret), req.Search, req.Page)
	}
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

func writePlatformSetupReadError(w http.ResponseWriter, err error) {
	if code := gitlab.ProjectReadErrorCode(err); code != "" {
		status := http.StatusBadGateway
		switch code {
		case "permission_denied":
			status = http.StatusForbidden
		case "credential":
			status = http.StatusUnauthorized
		case "not_found":
			status = http.StatusNotFound
		case "invalid_request":
			status = http.StatusBadRequest
		case "rate_limited":
			status = http.StatusTooManyRequests
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(struct {
			Code string `json:"code"`
		}{code})
		return
	}
	writeLarkReadError(w, err)
}
