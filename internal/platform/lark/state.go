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

type Config struct {
	ScopeMode       string              `json:"scope_mode,omitempty"`
	Enabled         bool                `json:"enabled"`
	ConnectionID    string              `json:"connection_id"`
	Chats           []string            `json:"chats"`
	Target          notification.Target `json:"target"`
	Private         bool                `json:"private"`
	GroupMentions   bool                `json:"group_mentions"`
	AtAll           bool                `json:"at_all"`
	Bots            bool                `json:"bots"`
	IntervalSeconds int                 `json:"interval_seconds"`
	BackfillSeconds int                 `json:"backfill_seconds"`
}

func (c Config) AllChats() bool { return c.ScopeMode == "all" }

func NormalizeConfig(c Config) (Config, error) {
	if c.IntervalSeconds == 0 {
		c.IntervalSeconds = 60
	}
	if !component(c.ConnectionID) || !c.Target.Valid() || !c.Target.Inbox() || (c.ScopeMode != "" && c.ScopeMode != "specified" && c.ScopeMode != "all") || c.AllChats() && len(c.Chats) != 0 || !c.AllChats() && (len(c.Chats) == 0 || len(c.Chats) > 32) || c.IntervalSeconds < 3 || c.IntervalSeconds > 3600 || c.BackfillSeconds < 0 || c.BackfillSeconds > 86400 {
		return c, notification.ErrRequest
	}
	c.Chats = slices.Clone(c.Chats)
	slices.Sort(c.Chats)
	if len(slices.Compact(slices.Clone(c.Chats))) != len(c.Chats) {
		return c, notification.ErrRequest
	}
	for _, id := range c.Chats {
		if !nativeID(id, "oc_") {
			return c, notification.ErrRequest
		}
	}
	return c, nil
}
func component(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") == ""
}

type Binding struct {
	ConnectionID      string                   `json:"connection_id"`
	Generation        string                   `json:"generation"`
	Account           platform.AccountIdentity `json:"account"`
	CredentialVersion uint64                   `json:"credential_version"`
	Fingerprint       string                   `json:"fingerprint"`
}

func (b Binding) Valid() bool {
	return component(b.ConnectionID) && component(b.Generation) && b.Account.Valid() && b.Account.Provider == "lark" && (b.Account.Instance == Origin("feishu") || b.Account.Instance == Origin("lark")) && b.Account.Tenant.Namespace == "lark:tenant_key" && b.Account.Tenant.Value != "" && strings.HasPrefix(b.Account.ID.Namespace, "lark:open_id:cli_") && nativeID(b.Account.ID.Value, "ou_") && b.CredentialVersion > 0 && len(b.Fingerprint) == 64 && strings.Trim(b.Fingerprint, "0123456789abcdef") == ""
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
	return notification.Metadata{Key: notification.Key{Owner: s.RoomIdentity, Source: MessagesSourceID, SubscriptionID: s.SubscriptionID}, Version: s.SubscriptionVersion, Enabled: s.Config.Enabled, ConnectionID: s.Binding.ConnectionID, Observed: len(s.Observed), Pending: len(s.Inbox), DeliveryError: s.DeliveryErrorCode}
}
func (s State) Validate(owner platform.RoomIdentity) error {
	c, e := NormalizeConfig(s.Config)
	if e != nil || !reflect.DeepEqual(c, s.Config) || s.RoomIdentity != owner || owner.RoomID == "" || owner.RoomInstance == "" || !component(s.SubscriptionID) || s.SubscriptionVersion == 0 || !s.Binding.Valid() || s.Config.ConnectionID != s.Binding.ConnectionID || s.Baseline.IsZero() || s.Chats == nil || s.Observed == nil || s.Accepted == nil || s.Inbox == nil || s.NextChat < 0 || s.NextChat >= MaxChats || len(s.Chats) > MaxChats || s.PriorityTurn < 0 || s.PriorityTurn > 2 || s.RuntimeState.Validate(c.Enabled) != nil || s.Retired && c.Enabled {
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
	c, e := NormalizeConfig(c)
	if e != nil {
		return State{}, e
	}
	var n State
	if current == nil {
		if !c.Enabled || !b.Valid() {
			return n, notification.ErrRequest
		}
		var id [16]byte
		if _, e = rand.Read(id[:]); e != nil {
			return n, e
		}
		n = State{RoomIdentity: owner, SubscriptionID: hex.EncodeToString(id[:]), Binding: b, Baseline: now.Add(-time.Duration(c.BackfillSeconds) * time.Second), Chats: map[string]ChatProgress{}, Observed: map[string]Observation{}, Accepted: map[string]notification.Route{}, Inbox: map[string]Entry{}}
	} else {
		n = current.Clone()
		if n.Retired {
			return n, notification.ErrConflict
		}
		if b.ConnectionID != n.Binding.ConnectionID || b.Generation != n.Binding.Generation || b.Account != n.Binding.Account || c.BackfillSeconds != n.Config.BackfillSeconds {
			return n, notification.ErrConflict
		}
	}
	if n.SubscriptionVersion == ^uint64(0) {
		return n, notification.ErrCapacity
	}
	if !c.Enabled {
		c.ConnectionID = b.ConnectionID
	}
	n.SubscriptionVersion++
	n.Binding = b
	n.Config = c
	if c.AllChats() && n.Directory == nil {
		n.Directory = &DirectoryProgress{}
	}
	if current != nil && c.ScopeMode != current.Config.ScopeMode && n.Directory != nil {
		n.Directory.RetryAt = time.Time{}
	}
	for id, p := range n.Chats {
		p.ErrorCode = ""
		p.RetryAt = time.Time{}
		n.Chats[id] = p
	}
	n.pruneObserved()
	n.updateFull(now)
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
