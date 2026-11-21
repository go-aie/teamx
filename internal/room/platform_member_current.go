package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

// Presence is part of the HTTP contract: explicit zero/null/empty fields must
// not silently opt complex or conditional writes into current-state observation.
func validatePlatformMemberRequestFields(path string, req platformMemberRequest, object map[string]json.RawMessage) error {
	var input map[string]json.RawMessage
	for name, value := range object {
		if strings.EqualFold(name, "input") {
			_ = json.Unmarshal(value, &input)
		}
		if req.ObserveCurrent {
			switch strings.ToLower(name) {
			case "operation_id", "query", "page_token", "type":
				return invalidField(name, "current observation accepts only the platform, original source sequence and total switch")
			}
		}
	}
	if req.ObserveCurrent {
		if path != "/v1/platforms/member/notifications" && path != "/v1/platforms/member/tool" && path != "/v1/platforms/member/get" {
			return invalidField("observe_current", "current observation is limited to member total switches and member get")
		}
		for name := range input {
			if strings.EqualFold(name, "platform_id") || path != "/v1/platforms/member/get" && strings.EqualFold(name, "enabled") {
				continue
			}
			return invalidField("input."+name, "current observation cannot include versions, bindings, sources, ranges or filters")
		}
	} else if path == "/v1/platforms/member/notifications" {
		for name, value := range input {
			if strings.EqualFold(name, "notification_version") && string(bytes.TrimSpace(value)) != "null" {
				return nil
			}
		}
		return invalidField("input.notification_version", "required observed subscription version; explicit 0 means absent")
	}
	return nil
}

func platformMemberToggleID(req platformMemberRequest) string {
	return platformMemberSwitchID(req, "notifications")
}

func platformMemberSwitchID(req platformMemberRequest, kind string) string {
	// A structured domain-separated identity survives service epochs and turns.
	// Enabled and observed versions deliberately do not participate.
	domain := "platform-member-" + kind + "-toggle/v1"
	if kind == "notifications" {
		// Preserve all pre-existing notification receipt identities.
		domain = "platform-member-notification-toggle/v1"
	}
	data, _ := json.Marshal(struct {
		Domain     string
		Member     MemberIdentity
		PlatformID string
		Kind       string
		SourceSeq  uint64
	}{domain, req.member(), req.Input.PlatformID, kind, req.SourceSeq})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16])
}

// A short tool switch restores only an exact saved intent, never current
// defaults. Disabled intents retain their generation so account replacement
// cannot inherit an earlier grant. Missing legacy intent needs the full flow.
func (m *roomManager) currentPlatformMemberToolBindingLocked(req platformMemberRequest, v platformMemberView) (*tool.Binding, error) {
	f, err := m.readToolBindingsLocked(req.RoomIdentity)
	if err != nil {
		return nil, err
	}
	for _, bindings := range [][]tool.Binding{f.Bindings, f.Disabled} {
		for _, b := range bindings {
			if b.AgentID != req.Caller.ID || b.MembershipID != req.Caller.MembershipID || b.PlatformID != v.PlatformID || b.Tool != v.Account.Provider {
				continue
			}
			if b.PlatformGeneration != v.account.Generation || b.Tool == "lark" && b.ActionAuthorization == nil {
				return nil, conflictReason("binding", "saved tool authorization cannot be restored for this account", "Use member get and the complete tool workflow to reconcile the exact account and grants; never infer default actions.")
			}
			return &b, nil
		}
	}
	return nil, invalidField("binding", "no saved tool binding; use the complete tool workflow for initial setup")
}

// Resolve the exact saved ID under the same lock as the view and reservation.
// Account verification and provider/version checks remain in the existing view.
func (m *roomManager) observePlatformMemberLocked(ctx context.Context, req platformMemberRequest) (platformMemberRequest, platformMemberView, error) {
	file, err := m.platforms.Load()
	if err != nil {
		return req, platformMemberView{}, err
	}
	for _, p := range file.Platforms {
		if p.ID == req.Input.PlatformID {
			in := *req.Input
			in.PlatformVersion = p.Version
			req.Input = &in
			v, err := m.platformMemberViewLocked(ctx, req)
			return req, v, err
		}
	}
	return req, platformMemberView{}, ErrRequest
}

