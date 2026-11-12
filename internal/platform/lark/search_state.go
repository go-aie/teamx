package lark

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const SearchSchema = 4
const searchWindow = int64(15 * 60)
const searchOverlap = int64(5 * 60)

// SearchSource is the sole live Lark collection adapter. Schema-3 snapshots
// require an explicit offline upgrade; no runtime fallback or migration exists.
type SearchSource struct{ Source }

// DefaultSearchConfig is a draft for new search subscriptions only. Existing
// settings always come from their saved state, including missing group scope.
func DefaultSearchConfig(platformID string) Config {
	c := DefaultConfig(platformID)
	c.Groups = GroupConfig{Enabled: true, Scope: "all", MessageMode: "mentions", AtMe: true, AtAll: true}
	return c
}

func (*SearchSource) Schema() int { return SearchSchema }
func (*SearchSource) Decode(raw []byte, owner platform.RoomIdentity) (notification.State, error) {
	var s State
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(any)) != io.EOF || s.Search == nil || s.Validate(owner) != nil {
		return nil, notification.ErrInvalid
	}
	return s, nil
}
func (s *SearchSource) NewRunner(h notification.StepHost) notification.Runner {
	return &searchRunner{h: h, source: s}
}

type SearchProgress struct {
	Branches []SearchBranch `json:"branches"`
	Next     int            `json:"next"`
	Metrics  SearchMetrics  `json:"metrics"`
}
type SearchMetrics struct {
	Pages         uint64 `json:"pages"`
	Batches       uint64 `json:"batches"`
	Singles       uint64 `json:"singles"`
	Verifications uint64 `json:"verifications"`
	Hits          uint64 `json:"hits"`
	Accepted      uint64 `json:"accepted"`
	Excluded      uint64 `json:"excluded"`
}
type SearchBranch struct {
	Kind        string      `json:"kind"`
	Fingerprint string      `json:"fingerprint"`
	Completed   int64       `json:"completed"`
	Scan        *SearchScan `json:"scan,omitempty"`
	LastSuccess time.Time   `json:"last_success"`
	RetryAt     time.Time   `json:"retry_at"`
	Failures    int         `json:"failures"`
	ErrorCode   string      `json:"error_code,omitempty"`
}
type SearchScan struct {
	Tokens map[string]bool `json:"tokens"`
	Start  int64           `json:"start"`
	End    int64           `json:"end"`
	Token  string          `json:"token,omitempty"`
	Page   *SearchPage     `json:"page,omitempty"`
}
type SearchPage struct {
	Items       []MessageSearchItem `json:"items"`
	NextToken   string              `json:"next_token,omitempty"`
	HasMore     bool                `json:"has_more"`
	Done        int                 `json:"done"`
	BatchRead   bool                `json:"batch_read"`
	Details     map[string]Message  `json:"details"`
	MemberToken string              `json:"member_token,omitempty"`
}

type branchQuery struct {
	kind string
	q    MessageSearchRequest
}

func (s State) searchRequests() []branchQuery {
	var out []branchQuery
	add := func(kind, typ, scope string, ids []string, own, mentions bool) {
		q := MessageSearchRequest{ChatType: typ, IsAtMe: mentions, PageSize: 50}
		if scope != "all" {
			q.ChatIDs = slices.Clone(ids)
		}
		if own {
			q.FromID = s.Binding.Account.ID.Value
		}
		out = append(out, branchQuery{kind, q})
	}
	if s.Config.Private.Enabled {
		add("private", "p2p", s.Config.Private.Scope, s.Config.Private.ChatIDs, false, false)
	}
	g := s.Config.Groups
	if g.Enabled {
		if g.MessageMode == "all" || g.AtMe || g.AtAll {
			add("groups", "group", g.Scope, g.ChatIDs, false, g.MessageMode != "all")
		}
		if g.MessageMode != "all" {
			add("own", "group", g.Scope, g.ChatIDs, true, false)
		}
	}
	return out
}

