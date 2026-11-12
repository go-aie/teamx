package room

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/registry"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// One durable operation fixes the target before authentication starts. It only
// initializes local settings; it never retries OAuth exchanges or remote writes.
type platformOnboardingSpec struct {
	MemberIdentity
	PlatformID   string `json:"platform_id,omitempty"`
	Provider     string `json:"provider,omitempty"`
	Origin       string `json:"origin,omitempty"`
	OnlyPlatform bool   `json:"only_platform,omitempty"`
	// Add initializes missing settings; it never overwrites an existing choice.
	PreserveExisting  bool                       `json:"preserve_existing,omitempty"`
	SkipNotifications bool                       `json:"skip_notifications,omitempty"`
	SkipTool          bool                       `json:"skip_tool,omitempty"`
	Backfill          *onboardingBackfill        `json:"backfill,omitempty"`
	Notifications     *bool                      `json:"notifications,omitempty"`
	Tool              *bool                      `json:"tool,omitempty"`
	GitLab            *gitlab.NotificationConfig `json:"gitlab,omitempty"`
	Lark              *lark.Config               `json:"lark,omitempty"`
}
type onboardingBackfill struct {
	Seconds int       `json:"seconds"`
	Start   time.Time `json:"start,omitzero"`
}
type platformOnboardingRequest struct {
	platformOnboardingSpec
	ServiceEpoch string `json:"service_epoch"`
	Caller       Caller `json:"caller"`
	OperationID  string `json:"operation_id"`
}
type onboardingStep struct {
	RequestedActions []tool.ActionGrant          `json:"requested_actions,omitempty"`
	Authorization    *tool.ActionAuthorization   `json:"action_authorization,omitempty"`
	Capabilities     *larkMemberCapabilityResult `json:"capabilities,omitempty"`
	Status           string                      `json:"status"`
	Version          uint64                      `json:"version,omitempty"`
	ErrorCode        string                      `json:"error_code,omitempty"`
}
type platformOnboarding struct {
	LarkActions               []tool.ActionGrant         `json:"lark_actions,omitempty"`
	Cancelled                 bool                       `json:"cancelled,omitempty"`
	Schema                    int                        `json:"schema"`
	ID                        string                     `json:"operation_id"`
	Owner                     string                     `json:"owner"`
	Input                     platformOnboardingSpec     `json:"input"`
	PlatformID                string                     `json:"platform_id"`
	Generation                string                     `json:"generation,omitempty"`
	Account                   platform.AccountIdentity   `json:"account"`
	Connection                onboardingStep             `json:"connection"`
	Notifications             onboardingStep             `json:"notifications"`
	Tool                      onboardingStep             `json:"tool"`
	BeforeToolRevision        uint64                     `json:"before_tool_revision"`
	BeforeNotificationVersion uint64                     `json:"before_notification_version"`
	GitLab                    *gitlab.NotificationConfig `json:"gitlab,omitempty"`
	Lark                      *lark.Config               `json:"lark,omitempty"`
}

const platformOnboardingSchema = 3

// This target is fixed before login and is also the notification settings
// locator: Input supplies the complete Room/member identity, PlatformID the
// concrete platform, and the saved config the matching notification target.
func (op platformOnboarding) notificationTarget() notification.Target {
	return notification.Target{Mode: "inbox", AgentID: op.Input.AgentID, MembershipID: op.Input.MembershipID}
}

type onboardingContextKey struct{}

