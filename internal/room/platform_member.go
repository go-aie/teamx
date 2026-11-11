package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func (req platformMemberRequest) member() MemberIdentity {
	return MemberIdentity{RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}
}
func platformMemberTarget(id MemberIdentity) notification.Target {
	return notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}
}
func (req platformMemberRequest) chatsRequest() larkChatsRequest {
	return larkChatsRequest{member: &toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}, ServiceEpoch: req.ServiceEpoch, RoomIdentity: req.RoomIdentity, Caller: req.Caller, PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion, Query: req.Query, PageToken: req.PageToken, Type: "group"}
}

func (req platformMemberRequest) projectsRequest() gitlabProjectsRequest {
	page, _ := strconv.Atoi(req.PageToken)
	return gitlabProjectsRequest{member: &toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}, ServiceEpoch: req.ServiceEpoch, RoomIdentity: req.RoomIdentity, Caller: req.Caller, PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion, Search: req.Query, Page: page}
}

type platformMemberView struct {
	MemberIdentity
	PlatformID          string                   `json:"platform_id"`
	PlatformVersion     uint64                   `json:"platform_version"`
	Account             platform.AccountIdentity `json:"account"`
	NotificationVersion uint64                   `json:"notification_version"`
	Config              *lark.Config             `json:"notification_config,omitempty"`
	GitLab              *gitlabNotificationView  `json:"gitlab_notifications,omitempty"`
	Baseline            time.Time                `json:"baseline,omitzero"`
	ToolRevision        uint64                   `json:"tool_revision"`
	ToolEnabled         bool                     `json:"tool_enabled"`
	Tool                *toolSelection           `json:"tool,omitempty"`
}

func (m *roomManager) platformMemberViewLocked(ctx context.Context, req platformMemberRequest) (platformMemberView, error) {
	v := platformMemberView{MemberIdentity: req.member()}
	a, err := m.platformMemberAccountLocked(ctx, req)
	if err != nil {
		return v, err
	}
	v.PlatformID, v.PlatformVersion, v.Account = a.PlatformID, a.PlatformVersion, a.Identity
	if a.Identity.Provider == "gitlab" {
		n, err := m.gitlabNotificationViewForTargetLocked(req.RoomIdentity, a.PlatformID, platformMemberTarget(v.MemberIdentity))
		if err != nil {
			return v, err
		}
		v.NotificationVersion, v.GitLab = n.Version, &n
		v.Baseline = n.Config.BackfillStart
		if v.Baseline.IsZero() {
			v.Baseline = m.gitlabMemberBaselineLocked(req)
		}
	} else {
		s, err := m.currentLarkForTargetLocked(req.RoomIdentity, a.PlatformID, platformMemberTarget(v.MemberIdentity))
		if err != nil {
			return v, err
		}
		if s != nil {
			v.NotificationVersion, v.Config, v.Baseline = s.SubscriptionVersion, &s.Config, s.Baseline
		}
	}
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return v, err
	}
	v.ToolRevision = f.Revision
	selections, err := m.selectionsLocked(f, req.Caller)
	if err != nil {
		return v, err
	}
	for _, s := range selections {
		if s.PlatformID == a.PlatformID {
			v.ToolEnabled, v.Tool = true, &s
			break
		}
	}
	return v, nil
}

type platformMemberContextKey struct{}
type platformMemberAdmission struct {
	Request platformMemberRequest
	Account platform.AccountIdentity
	Kind    string
}

// This guard runs inside the existing configuration lock, including after
// remote reads and immediately before writes. Human management admission stays
// unchanged; the internal call uses the server-read human evidence as operator.
func (m *roomManager) checkPlatformMemberContextLocked(ctx context.Context) error {
	a, ok := ctx.Value(platformMemberContextKey{}).(platformMemberAdmission)
	if !ok {
		return nil
	}
	v, err := m.platformMemberViewLocked(ctx, a.Request)
	if err != nil {
		return err
	}
	if v.Account != a.Account || a.Kind == "notifications" && v.NotificationVersion != a.Request.Input.NotificationVersion || a.Kind == "tool" && v.ToolRevision != a.Request.Input.ToolRevision {
		return ErrConflict
	}
	return nil
}

