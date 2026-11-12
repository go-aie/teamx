package room

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Each setting change has its own stable ID and frozen input. Configuration
// remains authoritative in the existing subscription and tool stores.
type platformMemberInput struct {
	PlatformID          string                      `json:"platform_id"`
	PlatformVersion     uint64                      `json:"platform_version"`
	NotificationVersion uint64                      `json:"notification_version,omitempty"`
	ToolRevision        uint64                      `json:"tool_revision,omitempty"`
	Actions             []string                    `json:"actions,omitempty"`
	Enabled             *bool                       `json:"enabled,omitempty"`
	EventsEnabled       *bool                       `json:"events_enabled,omitempty"`
	ProjectID           int64                       `json:"project_id,omitempty"`
	ProjectPath         string                      `json:"project_path,omitempty"`
	EventsFilter        *gitlab.EventsFilter        `json:"events_filter,omitempty"`
	TodosFilter         *gitlab.TodosFilter         `json:"todos_filter,omitempty"`
	TodosEnabled        *bool                       `json:"todos_enabled,omitempty"`
	AddChatIDs          []string                    `json:"add_chat_ids,omitempty"`
	RemoveChatIDs       []string                    `json:"remove_chat_ids,omitempty"`
	GroupMessageMode    string                      `json:"group_message_mode,omitempty"`
	Groups              *platformMemberGroupPatch   `json:"groups,omitempty"`
	Private             *platformMemberPrivatePatch `json:"private,omitempty"`
}

type platformMemberRequest struct {
	ServiceInfo
	Caller      Caller               `json:"caller"`
	OperationID string               `json:"operation_id,omitempty"`
	SourceSeq   uint64               `json:"source_seq,omitempty"`
	Input       *platformMemberInput `json:"input,omitempty"`
	Query       string               `json:"query,omitempty"`
	PageToken   string               `json:"page_token,omitempty"`
	Type        string               `json:"type,omitempty"` // chats only: group (default) or p2p personnel
}

type platformMemberOperation struct {
	Schema int `json:"schema"`
	MemberIdentity
	ID               string                      `json:"operation_id"`
	Kind             string                      `json:"kind"`
	Source           RoomMessage                 `json:"source"`
	Input            platformMemberInput         `json:"input"`
	Account          platform.AccountIdentity    `json:"account"`
	Status           string                      `json:"status"`
	ResultVersion    uint64                      `json:"result_version"`
	Result           onboardingStep              `json:"result"`
	Config           *lark.Config                `json:"notification_config,omitempty"`
	GitLabConfig     *gitlab.NotificationConfig  `json:"gitlab_notification_config,omitempty"`
	Project          *gitlab.Project             `json:"project,omitempty"`
	Chats            []lark.Chat                 `json:"chats,omitempty"`
	RequestedActions []tool.ActionGrant          `json:"requested_actions,omitempty"`
	Authorization    *tool.ActionAuthorization   `json:"action_authorization,omitempty"`
	Capabilities     *larkMemberCapabilityResult `json:"capabilities,omitempty"`
	CreatedAt        time.Time                   `json:"created_at"`
}

func (m *roomManager) platformMemberPath(id string) string {
	return filepath.Join(m.store.dir, "platform-member", id+".json")
}

func validPlatformMemberInput(kind string, in platformMemberInput) bool {
	if !tool.OpaqueID(in.PlatformID) || in.PlatformVersion == 0 {
		return false
	}
	gitlabFields := in.EventsEnabled != nil || in.ProjectID != 0 || in.ProjectPath != "" || in.EventsFilter != nil || in.TodosFilter != nil
	if kind == "tool" {
		if gitlabFields || len(in.Actions) > 32 || len(in.Actions) > 0 && (in.Enabled == nil || !*in.Enabled) {
			return false
		}
		return in.Enabled != nil && in.TodosEnabled == nil && in.NotificationVersion == 0 && !in.hasLarkNotificationFields()
	}
	if len(in.Actions) > 0 {
		return false
	}
	if kind == "notifications" && (in.TodosEnabled != nil || gitlabFields) {
		if (in.ProjectID == 0) != (in.ProjectPath == "") || in.ProjectID < 0 || in.ProjectPath != "" && !workspaceProjectPath(in.ProjectPath) {
			return false
		}
		if in.EventsFilter != nil {
			if _, err := gitlab.NormalizeEventsConfig(gitlab.EventsConfig{Filter: *in.EventsFilter}); err != nil {
				return false
			}
		}
		if in.TodosFilter != nil {
			if _, err := gitlab.NormalizeTodosConfig(gitlab.TodosConfig{PlatformID: in.PlatformID, Filter: *in.TodosFilter}); err != nil {
				return false
			}
		}
		return in.Enabled == nil && in.ToolRevision == 0 && !in.hasLarkNotificationFields()
	}
	return kind == "notifications" && in.ToolRevision == 0 && (in.Enabled != nil || in.hasLarkNotificationFields()) && validMemberLarkPatch(in)
}

