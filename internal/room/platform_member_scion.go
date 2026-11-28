package room

import (
	"reflect"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// Presence distinguishes an omitted setting from an explicit empty/zero value.
// Scion ranges belong to Scion, rather than GitLab projects or Lark chats.
type platformMemberScionPatch struct {
	Repository      *string    `json:"repository,omitempty"`
	ObjectTypes     *[]string  `json:"object_types,omitempty"`
	IntervalSeconds *int       `json:"interval_seconds,omitempty"`
	BackfillSeconds *int       `json:"backfill_seconds,omitempty"`
	BackfillStart   *time.Time `json:"backfill_start,omitempty"`
}

func validMemberScionPatch(p platformMemberScionPatch) bool {
	if p.BackfillSeconds != nil && p.BackfillStart != nil || p == (platformMemberScionPatch{}) || p.Repository != nil && *p.Repository == "" || p.ObjectTypes != nil && !slices.Equal(*p.ObjectTypes, []string{"issue"}) || p.IntervalSeconds != nil && (*p.IntervalSeconds < 30 || *p.IntervalSeconds > 3600) {
		return false
	}
	seconds, start := 0, time.Time{}
	if p.BackfillSeconds != nil {
		seconds = *p.BackfillSeconds
	}
	if p.BackfillStart != nil {
		start = *p.BackfillStart
		if start.IsZero() {
			return false
		}
	}
	_, err := notification.NormalizeBackfill(seconds, start)
	return err == nil
}

func (m *roomManager) currentScionForMemberLocked(req platformMemberRequest) (*scion.State, error) {
	s, err := m.notifications.CurrentLocked(req.RoomIdentity, scion.WorkitemsSourceID, req.Input.PlatformID, platformMemberTarget(req.member()))
	if err != nil {
		return nil, notificationRoomError(err)
	}
	if s == nil {
		return nil, nil
	}
	n, ok := s.(scion.State)
	if !ok {
		return nil, ErrInvalid
	}
	return &n, nil
}

func (m *roomManager) reserveScionMemberNotificationsLocked(v platformMemberView, req platformMemberRequest, op platformMemberOperation) (platformMemberOperation, error) {
	in := *req.Input
	currentBinding := true
	if v.Scion != nil {
		s, err := m.currentScionForMemberLocked(req)
		if err != nil {
			return op, err
		}
		if s == nil || s.Binding.Generation != v.account.Generation || s.Binding.Account != v.Account {
			return op, conflictReason("account", "Scion subscription belongs to a different account", "Inspect the exact connection and subscription; the previous account's progress cannot be inherited.")
		}
		currentBinding = s.Binding == scionBinding(v.account)
	}
	if v.Scion == nil && in.pureNotificationToggle() && !*in.Enabled {
		op.Status, op.ResultVersion = "completed", 1
		op.Result = onboardingStep{Status: "preserved"}
		return op, nil
	}
	cfg := scion.DefaultConfig(v.PlatformID)
	cfg.Target = platformMemberTarget(v.MemberIdentity)
	if v.Scion != nil {
		cfg = *v.Scion
	}
	if p := in.Scion; p != nil {
		if p.Repository != nil {
			name, err := scion.NormalizeRepository(v.Account.Instance, *p.Repository)
			if err != nil {
				return op, invalidField("scion.repository", "use a repository name or a link on this Scion instance")
			}
			cfg.Repository = name
		}
		if p.ObjectTypes != nil {
			cfg.ObjectTypes = slices.Clone(*p.ObjectTypes)
		}
		if p.IntervalSeconds != nil {
			cfg.IntervalSeconds = *p.IntervalSeconds
		}
		if p.BackfillSeconds != nil {
			cfg.BackfillSeconds = *p.BackfillSeconds
			cfg.BackfillStart = time.Time{}
		}
		if p.BackfillStart != nil {
			cfg.BackfillStart = *p.BackfillStart
			cfg.BackfillSeconds = 0
		}
	}
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if cfg.Repository == "" {
		return op, invalidField("scion.repository", "请选择仓库；Scion 通知首次开启需要明确仓库，不会自动使用 Workspace")
	}
	var err error
	cfg, err = scion.NormalizeConfig(cfg)
	if err != nil {
		return op, invalidField("scion", "Scion currently supports open Issue creation notices, a 30–3600 second interval and a valid reception start")
	}
	now := m.notifications.Now()
	if cfg.BackfillStart.After(now) {
		return op, invalidField("scion.backfill_start", "reception start cannot be in the future")
	}
	if v.Scion != nil {
		old := *v.Scion
		if cfg.Repository != old.Repository || !slices.Equal(cfg.ObjectTypes, old.ObjectTypes) || cfg.BackfillSeconds != old.BackfillSeconds || !cfg.BackfillStart.Equal(old.BackfillStart) {
			return op, invalidField("scion", "仓库、类型和开始时间保存后固定；本期仅支持通知开关和间隔修改")
		}
		if reflect.DeepEqual(cfg, old) && currentBinding {
			op.Status, op.ResultVersion = "completed", 1
			op.Result = onboardingStep{Status: "preserved", Version: v.NotificationVersion}
		}
	} else {
		// Freeze relative and default starts with the admitted input, before any
		// remote repository read or durable unknown receipt.
		cfg.BackfillStart, err = notification.BackfillBaseline(cfg.BackfillSeconds, cfg.BackfillStart, now)
		if err != nil {
			return op, err
		}
		cfg.BackfillStart = cfg.BackfillStart.UTC().Truncate(time.Second)
		cfg.BackfillSeconds = 0
	}
	op.ScionConfig = &cfg
	return op, nil
}

func validMemberScionReceipt(op platformMemberOperation) bool {
	if op.Account.Provider != "scion" {
		return op.ScionConfig == nil && op.Input.Scion == nil
	}
	if op.Schema != 4 || len(op.RequestedActions) != 0 || op.Authorization != nil || op.Capabilities != nil {
		return false
	}
	if op.Kind == "tool" {
		return op.ScionConfig == nil
	}
	if op.ScionConfig == nil {
		return op.Input.pureNotificationToggle() && !*op.Input.Enabled && op.Status == "completed" && op.Result.Status == "preserved" && op.Result.Version == 0
	}
	cfg, err := scion.NormalizeConfig(*op.ScionConfig)
	return err == nil && reflect.DeepEqual(cfg, *op.ScionConfig) && cfg.PlatformID == op.Input.PlatformID && cfg.Target == platformMemberTarget(op.MemberIdentity)
}
