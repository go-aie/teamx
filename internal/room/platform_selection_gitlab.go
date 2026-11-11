package room

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Empty provider is the persisted Feishu selection contract.
func selectionProvider(in platformSelectionInput) string {
	if in.Provider == "" {
		return "lark"
	}
	return in.Provider
}
func validGitLabSelectionInput(in platformSelectionInput) bool {
	if !gitlab.ValidProjectSearch(in.Query) {
		return false
	}
	if in.Kind == "account" {
		_, origin, err := registry.Normalize("GitLab", in.Origin)
		return err == nil && origin == in.Origin && in.PlatformID == "" && in.PlatformVersion == 0 && in.Query == ""
	}
	return in.Kind == "projects" && in.Origin == "" && tool.OpaqueID(in.PlatformID) && in.PlatformVersion > 0
}
func validGitLabSelectionDraft(in platformSelectionInput, d platformSelectionDraft) bool {
	if len(d.ChatIDs) != 0 || d.GroupMessageMode != "" {
		return false
	}
	if in.Kind == "account" {
		if d.ProjectID != 0 || d.ProjectPath != "" || d.EventsFilter != nil {
			return false
		}
		if d.Name != "" {
			name, _, err := registry.Normalize(d.Name, in.Origin)
			if err != nil || name != d.Name {
				return false
			}
		}
		return d.PlatformID == "" && d.PlatformVersion == 0 || tool.OpaqueID(d.PlatformID) && d.PlatformVersion > 0
	}
	if d.PlatformID != "" || d.PlatformVersion != 0 || d.Name != "" || d.ProjectID < 0 || (d.ProjectID == 0) != (d.ProjectPath == "") || d.ProjectPath != "" && (!workspaceProjectPath(d.ProjectPath) || !strings.Contains(d.ProjectPath, "/")) {
		return false
	}
	if d.EventsFilter != nil {
		_, err := gitlab.NormalizeEventsConfig(gitlab.EventsConfig{Filter: *d.EventsFilter})
		return err == nil
	}
	return true
}
func selectionProjectPage(token string) (int, error) {
	n, e := strconv.Atoi(token)
	if e != nil || n < 1 || n > 1000 {
		return 0, ErrRequest
	}
	return n, nil
}

// Called after every remote candidate read; account and member are checked again
// even when another tab has submitted or cancelled the form during the read.
func (m *roomManager) checkProjectSelectionCurrentLocked(ctx context.Context, req platformSelectionRequest, s platformSelection, member platformMemberRequest) error {
	current, err := m.readPlatformSelectionLocked(s.ID)
	if err == nil {
		err = m.checkSelectionCallerLocked(ctx, req, &current, true)
	}
	if err == nil && (current.Status != "waiting" || current.Version != s.Version || !time.Now().Before(current.ExpiresAt)) {
		err = ErrConflict
	}
	if err != nil {
		return err
	}
	a, err := m.platformMemberAccountLocked(ctx, member)
	if err == nil && a.Identity != s.Account {
		err = ErrConflict
	}
	return err
}
func (m *roomManager) submitGitLabProjectSelection(ctx context.Context, req platformSelectionRequest, s platformSelection, member platformMemberRequest) (platformSelection, error) {
	if s.Draft.ProjectID <= 0 {
		return s, ErrRequest
	}
	q := member.projectsRequest()
	q.verifyID, q.verifyPath = s.Draft.ProjectID, s.Draft.ProjectPath
	page, err := m.listGitLabProjects(ctx, q)
	if err != nil {
		return s, err
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
	s.Project = &page.Projects[0]
	s.Status = "completed"
	s.ResultVersion = 1
	s.SubmittedVersion = s.Version
	s.Version++
	s.ResultAt = time.Now().UTC()
	err = m.savePlatformSelectionLocked(s)
	if err == nil {
		m.wakeConnectNotificationsLocked()
	}
	return s, err
}
