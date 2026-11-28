package room

import (
	"context"
	"time"

	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/scion"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func validScionSelectionInput(in platformSelectionInput) bool {
	if in.Kind == "repositories" {
		return in.Origin == "" && tool.OpaqueID(in.PlatformID) && in.PlatformVersion > 0 && scion.ValidRepositorySearch(in.Query)
	}
	if in.Kind != "account" || in.PlatformID != "" || in.PlatformVersion != 0 || in.Query != "" {
		return false
	}
	if in.Origin == "" {
		return true
	}
	_, normalized, err := registry.Normalize("Scion", in.Origin)
	return err == nil && normalized == in.Origin
}

func validScionSelectionDraft(in platformSelectionInput, d platformSelectionDraft) bool {
	if len(d.ChatIDs) != 0 || d.GroupMessageMode != "" || d.ProjectID != 0 || d.ProjectPath != "" || d.EventsFilter != nil {
		return false
	}
	if in.Kind == "repositories" {
		return d.PlatformID == "" && d.PlatformVersion == 0 && d.Name == "" && (d.Repository == nil || d.Repository.Valid())
	}
	return d.Repository == nil && len(d.Name) <= 128 && (d.Name == "" || validText(d.Name)) && (d.PlatformID == "" && d.PlatformVersion == 0 || tool.OpaqueID(d.PlatformID) && d.PlatformVersion > 0)
}

func (m *roomManager) submitScionRepositorySelection(ctx context.Context, req platformSelectionRequest, s platformSelection, member platformMemberRequest) (platformSelection, error) {
	if s.Draft.Repository == nil {
		return s, ErrRequest
	}
	m.mu.Lock()
	a, err := m.checkScionRepositoriesLocked(ctx, member.repositoriesRequest())
	m.mu.Unlock()
	if err != nil {
		return s, notificationRoomError(err)
	}
	q := scion.QueryScope{Owner: s.RoomIdentity, Target: platformMemberTarget(s.MemberIdentity), Binding: scionBinding(a), Profile: *a.CLIProfile, PlatformVersion: a.PlatformVersion}
	repo, err := scion.ReadRepository(ctx, m.executeScionNotification, q, s.Draft.Repository.Name)
	if err != nil {
		return s, notificationRoomError(err)
	}
	if repo != *s.Draft.Repository {
		return s, ErrConflict
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	current, err := m.readPlatformSelectionLocked(s.ID)
	if err == nil {
		err = m.checkSelectionCallerLocked(ctx, req, &current, true)
	}
	if err == nil && current.Status == "completed" && current.SubmittedVersion == req.Version {
		return current, nil
	}
	if err == nil {
		err = m.checkProjectSelectionCurrentLocked(ctx, req, s, member)
	}
	if err != nil {
		return s, err
	}
	s.Status = "completed"
	s.ResultVersion = 1
	s.ResultActor = req.Caller.Author
	s.SubmittedVersion = s.Version
	s.Version++
	s.ResultAt = time.Now().UTC()
	err = m.savePlatformSelectionLocked(s)
	if err == nil {
		m.wakeConnectNotificationsLocked()
	}
	return s, err
}
