package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// A selection saves human intent, never credentials or configuration. The Agent
// applies its immutable receipt using the existing connect/member commands.
type platformSelectionInput struct {
	Kind            string `json:"kind"`
	Provider        string `json:"provider,omitempty"`
	Origin          string `json:"origin,omitempty"`
	PlatformID      string `json:"platform_id,omitempty"`
	PlatformVersion uint64 `json:"platform_version,omitempty"`
	Query           string `json:"query,omitempty"`
}
type platformSelectionDraft struct {
	PlatformID       string               `json:"platform_id,omitempty"`
	PlatformVersion  uint64               `json:"platform_version,omitempty"`
	Name             string               `json:"name,omitempty"`
	ChatIDs          []string             `json:"chat_ids,omitempty"`
	GroupMessageMode string               `json:"group_message_mode,omitempty"`
	ProjectID        int64                `json:"project_id,omitempty"`
	ProjectPath      string               `json:"project_path,omitempty"`
	EventsFilter     *gitlab.EventsFilter `json:"events_filter,omitempty"`
}
type platformSelection struct {
	Schema int `json:"schema"`
	MemberIdentity
	ID               string                   `json:"operation_id"`
	Source           RoomMessage              `json:"source"`
	Input            platformSelectionInput   `json:"input"`
	InitialDraft     platformSelectionDraft   `json:"initial_draft"`
	Draft            platformSelectionDraft   `json:"draft"`
	Version          uint64                   `json:"version"`
	SubmittedVersion uint64                   `json:"submitted_version,omitempty"`
	ResultVersion    uint64                   `json:"result_version"`
	Status           string                   `json:"status"`
	Account          platform.AccountIdentity `json:"account"`
	Project          *gitlab.Project          `json:"project,omitempty"`
	Chats            []lark.Chat              `json:"chats,omitempty"`
	CreatedAt        time.Time                `json:"created_at"`
	ExpiresAt        time.Time                `json:"expires_at"`
	ResultAt         time.Time                `json:"result_at,omitzero"`
}
type platformSelectionRequest struct {
	ServiceInfo
	Caller        Caller                  `json:"caller"`
	OperationID   string                  `json:"operation_id,omitempty"`
	SourceSeq     uint64                  `json:"source_seq,omitempty"`
	Version       uint64                  `json:"version,omitempty"`
	ResultVersion uint64                  `json:"result_version,omitempty"`
	Input         *platformSelectionInput `json:"input,omitempty"`
	Draft         *platformSelectionDraft `json:"draft,omitempty"`
	Notify        *string                 `json:"notify,omitempty"`
	Query         string                  `json:"query,omitempty"`
	PageToken     string                  `json:"page_token,omitempty"`
}
type platformSelectionView struct {
	platformSelection
	SourceSeq       uint64                        `json:"source_seq"`
	Interaction     platformConnectInteraction    `json:"interaction"`
	TargetAvailable bool                          `json:"target_available"`
	Continuation    platformOperationContinuation `json:"continuation"`
}

