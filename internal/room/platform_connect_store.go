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
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Fixed business input is distinct from observation and result delivery policy.
// Existing connections can only be reused at the explicitly observed version.
type platformConnectInput struct {
	Intent          string `json:"intent,omitempty"`
	Provider        string `json:"provider"`
	Name            string `json:"name"`
	PlatformID      string `json:"platform_id,omitempty"`
	PlatformVersion uint64 `json:"platform_version,omitempty"`
	Origin          string `json:"origin,omitempty"`
	ApplicationID   string `json:"application_id,omitempty"`
	AccountUsername string `json:"account_username,omitempty"`
}
type platformConnectRequest struct {
	ServiceInfo
	Caller        Caller                `json:"caller"`
	OperationID   string                `json:"operation_id,omitempty"`
	SourceSeq     uint64                `json:"source_seq,omitempty"`
	Input         *platformConnectInput `json:"input,omitempty"`
	Notify        *string               `json:"notify,omitempty"`
	ResultVersion uint64                `json:"result_version,omitempty"`
	Intent        string                `json:"intent,omitempty"`
	PlatformID    string                `json:"platform_id,omitempty"`
}
type platformConnectInteraction struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Live views only. Never serialize OAuth URLs, QR images or session IDs to disk.
	URL       string `json:"authorization_url,omitempty"`
	QRCodeURL string `json:"qrcode_url,omitempty"`
}
type platformConnectOperation struct {
	Schema int    `json:"schema"`
	ID     string `json:"operation_id"`
	MemberIdentity
	Caller                 Caller                         `json:"caller"`
	Source                 RoomMessage                    `json:"source"`
	Input                  platformConnectInput           `json:"input"`
	Version                uint64                         `json:"version"`
	ResultVersion          uint64                         `json:"result_version"`
	Status                 string                         `json:"status"`
	CancelRequested        bool                           `json:"cancel_requested,omitempty"`
	Stage                  string                         `json:"stage"`
	ErrorCode              string                         `json:"error_code,omitempty"`
	PlatformID             string                         `json:"platform_id"`
	Account                platform.AccountIdentity       `json:"account"`
	BoundApplicationID     string                         `json:"bound_application_id,omitempty"`
	ApplicationImport      bool                           `json:"application_import,omitempty"`
	Setup                  *gitlabConnectSetup            `json:"setup,omitempty"` // live projection only
	ApplicationFingerprint string                         `json:"application_fingerprint,omitempty"`
	Profile                *platform.CLIProfile           `json:"cli_profile,omitempty"`
	Application            onboardingStep                 `json:"application"`
	Authorization          onboardingStep                 `json:"authorization"`
	Connection             onboardingStep                 `json:"connection"`
	Interaction            platformConnectInteraction     `json:"interaction"`
	CreatedAt              time.Time                      `json:"created_at"`
	UpdatedAt              time.Time                      `json:"updated_at"`
	Continuation           *platformOperationContinuation `json:"continuation,omitempty"` // live projection only
	AuthorizationImpact    []larkAuthPlatform             `json:"authorization_impact,omitempty"`
	AuthorizationTargets   []larkAuthorizationTarget      `json:"authorization_targets,omitempty"`
	Permissions            *lark.PermissionCheck          `json:"permissions,omitempty"`
}

func connectIntent(in platformConnectInput) string {
	if in.Intent == "" {
		return "connect"
	}
	return in.Intent
}

