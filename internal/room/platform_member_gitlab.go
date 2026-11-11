package room

import (
	"context"
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/registry"
)

// Provider selection comes from the verified connection, never from a tool
// binding or an Agent-supplied account identity. Human management stays separate.
func (m *roomManager) platformMemberAccountLocked(ctx context.Context, req platformMemberRequest) (registry.Account, error) {
	if err := m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: req.ServiceInfo, Caller: req.Caller}); err != nil {
		return registry.Account{}, err
	}
	if m.busy || m.roomBusyLocked(req.RoomIdentity) {
		return registry.Account{}, ErrBusy
	}
	file, err := m.platforms.Load()
	if err != nil {
		return registry.Account{}, err
	}
	for _, p := range file.Platforms {
		if p.ID != req.Input.PlatformID {
			continue
		}
		switch p.Provider {
		case "lark":
			return m.checkLarkChatsLocked(ctx, req.chatsRequest(), nil)
		case "gitlab":
			a, _, err := m.resolvePlatformAccountLocked(req.RoomIdentity, p.ID, "gitlab")
			if err == nil && a.PlatformVersion != req.Input.PlatformVersion {
				err = ErrConflict
			}
			return a, err
		}
	}
	return registry.Account{}, ErrRequest
}

// Used only for a legacy view whose absolute baseline lives in its collectors.
func (m *roomManager) gitlabMemberBaselineLocked(req platformMemberRequest) time.Time {
	var start time.Time
	for _, state := range m.notifications.StatesLocked(req.RoomIdentity) {
		meta := state.Metadata()
		if meta.PlatformID != req.Input.PlatformID || meta.Target != platformMemberTarget(req.member()) || gitlabRetired(state) {
			continue
		}
		var baseline time.Time
		switch s := state.(type) {
		case gitlab.EventsState:
			baseline = s.Baseline
		case gitlab.TodosState:
			baseline = s.Baseline
		}
		if !baseline.IsZero() && (start.IsZero() || baseline.Before(start)) {
			start = baseline
		}
	}
	return start
}

func (m *roomManager) gitlabMemberNotificationConfigLocked(v platformMemberView, req platformMemberRequest) (gitlab.NotificationConfig, error) {
	n := v.GitLab
	cfg := n.Config
	// A read-only first-use draft has defaults; only an actual saved config or
	// collector establishes existing intent. New subscriptions opt into todos only.
	if n.Version == 0 && n.Events.SubscriptionID == "" && n.Todos.SubscriptionID == "" {
		cfg = gitlab.NotificationConfig{PlatformID: v.PlatformID, Target: platformMemberTarget(v.MemberIdentity), IntervalSeconds: gitlab.DefaultIntervalSeconds}
	}
	if n.Applying || len(n.Errors) != 0 || n.Pending["events"] || n.Pending["todos"] {
		return cfg, conflictReason("notifications", "existing notification configuration has unresolved changes", "Inspect member get and recover through Settings → Platforms before another change.")
	}
	in := req.Input
	if in.TodosEnabled != nil {
		cfg.Todos.Enabled = *in.TodosEnabled
	}
	if in.EventsEnabled != nil {
		cfg.Events.Enabled = *in.EventsEnabled
	}
	if in.ProjectID != 0 {
		cfg.Events.Project = in.ProjectPath
	}
	if in.EventsFilter != nil {
		cfg.Events.Filter = in.EventsFilter.Clone()
	}
	if in.TodosFilter != nil {
		if in.TodosFilter.Reasons != nil {
			cfg.Todos.Filter.Reasons = in.TodosFilter.Reasons
		}
		if in.TodosFilter.Types != nil {
			cfg.Todos.Filter.Types = in.TodosFilter.Types
		}
	}
	enabling := in.TodosEnabled != nil && *in.TodosEnabled || in.EventsEnabled != nil && *in.EventsEnabled
	if enabling && !cfg.Enabled && (cfg.Events.Enabled && in.EventsEnabled == nil || cfg.Todos.Enabled && in.TodosEnabled == nil) {
		return cfg, conflictReason("notifications.enabled", "enabling a source would also enable another dormant source", "Explicitly choose both sources; omitted source settings are preserved.")
	}
	if enabling {
		cfg.Enabled = true
	}
	if cfg.Events.Enabled && cfg.Events.Project == "" {
		return cfg, conflictReason("events.project", "project activity needs an exact project", "Search projects and supply the native project ID and path.")
	}
	// Switching projects must name both the old and new intent explicitly. A
	// native ID and path have already been verified under the original account.
	if in.EventsEnabled != nil && *in.EventsEnabled && in.ProjectID == 0 && !n.Config.Events.Enabled {
		return cfg, conflictReason("events.project", "enabling project activity requires project verification", "Supply the native project ID and path from projects or a selection receipt.")
	}

	return gitlab.NormalizeNotificationConfig(cfg)
}

// After desired settings are saved, subsequent collector writes must check the
// newly accepted version, while retaining the original member/account admission.
func platformMemberNotificationContext(ctx context.Context, version uint64) context.Context {
	a, ok := ctx.Value(platformMemberContextKey{}).(platformMemberAdmission)
	if !ok {
		return ctx
	}
	in := *a.Request.Input
	in.NotificationVersion = version
	a.Request.Input = &in
	return context.WithValue(ctx, platformMemberContextKey{}, a)
}
