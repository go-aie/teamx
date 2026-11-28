package room

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
)

type scionRepositoriesRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	RoomIdentity
	Caller          Caller              `json:"caller"`
	PlatformID      string              `json:"platform_id"`
	PlatformVersion uint64              `json:"platform_version"`
	Target          notification.Target `json:"target"`
	Query           string              `json:"query,omitempty"`
	PageToken       string              `json:"page_token,omitempty"`
	member          *toolsRequest
}

func (req platformMemberRequest) repositoriesRequest() scionRepositoriesRequest {
	return scionRepositoriesRequest{ServiceEpoch: req.ServiceEpoch, RoomIdentity: req.RoomIdentity, Caller: req.Caller, PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion, Target: platformMemberTarget(req.member()), Query: req.Query, PageToken: req.PageToken, member: &toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}}
}

func (m *roomManager) checkScionRepositoriesLocked(ctx context.Context, req scionRepositoriesRequest) (registry.Account, error) {
	if req.member != nil {
		if req.member.RoomIdentity != req.RoomIdentity || req.member.ServiceEpoch != req.ServiceEpoch || req.member.Caller != req.Caller || req.Target != platformMemberTarget(MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}) {
			return registry.Account{}, ErrIdentity
		}
		if err := m.checkToolMemberLocked(ctx, *req.member); err != nil {
			return registry.Account{}, err
		}
	} else if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		return registry.Account{}, ErrIdentity
	}
	if m.busy || m.roomBusyLocked(req.RoomIdentity) {
		return registry.Account{}, ErrBusy
	}
	if req.Target.Mode != "inbox" {
		return registry.Account{}, ErrRequest
	}
	if err := m.checkNotificationTargetLocked(ctx, req.RoomIdentity, req.Target); err != nil {
		return registry.Account{}, err
	}
	a, _, err := m.resolvePlatformAccountLocked(req.RoomIdentity, req.PlatformID, "scion")
	if err == nil && (a.PlatformVersion != req.PlatformVersion || a.CLIProfile == nil) {
		err = ErrConflict
	}
	return a, err
}

func (m *roomManager) listScionRepositories(ctx context.Context, req scionRepositoriesRequest) (scion.RepositoryPage, error) {
	m.mu.Lock()
	a, err := m.checkScionRepositoriesLocked(ctx, req)
	m.mu.Unlock()
	if err != nil {
		return scion.RepositoryPage{}, err
	}
	q := scion.QueryScope{Owner: req.RoomIdentity, Target: req.Target, Binding: scionBinding(a), Profile: *a.CLIProfile, PlatformVersion: a.PlatformVersion}
	page, err := scion.ListRepositories(ctx, m.executeScionNotification, q, req.Query, req.PageToken)
	m.mu.Lock()
	defer m.mu.Unlock()
	current, checkErr := m.checkScionRepositoriesLocked(ctx, req)
	if checkErr != nil {
		return scion.RepositoryPage{}, checkErr
	}
	if scionBinding(current) != q.Binding {
		return scion.RepositoryPage{}, ErrConflict
	}
	return page, notificationRoomError(err)
}

func (m *roomManager) serveScionRepositories(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req scionRepositoriesRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	page, err := m.listScionRepositories(r.Context(), req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(page)
}