func (m *roomManager) selectionPath(id string) string {
	return filepath.Join(m.store.dir, "platform-selections", id+".json")
}
func validSelectionDraft(in platformSelectionInput, d platformSelectionDraft) bool {
	if selectionProvider(in) == "gitlab" {
		return validGitLabSelectionDraft(in, d)
	}
	if d.ProjectID != 0 || d.ProjectPath != "" || d.EventsFilter != nil {
		return false
	}
	if in.Kind == "account" {
		if d.Name != "" {
			name, _, err := registry.Normalize(d.Name, lark.Origin("feishu"))
			if err != nil || name != d.Name {
				return false
			}
		}
		return len(d.ChatIDs) == 0 && d.GroupMessageMode == "" && len(d.Name) <= 128 && (d.Name == "" || validText(d.Name)) && (d.PlatformID == "" && d.PlatformVersion == 0 || tool.OpaqueID(d.PlatformID) && d.PlatformVersion > 0)
	}
	if in.Kind != "groups" || d.PlatformID != "" || d.PlatformVersion != 0 || d.Name != "" || len(d.ChatIDs) > 32 || d.GroupMessageMode != "" && d.GroupMessageMode != "all" && d.GroupMessageMode != "mentions" {
		return false
	}
	seen := map[string]bool{}
	for _, id := range d.ChatIDs {
		if !(lark.Chat{ID: id, Mode: "group"}).Valid() || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}
func (m *roomManager) readPlatformSelectionLocked(id string) (platformSelection, error) {
	var s platformSelection
	if !tool.OpaqueID(id) {
		return s, ErrRequest
	}
	if err := m.store.check(false); err != nil {
		return s, err
	}
	info, err := os.Lstat(m.selectionPath(id))
	if err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() {
		return s, ErrInvalid
	}
	if err = readSnapshot(m.selectionPath(id), &s, 128<<10); err != nil {
		return s, err
	}
	if s.Schema != 1 || s.ID != id || !tool.OpaqueID(s.RoomInstance) || !safeMembershipID(s.MembershipID) || !validText(s.RoomID) || !validText(s.AgentID) || !validHumanEvidence(s.Source) || s.Version == 0 || s.CreatedAt.IsZero() || !s.ExpiresAt.After(s.CreatedAt) || !validSelectionInput(s.Input) || !validSelectionDraft(s.Input, s.Draft) || !validSelectionDraft(s.Input, s.InitialDraft) {
		return s, ErrInvalid
	}
	if s.Status == "waiting" {
		if s.ResultVersion != 0 || !s.ResultAt.IsZero() || s.SubmittedVersion != 0 {
			return s, ErrInvalid
		}
	} else if s.Status != "completed" && s.Status != "cancelled" && s.Status != "expired" || s.ResultVersion != 1 || s.ResultAt.IsZero() || s.SubmittedVersion == 0 {
		return s, ErrInvalid
	}
	if s.Input.Kind != "projects" && s.Project != nil || selectionProvider(s.Input) == "gitlab" && len(s.Chats) != 0 {
		return s, ErrInvalid
	}
	if s.Status == "completed" {
		if selectionProvider(s.Input) == "gitlab" && s.Input.Kind == "account" && (s.Draft.PlatformID == "" || !s.Account.Valid() || s.Account.Instance != s.Input.Origin) {
			return s, ErrInvalid
		}
		if s.Input.Kind == "projects" && (s.Account.Provider != "gitlab" || !s.Account.Valid() || s.Project == nil || s.Project.ID != s.Draft.ProjectID || s.Project.Path != s.Draft.ProjectPath || s.Project.Name == "") {
			return s, ErrInvalid
		}
		if s.Account.Valid() && s.Account.Provider != selectionProvider(s.Input) {
			return s, ErrInvalid
		}
		if s.Input.Kind == "groups" && (!s.Account.Valid() || len(s.Chats) != len(s.Draft.ChatIDs) || len(s.Chats) == 0) {
			return s, ErrInvalid
		}
		for i, c := range s.Chats {
			if !c.Valid() || c.Mode == "p2p" || c.ID != s.Draft.ChatIDs[i] {
				return s, ErrInvalid
			}
		}
		if s.Input.Kind == "account" && (s.Draft.PlatformID != "" && !s.Account.Valid() || !validText(s.Draft.Name)) {
			return s, ErrInvalid
		}
	}
	return s, nil
}
func validSelectionInput(in platformSelectionInput) bool {
	if selectionProvider(in) == "gitlab" {
		return validGitLabSelectionInput(in)
	}
	if in.Provider != "" && in.Provider != "lark" || in.Origin != "" {
		return false
	}
	return len(in.Query) <= 256 && !strings.ContainsAny(in.Query, "\x00\r\n") && (in.Kind == "account" && in.PlatformID == "" && in.PlatformVersion == 0 && in.Query == "" || in.Kind == "groups" && tool.OpaqueID(in.PlatformID) && in.PlatformVersion > 0)
}
func (m *roomManager) savePlatformSelectionLocked(s platformSelection) error {
	if err := m.store.check(true); err != nil {
		return err
	}
	return m.store.save(m.selectionPath(s.ID), s, 128<<10)
}
func (m *roomManager) platformSelectionsLocked() ([]platformSelection, error) {
	dir := filepath.Dir(m.selectionPath("unused"))
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []platformSelection{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrInvalid
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	if len(entries) > 1000 {
		return nil, ErrCapacity
	}
	result := []platformSelection{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, err := m.readPlatformSelectionLocked(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, nil
}
func (s platformSelection) memberRequest(info ServiceInfo) platformMemberRequest {
	return platformMemberRequest{ServiceInfo: info, Caller: agentCallerIdentity(s.MemberIdentity), Input: &platformMemberInput{PlatformID: s.Input.PlatformID, PlatformVersion: s.Input.PlatformVersion}}
}
func (m *roomManager) selectionViewLocked(s platformSelection) (platformSelectionView, error) {
	svc, _ := m.runtimeServiceLocked(s.RoomIdentity)
	available := false
	if svc != nil {
		snapshot, err := svc.store.LoadRoom()
		if err != nil {
			return platformSelectionView{}, err
		}
		for _, a := range snapshot.Members {
			if a.AgentID == s.AgentID && a.MembershipID == s.MembershipID {
				available = true
			}
		}
	}
	n, err := m.readSelectionNotificationLocked(s)
	if err != nil {
		return platformSelectionView{}, err
	}
	continuation, err := m.operationContinuationLocked(s.MemberIdentity, s.Status != "waiting", n)
	return platformSelectionView{platformSelection: s, SourceSeq: s.Source.Seq, Interaction: platformConnectInteraction{ID: s.ID, Kind: "platform_" + s.Input.Kind}, TargetAvailable: available, Continuation: continuation}, err
}
func (m *roomManager) checkSelectionCallerLocked(ctx context.Context, req platformSelectionRequest, s *platformSelection, human bool) error {
	if !human {
		if err := m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}); err != nil {
			return err
		}
		if s != nil && s.MemberIdentity != (MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}) {
			return ErrIdentity
		}
		return nil
	}
	svc, rt := m.runtimeServiceLocked(req.RoomIdentity)
	if req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" || svc == nil || req.ServiceInfo != svc.info || s != nil && (s.RoomIdentity != req.RoomIdentity || s.Source.Author.ID != req.Caller.ID) {
		return ErrIdentity
	}
	if m.closing {
		return ErrClosing
	}
	if m.busy || rt.busy || rt.status != "ready" {
		return ErrBusy
	}
	return nil
}
func (m *roomManager) startPlatformSelectionLocked(ctx context.Context, req platformSelectionRequest) (platformSelection, error) {
	var s platformSelection
	if req.Input == nil || !validSelectionInput(*req.Input) || !tool.OpaqueID(req.OperationID) || req.SourceSeq == 0 {
		return s, ErrRequest
	}
	d := platformSelectionDraft{}
	if req.Draft != nil {
		d = *req.Draft
	}
	if !validSelectionDraft(*req.Input, d) {
		return s, ErrRequest
	}
	prior, err := m.readPlatformSelectionLocked(req.OperationID)
	if err == nil {
		if prior.MemberIdentity != (MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}) {
			return s, ErrIdentity
		}
		if prior.Source.Seq != req.SourceSeq || prior.Input != *req.Input || !reflect.DeepEqual(prior.InitialDraft, d) {
			return s, ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	if _, e := m.readPlatformConnectLocked(req.OperationID); !errors.Is(e, os.ErrNotExist) {
		if e == nil {
			return s, ErrConflict
		}
		return s, e
	}
	all, err := m.platformSelectionsLocked()
	if err != nil {
		return s, err
	}
	if len(all) >= 1000 {
		return s, ErrCapacity
	}
	for _, p := range all {
		if p.RoomIdentity == req.RoomIdentity && p.AgentID == req.Caller.ID && p.MembershipID == req.Caller.MembershipID && p.Source.Seq == req.SourceSeq && p.Input.Kind == req.Input.Kind && selectionProvider(p.Input) == selectionProvider(*req.Input) {
			return s, conflictReason("source_seq", "selection already exists", "Query selection status with the original source_seq.")
		}
	}
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	snapshot, err := svc.store.LoadRoom()
	if err != nil {
		return s, err
	}
	source, err := roomHumanMessage(snapshot, req.SourceSeq)
	if err != nil {
		return s, ErrIdentity
	}
	now := time.Now().UTC()
	s = platformSelection{Schema: 1, MemberIdentity: MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}, ID: req.OperationID, Source: source, Input: *req.Input, InitialDraft: d, Draft: d, Version: 1, Status: "waiting", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	if s.Input.Kind == "projects" {
		a, err := m.platformMemberAccountLocked(ctx, s.memberRequest(req.ServiceInfo))
		if err != nil {
			return s, err
		}
		if a.Identity.Provider != "gitlab" {
			return s, ErrRequest
		}
		s.Account = a.Identity
	}
	if s.Input.Kind == "groups" {
		a, err := m.checkLarkChatsLocked(ctx, s.memberRequest(req.ServiceInfo).chatsRequest(), nil)
		if err != nil {
			return s, err
		}
		s.Account = a.Identity
	}
	return s, m.savePlatformSelectionLocked(s)
}

// Submission verifies the selected stable IDs outside the manager gate, then
// rechecks the exact draft version before saving the single immutable result.
func (m *roomManager) submitPlatformSelection(ctx context.Context, req platformSelectionRequest) (platformSelection, error) {
	m.mu.Lock()
	s, err := m.readPlatformSelectionLocked(req.OperationID)
	if err == nil {
		err = m.checkSelectionCallerLocked(ctx, req, &s, true)
	}
	if err != nil {
		m.mu.Unlock()
		return s, err
	}
	if s.Status == "completed" && s.SubmittedVersion == req.Version {
		m.mu.Unlock()
		return s, nil
	}
	if s.Status != "waiting" || s.Version != req.Version || !time.Now().Before(s.ExpiresAt) {
		m.mu.Unlock()
		return s, ErrConflict
	}
	svc, _ := m.runtimeServiceLocked(s.RoomIdentity)
	member := s.memberRequest(svc.info)
	if err = m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: member.ServiceInfo, Caller: member.Caller}); err != nil {
		m.mu.Unlock()
		return s, err
	}
	if s.Input.Kind == "account" {
		if s.Draft.PlatformID != "" {
			file, e := m.platforms.Load()
			err = e
			found := false
			for _, c := range file.Platforms {
				if c.ID != s.Draft.PlatformID {
					continue
				}
				found = true
				if c.Provider != selectionProvider(s.Input) || c.Version != s.Draft.PlatformVersion || s.Input.Origin != "" && c.URL != s.Input.Origin {
					err = ErrConflict
					break
				}
				a, _, e := registry.ResolveAccessAccount(file, c)
				err = e
				if err == nil {
					q := member
					q.Input = &platformMemberInput{PlatformID: c.ID, PlatformVersion: c.Version}
					var current registry.Account
					current, err = m.platformMemberAccountLocked(ctx, q)
					if err == nil && !reflect.DeepEqual(current, a) {
						err = ErrConflict
					}
				}
				if err == nil {
					s.Account = a.Identity
					s.Draft.Name = c.Name
				}
			}
			if !found {
				err = ErrConflict
			}
		} else if selectionProvider(s.Input) == "gitlab" || !validText(s.Draft.Name) {
			err = ErrRequest
		}
		if err != nil {
			m.mu.Unlock()
			return s, err
		}
	} else if s.Input.Kind == "projects" {
		m.mu.Unlock()
		return m.submitGitLabProjectSelection(ctx, req, s, member)
	} else {
		if len(s.Draft.ChatIDs) == 0 {
			m.mu.Unlock()
			return s, ErrRequest
		}
		q := member.chatsRequest()
		q.verifyIDs = s.Draft.ChatIDs
		m.mu.Unlock()
		page, e := m.listLarkChats(ctx, q)
		m.mu.Lock()
		if e != nil {
			m.mu.Unlock()
			return s, e
		}
		current, e := m.readPlatformSelectionLocked(s.ID)
		if e == nil {
			e = m.checkSelectionCallerLocked(ctx, req, &current, true)
		}
		if e == nil && current.Status == "completed" && current.SubmittedVersion == req.Version {
			m.mu.Unlock()
			return current, nil
		}
		if e == nil && (current.Status != "waiting" || current.Version != req.Version || !time.Now().Before(current.ExpiresAt)) {
			e = ErrConflict
		}
		if e == nil {
			a, checkErr := m.checkLarkChatsLocked(ctx, q, nil)
			e = checkErr
			if e == nil && a.Identity != s.Account {
				e = ErrConflict
			}
		}
		if e != nil {
			m.mu.Unlock()
			return s, e
		}
		s.Chats = page.Chats
	}
	s.Status = "completed"
	s.ResultVersion = 1
	s.SubmittedVersion = s.Version
	s.Version++
	s.ResultAt = time.Now().UTC()
	err = m.savePlatformSelectionLocked(s)
	if err == nil {
		m.wakeConnectNotificationsLocked()
	}
	m.mu.Unlock()
	return s, err
}