func (m *roomManager) onboardingPath(id string) string {
	return filepath.Join(m.store.dir, "platform-onboarding", id+".json")
}
func (m *roomManager) readOnboardingLocked(id, owner string) (platformOnboarding, error) {
	var op platformOnboarding
	if !tool.OpaqueID(id) {
		return op, ErrRequest
	}
	if err := m.store.check(false); err != nil {
		return op, err
	}
	info, err := os.Lstat(m.onboardingPath(id))
	if err != nil {
		return op, err
	}
	if !info.Mode().IsRegular() {
		return op, ErrInvalid
	}
	if err := readSnapshot(m.onboardingPath(id), &op, 128<<10); err != nil {
		return op, err
	}
	if op.ID != id || !validOnboarding(op) {
		return op, ErrInvalid
	}
	if op.Owner != owner {
		return op, ErrIdentity
	}
	return op, nil
}
func (m *roomManager) saveOnboardingLocked(op platformOnboarding) error {
	if !validOnboarding(op) {
		return ErrInvalid
	}
	if err := m.store.check(true); err != nil {
		return err
	}
	return m.store.save(m.onboardingPath(op.ID), op, 128<<10)
}
func (m *roomManager) checkOnboardingTargetLocked(ctx context.Context, spec platformOnboardingSpec) error {
	if spec.OnlyPlatform {
		return nil
	}
	entry, err := m.workspaceRoom(spec.RoomIdentity)
	if err != nil || entry.Status != "ready" {
		return ErrIdentity
	}
	if m.roomBusyLocked(spec.RoomIdentity) {
		return ErrBusy
	}
	members, err := m.toolRosterLocked(ctx, spec.RoomIdentity)
	if err != nil {
		return err
	}
	for _, member := range members {
		if member.AgentID == spec.AgentID && member.MembershipID == spec.MembershipID && member.Runner.Kind == "dsh" {
			return nil
		}
	}
	return ErrIdentity
}
func onboardingEnabled(value *bool) bool { return value == nil || *value }
func (m *roomManager) prepareOnboardingLocked(ctx context.Context, req platformOnboardingRequest) (platformOnboarding, error) {
	if !tool.OpaqueID(req.OperationID) {
		return platformOnboarding{}, ErrRequest
	}
	prior, err := m.readOnboardingLocked(req.OperationID, req.Caller.ID)
	if err == nil {
		if !reflect.DeepEqual(prior.Input, req.platformOnboardingSpec) {
			return prior, ErrConflict
		}
		return prior, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return prior, err
	}
	ops, err := m.onboardingOperationsLocked()
	if err != nil {
		return prior, err
	}
	if len(ops) >= 1000 {
		return prior, ErrCapacity
	}
	spec := req.platformOnboardingSpec
	def, ok := platform.LookupType(spec.Provider)
	if !ok || spec.OnlyPlatform && (spec.MemberIdentity != (MemberIdentity{}) || spec.GitLab != nil || spec.Lark != nil || spec.Backfill != nil || spec.PreserveExisting || spec.SkipNotifications || spec.SkipTool) {
		return prior, ErrRequest
	}
	if spec.Backfill != nil {
		if len(def.NotificationChannels) == 0 || spec.SkipNotifications || spec.GitLab != nil || spec.Lark != nil {
			return prior, ErrRequest
		}
		if _, err := notification.BackfillBaseline(spec.Backfill.Seconds, spec.Backfill.Start, m.notifications.Now()); err != nil {
			return prior, notificationRoomError(err)
		}
	}
	_, origin, err := registry.Normalize("platform", spec.Origin)
	if err != nil || origin != spec.Origin {
		return prior, ErrRequest
	}
	if err := m.checkOnboardingTargetLocked(ctx, spec); err != nil {
		return prior, err
	}
	id := spec.PlatformID
	if id == "" {
		id, err = newOpaqueID()
	}
	if err != nil || !tool.OpaqueID(id) {
		return prior, ErrRequest
	}
	op := platformOnboarding{Schema: platformOnboardingSchema, ID: req.OperationID, Owner: req.Caller.ID, Input: spec, PlatformID: id, Connection: onboardingStep{Status: "waiting"}, Notifications: onboardingStep{Status: "pending"}, Tool: onboardingStep{Status: "pending"}}
	file, err := m.platforms.Load()
	if err != nil {
		return op, err
	}
	found := false
	for _, c := range file.Platforms {
		if c.ID != id {
			continue
		}
		found = true
		if c.Provider != spec.Provider || c.URL != spec.Origin {
			return op, ErrConflict
		}
		op.Generation = c.Generation
		if a, _, e := registry.ResolveAccessAccount(file, c); e == nil {
			op.Account = a.Identity
		}
	}
	if spec.PlatformID != "" && !found {
		return op, ErrIdentity
	}
	if spec.OnlyPlatform || spec.SkipNotifications || len(def.NotificationChannels) == 0 {
		op.Notifications.Status = "skipped"
	}
	if spec.OnlyPlatform || spec.SkipTool || def.ToolType == "" {
		op.Tool.Status = "skipped"
	}
	if !spec.OnlyPlatform {
		f, err := m.readToolBindingsLocked(spec.RoomIdentity)
		if err != nil {
			return op, err
		}
		op.BeforeToolRevision = f.Revision
		for i, b := range append(append([]tool.Binding(nil), f.Bindings...), f.Disabled...) {
			if b.AgentID == spec.AgentID && b.MembershipID == spec.MembershipID && b.PlatformID == id && b.Tool == def.ToolType && op.Tool.Status != "skipped" && (spec.PreserveExisting || spec.Tool == nil || *spec.Tool == (i < len(f.Bindings))) {
				if spec.Provider != "lark" || spec.PreserveExisting || spec.Tool == nil || !*spec.Tool {
					op.Tool.Status = "preserved"
				}
			}
		}

		if spec.Provider == "lark" && op.Tool.Status == "pending" && onboardingEnabled(spec.Tool) {
			caps, e := m.referenceCapabilities("lark")
			if e != nil {
				return op, e
			}
			op.LarkActions = defaultLarkActionGrants(caps)
			for _, b := range f.Bindings {
				if b.AgentID == spec.AgentID && b.MembershipID == spec.MembershipID && b.PlatformID == id && b.Tool == "lark" && b.ActionAuthorization != nil {
					op.LarkActions = append(op.LarkActions, b.ActionAuthorization.Grants...)
				}
			}
			op.LarkActions = normalizeActionGrants(op.LarkActions)
		}
		// Historical disable removed the binding entirely. Absence in such a
		// snapshot cannot authorize reopening a possibly disabled account.
		if op.Tool.Status == "pending" && (spec.PreserveExisting || spec.Tool == nil) && f.Revision > 0 && (f.LegacyUnknown || f.Disabled == nil) {
			op.Tool = onboardingStep{Status: "preserved", ErrorCode: "legacy_tool_choice_unknown"}
		}
		target := op.notificationTarget()
		switch spec.Provider {
		case "gitlab":
			current, e := m.gitlabNotificationViewForTargetLocked(spec.RoomIdentity, id, target)
			if e != nil {
				return op, e
			}
			op.BeforeNotificationVersion = current.Version
			cfg := gitlab.DefaultNotificationConfig(id)
			existing := current.Version > 0 || current.BackfillLocked
			if existing {
				cfg = current.Config
			}
			if spec.GitLab != nil && !(spec.PreserveExisting && existing) {
				cfg = *spec.GitLab
			}
			if (spec.PreserveExisting || spec.GitLab == nil && spec.Notifications == nil) && existing && op.Notifications.Status != "skipped" {
				op.Notifications.Status = "preserved"
			}
			if cfg.PlatformID != "" && cfg.PlatformID != id || spec.GitLab != nil && cfg.Target != (notification.Target{}) && cfg.Target != target {
				return op, ErrRequest
			}
			cfg.PlatformID = id
			cfg.Target = target
			if spec.Notifications != nil && !(spec.PreserveExisting && existing) {
				cfg.Enabled = *spec.Notifications
			}
			if spec.Backfill != nil && !existing {
				cfg.BackfillSeconds, cfg.BackfillStart = spec.Backfill.Seconds, spec.Backfill.Start
			}
			cfg, e = gitlab.NormalizeNotificationConfig(cfg)
			if e != nil {
				return op, notificationRoomError(e)
			}
			if cfg.Enabled {
				if _, e := notification.BackfillBaseline(cfg.BackfillSeconds, cfg.BackfillStart, m.notifications.Now()); e != nil {
					return op, notificationRoomError(e)
				}
			}
			op.GitLab = &cfg
			if spec.Lark != nil {
				return op, ErrRequest
			}
		case "lark":
			current, e := m.currentLarkForTargetLocked(spec.RoomIdentity, id, target)
			if e != nil {
				return op, e
			}
			if current != nil {
				op.BeforeNotificationVersion = current.SubscriptionVersion
				if (spec.PreserveExisting || spec.Lark == nil && spec.Notifications == nil) && op.Notifications.Status != "skipped" {
					op.Notifications.Status = "preserved"
				}
			}
			cfg := m.defaultLarkConfig(id)
			if current != nil {
				cfg = current.Config
			}
			if spec.Lark != nil && !(spec.PreserveExisting && current != nil) {
				cfg = *spec.Lark
			}
			if cfg.PlatformID != "" && cfg.PlatformID != id || spec.Lark != nil && cfg.Target != (notification.Target{}) && cfg.Target != target {
				return op, ErrRequest
			}
			cfg.PlatformID = id
			cfg.Target = target
			if spec.Notifications != nil && !(spec.PreserveExisting && current != nil) {
				cfg.Enabled = *spec.Notifications
			}
			if spec.Backfill != nil && current == nil {
				cfg.BackfillSeconds, cfg.BackfillStart = spec.Backfill.Seconds, spec.Backfill.Start
			}
			cfg, e = lark.NormalizeConfig(cfg)
			if e != nil {
				return op, notificationRoomError(e)
			}
			if cfg.Enabled {
				if _, e := notification.BackfillBaseline(cfg.BackfillSeconds, cfg.BackfillStart, m.notifications.Now()); e != nil {
					return op, notificationRoomError(e)
				}
			}
			op.Lark = &cfg
			if spec.GitLab != nil {
				return op, ErrRequest
			}
		default:
			if spec.GitLab != nil || spec.Lark != nil {
				return op, ErrRequest
			}
		}
	}
	return op, m.saveOnboardingLocked(op)
}