func (m *roomManager) configureCurrentPlatformMemberNotifications(ctx context.Context, kind string, req platformMemberRequest) (platformMemberOperation, error) {
	var op platformMemberOperation
	if kind != "notifications" || req.Input == nil || !tool.OpaqueID(req.Input.PlatformID) || req.SourceSeq == 0 || req.OperationID != "" || req.Query != "" || req.PageToken != "" || req.Type != "" {
		return op, ErrRequest
	}
	minimal := platformMemberInput{PlatformID: req.Input.PlatformID, Enabled: req.Input.Enabled}
	if req.Input.Enabled == nil || !reflect.DeepEqual(minimal, *req.Input) {
		return op, ErrRequest
	}
	req.OperationID = platformMemberToggleID(req)
	m.mu.Lock()
	// The caller must still own this live membership even for historical receipts.
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
		if prior.Source.Seq != req.SourceSeq || prior.Kind != kind || prior.Input.PlatformID != req.Input.PlatformID || !prior.Input.pureNotificationToggle() || *prior.Input.Enabled != *req.Input.Enabled {
			return op, operationConflict("operation_id", prior.ID, "operation ID has different business input")
		}
		return prior, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		m.mu.Unlock()
		return op, err
	}
	if err := m.checkPlatformMemberSourceLocked(req, kind); err != nil {
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
	req, v, err := m.observePlatformMemberLocked(ctx, req)
	if err != nil {
		m.mu.Unlock()
		return op, err
	}
	in := *req.Input
	in.NotificationVersion = v.NotificationVersion
	req.Input, req.ObserveCurrent = &in, false
	if !validPlatformMemberInput(kind, in) || !validPlatformMemberProviderInput(v.Account.Provider, kind, in) {
		m.mu.Unlock()
		return op, ErrRequest
	}
	admission := platformMemberAdmission{Request: req, Account: v.Account, AccountSnapshot: v.account, SubscriptionID: v.SubscriptionID, Kind: kind, BindingRevision: v.BindingRevision}
	ctx = context.WithValue(ctx, platformMemberContextKey{}, admission)
	op = platformMemberOperation{Schema: 1, MemberIdentity: req.member(), ID: req.OperationID, Kind: kind, Source: source, Input: in, Account: v.Account, Status: "unknown", Result: onboardingStep{Status: "unknown", ErrorCode: "submission_unconfirmed"}, CreatedAt: time.Now().UTC()}
	op, err = m.reservePlatformMemberNotificationsLocked(v, req, op)
	m.mu.Unlock()
	if err != nil || op.Status == "completed" {
		return op, err
	}
	return m.applyPlatformMemberNotifications(ctx, req, op)
}

// The caller holds manager.mu from observation through this durable fence.
// No remote verification or collector settlement occurs in this helper.
func (m *roomManager) reservePlatformMemberNotificationsLocked(v platformMemberView, req platformMemberRequest, op platformMemberOperation) (platformMemberOperation, error) {
	if v.Account.Provider == "gitlab" {
		cfg, err := m.gitlabMemberNotificationConfigLocked(v, req)
		if err != nil {
			return platformMemberOperation{}, err
		}
		op.GitLabConfig = &cfg
	} else {
		cfg, err := memberNotificationConfig(m, v, *req.Input)
		if err != nil {
			return platformMemberOperation{}, err
		}
		op.Config = &cfg
	}
	if op.Config != nil && memberLarkNotificationUnchanged(v, *req.Input, *op.Config) || op.GitLabConfig != nil && memberGitLabNotificationUnchanged(v, *req.Input, *op.GitLabConfig) {
		op.Status, op.ResultVersion = "completed", 1
		op.Result = onboardingStep{Status: "preserved", Version: v.NotificationVersion}
	}
	return op, m.savePlatformMemberLocked(op)
}
