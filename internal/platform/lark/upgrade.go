package lark

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// legacyConfig is decoded only by the explicit offline schema-1 upgrader.
// Keeping the original JSON intact on rejection lets the operator resolve the
// stated difference with the previous program before retrying.
type legacyConfig struct {
	ScopeMode       string              `json:"scope_mode,omitempty"`
	Enabled         bool                `json:"enabled"`
	PlatformID      string              `json:"platform_id"`
	Chats           []string            `json:"chats"`
	Target          notification.Target `json:"target"`
	Private         bool                `json:"private"`
	GroupMentions   bool                `json:"group_mentions"`
	GroupAll        bool                `json:"group_all,omitempty"`
	AtAll           bool                `json:"at_all"`
	Bots            bool                `json:"bots"`
	IntervalSeconds int                 `json:"interval_seconds"`
	BackfillSeconds int                 `json:"backfill_seconds"`
	BackfillStart   time.Time           `json:"backfill_start,omitzero"`
}

// UpgradeState preserves every state field except config. It requires stored
// native conversation evidence, never network discovery or guessed categories.
func UpgradeState(raw []byte, owner platform.RoomIdentity) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, notification.ErrInvalid
	}
	var old legacyConfig
	d := json.NewDecoder(bytes.NewReader(fields["config"]))
	d.DisallowUnknownFields()
	if d.Decode(&old) != nil || d.Decode(new(any)) != io.EOF {
		return nil, notification.ErrInvalid
	}
	var saved struct {
		Binding Binding                 `json:"binding"`
		Chats   map[string]ChatProgress `json:"chats"`
	}
	if json.Unmarshal(raw, &saved) != nil || !saved.Binding.Valid() || old.PlatformID != saved.Binding.PlatformID {
		return nil, notification.ErrInvalid
	}
	difference := func(reason string) ([]byte, error) {
		return nil, fmt.Errorf("%w: Lark schema 1 scope cannot be converted without changing reception: %s; original snapshot preserved", notification.ErrConflict, reason)
	}
	// The old all scope includes current/future groups (including own messages).
	// The new model deliberately supports only explicitly selected groups.
	if old.ScopeMode == "all" {
		return difference("all-conversation discovery includes groups; explicitly select conversations using the previous version")
	}
	if old.ScopeMode != "" && old.ScopeMode != "specified" {
		return nil, notification.ErrInvalid
	}
	ids, err := normalizeChatIDs(old.Chats)
	if err != nil || len(ids) == 0 || old.IntervalSeconds < 3 || old.IntervalSeconds > 3600 {
		return nil, notification.ErrInvalid
	}
	cfg := Config{Enabled: old.Enabled, PlatformID: old.PlatformID, Target: old.Target,
		Private: PrivateConfig{Scope: "specified"}, Groups: GroupConfig{MessageMode: "mentions", AtMe: old.GroupMentions, AtAll: old.AtAll},
		IntervalSeconds: old.IntervalSeconds, BackfillSeconds: old.BackfillSeconds, BackfillStart: old.BackfillStart}
	if old.GroupAll {
		cfg.Groups.MessageMode = "all"
	}
	for _, id := range ids {
		p, ok := saved.Chats[id]
		if !ok || p.NeedsVerification || !p.Chat.Valid() {
			return difference("selected conversation " + id + " has no verified category")
		}
		switch p.Chat.Mode {
		case "p2p":
			if old.Private && old.Bots {
				return difference("private bot reception is enabled")
			}
			if !p.Chat.Relation.ValidFor(saved.Binding.Account, id) || p.Chat.Relation.Kind != "peer" {
				return difference("selected private conversation " + id + " is not a proven person peer")
			}
			if !old.Private {
				return difference("private incoming messages disabled but own private messages were collected")
			}
			cfg.Private.Enabled = true
			cfg.Private.ChatIDs = append(cfg.Private.ChatIDs, id)
		case "group", "topic":
			cfg.Groups.Enabled = true
			cfg.Groups.ChatIDs = append(cfg.Groups.ChatIDs, id)
		}
	}
	// Even when both mention flags are false, the enabled group category retains
	// the old own-message-only behavior. MessageMode=mentions expresses this.
	cfg, err = NormalizeConfig(cfg)
	if err != nil {
		return nil, err
	}
	fields["config"], err = json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	next, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	if _, err = (&Source{}).Decode(next, owner); err != nil {
		return nil, err
	}
	return next, nil
}
