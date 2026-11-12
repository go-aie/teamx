package lark

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var MessagesSourceID = notification.SourceID{Provider: "lark", Channel: "messages"}

const DefaultIntervalSeconds = 10

// PrivateConfig selects proven person-to-person conversations. Saved selections
// survive switching to all, or disabling this category.
type PrivateConfig struct {
	Enabled bool     `json:"enabled"`
	Scope   string   `json:"scope"`
	ChatIDs []string `json:"chat_ids"`
}
type GroupConfig struct {
	Scope       string   `json:"scope,omitempty"`
	Enabled     bool     `json:"enabled"`
	ChatIDs     []string `json:"chat_ids"`
	MessageMode string   `json:"message_mode"`
	AtMe        bool     `json:"at_me"`
	AtAll       bool     `json:"at_all"`
}
type Config struct {
	Enabled         bool                `json:"enabled"`
	PlatformID      string              `json:"platform_id"`
	Target          notification.Target `json:"target"`
	Private         PrivateConfig       `json:"private"`
	Groups          GroupConfig         `json:"groups"`
	IntervalSeconds int                 `json:"interval_seconds"`
	BackfillSeconds int                 `json:"backfill_seconds"`
	BackfillStart   time.Time           `json:"backfill_start,omitzero"`
}

// DefaultConfig is a draft only; reading it never creates a subscription.
func DefaultConfig(platformID string) Config {
	return Config{Enabled: true, PlatformID: platformID, Private: PrivateConfig{Enabled: true, Scope: "all"}, Groups: GroupConfig{MessageMode: "mentions", AtMe: true}, IntervalSeconds: DefaultIntervalSeconds}
}
func (c Config) DiscoverPrivate() bool { return c.Private.Enabled && c.Private.Scope == "all" }

func normalizeChatIDs(ids []string) ([]string, error) {
	if len(ids) > 32 {
		return nil, notification.ErrRequest
	}
	out := slices.Clone(ids)
	slices.Sort(out)
	if len(slices.Compact(slices.Clone(out))) != len(out) {
		return nil, notification.ErrRequest
	}
	for _, id := range out {
		if !nativeID(id, "oc_") {
			return nil, notification.ErrRequest
		}
	}
	return out, nil
}
func NormalizeConfig(c Config) (Config, error) {
	if c.IntervalSeconds == 0 {
		c.IntervalSeconds = DefaultIntervalSeconds
	}
	if c.Private.Scope == "" {
		c.Private.Scope = "all"
	}
	if c.Groups.MessageMode == "" {
		c.Groups.MessageMode = "mentions"
	}
	start, err := notification.NormalizeBackfill(c.BackfillSeconds, c.BackfillStart)
	if err != nil {
		return c, err
	}
	c.BackfillStart = start
	if !component(c.PlatformID) || !c.Target.Valid() || !c.Target.Inbox() ||
		(c.Private.Scope != "all" && c.Private.Scope != "specified") ||
		(c.Groups.Scope != "" && c.Groups.Scope != "specified" && c.Groups.Scope != "all") ||
		(c.Groups.MessageMode != "mentions" && c.Groups.MessageMode != "all") ||
		c.Private.Enabled && c.Private.Scope == "specified" && len(c.Private.ChatIDs) == 0 ||
		c.Groups.Enabled && c.Groups.Scope != "all" && len(c.Groups.ChatIDs) == 0 ||
		c.Enabled && !c.Private.Enabled && !c.Groups.Enabled ||
		c.IntervalSeconds < 3 || c.IntervalSeconds > 3600 {
		return c, notification.ErrRequest
	}
	if c.Private.ChatIDs, err = normalizeChatIDs(c.Private.ChatIDs); err != nil {
		return c, err
	}
	if c.Groups.ChatIDs, err = normalizeChatIDs(c.Groups.ChatIDs); err != nil {
		return c, err
	}
	for _, id := range c.Private.ChatIDs {
		if slices.Contains(c.Groups.ChatIDs, id) {
			return c, notification.ErrRequest
		}
	}
	return c, nil
}

// Includes checks category and range before any sender/message exception. Native
// P2P relationship proof is checked separately before message bodies are read.
func (c Config) Includes(chat Chat) bool {
	switch chat.Mode {
	case "p2p":
		return c.Private.Enabled && (c.Private.Scope == "all" || slices.Contains(c.Private.ChatIDs, chat.ID))
	case "group", "topic":
		return c.Groups.Enabled && (c.Groups.Scope == "all" || slices.Contains(c.Groups.ChatIDs, chat.ID))
	}
	return false
}
func component(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}

type Binding struct {
	PlatformID        string                   `json:"platform_id"`
	Generation        string                   `json:"generation"`
	Account           platform.AccountIdentity `json:"account"`
	CredentialVersion uint64                   `json:"credential_version"`
	Fingerprint       string                   `json:"fingerprint"`
}

