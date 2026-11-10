package gitlab

import (
	"fmt"
	"maps"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// TodosBinding is account evidence, never a token. The credential stamp remains
// durable so a restart cannot silently adopt a rotated or re-resolved credential.
type TodosBinding struct {
	PlatformID            string `json:"platform_id"`
	PlatformGeneration    string `json:"platform_generation"`
	URL                   string `json:"url"`
	UserID                int64  `json:"user_id"`
	Username              string `json:"username"`
	CredentialVersion     uint64 `json:"credential_version"`
	CredentialFingerprint string `json:"credential_fingerprint"`
}

func (b TodosBinding) SameAccount(other TodosBinding) bool {
	return b.URL == other.URL && b.UserID == other.UserID
}
func (b TodosBinding) Valid() bool {
	u, err := url.Parse(b.URL)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.RawPath == "" && len(b.URL) <= 2048 && !strings.ContainsAny(b.URL, "# \t\r\n") && validRestartID(b.PlatformID) && validRestartID(b.PlatformGeneration) && b.UserID > 0 && platform.ValidUsername(b.Username) && b.CredentialVersion > 0 && len(b.CredentialFingerprint) == 64 && strings.Trim(b.CredentialFingerprint, "0123456789abcdef") == ""
}

// Scope adds the current metadata revision without persisting it as identity.
type TodosQueryScope struct {
	Binding         TodosBinding `json:"binding"`
	PlatformVersion uint64       `json:"platform_version"`
}

type TodosFilter struct {
	Reasons []string `json:"reasons"`
	Types   []string `json:"types"`
}
type TodosConfig struct {
	BackfillSeconds int                 `json:"backfill_seconds,omitempty"`
	BackfillStart   time.Time           `json:"backfill_start,omitzero"`
	Target          notification.Target `json:"target,omitzero"`
	Enabled         bool                `json:"enabled"`
	PlatformID      string              `json:"platform_id"`
	IntervalSeconds int                 `json:"interval_seconds"`
	Filter          TodosFilter         `json:"filter"`
}

func NormalizeTodosConfig(c TodosConfig) (TodosConfig, error) {
	return normalizeTodosConfig(c, true)
}
func normalizeTodosConfig(c TodosConfig, requirePlatform bool) (TodosConfig, error) {
	if !c.Target.Valid() {
		return c, notification.ErrRequest
	}
	interval, err := normalizeInterval(c.IntervalSeconds)
	if err != nil || requirePlatform && !validRestartID(c.PlatformID) {
		return c, notification.ErrRequest
	}
	c.IntervalSeconds = interval
	start, err := notification.NormalizeBackfill(c.BackfillSeconds, c.BackfillStart)
	if err != nil {
		return c, err
	}
	c.BackfillStart = start
	c.Filter.Reasons = slices.Clone(c.Filter.Reasons)
	c.Filter.Types = slices.Clone(c.Filter.Types)
	if c.Filter.Reasons == nil {
		c.Filter.Reasons = []string{"mentioned", "assigned", "review_requested", "other"}
	}
	if c.Filter.Types == nil {
		c.Filter.Types = []string{"merge_request", "work_item"}
	}
	if len(c.Filter.Reasons) > 4 || len(c.Filter.Types) > 2 {
		return c, notification.ErrRequest
	}
	for _, v := range c.Filter.Reasons {
		if !slices.Contains([]string{"mentioned", "assigned", "review_requested", "other"}, v) {
			return c, notification.ErrRequest
		}
	}
	for _, v := range c.Filter.Types {
		if !slices.Contains([]string{"merge_request", "work_item"}, v) {
			return c, notification.ErrRequest
		}
	}
	slices.Sort(c.Filter.Reasons)
	c.Filter.Reasons = slices.Compact(c.Filter.Reasons)
	slices.Sort(c.Filter.Types)
	c.Filter.Types = slices.Compact(c.Filter.Types)
	return c, nil
}

type TodosFilterRevision struct {
	Since  time.Time   `json:"since"`
	Filter TodosFilter `json:"filter"`
}
type TodosObservation struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Reason    string    `json:"reason"`
}
type TodosInboxEntry struct {
	Route notification.Route `json:"route,omitzero"`
	platform.RoomIdentity
	SubscriptionID string    `json:"subscription_id"`
	DeliveryID     string    `json:"delivery_id"`
	Todo           Todo      `json:"todo"`
	ReceivedAt     time.Time `json:"received_at"`
}