// Used at configuration admission while holding the same manager lock. No
// check-then-write gap is allowed between the operation and ordinary settings.
func (m *roomManager) checkOnboardingContextLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	op, ok := ctx.Value(onboardingContextKey{}).(platformOnboarding)
	if !ok {
		return nil
	}
	if err := m.checkOnboardingTargetLocked(ctx, op.Input); err != nil {
		return err
	}
	file, err := m.platforms.Load()
	if err != nil {
		return err
	}
	for _, c := range file.Platforms {
		if c.ID == op.PlatformID {
			a, _, err := registry.ResolveAccessAccount(file, c)
			if err != nil || a.Generation != op.Generation || a.Identity != op.Account {
				return ErrConflict
			}
			return nil
		}
	}
	return ErrIdentity
}
func onboardingFinished(step onboardingStep) bool {
	return step.Status == "applied" || step.Status == "preserved" || step.Status == "skipped"
}
func onboardingFailure(err error) string {
	if errors.Is(err, ErrIdentity) {
		return "target_changed"
	}
	if errors.Is(err, ErrConflict) {
		return "settings_changed"
	}
	if errors.Is(err, ErrBusy) {
		return "busy"
	}
	return "apply_interrupted"
}

func (m *roomManager) applyOnboarding(ctx context.Context, id, owner string) (platformOnboarding, error) {
	m.mu.Lock()
	op, err := m.readOnboardingLocked(id, owner)
	if err != nil {
		m.mu.Unlock()
		return op, err
	}
	// Completed pre-member-subscription operations are history only. Their
	// preserved notification may belong to a different member or the Room.
	// Never reinterpret or replay them under the new target contract.
	if op.Schema == 1 {
		m.mu.Unlock()
		if op.Cancelled || !onboardingFinished(op.Notifications) || !onboardingFinished(op.Tool) {
			return op, ErrConflict
		}
		return op, nil
	}
	if m.closing || m.busy || m.onboardingRunning[id] {
		m.mu.Unlock()
		return op, ErrBusy
	}
	if m.onboardingRunning == nil {
		m.onboardingRunning = map[string]bool{}
	}
	m.onboardingRunning[id] = true
	m.mu.Unlock()
	defer func() { m.mu.Lock(); delete(m.onboardingRunning, id); m.mu.Unlock() }()
	m.mu.Lock()
	if op.Cancelled {
		m.mu.Unlock()
		return op, ErrConflict
	}
	if err = m.store.check(true); err != nil {
		m.mu.Unlock()
		return op, err
	}
	file, err := m.platforms.Load()
	if err != nil {
		m.mu.Unlock()
		return op, err
	}
	var record registry.Record
	for _, c := range file.Platforms {
		if c.ID == op.PlatformID {
			record = c
		}
	}
	// Profile authentication and verification remain adapter-owned. apply also
	// completes their normal verification when the platform was explicitly saved.
	if record.ID != "" && record.CredentialKind == "cli_profile" && record.Validation.Status != "valid" && op.Connection.Status != "connected" && (op.Generation == "" || op.Generation == record.Generation) {
		m.mu.Unlock()
		_, err = m.verifyPlatform(ctx, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: Caller{Kind: "human", ID: owner}, ID: record.ID, Version: record.Version})
		m.mu.Lock()
		if err == nil {
			file, err = m.platforms.Load()
			for _, c := range file.Platforms {
				if c.ID == op.PlatformID {
					record = c
				}
			}
		}
	}
	a, _, accountErr := registry.ResolveAccessAccount(file, record)
	if err == nil {
		err = accountErr
	}
	if err == nil && (record.Provider != op.Input.Provider || record.URL != op.Input.Origin || op.Generation != "" && record.Generation != op.Generation || op.Account.Valid() && op.Account != a.Identity) {
		err = ErrConflict
	}
	if err != nil {
		op.Connection = onboardingStep{Status: "waiting", ErrorCode: "account_unavailable"}
		if errors.Is(err, ErrConflict) {
			op.Connection = onboardingStep{Status: "failed", ErrorCode: "account_changed"}
		}
		saveErr := m.saveOnboardingLocked(op)
		m.mu.Unlock()
		return op, saveErr
	}
	op.Generation, op.Account = record.Generation, a.Identity
	op.Connection = onboardingStep{Status: "connected", Version: record.Version}
	if err = m.checkOnboardingTargetLocked(ctx, op.Input); err != nil {
		if !onboardingFinished(op.Notifications) {
			op.Notifications = onboardingStep{Status: "failed", ErrorCode: onboardingFailure(err)}
		}
		if !onboardingFinished(op.Tool) {
			op.Tool = onboardingStep{Status: "failed", ErrorCode: onboardingFailure(err)}
		}
		saveErr := m.saveOnboardingLocked(op)
		m.mu.Unlock()
		return op, saveErr
	}
	// Prepare the first save's absolute baseline. If an earlier attempt never
	// committed settings, retry uses its new save time; a committed configuration
	// is the authority and keeps the exact previous start for receipt recovery.
	if !onboardingFinished(op.Notifications) {
		unstarted := false
		seconds := 0
		var start time.Time
		if op.GitLab != nil {
			current, e := m.gitlabNotificationViewForTargetLocked(op.Input.RoomIdentity, op.PlatformID, op.notificationTarget())
			if e != nil {
				m.mu.Unlock()
				return op, e
			}
			unstarted = current.Version == op.BeforeNotificationVersion && !current.BackfillLocked
			seconds, start = current.Config.BackfillSeconds, current.Config.BackfillStart
		}
		if op.Lark != nil {
			current, e := m.currentLarkForTargetLocked(op.Input.RoomIdentity, op.PlatformID, op.notificationTarget())
			if e != nil {
				m.mu.Unlock()
				return op, e
			}
			unstarted = current == nil
			if current != nil {
				unstarted = current.SubscriptionVersion == op.BeforeNotificationVersion && current.Baseline.IsZero()
				seconds, start = current.Config.BackfillSeconds, current.Config.BackfillStart
			}
		}
		if unstarted {
			if op.Input.Backfill != nil && op.BeforeNotificationVersion == 0 {
				seconds, start = op.Input.Backfill.Seconds, op.Input.Backfill.Start
			}
			if op.Input.GitLab != nil {
				seconds, start = op.Input.GitLab.BackfillSeconds, op.Input.GitLab.BackfillStart
			}
			if op.Input.Lark != nil {
				seconds, start = op.Input.Lark.BackfillSeconds, op.Input.Lark.BackfillStart
			}
			baseline, e := notification.BackfillBaseline(seconds, start, m.notifications.Now())
			if e != nil {
				m.mu.Unlock()
				return op, e
			}
			if op.GitLab != nil && op.GitLab.Enabled {
				cfg := *op.GitLab
				cfg.BackfillSeconds, cfg.BackfillStart = 0, baseline.UTC().Truncate(time.Second)
				op.GitLab = &cfg
			}
			if op.Lark != nil && op.Lark.Enabled {
				cfg := *op.Lark
				cfg.BackfillSeconds, cfg.BackfillStart = 0, baseline.UTC().Truncate(time.Second)
				op.Lark = &cfg
			}
		}
	}

	if err = m.saveOnboardingLocked(op); err != nil {
		m.mu.Unlock()
		return op, err
	}
	m.mu.Unlock()
	ctx = context.WithValue(ctx, onboardingContextKey{}, op)
	if !onboardingFinished(op.Notifications) {
		op.Notifications = m.applyOnboardingNotifications(ctx, op)
		m.mu.Lock()
		err = m.saveOnboardingLocked(op)
		m.mu.Unlock()
		if err != nil {
			return op, err
		}
	}
	if !onboardingFinished(op.Tool) && op.Tool.Status != "unknown" {
		m.mu.Lock()
		op.Tool = m.applyOnboardingToolLocked(ctx, op)
		err = m.saveOnboardingLocked(op)
		m.mu.Unlock()
	}
	return op, err
}
func (m *roomManager) applyOnboardingNotifications(ctx context.Context, op platformOnboarding) onboardingStep {
	result := onboardingStep{Status: "failed"}
	m.mu.Lock()
	err := m.checkOnboardingContextLocked(ctx)
	var version uint64
	var key notification.Key
	recovered := false
	if err == nil && op.GitLab != nil {
		current, e := m.gitlabNotificationViewForTargetLocked(op.Input.RoomIdentity, op.PlatformID, op.notificationTarget())
		err = e
		version = current.Version
		recovered = version == op.BeforeNotificationVersion+1 && reflect.DeepEqual(current.Config, *op.GitLab)
		if version != op.BeforeNotificationVersion && !recovered {
			err = ErrConflict
		}
		// Crash after committing both channels but before saving the journal receipt.
		if err == nil && recovered && !current.Pending["events"] && !current.Pending["todos"] {
			m.mu.Unlock()
			return onboardingStep{Status: "applied", Version: version}
		}
	}
	if err == nil && op.Lark != nil {
		current, e := m.currentLarkForTargetLocked(op.Input.RoomIdentity, op.PlatformID, op.notificationTarget())
		err = e
		key = notification.Key{Owner: op.Input.RoomIdentity, Source: lark.MessagesSourceID}
		if current != nil {
			version = current.SubscriptionVersion
			key = current.Metadata().Key
			recovered = version == op.BeforeNotificationVersion+1 && reflect.DeepEqual(current.Config, *op.Lark)
		}
		if version != op.BeforeNotificationVersion && !recovered {
			err = ErrConflict
		}
		if err == nil && recovered {
			m.mu.Unlock()
			return onboardingStep{Status: "applied", Version: version}
		}
	}
	m.mu.Unlock()
	if err == nil && op.GitLab != nil {
		var view gitlabNotificationView
		view, err = m.configureGitLabNotifications(ctx, op.Input.RoomIdentity, version, *op.GitLab)
		version = view.Version
		if err == nil && (len(view.Errors) > 0 || view.Pending["events"] || view.Pending["todos"]) {
			err = ErrBusy
		}
	}
	if err == nil && op.Lark != nil {
		var state lark.State
		state, err = m.configureLarkSubscription(ctx, key, version, *op.Lark)
		version = state.SubscriptionVersion
	}
	result.Version = version
	if err == nil {
		result.Status = "applied"
	} else {
		result.ErrorCode = onboardingFailure(err)
	}
	return result
}
func (m *roomManager) applyOnboardingToolLocked(ctx context.Context, op platformOnboarding) onboardingStep {
	if op.Schema >= 3 && op.Input.Provider == "lark" && onboardingEnabled(op.Input.Tool) {
		return m.applyOnboardingLarkToolLocked(ctx, op)
	}

	failure := func(err error) onboardingStep {
		return onboardingStep{Status: "failed", ErrorCode: onboardingFailure(err)}
	}
	if err := m.checkOnboardingContextLocked(ctx); err != nil {
		return failure(err)
	}
	f, err := m.readToolBindingsLocked(op.Input.RoomIdentity)
	if err != nil {
		return failure(err)
	}
	def, _ := platform.LookupType(op.Input.Provider)
	enabled := onboardingEnabled(op.Input.Tool)
	binding := tool.Binding{AgentID: op.Input.AgentID, MembershipID: op.Input.MembershipID, Tool: def.ToolType, PlatformID: op.PlatformID, PlatformGeneration: op.Generation}
	if !enabled {
		binding.PlatformGeneration = ""
	}
	if f.Revision == op.BeforeToolRevision+1 {
		candidates := f.Bindings
		if !enabled {
			candidates = f.Disabled
		}
		for _, b := range candidates {
			if reflect.DeepEqual(b, binding) {
				if !enabled {
					waits := m.cancelPlatformCallsLocked(func(call *platformCall) bool {
						return call.owner == op.Input.RoomIdentity && call.agentID == binding.AgentID && call.membershipID == binding.MembershipID && call.tool == binding.Tool && call.platformID == binding.PlatformID
					})
					if err := m.waitOnboardingToolLocked(ctx, waits); err != nil {
						return failure(err)
					}
				}
				return onboardingStep{Status: "applied", Version: f.Revision}
			}
		}
	}
	if f.Revision != op.BeforeToolRevision {
		return failure(ErrConflict)
	}
	file, err := m.platforms.Load()
	if err != nil {
		return failure(err)
	}
	var version uint64
	for _, c := range file.Platforms {
		if c.ID == op.PlatformID {
			version = c.Version
		}
	}
	binding.PlatformGeneration = ""
	if !enabled {
		version = 0
	}
	next, waits, err := m.changeToolBindingLocked(ctx, toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: op.Input.RoomIdentity, Caller: Caller{Kind: "human", ID: op.Owner}, Revision: f.Revision, Binding: binding, Enabled: enabled, PlatformVersion: version})
	if err == nil {
		err = m.waitOnboardingToolLocked(ctx, waits)
	}
	if err != nil {
		return failure(err)
	}
	result := onboardingStep{Status: "applied", Version: next.Revision}
	// Installation dependencies change availability, never the saved intent.
	selections, e := m.selectionsLocked(next, Caller{Author: Author{Kind: "agent", ID: binding.AgentID}, MembershipID: binding.MembershipID})
	if e == nil {
		for _, s := range selections {
			if s.PlatformID == op.PlatformID && s.UnavailableReason != "" {
				result.ErrorCode = s.UnavailableReason
			}
		}
	}
	return result
}
func (m *roomManager) servePlatformOnboarding(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/platforms/onboarding/targets" {
		m.serveOnboardingTargets(w, r)
		return
	}
	action := strings.TrimPrefix(r.URL.Path, "/v1/platforms/onboarding/")
	if action != "prepare" && action != "status" && action != "apply" && action != "cancel" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req platformOnboardingRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if dec.Decode(&req) != nil || dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		m.mu.Unlock()
		writeAPIError(w, ErrIdentity)
		return
	}
	if m.closing || m.busy {
		m.mu.Unlock()
		writeAPIError(w, ErrBusy)
		return
	}
	var op platformOnboarding
	var err error
	if action == "prepare" {
		op, err = m.prepareOnboardingLocked(r.Context(), req)
	} else if !reflect.DeepEqual(req.platformOnboardingSpec, platformOnboardingSpec{}) {
		err = ErrRequest
	} else {
		op, err = m.readOnboardingLocked(req.OperationID, req.Caller.ID)
	}
	if err == nil && action == "cancel" {
		if m.onboardingRunning[op.ID] {
			err = ErrBusy
		} else {
			op.Cancelled = true
			for _, session := range m.gitlabAuthSessions {
				if session.OnboardingID == op.ID {
					m.cancelGitLabAuthLocked(session)
				}
			}
			if !onboardingFinished(op.Notifications) {
				op.Notifications = onboardingStep{Status: "failed", Version: op.Notifications.Version, ErrorCode: "cancelled"}
			}
			if !onboardingFinished(op.Tool) {
				op.Tool = onboardingStep{Status: "failed", Version: op.Tool.Version, ErrorCode: "cancelled"}
			}
			err = m.saveOnboardingLocked(op)
		}
	}
	m.mu.Unlock()
	if err == nil && action == "apply" {
		op, err = m.applyOnboarding(r.Context(), req.OperationID, req.Caller.ID)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err = ErrIdentity
		}
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(op)
}