func (b Binding) Valid() bool {
	return component(b.PlatformID) && component(b.Generation) && b.Account.Valid() && b.Account.Provider == "lark" && (b.Account.Instance == Origin("feishu") || b.Account.Instance == Origin("lark")) && b.Account.Tenant.Namespace == "lark:tenant_key" && b.Account.Tenant.Value != "" && strings.HasPrefix(b.Account.ID.Namespace, "lark:open_id:cli_") && nativeID(b.Account.ID.Value, "ou_") && b.CredentialVersion > 0 && len(b.Fingerprint) == 64 && strings.Trim(b.Fingerprint, "0123456789abcdef") == ""
}

type Scan struct {
	Start int64  `json:"start"`
	End   int64  `json:"end"`
	Token string `json:"page_token,omitempty"`
	Pages int    `json:"pages"`
}

type ChatProgress struct {
	NeedsVerification bool      `json:"needs_verification,omitempty"`
	Unlisted          bool      `json:"unlisted,omitempty"`
	VerifiedAt        time.Time `json:"verified_at,omitzero"`
	LastAttempt       time.Time `json:"last_attempt,omitzero"`
	LastActivity      int64     `json:"last_activity,omitempty"`
	ErrorCode         string    `json:"error_code,omitempty"`
	RetryAt           time.Time `json:"retry_at,omitzero"`
	Chat              Chat      `json:"chat"`
	Completed         int64     `json:"completed"`
	LastCheck         time.Time `json:"last_check"`
	Scan              *Scan     `json:"scan,omitempty"`
}
type Observation struct {
	Message  Message            `json:"message"`
	Accepted bool               `json:"accepted"`
	Route    notification.Route `json:"route"`
}
type Entry struct {
	Message Message           `json:"message"`
	Item    notification.Item `json:"item"`
}
type State struct {
	platform.RoomIdentity
	Search              *SearchProgress               `json:"search,omitempty"`
	Retired             bool                          `json:"retired,omitempty"`
	SubscriptionID      string                        `json:"subscription_id"`
	SubscriptionVersion uint64                        `json:"subscription_version"`
	Binding             Binding                       `json:"binding"`
	Config              Config                        `json:"config"`
	Baseline            time.Time                     `json:"baseline"`
	Chats               map[string]ChatProgress       `json:"chats"`
	Directory           *DirectoryProgress            `json:"directory,omitempty"`
	PriorityTurn        int                           `json:"priority_turn,omitempty"`
	NextChat            int                           `json:"next_chat"`
	Observed            map[string]Observation        `json:"observed"`
	Accepted            map[string]notification.Route `json:"accepted"`
	Inbox               map[string]Entry              `json:"inbox"`
	LastCheck           time.Time                     `json:"last_check"`
	LastFull            time.Time                     `json:"last_full"`
	notification.RuntimeState
}

func (s State) Metadata() notification.Metadata {
	return notification.Metadata{Target: s.Config.Target.Canonical(), Key: notification.Key{Owner: s.RoomIdentity, Source: MessagesSourceID, SubscriptionID: s.SubscriptionID}, Version: s.SubscriptionVersion, Enabled: s.Config.Enabled, Retired: s.Retired, PlatformID: s.Binding.PlatformID, Observed: len(s.Observed), Pending: len(s.Inbox) - s.ExcludedItems(), DeliveryError: s.DeliveryErrorCode}
}