// These are diagnostic offset checkpoints, not stable cursors. A new runner,
// a failed request or an explicit sync restarts pending and done from page one.
type TodosScan struct {
	StartedAt time.Time `json:"started_at"`
	State     string    `json:"state"`
	NextPage  int       `json:"next_page"`
	Pages     int       `json:"pages"`
	Bytes     int64     `json:"bytes"`
	Limited   bool      `json:"limited"`
}
type TodosState struct {
	platform.RoomIdentity
	SubscriptionID      string                      `json:"subscription_id"`
	SubscriptionVersion uint64                      `json:"subscription_version"`
	Retired             bool                        `json:"retired"`
	Binding             TodosBinding                `json:"binding"`
	Config              TodosConfig                 `json:"config"`
	Baseline            time.Time                   `json:"baseline"`
	Filters             []TodosFilterRevision       `json:"filters"`
	Observed            map[string]TodosObservation `json:"observed"`
	Inbox               map[string]TodosInboxEntry  `json:"inbox"`
	DeliveredObjects    map[string]Todo             `json:"delivered_objects,omitempty"`
	Scan                *TodosScan                  `json:"scan,omitempty"`
	LastCheck           time.Time                   `json:"last_check"`
	LastFull            time.Time                   `json:"last_full"`
	notification.RuntimeState
}