// Read the actual membership, including closed Rooms, without opening a runtime.
// Team definitions and display names are not membership identities.
func (m *roomManager) serveOnboardingTargets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	owner := RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
	m.mu.Lock()
	defer m.mu.Unlock()
	if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
		writeAPIError(w, ErrIdentity)
		return
	}
	if m.closing || m.busy || m.roomBusyLocked(owner) {
		writeAPIError(w, ErrBusy)
		return
	}
	entry, err := m.workspaceRoom(owner)
	if err != nil || entry.Status != "ready" {
		writeAPIError(w, ErrIdentity)
		return
	}
	members, err := m.toolRosterLocked(r.Context(), owner)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	targets := []MemberIdentity{}
	for _, member := range members {
		if member.Runner.Kind == "dsh" {
			targets = append(targets, MemberIdentity{RoomIdentity: owner, AgentID: member.AgentID, MembershipID: member.MembershipID})
		}
	}
	_ = json.NewEncoder(w).Encode(struct {
		Members []MemberIdentity `json:"members"`
	}{targets})
}

func (m *roomManager) onboardingOperationsLocked() ([]platformOnboarding, error) {
	dir := filepath.Join(m.store.dir, "platform-onboarding")
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
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
	var ops []platformOnboarding
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !tool.OpaqueID(id) {
			return nil, ErrInvalid
		}
		var op platformOnboarding
		if err := readSnapshot(m.onboardingPath(id), &op, 128<<10); err != nil {
			return nil, err
		}
		if op.ID != id || !validOnboarding(op) {
			return nil, ErrInvalid
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func validOnboarding(op platformOnboarding) bool {
	if (op.Schema != 1 && op.Schema != 2 && op.Schema != platformOnboardingSchema) || !tool.OpaqueID(op.ID) || !tool.OpaqueID(op.PlatformID) || !validText(op.Owner) || op.Generation != "" && !tool.OpaqueID(op.Generation) || op.Account != (platform.AccountIdentity{}) && !op.Account.Valid() {
		return false
	}
	if _, ok := platform.LookupType(op.Input.Provider); !ok {
		return false
	}
	if op.Input.Backfill != nil {
		if op.Input.SkipNotifications || op.Input.OnlyPlatform || op.Input.GitLab != nil || op.Input.Lark != nil || (op.Input.Provider != "gitlab" && op.Input.Provider != "lark") {
			return false
		}
		if _, err := notification.NormalizeBackfill(op.Input.Backfill.Seconds, op.Input.Backfill.Start); err != nil {
			return false
		}
	}
	if op.Input.SkipTool && op.Tool.Status != "skipped" || op.Input.SkipNotifications && op.Notifications.Status != "skipped" {
		return false
	}
	if op.Input.PlatformID != "" && op.Input.PlatformID != op.PlatformID {
		return false
	}
	_, origin, err := registry.Normalize("platform", op.Input.Origin)
	if err != nil || origin != op.Input.Origin {
		return false
	}
	if !op.Input.OnlyPlatform && (!validText(op.Input.RoomID) || !tool.OpaqueID(op.Input.RoomInstance) || !validText(op.Input.AgentID) || !safeMembershipID(op.Input.MembershipID)) {
		return false
	}
	if op.GitLab != nil && (op.Input.Provider != "gitlab" || op.GitLab.PlatformID != op.PlatformID) || op.Lark != nil && (op.Input.Provider != "lark" || op.Lark.PlatformID != op.PlatformID) {
		return false
	}
	if op.Schema < 3 && (len(op.LarkActions) > 0 || op.Tool.Authorization != nil || op.Tool.Capabilities != nil || len(op.Tool.RequestedActions) > 0) {
		return false
	}
	if op.Schema >= 3 && len(op.LarkActions) > 0 {
		auth := &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: op.LarkActions}
		if !auth.Valid() || op.Input.Provider != "lark" || !onboardingEnabled(op.Input.Tool) || op.Input.SkipTool || op.Input.OnlyPlatform {
			return false
		}
	}
	if op.Tool.Capabilities != nil {
		c := op.Tool.Capabilities
		if op.Schema < 3 || !validLarkConfigurationReceipt(op.Input.MemberIdentity, op.PlatformID, op.Account, op.Tool.RequestedActions, op.Tool.Authorization, c) {
			return false
		}
		if (onboardingFinished(op.Tool) || op.Tool.Status == "unknown") && c.ActionRevision != op.Tool.Authorization.Revision {
			return false
		}
		if onboardingFinished(op.Tool) && (c.ToolRevision != op.Tool.Version || c.ActionRevision != op.Tool.Authorization.Revision) {
			return false
		}
	}
	if op.Schema >= 2 {
		if op.Input.OnlyPlatform {
			if op.Input.MemberIdentity != (MemberIdentity{}) || op.Input.Backfill != nil || op.Input.PreserveExisting || op.Input.SkipNotifications || op.Input.SkipTool || op.Input.GitLab != nil || op.Input.Lark != nil || op.GitLab != nil || op.Lark != nil || op.Notifications.Status != "skipped" || op.Tool.Status != "skipped" {
				return false
			}
		} else {
			target := op.notificationTarget()
			if op.Input.Provider == "gitlab" && op.GitLab == nil || op.Input.Provider == "lark" && op.Lark == nil {
				return false
			}
			if op.GitLab != nil {
				cfg, err := gitlab.NormalizeNotificationConfig(*op.GitLab)
				if err != nil || cfg.Target != target || !reflect.DeepEqual(cfg, *op.GitLab) {
					return false
				}
			}
			if op.Lark != nil {
				cfg, err := lark.NormalizeConfig(*op.Lark)
				if err != nil || cfg.Target != target || !reflect.DeepEqual(cfg, *op.Lark) {
					return false
				}
			}
		}
	}
	if op.Connection.Status != "waiting" && op.Connection.Status != "connected" && op.Connection.Status != "failed" {
		return false
	}
	for _, step := range []onboardingStep{op.Notifications, op.Tool} {
		if step.Status != "pending" && step.Status != "applied" && step.Status != "preserved" && step.Status != "skipped" && step.Status != "failed" && !(op.Schema >= 3 && (step.Status == "unknown" || step.Status == "not_applied")) {
			return false
		}
	}
	return true
}

// The binding is already durably changed. Join its calls without a global busy
// fence, so another platform remains usable while this member's call settles.
func (m *roomManager) waitOnboardingToolLocked(ctx context.Context, waits []<-chan struct{}) error {
	if len(waits) == 0 {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	m.mu.Unlock()
	err := notification.Wait(waitCtx, waits)
	m.mu.Lock()
	if err != nil {
		return ErrBusy
	}
	return nil
}
