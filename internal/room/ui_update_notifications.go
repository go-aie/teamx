package room

import (
	"time"

	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Only the Room adapter interprets provider states. The common store forwards
// immutable before/after installations without knowing about UI categories.
// This fixed summary excludes observation sets, payloads, cursors and metrics.
type notificationSummary struct {
	Target                               notification.Target
	Version                              uint64
	Enabled, Retired                     bool
	PlatformID                           string
	Pending                              int
	Status, Error, DeliveryError         string
	Gap                                  gitlab.EventsGap
	BranchErrors                         [4]string
	FailedChats, UnlistedChats, GapChats int
}

func summarizeNotification(state notification.State, now time.Time) notificationSummary {
	meta := state.Metadata()
	v := notificationSummary{Target: meta.Target, Version: meta.Version, Enabled: meta.Enabled,
		Retired: meta.Retired, PlatformID: meta.PlatformID, Pending: meta.Pending, DeliveryError: meta.DeliveryError}
	var runtime notification.RuntimeState
	switch s := state.(type) {
	case gitlab.EventsState:
		runtime = s.RuntimeState
		if s.Gap != nil {
			v.Gap = *s.Gap
		}
	case gitlab.TodosState:
		runtime = s.RuntimeState
	case lark.State:
		runtime = s.RuntimeState
		if s.Search != nil {
			// Branch order is fixed by the normalized source configuration.
			for i, b := range s.Search.Branches {
				if i < len(v.BranchErrors) {
					v.BranchErrors[i] = b.ErrorCode
				}
			}
		} else {
			// Legacy diagnostics are bounded by the provider's MaxChats. No
			// full view, message list or sorted directory is constructed.
			for _, p := range s.Chats {
				if p.ErrorCode != "" {
					v.FailedChats++
				}
				if p.Unlisted {
					v.UnlistedChats++
				}
				if now.Unix()-max(s.Baseline.Unix(), p.Completed) > 86400 {
					v.GapChats++
				}
			}
		}
	}
	v.Status, v.Error = runtime.Status, runtime.ErrorCode
	return v
}

// Called with manager.mu held. Never query the state owner or construct an HTTP
// projection, and skip classification entirely without a matching Room stream.
func (m *roomManager) observeNotificationChange(before, after notification.State) {
	if m.updates == nil || m.updates.listeners.Load() == 0 {
		return
	}
	state := after
	if state == nil {
		state = before
	}
	// Resolve ownership from the provider header before Metadata: Lark's
	// pending count may inspect excluded Inbox items and is unnecessary for a
	// stream subscribed to another Room (or only to the global catalog).
	var owner RoomIdentity
	switch s := state.(type) {
	case gitlab.EventsState:
		owner = s.RoomIdentity
	case gitlab.TodosState:
		owner = s.RoomIdentity
	case lark.State:
		owner = s.RoomIdentity
	default:
		return
	}
	svc, _ := m.runtimeServiceLocked(owner)
	if svc == nil || !m.updates.observesRoom(svc.info) {
		return
	}
	now := m.notifications.Now()
	current := summarizeNotification(state, now)
	kind := "summary"
	if before != nil && after != nil && summarizeNotification(before, now) == current {
		kind = "diagnostics"
	}
	u := roomUIUpdate("notifications", svc.info)
	u.Kind = kind
	u.AgentID, u.MembershipID = current.Target.AgentID, current.Target.MembershipID
	m.updates.mark(u)
	// Inbox installation/consumption and member owners publish their own
	// signals. Source checkpoints never imply that those resources changed.
}