// A branch's request fingerprint also freezes local mention predicates and the
// credential binding. Target/interval changes do not invalidate accepted pages.
func (s State) searchFingerprint(kind string, q MessageSearchRequest) string {
	if kind == "groups" {
		return hash([]any{kind, q, s.Binding, s.Config.Groups.MessageMode, s.Config.Groups.AtMe, s.Config.Groups.AtAll})
	}
	return hash([]any{kind, q, s.Binding})
}
func (s State) searchStart(q MessageSearchRequest) int64 {
	start := s.Baseline.Unix()
	if len(q.ChatIDs) == 0 {
		return start // unknown conversations must start at the locked baseline
	}
	first := true
	for _, id := range q.ChatIDs {
		p := s.Chats[id]
		floor := max(s.Baseline.Unix(), p.Completed-searchOverlap)
		if p.Scan != nil {
			floor = min(floor, p.Scan.Start)
		}
		if first || floor < start {
			start = floor
		}
		first = false
	}
	return start
}
func (s *State) reconfigureSearch() {
	old := s.Search
	next := &SearchProgress{Branches: []SearchBranch{}, Metrics: old.Metrics}
	for _, request := range s.searchRequests() {
		b := SearchBranch{Kind: request.kind, Fingerprint: s.searchFingerprint(request.kind, request.q)}
		for _, saved := range old.Branches {
			if saved.Kind == b.Kind && saved.Fingerprint == b.Fingerprint {
				b = saved
				b.ErrorCode, b.Failures, b.RetryAt = "", 0, time.Time{}
				break
			}
		}
		next.Branches = append(next.Branches, b)
	}
	if len(next.Branches) == len(old.Branches) {
		same := true
		for i, b := range next.Branches {
			if b.Kind != old.Branches[i].Kind || b.Fingerprint != old.Branches[i].Fingerprint {
				same = false
			}
		}
		if same {
			next.Next = old.Next
		}
	}
	s.Search = next
	s.Directory = nil
}

// ConfigureSearch uses the same binding, baseline and frozen-route lifecycle.
// Callers explicitly compose the search adapter; state never selects a codec.
func ConfigureSearch(owner platform.RoomIdentity, current *State, binding Binding, cfg Config, now time.Time) (State, error) {
	if cfg.Groups.Scope == "" {
		cfg.Groups.Scope = "specified"
	}
	if current != nil && current.Search == nil {
		return State{}, notification.ErrConflict // use the explicit upgrader
	}
	n, err := configure(owner, current, binding, cfg, now)
	if err != nil {
		return n, err
	}
	if n.Search == nil {
		n.Search = &SearchProgress{Branches: []SearchBranch{}}
		n.reconfigureSearch()
	}
	n.updateFull(now)
	return n, n.Validate(owner)
}

func (s State) validateSearch() error {
	requests := s.searchRequests()
	if s.Search.Branches == nil || len(s.Search.Branches) != len(requests) || s.Search.Next < 0 || s.Search.Next >= max(1, len(requests)) || s.Directory != nil {
		return notification.ErrInvalid
	}
	for i, b := range s.Search.Branches {
		if b.Kind != requests[i].kind || b.Fingerprint != s.searchFingerprint(b.Kind, requests[i].q) || b.Completed < 0 || b.Completed > 0 && b.Completed < s.Baseline.Unix() || b.Failures < 0 || b.Failures > 30 || !componentOrEmpty(b.ErrorCode) {
			return notification.ErrInvalid
		}
		if b.Scan == nil {
			continue
		}
		scan := b.Scan
		if s.Baseline.IsZero() || scan.Start < s.Baseline.Unix() || scan.End <= scan.Start || scan.End-scan.Start > searchWindow || scan.Tokens == nil || !validPageToken(scan.Token) {
			return notification.ErrInvalid
		}
		for digest, seen := range scan.Tokens {
			if len(digest) != 64 || !seen || strings.Trim(digest, "0123456789abcdef") != "" {
				return notification.ErrInvalid
			}
		}
		if scan.Token != "" && !scan.Tokens[hash(scan.Token)] {
			return notification.ErrInvalid
		}
		p := scan.Page
		if p == nil {
			continue
		}
		if p.Items == nil || len(p.Items) > 50 || p.Done < 0 || p.Done > len(p.Items) || p.Details == nil || !validPageToken(p.NextToken) || !validPageToken(p.MemberToken) || p.HasMore && (p.NextToken == "" || p.NextToken == scan.Token || scan.Tokens[hash(p.NextToken)]) {
			return notification.ErrInvalid
		}
		seen := map[string]bool{}
		remaining := map[string]MessageSearchItem{}
		q := requests[i].q
		q.Start, q.End = time.Unix(scan.Start, 0), time.Unix(scan.End, 0)
		for j, item := range p.Items {
			if !validSearchCandidate(item, q) || seen[item.ID] {
				return notification.ErrInvalid
			}
			seen[item.ID] = true
			if j >= p.Done {
				remaining[item.ID] = item
			}
		}
		for id, m := range p.Details {
			item, ok := remaining[id]
			if !ok || id != m.ID || !searchDetailMatches(item, m, q) {
				return notification.ErrInvalid
			}
		}
	}
	return nil
}
func componentOrEmpty(s string) bool { return s == "" || component(s) }