func connectTerminal(status string) bool {
	switch status {
	case "completed", "cancelled", "expired", "failed", "unknown":
		return true
	}
	return false
}
func (m *roomManager) connectPath(id string) string {
	return filepath.Join(m.store.dir, "platform-connect", id+".json")
}
func validPlatformConnect(op platformConnectOperation) bool {
	if op.Continuation != nil || op.Setup != nil {
		return false
	}
	if (op.Schema != 1 && op.Schema != 2) || !tool.OpaqueID(op.ID) || !tool.OpaqueID(op.PlatformID) || !tool.OpaqueID(op.RoomInstance) || !safeMembershipID(op.MembershipID) || !validText(op.RoomID) || !validText(op.AgentID) || op.Caller != agentCallerIdentity(op.MemberIdentity) || !validHumanEvidence(op.Source) || !validText(op.Input.Name) || len(op.Input.Name) > 128 || op.Version == 0 || op.CreatedAt.IsZero() || op.UpdatedAt.Before(op.CreatedAt) || op.Interaction.ID != op.ID || op.Interaction.URL != "" || op.Interaction.QRCodeURL != "" {
		return false
	}
	if op.Schema == 1 && (op.Input.Intent != "" || len(op.AuthorizationTargets) != 0 || len(op.AuthorizationImpact) != 0 || op.Permissions != nil) {
		return false
	}
	if !validLarkAuthorizationTargets(op) {
		return false
	}
	if !validConnectProvider(op) {
		return false
	}
	if op.Profile != nil && (!lark.ValidProfile(*op.Profile) || op.Profile.Brand != "feishu") {
		return false
	}
	if op.Account != (platform.AccountIdentity{}) && (!op.Account.Valid() || op.Account.Provider != op.Input.Provider || op.Account.Instance != connectOrigin(op.Input)) {
		return false
	}
	if op.Input.PlatformID != "" && (!tool.OpaqueID(op.Input.PlatformID) || op.Input.PlatformID != op.PlatformID || op.Input.PlatformVersion == 0) || op.Input.PlatformID == "" && op.Input.PlatformVersion != 0 {
		return false
	}
	if op.Status == "cancelling" && !op.CancelRequested || op.CancelRequested && op.Status != "cancelling" && op.Status != "cancelled" {
		return false
	}
	if connectTerminal(op.Status) != (op.ResultVersion > 0) {
		return false
	}
	switch op.Stage {
	case "application", "authorization", "connection", "complete":
	default:
		return false
	}
	switch op.Status {
	case "running", "waiting", "cancelling", "completed", "cancelled", "expired", "failed", "unknown":
	default:
		return false
	}
	for _, step := range []onboardingStep{op.Application, op.Authorization, op.Connection} {
		switch step.Status {
		case "pending", "applied", "connected", "failed", "unknown", "reused":
		default:
			return false
		}
	}
	return op.Status != "completed" || op.Connection.Status == "connected" && op.Account.Valid()
}
func (m *roomManager) readPlatformConnectLocked(id string) (platformConnectOperation, error) {
	var op platformConnectOperation
	if !tool.OpaqueID(id) {
		return op, ErrRequest
	}
	if err := m.store.check(false); err != nil {
		return op, err
	}
	info, err := os.Lstat(m.connectPath(id))
	if err != nil {
		return op, err
	}
	if !info.Mode().IsRegular() {
		return op, ErrInvalid
	}
	if err = readSnapshot(m.connectPath(id), &op, 128<<10); err != nil {
		return op, err
	}
	if op.ID != id || !validPlatformConnect(op) {
		return op, ErrInvalid
	}
	return op, nil
}
func (m *roomManager) savePlatformConnectLocked(op *platformConnectOperation) error {
	if err := m.store.check(true); err != nil {
		return err
	}
	op.Version++
	op.UpdatedAt = time.Now().UTC()
	if !validPlatformConnect(*op) {
		return ErrInvalid
	}
	return m.store.save(m.connectPath(op.ID), op, 128<<10)
}
func (m *roomManager) platformConnectOperationsLocked() ([]platformConnectOperation, error) {
	dir := filepath.Dir(m.connectPath("unused"))
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []platformConnectOperation{}, nil
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
	ops := []platformConnectOperation{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		op, err := m.readPlatformConnectLocked(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// Startup is the only recovery writer. status remains a pure observation.
// Never resume a temporary OAuth session or create a connection during recovery.
func (m *roomManager) recoverPlatformConnectLocked() error {
	selections, err := m.platformSelectionsLocked()
	if err != nil {
		return err
	}
	for _, s := range selections {
		if _, err := m.readSelectionNotificationLocked(s); err != nil {
			return err
		}
	}
	ops, err := m.platformConnectOperationsLocked()
	if err != nil {
		return err
	}
	file, err := m.platforms.Load()
	if err != nil {
		return err
	}
	for _, op := range ops {
		if connectTerminal(op.Status) || op.Input.Provider == "gitlab" && op.ApplicationFingerprint == "" && op.Status == "waiting" {
			continue
		}
		op.Status, op.ErrorCode, op.ResultVersion = "unknown", "service_restarted", 1
		if op.CancelRequested {
			op.Status, op.ErrorCode = "cancelled", "cancelled"
		}
		for _, c := range file.Platforms {
			// An old verified connection is not evidence that an interrupted OAuth committed.
			if connectIntent(op.Input) == "authorize" {
				continue
			}
			if op.CancelRequested || c.ID != op.PlatformID || c.Provider != op.Input.Provider || c.Name != op.Input.Name {
				continue
			}
			if op.Input.Provider == "gitlab" && !gitlabConnectRecordMatches(file, c, op) {
				continue
			}
			if op.Profile != nil && !reflect.DeepEqual(c.CLIProfile, op.Profile) {
				continue
			}
			if a, _, e := registry.ResolveAccessAccount(file, c); e == nil && op.Account.Valid() && a.Identity == op.Account {
				op.Status, op.Stage, op.ErrorCode = "completed", "complete", ""
				op.Connection = onboardingStep{Status: "connected", Version: c.Version}
			}
		}
		if err := m.savePlatformConnectLocked(&op); err != nil {
			return err
		}
	}
	for _, op := range ops {
		// Recovery may have settled an interrupted operation above.
		current, err := m.readPlatformConnectLocked(op.ID)
		if err != nil {
			return err
		}
		if _, err := m.readConnectNotificationLocked(current); err != nil {
			return err
		}
	}
	return nil
}