func (s State) ExcludedItems() int {
	count := 0
	for _, e := range s.Inbox {
		r := s.Chats[e.Message.ChatID].Chat.Relation
		if r.Kind == "self" && r.ValidFor(s.Binding.Account, e.Message.ChatID) {
			count++
		}
	}
	return count
}
func (s State) Validate(owner platform.RoomIdentity) error {
	c, e := NormalizeConfig(s.Config)
	if e != nil || !reflect.DeepEqual(c, s.Config) || s.RoomIdentity != owner || owner.RoomID == "" || owner.RoomInstance == "" || !component(s.SubscriptionID) || s.SubscriptionVersion == 0 || !s.Binding.Valid() || s.Config.PlatformID != s.Binding.PlatformID || s.Baseline.IsZero() && (c.Enabled || len(s.Chats)+len(s.Observed)+len(s.Accepted)+len(s.Inbox) > 0 || s.Directory != nil || !s.LastFull.IsZero()) || s.Chats == nil || s.Observed == nil || s.Accepted == nil || s.Inbox == nil || s.NextChat < 0 || s.NextChat >= MaxChats || len(s.Chats) > MaxChats || s.PriorityTurn < 0 || s.PriorityTurn > 2 || s.RuntimeState.Validate(c.Enabled) != nil || s.Retired && c.Enabled {
		return notification.ErrInvalid
	}
	if s.Search != nil && s.validateSearch() != nil {
		return notification.ErrInvalid
	}
	if !s.Baseline.IsZero() && !c.BackfillStart.IsZero() && !s.Baseline.Equal(c.BackfillStart) {
		return notification.ErrInvalid
	}
	if s.Directory != nil {
		d := s.Directory
		if d.Scan != nil {
			if d.Scan.Pages < 0 || len(d.Scan.Token) > 8192 || strings.ContainsAny(d.Scan.Token, "\x00\r\n") || d.Scan.Pages == 0 && (d.Scan.Token != "" || len(d.Scan.Seen) > 0) || d.Scan.Pages > 0 && d.Scan.Token == "" || d.Scan.Seen == nil || len(d.Scan.Seen) > MaxChats {
				return notification.ErrInvalid
			}
			for id, v := range d.Scan.Seen {
				if !nativeID(id, "oc_") || !v {
					return notification.ErrInvalid
				}
				if _, ok := s.Chats[id]; !ok {
					return notification.ErrInvalid
				}
			}
		}
	}
	for id, p := range s.Chats {
		if p.Chat.Relation != (ConversationRelation{}) && (!p.Chat.Relation.ValidFor(s.Binding.Account, id) || p.Chat.Mode == "p2p" && p.Chat.Relation.Kind == "group" || p.Chat.Mode != "p2p" && p.Chat.Relation.Kind != "group") {
			return notification.ErrInvalid
		}
		if !(p.Chat.Valid() || p.NeedsVerification && p.Chat == (Chat{ID: id}) && nativeID(id, "oc_") && p.Completed == 0 && p.Scan == nil) || p.Chat.ID != id || p.Completed < 0 || p.LastActivity < 0 || !validChatError(p.ErrorCode) || p.Scan != nil && (p.Scan.Start < s.Baseline.Unix() || p.Scan.End < p.Scan.Start || p.Scan.Pages < 0 || len(p.Scan.Token) > 8192) {
			return notification.ErrInvalid
		}
	}
	for key, o := range s.Observed {
		if key != RevisionKey(s.Binding, o.Message) || !o.Message.valid() || !o.Route.Valid(s.SubscriptionVersion) || !o.Route.Target.Inbox() {
			return notification.ErrInvalid
		}
	}
	for _, o := range s.Observed {
		if o.Accepted {
			if route, ok := s.Accepted[MessageKey(s.Binding, o.Message)]; !ok || route != o.Route {
				return notification.ErrInvalid
			}
		}
	}
	for key, r := range s.Accepted {
		if len(key) != 64 || !r.Valid(s.SubscriptionVersion) {
			return notification.ErrInvalid
		}
	}
	for key, e := range s.Inbox {
		if !e.Item.Content.Valid() {
			return notification.ErrInvalid
		}
		o, ok := s.Observed[key]
		chat, hasChat := s.Chats[e.Message.ChatID]
		ms, _ := strconv.ParseInt(e.Message.Created, 10, 64)
		if !ok || !hasChat || !o.Accepted || !reflect.DeepEqual(o.Message, e.Message) || o.Route != e.Item.Route || e.Item.Source != sourceRef(s, e.Message, key, chat.Chat) || e.Item.Order != e.Message.ID+":"+key || !e.Item.CreatedAt.Equal(time.UnixMilli(ms)) || len(e.Item.Text) == 0 || len(e.Item.Text) > 16<<10 || !utf8.ValidString(e.Item.Text) || strings.ContainsRune(e.Item.Text, 0) || key != e.Item.Key || e.Item.Source.ItemKey != key || e.Item.Source.Provider != "lark" || e.Item.Source.SubscriptionID != s.SubscriptionID || !e.Item.Route.Valid(s.SubscriptionVersion) || e.Item.DeliveryID != e.Item.Route.DeliveryID(notification.DeliveryID(s.Metadata().Key, key)) || !e.Item.Source.Reference.Valid() {
			return notification.ErrInvalid
		}
	}
	return nil
}
func (s State) Clone() State {
	b, _ := json.Marshal(s)
	var c State
	_ = json.Unmarshal(b, &c)
	return c
}
func (s State) CloneState() notification.State { return s.Clone() }
func (s State) PendingItems() []notification.Item {
	out := []notification.Item{}
	for _, e := range s.Inbox {
		p := s.Chats[e.Message.ChatID]
		if p.Chat.Mode == "p2p" && (!p.Chat.Relation.ValidFor(s.Binding.Account, p.Chat.ID) || p.Chat.Relation.Kind != "peer" || p.NeedsVerification) {
			continue
		}
		out = append(out, e.Item)
	}
	return out
}
func (s State) Notification(k string) (notification.Item, bool) {
	e, ok := s.Inbox[k]
	return e.Item, ok
}
func (s State) Acknowledge(k string) notification.State {
	n := s.Clone()
	delete(n.Inbox, k)
	n.RuntimeState.ResumeCapacity()
	n.DeliveryErrorCode = ""
	return n
}
func (s State) WithDeliveryError(code string) notification.State {
	n := s.Clone()
	n.DeliveryErrorCode = code
	return n
}
func (s State) InvalidateCredential() (notification.State, error) {
	n := s.Clone()

	if n.SubscriptionVersion == ^uint64(0) {
		return n, notification.ErrCapacity
	}
	n.SubscriptionVersion++
	n.RuntimeState.InvalidateCredential(n.Config.Enabled)
	return n, nil
}
func (s State) ScopeFailed(e error) notification.State {
	n := s.Clone()
	n.RuntimeState.ScopeFailed(e)
	return n
}
func Configure(owner platform.RoomIdentity, current *State, b Binding, c Config, now time.Time) (State, error) {
	if c.Groups.Scope == "all" && (current == nil || current.Search == nil) {
		return State{}, notification.ErrRequest
	}
	return configure(owner, current, b, c, now)
}