func validSearchCandidate(item MessageSearchItem, q MessageSearchRequest) bool {
	m := item.MetaData
	t, err := time.Parse(time.RFC3339Nano, m.Created)
	return nativeID(item.ID, "om_") && item.ID == m.MessageID && nativeID(m.ChatID, "oc_") && nativeID(m.FromID, "ou_") && m.IsP2PChat != nil && *m.IsP2PChat == (q.ChatType == "p2p") && err == nil && !t.Before(q.Start) && !t.After(q.End) && (len(q.ChatIDs) == 0 || slices.Contains(q.ChatIDs, m.ChatID)) && (q.FromID == "" || m.FromID == q.FromID)
}
func searchDetailMatches(item MessageSearchItem, m Message, q MessageSearchRequest) bool {
	ms, err := strconv.ParseInt(m.Created, 10, 64)
	t, e := time.Parse(time.RFC3339Nano, item.MetaData.Created)
	// Native search metadata can omit fractional seconds, while mget retains
	// milliseconds. Compare at the precision actually supplied by discovery;
	// the runner checks the exact window again before admission. The native
	// search can include the entire upper-bound second, requiring deferral.
	secondsOnly := !strings.ContainsAny(item.MetaData.Created, ".,")
	sameCreated := t.UnixMilli() == ms || secondsOnly && t.Unix() == ms/1000
	withinEnd := !time.UnixMilli(ms).After(q.End) || secondsOnly && t.Equal(q.End)
	// Native app messages use the bot's open ID in search and its application
	// ID in mget. These namespaces cannot be compared as user IDs. The exact
	// message/chat/time locator still has to match; user senders stay exact.
	senderMatches := m.Sender.ID == item.MetaData.FromID && m.Sender.IDType == "open_id" || m.Sender.Type == "app" && m.Sender.IDType == "app_id" && nativeID(m.Sender.ID, "cli_")
	return m.valid() && item.ID == m.ID && item.MetaData.ChatID == m.ChatID && err == nil && e == nil && sameCreated && !time.UnixMilli(ms).Before(q.Start) && withinEnd && (m.Type == "system" || senderMatches)
}

// UpgradeSearchState is pure and deterministic. The existing offline transaction
// boundary supplies backups, journal, locking and recovery before writing this.
// Schema 3 is the only direct predecessor; older formats first use their own
// validated offline conversion. Unknown fields/versions never get rewritten.
func UpgradeSearchState(raw []byte, schema int, owner platform.RoomIdentity) ([]byte, error) {
	if schema == SearchSchema {
		if _, err := (&SearchSource{}).Decode(raw, owner); err != nil {
			return nil, err
		}
		return slices.Clone(raw), nil
	}
	if schema != 3 {
		return nil, notification.ErrInvalid
	}
	decoded, err := (&Source{}).Decode(raw, owner)
	if err != nil {
		return nil, err
	}
	s := decoded.(State)
	s.Config.Groups.Scope = "specified"
	s.Search = &SearchProgress{Branches: []SearchBranch{}}
	s.reconfigureSearch()
	// A well-formed legacy worker only pruned below Completed-overlap (or
	// an earlier active scan). Reject ambiguous snapshots instead of replaying
	// an interval whose delivery evidence might already have been removed.
	for id, p := range s.Chats {
		if p.Chat.Mode != "p2p" {
			p.NeedsVerification, p.VerifiedAt = true, time.Time{}
			s.Chats[id] = p
		}
		if p.Completed > 0 && p.Scan != nil && p.Scan.Start < p.Completed-searchOverlap {
			return nil, notification.ErrConflict
		}
	}
	for _, o := range s.Observed {
		if _, ok := s.Chats[o.Message.ChatID]; !ok {
			return nil, notification.ErrConflict
		}
	}
	s.updateFull(time.Time{})
	if s.Validate(owner) != nil {
		return nil, notification.ErrInvalid
	}
	return json.Marshal(s)
}