func validPlatformMemberOperation(op platformMemberOperation) bool {
	if (op.Schema != 1 && op.Schema != 2) || !tool.OpaqueID(op.ID) || !tool.OpaqueID(op.RoomInstance) || !validText(op.RoomID) || !validText(op.AgentID) || !safeMembershipID(op.MembershipID) || !validHumanEvidence(op.Source) || !validPlatformMemberInput(op.Kind, op.Input) || !op.Account.Valid() || op.CreatedAt.IsZero() {
		return false
	}
	groupIDs, privateIDs := op.Input.verifiedGroupIDs(), op.Input.verifiedPrivateIDs()
	if !validPlatformMemberProviderInput(op.Account.Provider, op.Kind, op.Input) || len(op.Chats) != len(groupIDs)+len(privateIDs) {
		return false
	}
	if (op.Project != nil) != (op.Input.ProjectID != 0) || op.Project != nil && (op.Project.ID != op.Input.ProjectID || op.Project.Path != op.Input.ProjectPath || op.Project.Name == "") {
		return false
	}
	if (op.Kind == "notifications" && op.Account.Provider == "lark") != (op.Config != nil) || (op.Kind == "notifications" && op.Account.Provider == "gitlab") != (op.GitLabConfig != nil) {
		return false
	}
	for i, c := range op.Chats {
		if i < len(groupIDs) {
			if !c.Valid() || c.Mode == "p2p" || c.ID != groupIDs[i] {
				return false
			}
		} else if !validMemberPrivateChat(c, op.Account, privateIDs[i-len(groupIDs)]) {
			return false
		}
	}
	if op.Config != nil {
		cfg, err := lark.NormalizeConfig(*op.Config)
		if err != nil || !reflect.DeepEqual(cfg, *op.Config) || cfg.PlatformID != op.Input.PlatformID || cfg.Target != platformMemberTarget(op.MemberIdentity) {
			return false
		}
	}
	if op.GitLabConfig != nil {
		cfg, err := gitlab.NormalizeNotificationConfig(*op.GitLabConfig)
		if err != nil || !reflect.DeepEqual(cfg, *op.GitLabConfig) || cfg.PlatformID != op.Input.PlatformID || cfg.Target != platformMemberTarget(op.MemberIdentity) {
			return false
		}
	}
	if op.Schema == 1 && (len(op.Input.Actions) > 0 || len(op.RequestedActions) > 0 || op.Authorization != nil || op.Capabilities != nil) {
		return false
	}
	if op.Schema == 2 {
		if op.Kind != "tool" || op.Account.Provider != "lark" || op.Input.Enabled == nil || !*op.Input.Enabled || !validLarkConfigurationReceipt(op.MemberIdentity, op.Input.PlatformID, op.Account, op.RequestedActions, op.Authorization, op.Capabilities) {
			return false
		}
		if op.Capabilities.PlatformVersion != op.Input.PlatformVersion || op.Capabilities.ActionRevision != op.Authorization.Revision {
			return false
		}
		if op.Status == "completed" && (op.Capabilities.ToolRevision != op.Result.Version || op.Capabilities.ActionRevision != op.Authorization.Revision) {
			return false
		}
	}
	switch op.Status {
	case "unknown":
		return op.ResultVersion == 0 && op.Result.Status == "unknown"
	case "completed":
		return op.ResultVersion == 1 && (op.Result.Status == "applied" || op.Result.Status == "preserved")
	case "failed":
		return op.ResultVersion == 1 && op.Result.Status == "failed"
	}
	return false
}

func validPlatformMemberProviderInput(provider, kind string, in platformMemberInput) bool {
	switch provider {
	case "lark":
		for _, op := range in.Actions {
			if op != "message.reply" && op != "message.send" && op != "message.send_self" {
				return false
			}
		}
		return in.TodosEnabled == nil && in.EventsEnabled == nil && in.ProjectID == 0 && in.ProjectPath == "" && in.EventsFilter == nil && in.TodosFilter == nil
	case "gitlab":
		return len(in.Actions) == 0 && !in.hasLarkNotificationFields() && (kind == "tool" || (in.TodosEnabled != nil || in.EventsEnabled != nil || in.ProjectID != 0 || in.EventsFilter != nil || in.TodosFilter != nil) && in.Enabled == nil)
	}
	return false
}

func (m *roomManager) readPlatformMemberLocked(id string) (platformMemberOperation, error) {
	var op platformMemberOperation
	if !tool.OpaqueID(id) {
		return op, ErrRequest
	}
	if err := m.store.check(false); err != nil {
		return op, err
	}
	info, err := os.Lstat(m.platformMemberPath(id))
	if err != nil {
		return op, err
	}
	if !info.Mode().IsRegular() {
		return op, ErrInvalid
	}
	if err := readSnapshot(m.platformMemberPath(id), &op, 128<<10); err != nil {
		return op, err
	}
	if op.ID != id || !validPlatformMemberOperation(op) {
		return op, ErrInvalid
	}
	return op, nil
}

func (m *roomManager) savePlatformMemberLocked(op platformMemberOperation) error {
	if !validPlatformMemberOperation(op) {
		return ErrInvalid
	}
	if err := m.store.check(true); err != nil {
		return err
	}
	return m.store.save(m.platformMemberPath(op.ID), op, 128<<10)
}

func (m *roomManager) platformMemberOperationsLocked() ([]platformMemberOperation, error) {
	dir := filepath.Dir(m.platformMemberPath("unused"))
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []platformMemberOperation{}, nil
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
	ops := []platformMemberOperation{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		op, err := m.readPlatformMemberLocked(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}
