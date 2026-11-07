package gitlab

import (
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const (
	EventsOverlap             = 5 * time.Minute
	EventsFullInterval        = 5 * time.Minute
	MaxEventsStateBytes int64 = 64 << 20
	MaxEventsObserved         = 100000
	MaxEventsInbox            = 10000
	MaxEventsFilters          = 256
)

type EventsFilter struct {
	Types []string `json:"types,omitempty"`
}

func (f EventsFilter) Clone() EventsFilter {
	f.Types = append([]string(nil), f.Types...)
	return f
}

type EventsConfig struct {
	Target          notification.Target `json:"target,omitzero"`
	ConnectionID    string              `json:"connection_id,omitempty"`
	Project         string              `json:"project,omitempty"`
	Enabled         bool                `json:"enabled"`
	IntervalSeconds int                 `json:"interval_seconds"`
	Filter          EventsFilter        `json:"filter"`
	// Opt-in only after verification on the target Events endpoint. First query
	// after process/worker restart is always unconditional, even with disk cache.
	ETagVerified bool `json:"etag_verified,omitempty"`
}

type EventsFilterRevision struct {
	Since  time.Time    `json:"since"`
	Filter EventsFilter `json:"filter"`
}

type EventsObservation struct {
	ID        int64     `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Reason    string    `json:"reason"`
}

type EventsInboxEntry struct {
	Route          notification.Route `json:"route,omitzero"`
	DeliveryID     string             `json:"delivery_id"`
	SubscriptionID string             `json:"subscription_id"`
	platform.RoomIdentity
	Event      Event     `json:"event"`
	ReceivedAt time.Time `json:"received_at"`
}

type EventsScan struct {
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
	NextPage int       `json:"next_page"`
	Pages    int       `json:"pages"`
	Bytes    int64     `json:"bytes"`
}

type EventsCache struct {
	SubscriptionVersion uint64  `json:"subscription_version"`
	ETag                string  `json:"etag"`
	IDs                 []int64 `json:"ids"`
}

type EventsGap struct {
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	Reason string    `json:"reason"`
}

// One independently committed snapshot owns subscription, inbox, observation
// set, cache and scan checkpoint. It is never part of chat reset/history state.
// The catalog's root Store lock owns these files even for inactive rooms.
type EventsState struct {
	platform.RoomIdentity
	SubscriptionID      string                       `json:"subscription_id"`
	SubscriptionVersion uint64                       `json:"subscription_version"`
	Binding             platform.ProjectBinding      `json:"binding"`
	UserID              int64                        `json:"user_id,omitempty"`
	Username            string                       `json:"username,omitempty"`
	Retired             bool                         `json:"retired,omitempty"`
	ProjectID           int64                        `json:"project_id"` // zero only until the first successful verification
	Config              EventsConfig                 `json:"config"`
	Baseline            time.Time                    `json:"baseline"`
	Watermark           time.Time                    `json:"watermark"`
	IgnoreBefore        time.Time                    `json:"ignore_before"`
	Filters             []EventsFilterRevision       `json:"filters"`
	Observed            map[string]EventsObservation `json:"observed"`
	Inbox               map[string]EventsInboxEntry  `json:"inbox"`
	Scan                *EventsScan                  `json:"scan,omitempty"`
	QuickCache          *EventsCache                 `json:"quick_cache,omitempty"`
	LastCheck           time.Time                    `json:"last_check"`
	LastFull            time.Time                    `json:"last_full"`
	notification.RuntimeState
	Gap *EventsGap `json:"gap,omitempty"`
}

func NormalizeEventsConfig(c EventsConfig) (EventsConfig, error) {
	if !c.Target.Valid() {
		return c, notification.ErrRequest
	}
	if c.ConnectionID != "" && !validRestartID(c.ConnectionID) || c.Project != "" && !validEventsProjectPath(c.Project) {
		return c, notification.ErrRequest
	}
	interval, err := normalizeInterval(c.IntervalSeconds)
	if err != nil {
		return c, err
	}
	c.IntervalSeconds = interval
	f := &c.Filter
	f.Types = append([]string(nil), f.Types...)
	for _, typ := range f.Types {
		if !slices.Contains([]string{"merge_request.opened", "merge_request.merged", "merge_request.closed", "note", "push"}, typ) {
			return c, notification.ErrRequest
		}
	}
	if len(f.Types) > 64 {
		return c, notification.ErrRequest
	}
	slices.Sort(f.Types)
	f.Types = slices.Compact(f.Types)
	return c, nil
}

func EventsKey(b platform.ProjectBinding, project, id int64) string {
	return b.URL + "|" + strconv.FormatInt(project, 10) + "|" + strconv.FormatInt(id, 10)
}

func (s EventsState) DeliveryID(key string) string {
	return notification.DeliveryID(s.Metadata().Key, key)
}

func (s EventsState) Clone() EventsState {
	next := s
	next.Config.Filter = s.Config.Filter.Clone()
	next.Filters = append([]EventsFilterRevision(nil), s.Filters...)
	for i := range next.Filters {
		next.Filters[i].Filter = s.Filters[i].Filter.Clone()
	}
	next.Observed = make(map[string]EventsObservation, len(s.Observed))
	for k, v := range s.Observed {
		next.Observed[k] = v
	}
	next.Inbox = make(map[string]EventsInboxEntry, len(s.Inbox))
	for k, v := range s.Inbox {
		next.Inbox[k] = v
	}
	if s.Scan != nil {
		scan := *s.Scan
		next.Scan = &scan
	}
	if s.QuickCache != nil {
		c := *s.QuickCache
		c.IDs = append([]int64{}, c.IDs...)
		next.QuickCache = &c
	}
	if s.Gap != nil {
		gap := *s.Gap
		next.Gap = &gap
	}
	return next
}

func (s EventsState) Validate(owner platform.RoomIdentity) error {
	if s.Retired && s.Config.Enabled || s.UserID < 0 || s.UserID == 0 && s.Username != "" || s.Config.ConnectionID != "" && s.UserID <= 0 || s.UserID > 0 && (!platform.ValidUsername(s.Username) || s.Binding.ConnectionID == "") ||
		s.Config.ConnectionID != "" && s.Config.ConnectionID != s.Binding.ConnectionID || s.Config.Project != "" && s.Config.Project != s.Binding.Project {
		return notification.ErrInvalid
	}
	if s.RoomIdentity != owner || !validRestartID(s.SubscriptionID) || s.SubscriptionVersion == 0 ||
		s.Baseline.IsZero() || s.Watermark.Before(s.Baseline) || s.IgnoreBefore.Before(s.Baseline) || s.IgnoreBefore.After(s.Watermark) ||
		s.Observed == nil || s.Inbox == nil || len(s.Observed) > MaxEventsObserved || len(s.Inbox) > MaxEventsInbox || s.ProjectID < 0 ||
		len(s.Filters) == 0 || len(s.Filters) > MaxEventsFilters || s.Filters[0].Since.After(s.IgnoreBefore) || s.Filters[0].Since.Before(s.Baseline) {
		return notification.ErrInvalid
	}
	if !s.IgnoreBefore.Equal(maxEventTime(s.Baseline, s.Watermark.Add(-EventsOverlap))) ||
		s.LastFull.IsZero() && !s.Watermark.Equal(s.Baseline) ||
		!s.LastFull.IsZero() && (s.LastFull.Before(s.Watermark) || s.ProjectID == 0) ||
		!s.LastCheck.IsZero() && s.LastCheck.Before(s.Baseline) {
		return notification.ErrInvalid
	}
	if !validEventsProjectBinding(s.Binding) {
		return notification.ErrInvalid
	}
	cfg, err := NormalizeEventsConfig(s.Config)
	if err != nil || !reflect.DeepEqual(cfg, s.Config) {
		return notification.ErrInvalid
	}
	for i, r := range s.Filters {
		c, err := NormalizeEventsConfig(EventsConfig{Filter: r.Filter})
		if err != nil || !reflect.DeepEqual(c.Filter, r.Filter) || i > 0 && r.Since.Before(s.Filters[i-1].Since) {
			return notification.ErrInvalid
		}
	}
	if !reflect.DeepEqual(s.Filters[len(s.Filters)-1].Filter, s.Config.Filter) {
		return notification.ErrInvalid
	}
	if s.ProjectID == 0 && (len(s.Observed) > 0 || len(s.Inbox) > 0 || s.Scan != nil || s.QuickCache != nil || !s.LastCheck.IsZero() || !s.LastFull.IsZero()) {
		return notification.ErrInvalid
	}
	for key, o := range s.Observed {
		if o.ID <= 0 || key != EventsKey(s.Binding, s.ProjectID, o.ID) || o.CreatedAt.Before(s.Baseline) ||
			!slices.Contains([]string{"accepted", "event_type", "unsupported_event", "teamx_note"}, o.Reason) {
			return notification.ErrInvalid
		}
	}
	for key, e := range s.Inbox {
		o, ok := s.Observed[key]
		if !ok || !e.Route.Valid(s.SubscriptionVersion) || o.Reason != "accepted" || e.RoomIdentity != owner || e.SubscriptionID != s.SubscriptionID || e.DeliveryID != s.DeliveryID(key) ||
			e.Event.ProjectID != s.ProjectID || e.Event.ID != o.ID || !e.Event.CreatedAt.Equal(o.CreatedAt) || e.ReceivedAt.IsZero() {
			return notification.ErrInvalid
		}
	}
	if s.Scan != nil && (s.Scan.Start.Before(s.IgnoreBefore) || !s.Scan.Start.Before(s.Scan.End) || s.Scan.End.Before(s.Watermark) || s.Scan.NextPage < 1 || s.Scan.NextPage > 10000 || s.Scan.Pages < 0 || s.Scan.Pages >= s.Scan.NextPage || s.Scan.Bytes < 0) {
		return notification.ErrInvalid
	}
	if s.QuickCache != nil && (s.QuickCache.SubscriptionVersion != s.SubscriptionVersion || !s.Config.ETagVerified || s.QuickCache.ETag == "" || len(s.QuickCache.ETag) > 1024 || len(s.QuickCache.IDs) > 20) {
		return notification.ErrInvalid
	}
	if s.QuickCache != nil {
		for _, c := range s.QuickCache.ETag {
			if c < 32 || c > 126 {
				return notification.ErrInvalid
			}
		}
		for _, id := range s.QuickCache.IDs {
			if id <= 0 {
				return notification.ErrInvalid
			}
		}
	}
	if s.RuntimeState.Validate(s.Config.Enabled) != nil {
		return notification.ErrInvalid
	}
	if s.Gap != nil && (s.Gap.Start.Before(s.Baseline) || !s.Gap.Start.Before(s.Gap.End) || s.Gap.Reason != "retention_unverified") {
		return notification.ErrInvalid
	}
	return nil
}

func (s EventsState) Reason(e Event) string {
	if e.CreatedAt.Before(s.Baseline) {
		return "before_baseline"
	}
	if e.CreatedAt.Before(s.IgnoreBefore) {
		return "before_watermark"
	}
	if e.Kind == "unknown" || e.Kind == "" || e.SkipReason != "" {
		return "unsupported_event"
	}
	if e.Kind == "note" && IsTeamXReviewNote(e.NoteBody) {
		return "teamx_note"
	}
	filter := s.Filters[0].Filter
	for _, r := range s.Filters {
		if r.Since.After(e.CreatedAt) {
			break
		}
		filter = r.Filter
	}
	typ := e.Kind
	if typ == "merge_request" {
		typ += "." + e.Action
	}
	if len(filter.Types) > 0 && !slices.Contains(filter.Types, typ) {
		return "event_type"
	}
	return "accepted"
}

// Never evict by count. The monotonic admission floor makes removal of an old
// observation safe, even if an old event resurfaces in the latest quick page.
// Pending inbox entries are retained regardless of age until delivery is acknowledged.
func (s *EventsState) Compact() {
	for key, o := range s.Observed {
		if _, pending := s.Inbox[key]; !pending && o.CreatedAt.Before(s.IgnoreBefore) {
			delete(s.Observed, key)
		}
	}
	at := 0
	for i, r := range s.Filters {
		if !r.Since.After(s.IgnoreBefore) {
			at = i
		}
	}
	s.Filters = append([]EventsFilterRevision(nil), s.Filters[at:]...)
}

// Quick checks persist all unknown IDs (including filtered/unsupported ones),
// rather than stopping at the first known ID or trusting a maximum event ID.
func (s *EventsState) Accept(events []Event, now time.Time) (unknown, known int, err error) {
	for _, e := range events {
		if e.ID <= 0 || e.ProjectID != s.ProjectID || e.CreatedAt.IsZero() {
			return 0, 0, notification.ErrInvalid
		}
		key := EventsKey(s.Binding, s.ProjectID, e.ID)
		if _, ok := s.Observed[key]; ok || e.CreatedAt.Before(s.IgnoreBefore) {
			known++
			continue
		}
		unknown++
		reason := s.Reason(e)
		s.Observed[key] = EventsObservation{e.ID, e.CreatedAt, reason}
		s.Metrics.NewItems++
		if reason == "accepted" {
			s.Inbox[key] = EventsInboxEntry{Route: notification.Route{Target: s.Config.Target, Version: s.SubscriptionVersion}, DeliveryID: s.DeliveryID(key), SubscriptionID: s.SubscriptionID, RoomIdentity: s.RoomIdentity, Event: e, ReceivedAt: now}
		}
	}
	return unknown, known, nil
}

// Existing review writes include a stable base/head content marker and return
// the note ID. Match that format, independent of the human/token account.
var teamXReviewMarker = regexp.MustCompile(`(?s)^## TeamX reviewer

Reviewed base: ` + "`" + `([0-9a-f]{40})` + "`" + `
Reviewed head: ` + "`" + `([0-9a-f]{40})` + "`" + `

.*

<!-- teamx-review:([0-9a-f]{40}):([0-9a-f]{40}) -->$`)

func IsTeamXReviewNote(body string) bool {
	m := teamXReviewMarker.FindStringSubmatch(strings.TrimSpace(body))
	return len(m) == 5 && m[1] == m[3] && m[2] == m[4]
}

func validRestartID(id string) bool {
	return len(id) == 32 && strings.Trim(id, "0123456789abcdef") == ""
}
func validText(s string) bool { return strings.TrimSpace(s) != "" && utf8.ValidString(s) }
func validEventsProjectBinding(b platform.ProjectBinding) bool {
	u, err := url.Parse(b.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || len(b.URL) > 4096 || strings.IndexFunc(b.URL, unicode.IsSpace) >= 0 || strings.IndexFunc(b.URL, unicode.IsControl) >= 0 || b.Provider != "gitlab" || len(b.Project) > 2048 || !regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+$`).MatchString(b.Project) {
		return false
	}
	for _, part := range strings.Split(b.Project, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return validRestartID(b.ConnectionID) && validRestartID(b.ConnectionGeneration) && b.ProjectID > 0
}

func validEventsProjectPath(project string) bool {
	if len(project) > 2048 || !regexp.MustCompile(`^[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+$`).MatchString(project) {
		return false
	}
	for _, part := range strings.Split(project, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}
