package frozen

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const Contract = "inbox_streams/v2"
const DiscoverySchema = `{"within":{"type":"string"},"filter":{"type":"object"},"kind":{"enum":["room","notification"]},"query":{"type":"string"},"state":{"enum":["readable","blocked","all"]},"attention":{"enum":["high","normal","low"]},"sort":{"enum":["attention","oldest"]},"limit":{"type":"integer","minimum":1,"maximum":20},"cursor":{"type":"string"}}`

// Observation is a fixed, caller-validated view of authoritative consumption.
// Catalog never persists it; the member reader supplies authoritative facts.
type Observation struct {
	Bounds      map[string]Snapshot
	ObservedAt  time.Time
	Excluded    map[Location]bool
	Blocked     map[Location]string
	Preferences Preferences
}
type Preferences struct {
	LastSource  string
	Version     uint64
	Attention   map[string]string
	LastExposed map[string]uint64
}
type Query struct {
	Within    string          `json:"within,omitempty"`
	Filter    json.RawMessage `json:"filter,omitempty"`
	Kind      string          `json:"kind,omitempty"`
	Text      string          `json:"query,omitempty"`
	State     string          `json:"state,omitempty"`
	Attention string          `json:"attention,omitempty"`
	Sort      string          `json:"sort,omitempty"`
	Limit     int             `json:"limit,omitempty"`
	Cursor    string          `json:"cursor,omitempty"`
}
type StatsQuery struct {
	Within string          `json:"within,omitempty"`
	Filter json.RawMessage `json:"filter,omitempty"`
	Kind   string          `json:"kind,omitempty"`
	Limit  int             `json:"limit,omitempty"`
	Cursor string          `json:"cursor,omitempty"`
}
type StreamSummary struct {
	Hint        string `json:"hint,omitempty"`
	Ref         string `json:"stream_ref"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	SourceLabel string `json:"source_label,omitempty"`
	Pending     int    `json:"pending_entries"`
	Attention   string `json:"attention,omitempty"`
	Blocked     string `json:"blocked_reason,omitempty"`
}
type List struct {
	Guidance *Guidance       `json:"guidance,omitempty"`
	ScopeRef string          `json:"scope_ref,omitempty"`
	Matched  int             `json:"matched_streams"`
	Streams  []StreamSummary `json:"streams"`
	Next     string          `json:"next_cursor,omitempty"`
	Notice   string          `json:"notice,omitempty"`
	Stop     string          `json:"stop_reason,omitempty"`
}
type SourceSummary struct {
	ScopeRef string `json:"scope_ref"`
	Label    string `json:"source_label"`
	Pending  int    `json:"pending_streams"`
	Entries  int    `json:"pending_entries"`
	Blocked  int    `json:"blocked_streams,omitempty"`
}
type Stats struct {
	Guidance   *Guidance       `json:"guidance,omitempty"`
	ObservedAt time.Time       `json:"observed_at"`
	ScopeRef   string          `json:"scope_ref,omitempty"`
	Pending    int             `json:"pending_streams"`
	Entries    int             `json:"pending_entries"`
	Blocked    int             `json:"blocked_streams,omitempty"`
	Sources    []SourceSummary `json:"sources"`
	Next       string          `json:"next_cursor,omitempty"`
	Notice     string          `json:"notice,omitempty"`
	Stop       string          `json:"stop_reason,omitempty"`
}
type Preview struct {
	Blocked string   `json:"blocked_reason,omitempty"`
	Stop    string   `json:"stop_reason,omitempty"`
	Ref     string   `json:"stream_ref"`
	Entries []string `json:"entries"`
	Notice  string   `json:"notice,omitempty"`
}
type catalogEntry struct {
	searchIncomplete                      bool
	labelTruncated                        bool
	fallback                              bool
	location                              Location
	stream, source, search, text, blocked string
	scopes                                []string
	at                                    time.Time
	incomplete                            bool
}
type catalogScope struct {
	ref, key, source, provider, label string
	stream                            bool
}
type catalogProvider struct {
	snapshot Snapshot
	native   NativeIndex
	version  int
	codec    Codec
}
type catalogPage struct {
	ends          []int
	query         string
	purpose       string
	list          List
	stats         Stats
	offset, limit int
}

// Catalog is a frozen member/turn view. Snapshot pages never change after new
// append, consumption or preference edits. Query paths do no disk I/O or decode.
type Catalog struct {
	directory                    *InputDirectory
	exposure                     Exposure
	observedAt                   time.Time
	queries                      []string
	queryNumber                  uint64
	mu                           sync.Mutex
	member                       Member
	turn                         uint64
	providers                    map[string]catalogProvider
	scopes                       map[string]catalogScope
	entries                      []catalogEntry
	streams                      map[string]StreamSummary
	streamEntries                map[string][]int
	preferences                  Preferences
	pages                        map[string]catalogPage
	nonce                        string
	discoveryBytes, previewBytes int
}

func normalizeSearch(s string) string {
	if len(s) > MaxSearchBytes {
		s = s[:MaxSearchBytes]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.ToLower(s)
}
func mergeScopes(saved []scopeMetadata, provider, channel string, p Projection, sequence ...uint64) []scopeMetadata {
	source := p.Group.SourceKey(provider, channel)
	all := append([]Scope{{Key: source, Label: p.SourceLabel}}, p.Scopes...)
	first := uint64(0)
	if len(sequence) > 0 {
		first = sequence[0]
	}
	for _, scope := range all {
		found := false
		for i := range saved {
			if saved[i].Key == scope.Key && saved[i].Source == source {
				saved[i].Label = scope.Label
				found = true
				break
			}
		}
		if !found {
			saved = append(saved, scopeMetadata{Key: scope.Key, Source: source, Label: scope.Label, First: first})
		}
	}
	return saved
}
func scopeRef(m Member, provider, source, key string) string {
	return Ref("scope_", m, provider, source, key)
}
func (r *Repository) Catalog(turn uint64, o Observation) (*Catalog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	if turn == 0 {
		return nil, ErrInvalid
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	c := &Catalog{member: r.member, turn: turn, providers: map[string]catalogProvider{}, scopes: map[string]catalogScope{}, streams: map[string]StreamSummary{}, streamEntries: map[string][]int{}, preferences: o.Preferences, pages: map[string]catalogPage{}, nonce: hex.EncodeToString(nonce[:])}
	c.observedAt = o.ObservedAt
	if c.observedAt.IsZero() {
		c.observedAt = time.Now()
	}
	c.preferences.Attention = cloneMap(o.Preferences.Attention)
	c.preferences.LastExposed = cloneMap(o.Preferences.LastExposed)
	known := map[Location]bool{}
	for _, part := range r.parts {
		for _, rec := range part.records {
			known[rec.Location] = true
		}
	}
	for provider, p := range r.parts {
		bound := snapshot(p)
		if o.Bounds != nil {
			var ok bool
			bound, ok = o.Bounds[provider]
			if !ok {
				continue
			}
		}

		// Freeze private facts along with common positions and consumption.
		var native NativeIndex
		if p.native != nil {
			native = p.native.Clone()
		}
		c.providers[provider] = catalogProvider{bound, native, 1, r.codecs[provider]}
		for _, scope := range p.state.Scopes {
			if scope.First > bound.Through {
				continue
			}
			ref := scopeRef(r.member, provider, scope.Source, scope.Key)
			c.scopes[ref] = catalogScope{ref, scope.Key, scope.Source, provider, scope.Label, false}
		}
		for _, seq := range sortedSeqs(p) {
			rec := p.records[seq]
			if seq > bound.Through {
				continue
			}
			known[rec.Location] = true
			knownProjection := p.projections[seq]
			knownSource := rec.Group.SourceKey(provider, rec.Channel)
			knownRef := rec.Group.StreamRef(r.member, provider, rec.Channel)
			c.scopes[knownRef] = catalogScope{knownRef, Ref("context_", rec.Group.Context), knownSource, provider, knownProjection.Label, true}
			if o.Excluded[rec.Location] {
				continue
			}
			projection := p.projections[seq]
			source := rec.Group.SourceKey(provider, rec.Channel)
			sourceRef := scopeRef(r.member, provider, source, source)
			ref := rec.Group.StreamRef(r.member, provider, rec.Channel)
			c.scopes[ref] = catalogScope{ref, Ref("context_", rec.Group.Context), source, provider, projection.Label, true}
			attention := c.preferences.Attention[sourceRef]
			if attention == "" {
				attention = "normal"
			}
			if attention != "high" && attention != "normal" && attention != "low" {
				return nil, ErrInvalid
			}
			scopes := []string{sourceRef, ref}
			for _, scope := range projection.Scopes {
				scopes = append(scopes, scopeRef(r.member, provider, source, scope.Key))
			}
			blocked := o.Blocked[rec.Location]
			if blocked == "" {
				blocked = projection.Blocked
			}
			entry := catalogEntry{searchIncomplete: len(projection.Label+" "+projection.SourceLabel+" "+projection.Text) > MaxSearchBytes, labelTruncated: len(projection.Label) > 200 || len(projection.SourceLabel) > 200, location: rec.Location, stream: ref, source: sourceRef, search: p.search[seq], text: projection.Text, blocked: blocked, scopes: scopes, at: rec.ReceivedAt, incomplete: projection.Incomplete, fallback: rec.Group.Precision == "source"}
			c.streamEntries[ref] = append(c.streamEntries[ref], len(c.entries))
			c.entries = append(c.entries, entry)
			summary := c.streams[ref]
			summary.Ref = ref
			summary.Kind = "notification"
			summary.Label, _ = boundedText(projection.Label, 200)
			summary.SourceLabel, _ = boundedText(projection.SourceLabel, 200)
			summary.Pending++
			summary.Attention = attention
			if blocked != "" {
				summary.Blocked = blocked
			}
			c.streams[ref] = summary
		}
	}
	for ref, indices := range c.streamEntries {
		summary := c.streams[ref]
		summary.Hint = c.hint(indices)
		c.streams[ref] = summary
	}
	for l := range o.Excluded {
		if !known[l] {
			return nil, ErrIdentity
		}
	}
	for l := range o.Blocked {
		if !known[l] {
			return nil, ErrIdentity
		}
	}
	return c, nil
}
func hasFilter(b json.RawMessage) bool {
	var fields map[string]json.RawMessage
	return len(b) > 0 && json.Unmarshal(b, &fields) == nil && len(fields) > 0
}
func (c *Catalog) options() []ScopeOption {
	out := []ScopeOption{}
	for ref, s := range c.scopes {
		if !s.stream && s.key == s.source {
			out = append(out, ScopeOption{ref, s.label})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}
func (c *Catalog) resolve(q Query) ([]string, string, string, error) {
	if q.Kind != "" && q.Kind != "notification" && q.Kind != "room" || q.State != "" && q.State != "readable" && q.State != "all" && q.State != "blocked" || q.Sort != "" && q.Sort != "attention" && q.Sort != "oldest" || q.Attention != "" && q.Attention != "high" && q.Attention != "normal" && q.Attention != "low" || len(q.Text) > 4096 {
		return nil, "", "", ErrInvalid
	}
	if err := ValidateFilter(q.Filter); err != nil {
		return nil, "", "", err
	}
	var bound catalogScope
	if q.Within != "" {
		var ok bool
		bound, ok = c.scopes[q.Within]
		if !ok {
			return nil, "", "", ErrIdentity
		}
	}
	if hasFilter(q.Filter) && q.Within == "" {
		return nil, "", "", &ScopeError{Reason: ErrScopeRequired, Options: c.options()}
	}
	allowed := map[Location]bool{}
	locations := []Location{}
	for _, e := range c.entries {
		if q.Within != "" && !contains(e.scopes, q.Within) {
			continue
		}
		allowed[e.location] = true
		locations = append(locations, e.location)
	}
	var native map[Location]bool
	notice := ""
	resolved := ""
	if hasFilter(q.Filter) {
		p := c.providers[bound.provider]
		if p.native == nil {
			return nil, "", "", ErrUnsupported
		}
		selection, err := p.native.CompileInputFilter(FilterContext{bound.key, p.snapshot, locations, p.version}, q.Filter)
		if err != nil {
			var scopeErr *ScopeError
			if errors.As(err, &scopeErr) && len(scopeErr.Native) > 0 {
				converted := &ScopeError{Reason: scopeErr.Reason}
				for _, choice := range scopeErr.Native {
					for ref, s := range c.scopes {
						if s.provider == bound.provider && s.source == bound.source && s.key == choice.Key {
							label, _ := boundedText(choice.Label, 200)
							converted.Options = append(converted.Options, ScopeOption{ref, label})
						}
					}
				}
				sort.Slice(converted.Options, func(i, j int) bool { return converted.Options[i].Ref < converted.Options[j].Ref })
				if len(converted.Options) > 5 {
					converted.Options = converted.Options[:5]
				}
				return nil, "", "", converted
			}
			return nil, "", "", err
		}
		native = map[Location]bool{}
		for _, l := range selection.Entries {
			if !allowed[l] || l.Partition != p.snapshot.Partition || l.Seq > p.snapshot.Through {
				return nil, "", "", ErrIdentity
			}
			native[l] = true
		}
		if selection.Missing > 0 {
			notice = "Some saved entries lack native facts; broaden the filter to inspect fallback streams."
		}
		if selection.ResolvedScope != "" {
			ref := scopeRef(c.member, bound.provider, bound.source, selection.ResolvedScope)
			s, ok := c.scopes[ref]
			if !ok || s.source != bound.source {
				return nil, "", "", ErrIdentity
			}
			if q.Within != ref && bound.key != bound.source {
				for _, entry := range c.entries {
					if contains(entry.scopes, ref) && !contains(entry.scopes, q.Within) {
						return nil, "", "", ErrIdentity
					}
				}
			}

			resolved = ref
		}
	}
	words := strings.Fields(strings.ToLower(q.Text))
	if len(words) > 0 {
		for _, e := range c.entries {
			if allowed[e.location] && e.searchIncomplete {
				if notice != "" {
					notice += " "
				}
				notice += "Search covers bounded saved text; inspect the stream for full retained context."
				break
			}
		}
	}
	matches := map[string]bool{}
	for _, e := range c.entries {
		if !allowed[e.location] || native != nil && !native[e.location] {
			continue
		}
		summary := c.streams[e.stream]
		if q.Kind != "" && q.Kind != summary.Kind || q.Attention != "" && q.Attention != summary.Attention {
			continue
		}
		if q.State == "blocked" && summary.Blocked == "" || (q.State == "" || q.State == "readable") && summary.Blocked != "" {
			continue
		}
		ok := true
		for _, word := range words {
			if !strings.Contains(e.search, word) {
				ok = false
				break
			}
		}
		if ok {
			matches[e.stream] = true
		}
	}
	refs := make([]string, 0, len(matches))
	for ref := range matches {
		refs = append(refs, ref)
	}
	rank := func(s string) int {
		switch s {
		case "high":
			return 0
		case "low":
			return 2
		}
		return 1
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := c.streams[refs[i]], c.streams[refs[j]]
		if q.Sort != "oldest" && rank(a.Attention) != rank(b.Attention) {
			return rank(a.Attention) < rank(b.Attention)
		}
		ae, be := c.entries[c.streamEntries[a.Ref][0]], c.entries[c.streamEntries[b.Ref][0]]
		if !ae.at.Equal(be.at) {
			return ae.at.Before(be.at)
		}
		return a.Ref < b.Ref
	})
	if resolved == "" && q.Within == "" {
		source := ""
		for _, ref := range refs {
			s := c.entries[c.streamEntries[ref][0]].source
			if source != "" && source != s {
				source = ""
				break
			}
			source = s
		}
		resolved = source
	}
	if len(refs) == 0 && notice == "" {
		if len(locations) == 0 {
			notice = "The saved scope has no pending input."
		} else {
			notice = "No saved entries match these conditions; this says nothing about remote history."
		}
	}
	if resolved == q.Within {
		resolved = ""
	}
	return refs, resolved, notice, nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func validLimit(n, def int) (int, error) {
	if n == 0 {
		return def, nil
	}
	if n < 1 || n > 20 {
		return 0, ErrInvalid
	}
	return n, nil
}
func (c *Catalog) newCursor(p catalogPage) string {
	if p.query == "" {
		c.queryNumber++
		p.query = Ref("query_", c.nonce, c.queryNumber)
		c.queries = append(c.queries, p.query)
		if len(c.queries) > 4 {
			old := c.queries[0]
			c.queries = c.queries[1:]
			for token, previous := range c.pages {
				if previous.query == old {
					delete(c.pages, token)
				}
			}
		}
	}
	if len(c.pages) >= 512 {
		return ""
	}
	ref := Ref("catalog_", c.nonce, c.turn, p.query, p.offset, p.purpose)
	c.pages[ref] = p
	return ref
}
func (c *Catalog) List(q Query) (List, error) { c.mu.Lock(); defer c.mu.Unlock(); return c.list(q) }
func (c *Catalog) list(q Query) (List, error) {
	limit, err := validLimit(q.Limit, 10)
	if err != nil {
		return List{}, err
	}
	var p catalogPage
	if q.Cursor != "" {
		if q.Within != "" || len(q.Filter) > 0 || q.Text != "" || q.Kind != "" || q.State != "" || q.Sort != "" || q.Attention != "" || q.Limit != 0 {
			return List{}, ErrConflict
		}
		var ok bool
		p, ok = c.pages[q.Cursor]
		if !ok || p.purpose != "list" {
			return List{}, ErrExpired
		}
	} else {
		refs, scope, notice, err := c.resolve(q)
		if err != nil {
			return List{}, c.queryError(q.Within, q.Filter, err)
		}
		unlimited := q.Within != "" || q.Sort == "oldest"

		refs, ends := c.orderPages(refs, limit, unlimited)
		all := List{ScopeRef: scope, Matched: len(refs), Notice: notice, Streams: []StreamSummary{}}
		for _, ref := range refs {
			summary := c.streams[ref]
			if summary.Attention == "normal" {
				summary.Attention = ""
			}
			all.Streams = append(all.Streams, summary)
		}
		if q.Within != "" && !hasFilter(q.Filter) {
			all.Guidance = c.guidance(q.Within)
		}
		p = catalogPage{purpose: "list", list: all, limit: limit, ends: ends}
	}
	out := p.list
	if q.Cursor != "" {
		out.Guidance = nil
	}
	out.Streams = []StreamSummary{}
	out.Next = ""
	if c.discoveryBytes >= 128<<10 {
		out.Stop = "discovery_budget_exhausted"
		return out, nil
	}
	end := p.offset
	pageEnd := len(p.list.Streams)
	for _, boundary := range p.ends {
		if boundary > p.offset {
			pageEnd = boundary
			break
		}
	}
	for end < pageEnd && end-p.offset < p.limit {
		candidate := append(out.Streams, p.list.Streams[end])
		out.Streams = candidate
		b, _ := json.Marshal(out)
		if len(b) > 15<<10 {
			out.Streams = out.Streams[:len(out.Streams)-1]
			break
		}
		end++
	}
	if end < len(p.list.Streams) {
		p.offset = end
		out.Next = c.newCursor(p)
		if out.Next == "" {
			return List{}, ErrCapacity
		}
	}
	b, _ := json.Marshal(out)
	if c.discoveryBytes+len(b) > 128<<10 {
		return List{ScopeRef: out.ScopeRef, Matched: out.Matched, Streams: []StreamSummary{}, Stop: "discovery_budget_exhausted"}, nil
	}
	c.discoveryBytes += len(b)
	return out, nil
}
func (c *Catalog) Stats(q StatsQuery) (Stats, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	limit, err := validLimit(q.Limit, 20)
	if err != nil {
		return Stats{}, err
	}
	var p catalogPage
	if q.Cursor != "" {
		if q.Within != "" || len(q.Filter) > 0 || q.Kind != "" || q.Limit != 0 {
			return Stats{}, ErrConflict
		}
		var ok bool
		p, ok = c.pages[q.Cursor]
		if !ok || p.purpose != "stats" {
			return Stats{}, ErrExpired
		}
	} else {
		refs, scope, notice, err := c.resolve(Query{Within: q.Within, Filter: q.Filter, Kind: q.Kind, State: "all"})
		if err != nil {
			return Stats{}, c.queryError(q.Within, q.Filter, err)
		}
		all := Stats{ObservedAt: c.observedAt, ScopeRef: scope, Pending: len(refs), Notice: notice, Sources: []SourceSummary{}}
		sources := map[string]SourceSummary{}
		for _, ref := range refs {
			s := c.streams[ref]
			source := c.entries[c.streamEntries[ref][0]].source
			sum := sources[source]
			sum.ScopeRef = source
			sum.Label = s.SourceLabel
			sum.Pending++
			sum.Entries += s.Pending
			all.Entries += s.Pending
			if s.Blocked != "" {
				sum.Blocked++
				all.Blocked++
			}
			sources[source] = sum
		}
		for _, s := range sources {
			all.Sources = append(all.Sources, s)
		}
		sort.Slice(all.Sources, func(i, j int) bool { return all.Sources[i].ScopeRef < all.Sources[j].ScopeRef })
		if q.Within != "" && !hasFilter(q.Filter) {
			all.Guidance = c.guidance(q.Within)
		}
		p = catalogPage{purpose: "stats", stats: all, limit: limit}
	}
	out := p.stats
	if q.Cursor != "" {
		out.Guidance = nil
	}
	out.Sources = []SourceSummary{}
	out.Next = ""
	if c.discoveryBytes >= 128<<10 {
		out.Stop = "discovery_budget_exhausted"
		return out, nil
	}
	end := p.offset
	for end < len(p.stats.Sources) && end-p.offset < p.limit {
		out.Sources = append(out.Sources, p.stats.Sources[end])
		b, _ := json.Marshal(out)
		if len(b) > 7<<10 {
			out.Sources = out.Sources[:len(out.Sources)-1]
			break
		}
		end++
	}
	if end < len(p.stats.Sources) {
		p.offset = end
		out.Next = c.newCursor(p)
		if out.Next == "" {
			return Stats{}, ErrCapacity
		}
	}
	b, _ := json.Marshal(out)
	if c.discoveryBytes+len(b) > 128<<10 {
		return Stats{ObservedAt: c.observedAt, ScopeRef: out.ScopeRef, Pending: out.Pending, Entries: out.Entries, Blocked: out.Blocked, Sources: []SourceSummary{}, Stop: "discovery_budget_exhausted"}, nil
	}
	c.discoveryBytes += len(b)
	return out, nil
}
func (c *Catalog) Preview(ref string, limit int) (Preview, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit == 0 {
		limit = 1
	}
	if limit < 1 || limit > 3 {
		return Preview{}, ErrInvalid
	}
	indices, ok := c.streamEntries[ref]
	if !ok {
		if scope, known := c.scopes[ref]; known && scope.stream {
			return Preview{Ref: ref, Entries: []string{}, Stop: "stream_empty", Notice: "No pending saved input; use saved history for delivered evidence."}, nil
		}
		return Preview{}, ErrIdentity
	}
	out := Preview{Ref: ref, Entries: []string{}}
	if c.streams[ref].Blocked != "" {
		out.Blocked = c.streams[ref].Blocked
		out.Notice = "This saved stream is blocked; no preview body supplied."
		return out, nil
	}
	for _, idx := range indices {
		e := c.entries[idx]
		text := e.text
		if len(text) > 1800 {
			text = text[:1800]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
			out.Notice = "Saved preview truncated; formal reading supplies context."
		}
		out.Entries = append(out.Entries, text)
		for {
			encoded, _ := json.Marshal(out)
			if len(encoded) <= PreviewBytes {
				break
			}
			if len(text) == 0 {
				out.Entries = out.Entries[:len(out.Entries)-1]
				break
			}
			text, _ = boundedText(text, len(text)/2)
			out.Entries[len(out.Entries)-1] = text
			out.Notice = "Saved preview truncated; formal reading supplies context."
		}
		if e.incomplete {
			out.Notice = "Saved coverage is incomplete; formally read or inspect saved source history."
		}
		if len(out.Entries) == limit {
			break
		}
	}
	b, _ := json.Marshal(out)
	if len(b) > 8<<10 || c.previewBytes+len(b) > 32<<10 {
		return Preview{Ref: ref, Entries: []string{}, Stop: "preview_budget_exhausted"}, nil
	}
	c.previewBytes += len(b)
	return out, nil
}
func (c *Catalog) DescribeInput(within string) (Guidance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	scope, ok := c.scopes[within]
	if !ok {
		return Guidance{}, ErrIdentity
	}
	codec := c.providers[scope.provider].codec
	if codec == nil {
		return Guidance{}, ErrUnsupported
	}
	g := codec.DescribeInput()
	b, _ := json.Marshal(g)
	if g.Version == "" || g.Text == "" {
		return Guidance{}, ErrUnsupported
	}
	if len(b) > 1024 {
		return Guidance{}, ErrCapacity
	}
	return g, nil
}

func (c *Catalog) guidance(within string) *Guidance {
	scope, ok := c.scopes[within]
	if !ok {
		return nil
	}
	codec := c.providers[scope.provider].codec
	if codec == nil {
		return nil
	}
	g := codec.DescribeInput()
	b, _ := json.Marshal(g)
	if g.Text == "" || len(b) > GuidanceBytes {
		return nil
	}
	return &g
}

func (c *Catalog) queryError(within string, filter json.RawMessage, err error) error {
	var scope *ScopeError
	if errors.As(err, &scope) {
		return err
	}
	if within != "" && hasFilter(filter) {
		if g := c.guidance(within); g != nil {
			return &GuidanceError{Reason: err, Guidance: *g}
		}
	}
	return err
}
