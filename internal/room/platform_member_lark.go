package room

import (
	"reflect"
	"slices"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// A nil field preserves the current value. In particular, a present empty
// ChatIDs list clears the specified range, rather than omitting the change.
type platformMemberGroupPatch struct {
	Enabled *bool     `json:"enabled,omitempty"`
	Scope   string    `json:"scope,omitempty"`
	ChatIDs *[]string `json:"chat_ids,omitempty"`
	AtMe    *bool     `json:"at_me,omitempty"`
	AtAll   *bool     `json:"at_all,omitempty"`
}

type platformMemberPrivatePatch struct {
	Enabled *bool     `json:"enabled,omitempty"`
	Scope   string    `json:"scope,omitempty"`
	ChatIDs *[]string `json:"chat_ids,omitempty"`
}

func (in platformMemberInput) hasLarkNotificationFields() bool {
	return len(in.AddChatIDs) > 0 || len(in.RemoveChatIDs) > 0 || in.GroupMessageMode != "" || in.Groups != nil || in.Private != nil
}

func (in platformMemberInput) verifiedGroupIDs() []string {
	if in.Groups != nil && in.Groups.ChatIDs != nil {
		return *in.Groups.ChatIDs
	}
	return in.AddChatIDs
}

func (in platformMemberInput) verifiedPrivateIDs() []string {
	if in.Private != nil && in.Private.ChatIDs != nil {
		return *in.Private.ChatIDs
	}
	return nil
}

func validMemberPrivateChat(c lark.Chat, account platform.AccountIdentity, id string) bool {
	return c.Valid() && c.ID == id && c.Mode == "p2p" && c.Relation.Kind == "peer" && c.Relation.ValidFor(account, id)
}

func validMemberChatIDs(ids []string) bool {
	if len(ids) > 32 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !(lark.Chat{ID: id, Mode: "group"}).Valid() || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func validMemberLarkPatch(in platformMemberInput) bool {
	if !validMemberChatIDs(in.AddChatIDs) || !validMemberChatIDs(in.RemoveChatIDs) || in.GroupMessageMode != "" && in.GroupMessageMode != "all" && in.GroupMessageMode != "mentions" {
		return false
	}
	for _, id := range in.AddChatIDs {
		if slices.Contains(in.RemoveChatIDs, id) {
			return false
		}
	}
	if p := in.Groups; p != nil {
		if p.Enabled == nil && p.Scope == "" && p.ChatIDs == nil && p.AtMe == nil && p.AtAll == nil || p.Scope != "" && p.Scope != "all" && p.Scope != "specified" {
			return false
		}
		if p.ChatIDs != nil && (*p.ChatIDs == nil || !validMemberChatIDs(*p.ChatIDs) || len(in.AddChatIDs)+len(in.RemoveChatIDs) > 0) {
			return false
		}
		if len(in.AddChatIDs) > 0 && p.Enabled != nil && !*p.Enabled || len(in.AddChatIDs)+len(in.RemoveChatIDs) > 0 && p.Scope == "all" {
			return false
		}
	}
	if p := in.Private; p != nil {
		if p.Enabled == nil && p.Scope == "" && p.ChatIDs == nil || p.Scope != "" && p.Scope != "all" && p.Scope != "specified" {
			return false
		}
		if p.ChatIDs != nil && (*p.ChatIDs == nil || !validMemberChatIDs(*p.ChatIDs)) {
			return false
		}
	}
	return true
}

func memberNotificationConfig(m *roomManager, v platformMemberView, in platformMemberInput) (lark.Config, error) {
	var cfg lark.Config
	if v.Config != nil {
		cfg = *v.Config
		cfg.Private.ChatIDs = slices.Clone(cfg.Private.ChatIDs)
		cfg.Groups.ChatIDs = slices.Clone(cfg.Groups.ChatIDs)
	} else {
		// New requests opt into only the explicitly enabled branches.
		cfg = m.defaultLarkConfig(in.PlatformID)
		cfg.Enabled, cfg.Private.Enabled = false, false
		cfg.Groups = lark.GroupConfig{Scope: "specified", MessageMode: "all"}
		cfg.Target = platformMemberTarget(v.MemberIdentity)
	}
	wasEnabled := cfg.Enabled
	if len(in.AddChatIDs)+len(in.RemoveChatIDs) > 0 && cfg.Groups.Scope == "all" {
		return cfg, conflictReason("groups.scope", "existing subscription covers all groups", "Choose an explicit specified scope and complete group list; all groups has no exclusion list.")
	}
	if p := in.Groups; p != nil {
		if p.Scope == "specified" && cfg.Groups.Scope == "all" && p.ChatIDs == nil {
			return cfg, conflictReason("groups.chat_ids", "switching from all groups needs a complete list", "Supply group-scope specified and the complete group-chat-id list, or an explicit empty list to clear it.")
		}
		if p.Scope != "" {
			cfg.Groups.Scope = p.Scope
		}
		if p.ChatIDs != nil {
			cfg.Groups.ChatIDs = slices.Clone(*p.ChatIDs)
		}
		if p.Enabled != nil {
			cfg.Groups.Enabled = *p.Enabled
		}
		if p.AtMe != nil {
			cfg.Groups.AtMe = *p.AtMe
		}
		if p.AtAll != nil {
			cfg.Groups.AtAll = *p.AtAll
		}
	}
	for _, id := range in.AddChatIDs {
		if !slices.Contains(cfg.Groups.ChatIDs, id) {
			cfg.Groups.ChatIDs = append(cfg.Groups.ChatIDs, id)
		}
	}
	if len(in.AddChatIDs) > 0 {
		cfg.Groups.Scope, cfg.Groups.Enabled = "specified", true
	}
	if len(in.RemoveChatIDs) > 0 {
		cfg.Groups.ChatIDs = slices.DeleteFunc(cfg.Groups.ChatIDs, func(id string) bool { return slices.Contains(in.RemoveChatIDs, id) })
	}
	if in.GroupMessageMode != "" {
		cfg.Groups.MessageMode = in.GroupMessageMode
	}
	mentionPatch := in.Groups != nil && (in.Groups.AtMe != nil || in.Groups.AtAll != nil)
	if cfg.Groups.MessageMode == "mentions" && !cfg.Groups.AtMe && !cfg.Groups.AtAll {
		if mentionPatch {
			return cfg, conflictReason("groups.mentions", "mentions mode needs at least one mention condition", "Enable group-at-me or group-at-all, or explicitly choose all group messages.")
		}
		if in.GroupMessageMode == "mentions" {
			cfg.Groups.AtMe = true
		}
	}
	if p := in.Private; p != nil {
		if p.Scope == "specified" && cfg.Private.Scope == "all" && p.ChatIDs == nil {
			return cfg, conflictReason("private.chat_ids", "switching from all private conversations needs a complete list", "Supply private-scope specified and the complete verified private-chat-id list, or an explicit empty list to clear it.")
		}
		if p.Scope != "" {
			cfg.Private.Scope = p.Scope
		}
		if p.ChatIDs != nil {
			cfg.Private.ChatIDs = slices.Clone(*p.ChatIDs)
		}
		if p.Enabled != nil {
			cfg.Private.Enabled = *p.Enabled
		}
	}
	if cfg.Groups.Scope != "all" && len(cfg.Groups.ChatIDs) == 0 {
		if in.Groups != nil && in.Groups.Enabled != nil && *in.Groups.Enabled {
			return cfg, conflictReason("groups.chat_ids", "specified group range is empty", "Choose at least one group, choose all groups explicitly, or disable group reception.")
		}
		cfg.Groups.Enabled = false
	}
	if cfg.Private.Scope == "specified" && len(cfg.Private.ChatIDs) == 0 {
		if in.Private != nil && in.Private.Enabled != nil && *in.Private.Enabled {
			return cfg, conflictReason("private.chat_ids", "specified private range is empty", "Choose at least one verified personnel conversation, choose all private conversations explicitly, or disable private reception.")
		}
		cfg.Private.Enabled = false
	}
	groupEnable := len(in.AddChatIDs) > 0 || in.Groups != nil && in.Groups.Enabled != nil && *in.Groups.Enabled
	privateEnable := in.Private != nil && in.Private.Enabled != nil && *in.Private.Enabled
	if groupEnable || privateEnable {
		cfg.Enabled = true
	}
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	privateChosen := in.Private != nil && in.Private.Enabled != nil
	groupChosen := len(in.AddChatIDs) > 0 || in.Groups != nil && in.Groups.Enabled != nil
	if !wasEnabled && cfg.Enabled && cfg.Private.Enabled && cfg.Groups.Enabled && (!privateChosen || !groupChosen) {
		return cfg, conflictReason("notifications.enabled", "enabling reception would also activate an unrequested dormant source", "Explicitly choose both private-enabled and group-enabled when resuming both sources; disable the unrequested source otherwise.")
	}
	if !cfg.Private.Enabled && !cfg.Groups.Enabled {
		if in.Enabled != nil && *in.Enabled {
			return cfg, conflictReason("notifications.enabled", "no reception source is enabled", "Explicitly enable a valid private or group range, or leave notifications disabled.")
		}
		cfg.Enabled = false
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

func memberLarkNotificationUnchanged(v platformMemberView, in platformMemberInput, cfg lark.Config) bool {
	if v.Config != nil {
		return reflect.DeepEqual(cfg, *v.Config)
	}
	// Closing an absent subscription or removing absent targets must not create
	// a disabled subscription or lock a first-reception baseline.
	if in.Enabled != nil && *in.Enabled || len(in.AddChatIDs) > 0 || in.GroupMessageMode != "" {
		return false
	}
	if p := in.Groups; p != nil && (p.Enabled != nil && *p.Enabled || p.Scope != "" || p.ChatIDs != nil && len(*p.ChatIDs) > 0 || p.AtMe != nil || p.AtAll != nil) {
		return false
	}
	if p := in.Private; p != nil && (p.Enabled != nil && *p.Enabled || p.Scope != "" || p.ChatIDs != nil && len(*p.ChatIDs) > 0) {
		return false
	}
	return true
}