func memberNotificationConfig(m *roomManager, v platformMemberView, in platformMemberInput) (lark.Config, error) {
	var cfg lark.Config
	if v.Config != nil {
		cfg = *v.Config
		cfg.Groups.ChatIDs = slices.Clone(cfg.Groups.ChatIDs)
	} else {
		// A request to follow selected groups must not opt into private chats,
		// all groups or independent mention/own-message branches by default.
		cfg = m.defaultLarkConfig(in.PlatformID)
		cfg.Enabled = false
		cfg.Private.Enabled = false
		cfg.Groups = lark.GroupConfig{Scope: "specified", MessageMode: "all"}
		cfg.Target = platformMemberTarget(v.MemberIdentity)
	}
	if len(in.AddChatIDs) > 0 {
		if cfg.Groups.Scope == "all" {
			return cfg, conflictReason("groups.scope", "existing subscription covers all groups", "Use platform settings to explicitly change the existing group scope.")
		}
		for _, id := range in.AddChatIDs {
			if !slices.Contains(cfg.Groups.ChatIDs, id) {
				cfg.Groups.ChatIDs = append(cfg.Groups.ChatIDs, id)
			}
		}
		cfg.Groups.Scope, cfg.Groups.Enabled, cfg.Enabled = "specified", true, true
	}
	if in.GroupMessageMode != "" {
		cfg.Groups.MessageMode = in.GroupMessageMode
		if in.GroupMessageMode == "mentions" && !cfg.Groups.AtMe && !cfg.Groups.AtAll {
			cfg.Groups.AtMe = true
		}
	}
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if v.Config == nil && cfg.Enabled {
		baseline, err := notification.BackfillBaseline(cfg.BackfillSeconds, cfg.BackfillStart, m.notifications.Now())
		if err != nil {
			return cfg, err
		}
		cfg.BackfillSeconds, cfg.BackfillStart = 0, baseline.UTC().Truncate(time.Second)
	}
	cfg, err := lark.NormalizeConfig(cfg)
	return cfg, notificationRoomError(err)
}

// A persisted unknown record is an at-most-once submission fence. Neither status
// nor repeated configure calls execute it again, even after a service restart.
func (m *roomManager) configurePlatformMember(ctx context.Context, kind string, req platformMemberRequest) (platformMemberOperation, error) {
	var op platformMemberOperation
	if req.Input == nil || !validPlatformMemberInput(kind, *req.Input) || !tool.OpaqueID(req.OperationID) || req.SourceSeq == 0 {
		return op, ErrRequest
	}
	m.mu.Lock()
	if err := m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}); err != nil {
		m.mu.Unlock()
		return op, err
	}
	prior, err := m.readPlatformMemberLocked(req.OperationID)
	if err == nil {
		m.mu.Unlock()
		if prior.MemberIdentity != req.member() {
			return op, ErrIdentity
		}
		if prior.Source.Seq != req.SourceSeq || prior.Kind != kind || !reflect.DeepEqual(prior.Input, *req.Input) {
			return op, ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		m.mu.Unlock()
		return op, err
	}
	svc, _ := m.runtimeServiceLocked(req.RoomIdentity)
	snapshot, err := svc.store.LoadRoom()
	if err != nil {
		m.mu.Unlock()
		return op, err
	}
	source, err := roomHumanMessage(snapshot, req.SourceSeq)
	if err != nil {
		m.mu.Unlock()
		return op, ErrIdentity
	}
	v, err := m.platformMemberViewLocked(ctx, req)
	if err != nil {
		m.mu.Unlock()
		return op, err
	}
	if !validPlatformMemberProviderInput(v.Account.Provider, kind, *req.Input) {
		m.mu.Unlock()
		return op, ErrRequest
	}
	admission := platformMemberAdmission{Request: req, Account: v.Account, Kind: kind}
	ctx = context.WithValue(ctx, platformMemberContextKey{}, admission)
	if err = m.checkPlatformMemberContextLocked(ctx); err != nil {
		m.mu.Unlock()
		return op, err
	}
	m.mu.Unlock()
	var chats []lark.Chat
	if len(req.Input.AddChatIDs) > 0 {
		q := req.chatsRequest()
		q.verifyIDs = req.Input.AddChatIDs
		page, err := m.listLarkChats(ctx, q)
		if err != nil {
			return op, err
		}
		chats = page.Chats
	}
	var project *gitlab.Project
	if req.Input.ProjectID != 0 {
		q := req.projectsRequest()
		q.verifyID, q.verifyPath = req.Input.ProjectID, req.Input.ProjectPath
		page, e := m.listGitLabProjects(ctx, q)
		if e != nil {
			return op, e
		}
		project = &page.Projects[0]
	}
	m.mu.Lock()
	// Recheck ID and source reservations after yielding for exact-ID verification.
	if _, err = m.readPlatformMemberLocked(req.OperationID); !errors.Is(err, os.ErrNotExist) {
		m.mu.Unlock()
		if err == nil {
			return m.configurePlatformMember(ctx, kind, req)
		}
		return op, err
	}
	ops, err := m.platformMemberOperationsLocked()
	if err == nil && len(ops) >= 1000 {
		err = ErrCapacity
	}
	for _, p := range ops {
		if p.MemberIdentity == req.member() && p.Source.Seq == req.SourceSeq && p.Kind == kind && p.Input.PlatformID == req.Input.PlatformID {
			err = conflictReason("source_seq", "configuration operation already exists", "Query member status with the original source_seq; do not resubmit with a new ID.")
		}
	}
	if err == nil {
		err = m.checkPlatformMemberContextLocked(ctx)
	}
	if err != nil {
		m.mu.Unlock()
		return op, err
	}
	op = platformMemberOperation{Schema: 1, MemberIdentity: req.member(), ID: req.OperationID, Kind: kind, Source: source, Input: *req.Input, Account: v.Account, Status: "unknown", Result: onboardingStep{Status: "unknown", ErrorCode: "submission_unconfirmed"}, Chats: chats, Project: project, CreatedAt: time.Now().UTC()}
	if kind == "notifications" {
		if v.Account.Provider == "gitlab" {
			var cfg gitlab.NotificationConfig
			cfg, err = m.gitlabMemberNotificationConfigLocked(v, req)
			op.GitLabConfig = &cfg
		} else {
			var cfg lark.Config
			cfg, err = memberNotificationConfig(m, v, *req.Input)
			op.Config = &cfg
		}
		if err != nil {
			m.mu.Unlock()
			return op, err
		}
	}
	if err = m.savePlatformMemberLocked(op); err != nil {
		m.mu.Unlock()
		return op, err
	}
	if kind == "tool" {
		op.Result, err = m.applyPlatformMemberToolLocked(ctx, op)
		m.mu.Unlock()
	} else if op.GitLabConfig != nil {
		m.mu.Unlock()
		var next gitlabNotificationView
		next, err = m.configureGitLabNotifications(ctx, req.RoomIdentity, req.Input.NotificationVersion, *op.GitLabConfig)
		op.Result = onboardingStep{Status: "applied", Version: next.Version}
		if next.Version != 0 {
			op.GitLabConfig = &next.Config
		}
		if err == nil && (next.Applying || len(next.Errors) != 0 || next.Pending["events"] || next.Pending["todos"]) {
			err = ErrConflict
		}
	} else {
		key := notification.Key{Owner: req.RoomIdentity, Source: lark.MessagesSourceID}
		current, e := m.currentLarkForTargetLocked(req.RoomIdentity, req.Input.PlatformID, platformMemberTarget(req.member()))
		if current != nil {
			key = current.Metadata().Key
		}
		m.mu.Unlock()
		if e != nil {
			err = e
		} else {
			var next lark.State
			next, err = m.configureLarkSubscription(ctx, key, req.Input.NotificationVersion, *op.Config)
			op.Result = onboardingStep{Status: "applied", Version: next.SubscriptionVersion}
		}
	}
	if err != nil {
		// A failure after admission may follow a durable write or failed join.
		// Keep it unknown and inspect the authoritative settings before recovery.
		op.Result = onboardingStep{Status: "unknown", ErrorCode: onboardingFailure(err)}
	} else {
		op.Status, op.ResultVersion = "completed", 1
	}
	m.mu.Lock()
	err = m.savePlatformMemberLocked(op)
	m.mu.Unlock()
	return op, err
}