func (m *roomManager) servePlatformSelection(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	human := strings.HasPrefix(r.URL.Path, "/v1/platforms/selection-interactions/")
	if !human && (r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "") {
		writeAPIError(w, ErrIdentity)
		return
	}
	var req platformSelectionRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	prefix := "/v1/platforms/selection/"
	if human {
		prefix = "/v1/platforms/selection-interactions/"
	}
	action := strings.TrimPrefix(r.URL.Path, prefix)
	// Reject irrelevant fields instead of silently interpreting malformed requests.
	if req.Input != nil && action != "start" || req.Draft != nil && action != "start" && action != "update" || req.Notify != nil && action != "start" && action != "notify" || req.ResultVersion != 0 && action != "acknowledge" || (req.Query != "" || req.PageToken != "") && action != "candidates" || req.Version != 0 && action != "update" && action != "submit" && action != "cancel" || req.SourceSeq != 0 && action != "start" && action != "status" || human && (action == "start" || action == "notify" || action == "acknowledge" || action == "status") || !human && (action == "submit" || action == "list" || action == "candidates") {
		writeAPIError(w, ErrRequest)
		return
	}
	if action == "status" && (req.OperationID == "") == (req.SourceSeq == 0) {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.Notify != nil && *req.Notify != "none" && *req.Notify != "inbox" {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	err := m.checkSelectionCallerLocked(r.Context(), req, nil, human)
	var result any
	var s platformSelection
	if err == nil && (action == "list" || action == "status" && req.OperationID == "") {
		if req.Input != nil || req.Draft != nil || req.OperationID != "" || action == "status" && req.SourceSeq == 0 {
			err = ErrRequest
		} else {
			var all []platformSelection
			all, err = m.platformSelectionsLocked()
			views := []platformSelectionView{}
			for _, op := range all {
				if op.RoomIdentity != req.RoomIdentity || human && op.Source.Author.ID != req.Caller.ID || !human && (op.AgentID != req.Caller.ID || op.MembershipID != req.Caller.MembershipID || op.Source.Seq != req.SourceSeq) {
					continue
				}
				var v platformSelectionView
				v, err = m.selectionViewLocked(op)
				if err != nil {
					break
				}
				views = append(views, v)
			}
			if err == nil && action == "status" && len(views) == 0 {
				err = os.ErrNotExist
			}
			result = struct {
				Interactions []platformSelectionView `json:"interactions"`
			}{views}
		}
	} else if err == nil {
		if action == "start" {
			s, err = m.startPlatformSelectionLocked(r.Context(), req)
		} else {
			s, err = m.readPlatformSelectionLocked(req.OperationID)
			if err == nil {
				err = m.checkSelectionCallerLocked(r.Context(), req, &s, human)
			}
		}
		if err == nil {
			switch action {
			case "start", "notify":
				if action == "notify" && req.Notify == nil {
					err = ErrRequest
				} else if req.Notify != nil {
					var n platformOperationNotification
					n, err = m.readSelectionNotificationLocked(s)
					if err == nil {
						n.Mode = *req.Notify
						err = m.saveSelectionNotificationLocked(n)
						m.wakeConnectNotificationsLocked()
					}
				}
			case "status":
			case "update", "cancel":
				if req.Version == 0 {
					err = ErrRequest
					break
				}
				if action == "cancel" && s.Status == "cancelled" && s.SubmittedVersion == req.Version {
					break
				}
				if s.Status != "waiting" || s.Version != req.Version || !time.Now().Before(s.ExpiresAt) {
					err = ErrConflict
					break
				}
				if action == "update" {
					if req.Draft == nil || !validSelectionDraft(s.Input, *req.Draft) {
						err = ErrRequest
						break
					}
					s.Draft = *req.Draft
				} else {
					s.Status = "cancelled"
					s.ResultVersion = 1
					s.SubmittedVersion = s.Version
					s.ResultAt = time.Now().UTC()
				}
				s.Version++
				err = m.savePlatformSelectionLocked(s)
				if err == nil {
					m.wakeConnectNotificationsLocked()
				}
			case "acknowledge":
				if s.ResultVersion == 0 || req.ResultVersion != s.ResultVersion {
					err = ErrConflict
					break
				}
				var n platformOperationNotification
				n, err = m.readSelectionNotificationLocked(s)
				if err == nil {
					n.HandledVersion = req.ResultVersion
					err = m.saveSelectionNotificationLocked(n)
				}
			case "submit":
				m.mu.Unlock()
				s, err = m.submitPlatformSelection(r.Context(), req)
				m.mu.Lock()
			case "candidates":
				if s.Status != "waiting" || !time.Now().Before(s.ExpiresAt) {
					err = ErrConflict
					break
				}
				svc, _ := m.runtimeServiceLocked(s.RoomIdentity)
				member := s.memberRequest(svc.info)
				err = m.checkToolMemberLocked(r.Context(), toolsRequest{ServiceInfo: member.ServiceInfo, Caller: member.Caller})
				if err != nil {
					break
				}
				if s.Input.Kind == "account" {
					result, err = m.selectionAccountsForInputLocked(s.Input)
				} else if s.Input.Kind == "projects" {
					q := member.projectsRequest()
					q.Search = req.Query
					var page gitlab.ProjectPage
					if req.PageToken != "" {
						n, e := selectionProjectPage(req.PageToken)
						err = e
						q.Page = n
					}
					if err == nil {
						m.mu.Unlock()
						page, err = m.listGitLabProjects(r.Context(), q)
						m.mu.Lock()
						if err == nil {
							err = m.checkProjectSelectionCurrentLocked(r.Context(), req, s, member)
						}
					}
					result = page
				} else {
					q := member.chatsRequest()
					q.Query = req.Query
					q.PageToken = req.PageToken
					m.mu.Unlock()
					var page lark.ChatPage
					page, err = m.listLarkChats(r.Context(), q)
					m.mu.Lock()
					if err == nil {
						var current platformSelection
						current, err = m.readPlatformSelectionLocked(s.ID)
						if err == nil {
							err = m.checkSelectionCallerLocked(r.Context(), req, &current, true)
						}
						if err == nil && (current.Status != "waiting" || !time.Now().Before(current.ExpiresAt)) {
							err = ErrConflict
						}
					}
					page.Chats = slices.DeleteFunc(page.Chats, func(c lark.Chat) bool { return c.Mode == "p2p" })
					result = page
				}
			default:
				err = ErrRequest
			}
			if err == nil && result == nil {
				if req.Notify != nil && *req.Notify == "inbox" {
					m.startConnectNotificationsLocked()
				}
				if action != "status" {
					m.wakeConnectNotificationsLocked()
				}
				result, err = m.selectionViewLocked(s)
			}
		}
	}
	m.mu.Unlock()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(apiError{Error: "selection not found", Code: "operation_not_found", Message: "No selection matches this request."})
		} else {
			writePlatformSetupReadError(w, err)
		}
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

type platformSelectionAccount struct {
	Username string                   `json:"username,omitempty"`
	ID       string                   `json:"id"`
	Name     string                   `json:"name"`
	Version  uint64                   `json:"version"`
	Account  platform.AccountIdentity `json:"account"`
}

func (m *roomManager) selectionAccountsForInputLocked(in platformSelectionInput) ([]platformSelectionAccount, error) {
	file, err := m.platforms.Load()
	if err != nil {
		return nil, err
	}
	out := []platformSelectionAccount{}
	for _, c := range file.Platforms {
		if c.Provider != selectionProvider(in) || in.Origin != "" && c.URL != in.Origin {
			continue
		}
		a, _, e := registry.ResolveAccessAccount(file, c)
		if e == nil {
			out = append(out, platformSelectionAccount{ID: c.ID, Name: c.Name, Version: c.Version, Account: a.Identity, Username: c.Validation.Username})
		}
	}
	return out, nil
}
func selectionResultEvent(s platformSelection) InboxTaskEvent {
	kind := "operation_failed"
	if s.Status == "completed" {
		kind = "user_selection"
	}
	return InboxTaskEvent{EventID: fmt.Sprintf("platform-selection:%s:%d", s.ID, s.ResultVersion), OperationID: s.ID, ResultVersion: s.ResultVersion, SourceSeq: s.Source.Seq, EventType: kind, ResultRef: "platforms/selection/" + s.ID, CreatedAt: s.ResultAt}
}
func (m *roomManager) selectionNotificationPath(id string) string {
	return filepath.Join(m.store.dir, "platform-selection-notifications", id+".json")
}
func (m *roomManager) readSelectionNotificationLocked(s platformSelection) (platformOperationNotification, error) {
	n := platformOperationNotification{Schema: 1, MemberIdentity: s.MemberIdentity, OperationID: s.ID, Mode: "none"}
	err := readSnapshot(m.selectionNotificationPath(s.ID), &n, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return n, nil
	}
	if err != nil {
		return n, err
	}
	if n.Schema != 1 || n.MemberIdentity != s.MemberIdentity || n.OperationID != s.ID || n.Mode != "inbox" && n.Mode != "none" || n.HandledVersion > s.ResultVersion || n.Event != nil && (s.ResultVersion == 0 || !reflect.DeepEqual(*n.Event, selectionResultEvent(s))) || n.Receipt != nil && (n.Event == nil || n.Receipt.MemberIdentity != s.MemberIdentity || n.Receipt.Seq == 0 || n.Receipt.DeliveryID != (InboxEntry{MemberIdentity: s.MemberIdentity, Type: InboxTaskEventType, TaskEvent: n.Event}).metadata().deliveryID) {
		return n, ErrInvalid
	}
	return n, nil
}
func (m *roomManager) saveSelectionNotificationLocked(n platformOperationNotification) error {
	if err := m.store.check(true); err != nil {
		return err
	}
	return m.store.save(m.selectionNotificationPath(n.OperationID), n, maxPrivateFileBytes)
}
func (m *roomManager) deliverPlatformSelectionLocked(ctx context.Context, id string) {
	s, err := m.readPlatformSelectionLocked(id)
	if err != nil {
		return
	}
	if s.Status == "waiting" && !time.Now().Before(s.ExpiresAt) {
		s.Status = "expired"
		s.SubmittedVersion = s.Version
		s.Version++
		s.ResultVersion = 1
		s.ResultAt = time.Now().UTC()
		if m.savePlatformSelectionLocked(s) != nil {
			return
		}
	}
	if s.ResultVersion == 0 {
		return
	}
	n, err := m.readSelectionNotificationLocked(s)
	if err != nil || n.Mode != "inbox" || n.Receipt != nil || n.HandledVersion == s.ResultVersion {
		return
	}
	if n.Event == nil {
		event := selectionResultEvent(s)
		n.Event = &event
		if m.saveSelectionNotificationLocked(n) != nil {
			return
		}
	}
	m.deliverOperationResultLocked(ctx, s.MemberIdentity, n, m.saveSelectionNotificationLocked)
}