func TodosKey(b TodosBinding, id int64) string {
	return b.URL + "|" + strconv.FormatInt(b.UserID, 10) + "|" + strconv.FormatInt(id, 10)
}
func cloneTodosFilter(f TodosFilter) TodosFilter {
	return TodosFilter{Reasons: slices.Clone(f.Reasons), Types: slices.Clone(f.Types)}
}
func (s TodosState) Clone() TodosState {
	next := s
	next.Config.Filter = cloneTodosFilter(s.Config.Filter)
	next.Filters = append([]TodosFilterRevision(nil), s.Filters...)
	for i := range next.Filters {
		next.Filters[i].Filter = cloneTodosFilter(next.Filters[i].Filter)
	}
	if s.Scan != nil {
		scan := *s.Scan
		next.Scan = &scan
	}
	next.Observed = maps.Clone(s.Observed)
	next.Inbox = maps.Clone(s.Inbox)
	next.DeliveredObjects = maps.Clone(s.DeliveredObjects)
	for key, t := range next.DeliveredObjects {
		if t.Actor != nil {
			v := *t.Actor
			t.Actor = &v
		}
		if t.TargetAuthor != nil {
			v := *t.TargetAuthor
			t.TargetAuthor = &v
		}
		next.DeliveredObjects[key] = t
	}
	for key, entry := range next.Inbox {
		if entry.Todo.Actor != nil {
			actor := *entry.Todo.Actor
			entry.Todo.Actor = &actor
		}
		if entry.Todo.TargetAuthor != nil {
			author := *entry.Todo.TargetAuthor
			entry.Todo.TargetAuthor = &author
		}
		next.Inbox[key] = entry
	}
	return next
}
func (s TodosState) Validate(owner platform.RoomIdentity) error {
	if !s.Config.BackfillStart.IsZero() && !s.Baseline.Equal(s.Config.BackfillStart) {
		return notification.ErrInvalid
	}
	cfg, err := NormalizeTodosConfig(s.Config)
	if err != nil || !reflect.DeepEqual(cfg, s.Config) || s.RoomIdentity != owner || !validRestartID(s.SubscriptionID) || s.SubscriptionVersion == 0 || !s.Binding.Valid() || cfg.PlatformID != s.Binding.PlatformID || s.Baseline.IsZero() || s.Retired && cfg.Enabled || s.RuntimeState.Validate(cfg.Enabled) != nil || s.Observed == nil || s.Inbox == nil || len(s.Observed) > 100000 || len(s.Inbox) > 10000 || len(s.Filters) == 0 || len(s.Filters) > 256 || !s.Filters[0].Since.Equal(s.Baseline) {
		return notification.ErrInvalid
	}
	for i, f := range s.Filters {
		c, e := NormalizeTodosConfig(TodosConfig{PlatformID: cfg.PlatformID, Filter: f.Filter})
		if e != nil || !reflect.DeepEqual(c.Filter, f.Filter) || i > 0 && f.Since.Before(s.Filters[i-1].Since) {
			return notification.ErrInvalid
		}
	}
	last := s.Filters[len(s.Filters)-1]
	if !reflect.DeepEqual(last.Filter, cfg.Filter) {
		return notification.ErrInvalid
	}
	if !s.LastCheck.IsZero() && s.LastCheck.Before(s.Baseline) || !s.LastFull.IsZero() && (s.LastFull.Before(s.Baseline) || s.LastCheck.Before(s.LastFull)) {
		return notification.ErrInvalid
	}
	if s.Scan != nil && (s.Scan.StartedAt.Before(s.Baseline) || (s.Scan.State != "pending" && s.Scan.State != "done") || s.Scan.NextPage < 1 || s.Scan.NextPage > 10000 || s.Scan.Pages < 0 || s.Scan.Pages > 20000 || s.Scan.Bytes < 0 || s.Scan.Bytes > 20000*int64(maxTodoOutput) || s.LastFull.After(s.Scan.StartedAt)) {
		return notification.ErrInvalid
	}
	for key, o := range s.Observed {
		if key != TodosKey(s.Binding, o.ID) || o.ID <= 0 || o.CreatedAt.Before(s.Baseline) || !slices.Contains([]string{"accepted", "reason", "type", "no_project", "unsupported_target", "target_unavailable", "untrusted_url"}, o.Reason) {
			return notification.ErrInvalid
		}
	}
	for key, e := range s.Inbox {
		o, ok := s.Observed[key]
		if !ok || !e.Route.Valid(s.SubscriptionVersion) || o.Reason != "accepted" || e.RoomIdentity != owner || e.SubscriptionID != s.SubscriptionID || e.DeliveryID != notification.DeliveryID(s.Metadata().Key, key) || e.Todo.ID != o.ID || !e.Todo.CreatedAt.Equal(o.CreatedAt) || e.ReceivedAt.Before(s.Baseline) || (e.Todo.State != "pending" && e.Todo.State != "done") || e.Todo.SkipReason != "" || !validTodo(e.Todo, s.Binding.URL, s.Binding.UserID, TodoQuery{State: e.Todo.State}) {
			return notification.ErrInvalid
		}
	}
	if len(s.DeliveredObjects) > 100000 {
		return notification.ErrInvalid
	}
	for key, t := range s.DeliveredObjects {
		o, ok := s.Observed[key]
		if !ok || o.Reason != "accepted" || key != TodosKey(s.Binding, t.ID) || o.ID != t.ID || !t.CreatedAt.Equal(o.CreatedAt) || !validTodo(t, s.Binding.URL, s.Binding.UserID, TodoQuery{State: t.State}) {
			return notification.ErrInvalid
		}
	}
	return nil
}
func (s TodosState) Reason(t Todo) string {
	if t.CreatedAt.Before(s.Baseline) {
		return "before_baseline"
	}
	if t.SkipReason != "" {
		return t.SkipReason
	}
	f := s.Filters[0]
	for _, r := range s.Filters {
		if r.Since.After(t.CreatedAt) {
			break
		}
		f = r
	}
	if !slices.Contains(f.Filter.Reasons, t.Reason) {
		return "reason"
	}
	if !slices.Contains(f.Filter.Types, t.Kind) {
		return "type"
	}
	return "accepted"
}
func (s *TodosState) Accept(todos []Todo, now time.Time) {
	for _, todo := range todos {
		key := TodosKey(s.Binding, todo.ID)
		if _, known := s.Observed[key]; known || todo.CreatedAt.Before(s.Baseline) {
			continue
		}
		reason := s.Reason(todo)
		s.Observed[key] = TodosObservation{todo.ID, todo.CreatedAt, reason}
		s.Metrics.NewItems++
		if reason == "accepted" {
			s.Inbox[key] = TodosInboxEntry{Route: notification.Route{Target: s.Config.Target, Version: s.SubscriptionVersion}, RoomIdentity: s.RoomIdentity, SubscriptionID: s.SubscriptionID, DeliveryID: notification.DeliveryID(s.Metadata().Key, key), Todo: todo, ReceivedAt: now}
		}
	}
}
func (s TodosState) Metadata() notification.Metadata {
	return notification.Metadata{Target: s.Config.Target.Canonical(), Key: notification.Key{Owner: s.RoomIdentity, Source: TodosSourceID, SubscriptionID: s.SubscriptionID}, Version: s.SubscriptionVersion, Enabled: s.Config.Enabled, Retired: s.Retired, PlatformID: s.Binding.PlatformID, Observed: len(s.Observed), Pending: len(s.Inbox), DeliveryError: s.DeliveryErrorCode}
}
func (s TodosState) CloneState() notification.State { return s.Clone() }
func (s TodosState) PendingItems() []notification.Item {
	items := make([]notification.Item, 0, len(s.Inbox))
	for key, e := range s.Inbox {
		items = append(items, notification.Item{Route: e.Route, Key: key, DeliveryID: e.Route.DeliveryID(e.DeliveryID), CreatedAt: e.Todo.CreatedAt, Order: fmt.Sprintf("%020d", e.Todo.ID)})
	}
	return items
}
func (s TodosState) Notification(key string) (notification.Item, bool) {
	e, ok := s.Inbox[key]
	if !ok {
		return notification.Item{}, false
	}
	return TodosMessage(s, e), true
}
func (s TodosState) Acknowledge(key string) notification.State {
	next := s.Clone()
	if entry, ok := next.Inbox[key]; ok {
		if next.DeliveredObjects == nil {
			next.DeliveredObjects = map[string]Todo{}
		}
		next.DeliveredObjects[key] = entry.Todo
	}
	delete(next.Inbox, key)
	next.DeliveryErrorCode = ""
	return next
}
func (s TodosState) WithDeliveryError(code string) notification.State {
	next := s.Clone()
	next.DeliveryErrorCode = code
	return next
}
func (s TodosState) InvalidateCredential() (notification.State, error) {
	next := s.Clone()
	if next.SubscriptionVersion == ^uint64(0) {
		return nil, notification.ErrCapacity
	}
	next.SubscriptionVersion++
	next.RuntimeState.InvalidateCredential(next.Config.Enabled)
	next.Scan = nil
	return next, nil
}
func (s TodosState) ScopeFailed(err error) notification.State {
	next := s.Clone()
	if next.Config.Enabled {
		next.RuntimeState.ScopeFailed(err)
	}
	return next
}