func (m *roomManager) applyPlatformMemberToolLocked(ctx context.Context, op platformMemberOperation) (onboardingStep, error) {
	if err := m.checkPlatformMemberContextLocked(ctx); err != nil {
		return onboardingStep{}, err
	}
	f, err := m.readToolBindingsLocked(op.RoomIdentity)
	if err != nil {
		return onboardingStep{}, err
	}
	admission, ok := ctx.Value(platformMemberContextKey{}).(platformMemberAdmission)
	if !ok {
		return onboardingStep{}, ErrIdentity
	}
	account, err := m.platformMemberAccountLocked(ctx, admission.Request)
	if err != nil {
		return onboardingStep{}, err
	}
	enabled := *op.Input.Enabled
	for i, b := range append(slices.Clone(f.Bindings), f.Disabled...) {
		if b.AgentID == op.AgentID && b.MembershipID == op.MembershipID && b.PlatformID == op.Input.PlatformID && b.Tool == op.Account.Provider && enabled == (i < len(f.Bindings)) && (!enabled || b.PlatformGeneration == account.Generation) {
			// Preserve existing grants and unrelated settings on repeated enable.
			return onboardingStep{Status: "preserved", Version: f.Revision}, nil
		}
	}
	version := op.Input.PlatformVersion
	if !enabled {
		version = 0
	}
	next, waits, err := m.changeToolBindingLocked(ctx, toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: op.RoomIdentity, Caller: Caller{Kind: "human", ID: op.Source.Author.ID}, Revision: op.Input.ToolRevision, PlatformVersion: version, Binding: tool.Binding{AgentID: op.AgentID, MembershipID: op.MembershipID, PlatformID: op.Input.PlatformID, Tool: op.Account.Provider}, Enabled: enabled})
	if err == nil {
		err = m.waitOnboardingToolLocked(ctx, waits)
	}
	return onboardingStep{Status: "applied", Version: next.Revision}, err
}