func configure(owner platform.RoomIdentity, current *State, b Binding, c Config, now time.Time) (State, error) {
	c, e := NormalizeConfig(c)
	if e != nil {
		return State{}, e
	}
	var n State
	if current == nil {
		if !b.Valid() {
			return n, notification.ErrRequest
		}
		var baseline time.Time
		if c.Enabled {
			var err error
			baseline, err = notification.BackfillBaseline(c.BackfillSeconds, c.BackfillStart, now)
			if err != nil {
				return n, err
			}
		}
		var id [16]byte
		if _, e = rand.Read(id[:]); e != nil {
			return n, e
		}
		n = State{RoomIdentity: owner, SubscriptionID: hex.EncodeToString(id[:]), Binding: b, Baseline: baseline, Chats: map[string]ChatProgress{}, Observed: map[string]Observation{}, Accepted: map[string]notification.Route{}, Inbox: map[string]Entry{}}
	} else {
		n = current.Clone()
		if n.Retired {
			return n, notification.ErrConflict
		}
		if b.PlatformID != n.Binding.PlatformID || b.Generation != n.Binding.Generation || b.Account != n.Binding.Account || !n.Baseline.IsZero() && (c.BackfillSeconds != n.Config.BackfillSeconds || !c.BackfillStart.Equal(n.Config.BackfillStart)) {
			return n, notification.ErrConflict
		}
	}
	if n.Baseline.IsZero() && c.Enabled {
		baseline, err := notification.BackfillBaseline(c.BackfillSeconds, c.BackfillStart, now)
		if err != nil {
			return n, err
		}
		n.Baseline = baseline
	}
	if n.SubscriptionVersion == ^uint64(0) {
		return n, notification.ErrCapacity
	}
	if !c.Enabled {
		c.PlatformID = b.PlatformID
	}
	n.SubscriptionVersion++
	// Credential renewal under the exact same account/generation changes query
	// admission evidence, not the business query. Rebind retained fingerprints
	// before reconfiguration so unchanged branches keep their cursor and scan.
	// This happens only on an explicit configuration write, never during decode.
	if current != nil && n.Search != nil && n.Binding != b {
		requests := n.searchRequests()
		n.Binding = b
		for i := range n.Search.Branches {
			for _, request := range requests {
				if request.kind == n.Search.Branches[i].Kind {
					n.Search.Branches[i].Fingerprint = n.searchFingerprint(request.kind, request.q)
				}
			}
		}
	}
	n.Binding = b
	n.Config = c
	if c.Enabled && c.DiscoverPrivate() && n.Directory == nil {
		n.Directory = &DirectoryProgress{}
	}
	if current != nil && (c.Private.Scope != current.Config.Private.Scope || c.Private.Enabled != current.Config.Private.Enabled) && n.Directory != nil {
		n.Directory.RetryAt = time.Time{}
	}
	for id, p := range n.Chats {
		p.ErrorCode = ""
		p.RetryAt = time.Time{}
		n.Chats[id] = p
	}
	if n.Search != nil {
		n.reconfigureSearch()
	}
	n.pruneObserved()
	if !n.Baseline.IsZero() {
		n.updateFull(now)
	}
	n.NextChat = 0
	n.RuntimeState.Reset(c.Enabled, n.LastFull.IsZero())
	return n, n.Validate(owner)
}
func (s State) String() string { return fmt.Sprintf("lark messages subscription %s", s.SubscriptionID) }

func Retire(s State) (State, error) {
	n := s.Clone()
	if n.SubscriptionVersion == ^uint64(0) {
		return n, notification.ErrCapacity
	}
	n.SubscriptionVersion++
	n.Retired = true
	n.Config.Enabled = false
	n.RuntimeState.Reset(false, false)
	return n, n.Validate(n.RoomIdentity)
}
