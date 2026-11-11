package room

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Each setting change has its own stable ID and frozen input. Configuration
// remains authoritative in the existing subscription and tool stores.
type platformMemberInput struct {
	PlatformID          string   `json:"platform_id"`
	PlatformVersion     uint64   `json:"platform_version"`
	NotificationVersion uint64   `json:"notification_version,omitempty"`
	ToolRevision        uint64   `json:"tool_revision,omitempty"`
	Enabled             *bool    `json:"enabled,omitempty"`
	AddChatIDs          []string `json:"add_chat_ids,omitempty"`
	GroupMessageMode    string   `json:"group_message_mode,omitempty"`
}

type platformMemberRequest struct {
	ServiceInfo
	Caller      Caller               `json:"caller"`
	OperationID string               `json:"operation_id,omitempty"`
	SourceSeq   uint64               `json:"source_seq,omitempty"`
	Input       *platformMemberInput `json:"input,omitempty"`
	Query       string               `json:"query,omitempty"`
	PageToken   string               `json:"page_token,omitempty"`
}

type platformMemberOperation struct {
	Schema int `json:"schema"`
	MemberIdentity
	ID            string                   `json:"operation_id"`
	Kind          string                   `json:"kind"`
	Source        RoomMessage              `json:"source"`
	Input         platformMemberInput      `json:"input"`
	Account       platform.AccountIdentity `json:"account"`
	Status        string                   `json:"status"`
	ResultVersion uint64                   `json:"result_version"`
	Result        onboardingStep           `json:"result"`
	Config        *lark.Config             `json:"notification_config,omitempty"`
	Chats         []lark.Chat              `json:"chats,omitempty"`
	CreatedAt     time.Time                `json:"created_at"`
}

func (m *roomManager) platformMemberPath(id string) string {
	return filepath.Join(m.store.dir, "platform-member", id+".json")
}

func validPlatformMemberInput(kind string, in platformMemberInput) bool {
	if !tool.OpaqueID(in.PlatformID) || in.PlatformVersion == 0 {
		return false
	}
	if kind == "tool" {
		return in.Enabled != nil && in.NotificationVersion == 0 && len(in.AddChatIDs) == 0 && in.GroupMessageMode == ""
	}
	if kind != "notifications" || in.ToolRevision != 0 || len(in.AddChatIDs) > 32 || in.Enabled == nil && len(in.AddChatIDs) == 0 && in.GroupMessageMode == "" || in.GroupMessageMode != "" && in.GroupMessageMode != "all" && in.GroupMessageMode != "mentions" {
		return false
	}
	seen := map[string]bool{}
	for _, id := range in.AddChatIDs {
		if !(lark.Chat{ID: id, Mode: "group"}).Valid() || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func validPlatformMemberOperation(op platformMemberOperation) bool {
	if op.Schema != 1 || !tool.OpaqueID(op.ID) || !tool.OpaqueID(op.RoomInstance) || !validText(op.RoomID) || !validText(op.AgentID) || !safeMembershipID(op.MembershipID) || !validHumanEvidence(op.Source) || !validPlatformMemberInput(op.Kind, op.Input) || !op.Account.Valid() || op.Account.Provider != "lark" || op.CreatedAt.IsZero() {
		return false
	}
	if (op.Kind == "notifications") != (op.Config != nil) || len(op.Chats) != len(op.Input.AddChatIDs) {
		return false
	}
	for i, c := range op.Chats {
		if !c.Valid() || c.Mode == "p2p" || c.ID != op.Input.AddChatIDs[i] {
			return false
		}
	}
	if op.Config != nil {
		cfg, err := lark.NormalizeConfig(*op.Config)
		if err != nil || !reflect.DeepEqual(cfg, *op.Config) || cfg.PlatformID != op.Input.PlatformID || cfg.Target != platformMemberTarget(op.MemberIdentity) {
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