func (m *roomManager) platformMemberStatusLocked(req platformMemberRequest) ([]platformMemberOperation, error) {
	ops := []platformMemberOperation{}
	if req.OperationID != "" {
		op, err := m.readPlatformMemberLocked(req.OperationID)
		if err != nil {
			return nil, err
		}
		if op.MemberIdentity != req.member() {
			return nil, ErrIdentity
		}
		return append(ops, op), nil
	}
	all, err := m.platformMemberOperationsLocked()
	if err != nil {
		return nil, err
	}
	for _, op := range all {
		if op.MemberIdentity == req.member() && op.Source.Seq == req.SourceSeq {
			ops = append(ops, op)
		}
	}
	if len(ops) == 0 {
		return nil, os.ErrNotExist
	}
	return ops, nil
}

func (m *roomManager) servePlatformMember(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Mode") != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var req platformMemberRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/v1/platforms/member/")
	if r.URL.Path == "/v1/platforms/chats" || r.URL.Path == "/v1/platforms/projects" {
		action = strings.TrimPrefix(r.URL.Path, "/v1/platforms/")
	}
	var result any
	var err error
	if action == "notifications" || action == "tool" {
		if req.Query != "" || req.PageToken != "" {
			err = ErrRequest
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
			defer cancel()
			result, err = m.configurePlatformMember(ctx, action, req)
		}
	} else {
		m.mu.Lock()
		err = m.checkToolMemberLocked(r.Context(), toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller})
		if err == nil {
			switch action {
			case "status":
				if req.Input != nil || (req.OperationID == "") == (req.SourceSeq == 0) || req.Query != "" || req.PageToken != "" {
					err = ErrRequest
				} else {
					var ops []platformMemberOperation
					ops, err = m.platformMemberStatusLocked(req)
					result = struct {
						Operations []platformMemberOperation `json:"operations"`
					}{ops}
				}
			case "get", "chats", "projects":
				if req.Input == nil || req.OperationID != "" || req.SourceSeq != 0 {
					err = ErrRequest
				} else {
					minimal := platformMemberInput{PlatformID: req.Input.PlatformID, PlatformVersion: req.Input.PlatformVersion}
					if !reflect.DeepEqual(minimal, *req.Input) || !tool.OpaqueID(minimal.PlatformID) || minimal.PlatformVersion == 0 || action == "get" && (req.Query != "" || req.PageToken != "") {
						err = ErrRequest
					} else if action == "get" {
						result, err = m.platformMemberViewLocked(r.Context(), req)
					}
				}
			default:
				err = ErrRequest
			}
		}
		m.mu.Unlock()
		if err == nil && action == "projects" {
			if req.PageToken != "" {
				n, e := strconv.Atoi(req.PageToken)
				if e != nil || n < 1 || n > 1000 {
					err = ErrRequest
				}
			}
			if err == nil {
				var page gitlab.ProjectPage
				page, err = m.listGitLabProjects(r.Context(), req.projectsRequest())
				if err == nil {
					m.mu.Lock()
					account, checkErr := m.platformMemberAccountLocked(r.Context(), req)
					m.mu.Unlock()
					err = checkErr
					if err == nil {
						result = struct {
							PlatformID      string                   `json:"platform_id"`
							PlatformVersion uint64                   `json:"platform_version"`
							Account         platform.AccountIdentity `json:"account"`
							gitlab.ProjectPage
						}{req.Input.PlatformID, req.Input.PlatformVersion, account.Identity, page}
					}
				}
			}
		}
		if err == nil && action == "chats" {
			var page lark.ChatPage
			page, err = m.listLarkChats(r.Context(), req.chatsRequest())
			if err == nil {
				m.mu.Lock()
				account, checkErr := m.checkLarkChatsLocked(r.Context(), req.chatsRequest(), nil)
				m.mu.Unlock()
				if checkErr != nil {
					writeLarkReadError(w, checkErr)
					return
				}
				page.Chats = slices.DeleteFunc(page.Chats, func(c lark.Chat) bool { return c.Mode == "p2p" })
				result = struct {
					PlatformID      string                   `json:"platform_id"`
					PlatformVersion uint64                   `json:"platform_version"`
					Account         platform.AccountIdentity `json:"account"`
					lark.ChatPage
				}{req.Input.PlatformID, req.Input.PlatformVersion, account.Identity, page}
			}
		}
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(apiError{Error: "member configuration operation not found", Code: "operation_not_found", Message: "No member configuration operation matches this request."})
		} else {
			writePlatformSetupReadError(w, err)
		}
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
