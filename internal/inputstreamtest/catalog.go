package inputstreamtest

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Descriptor is projected from the existing notification capability assembly.
// Family declares supported native facts; no provider names occur in queries.
type Descriptor struct {
	Provider, Family string
	Channels         []string
	Codec            string
	Schema           int
	Policy           string
}
type Record struct {
	Location   Location
	Group      Group
	ReceivedAt time.Time
	Pending    bool
	Blocked    string
	Excerpt    string
	Incomplete bool
}
type Preferences struct {
	Version     uint64
	Attention   map[string]string // stable source key -> high/normal/low
	LastSource  string
	LastExposed map[string]uint64
}
type Exposure struct {
	Turn       uint64
	Sources    []string
	LastSource string
}
type indexedStream struct {
	summary StreamSummary
	group   Group
	source  string
	account string
	records []Record
	oldest  time.Time
}
type catalogPage struct {
	scopeRef                        string
	kind                            string
	query                           Query
	streams                         []StreamSummary
	sources                         []SourceSummary
	ends                            []int
	offset, total, entries, blocked int
	notice                          string
	at                              time.Time
	find                            *findSnapshot
}

// Catalog snapshots are immutable and bounded. Rebuild swaps only the derived
// index, leaving old cursors fixed. The host serializes exposure persistence.
type Catalog struct {
	mu                           sync.Mutex
	member                       Member
	descriptors                  []Descriptor
	streams                      []*indexedStream
	byRef                        map[string]*indexedStream
	prefs                        Preferences
	turn                         uint64
	pages                        map[string]catalogPage
	queries                      int
	discoveryBytes, previewBytes int
	directory                    *InputDirectory
	exposure                     Exposure
	now                          func() time.Time
	visible                      map[string]uint64
	accounts                     map[string]Group
	scopes                       []Group
	knownRefs                    map[string]bool
	ranges                       map[string]savedRange
}

func NewCatalog(m Member, turn uint64, descriptors []Descriptor, records []Record, prefs Preferences) (*Catalog, error) {
	if !m.Valid() || turn == 0 {
		return nil, ErrIdentity
	}
	c := &Catalog{member: m, turn: turn, descriptors: slices.Clone(descriptors), pages: map[string]catalogPage{}, now: time.Now}
	c.visible = map[string]uint64{}
	c.accounts = map[string]Group{}
	c.knownRefs = map[string]bool{}
	c.ranges = map[string]savedRange{}
	scopeSeen := map[string]bool{}
	for _, r := range records {
		ref, err := r.Group.StreamRef(m)
		if err != nil {
			return nil, err
		}
		c.knownRefs[ref] = true
		c.ranges[ref] = savedRange{group: r.Group, kind: "stream", ref: ref}
		if account := r.Group.AccountRef(m); account != "" {
			c.ranges[account] = savedRange{group: r.Group, kind: "account", ref: account}
		}
		if project := r.Group.ProjectScopeRef(m); project != "" {
			c.ranges[project] = savedRange{group: r.Group, kind: "project", ref: project}
		}

		c.visible[r.Location.Partition] = max(c.visible[r.Location.Partition], r.Location.Seq)
		if ref := r.Group.AccountRef(m); ref != "" {
			c.accounts[ref] = r.Group
			g := r.Group
			scope := ref + "/" + g.Facts.ProjectID
			if g.Validate() == nil && g.Facts.ProjectID != "" && !scopeSeen[scope] {
				g.Facts.Summary = ""
				c.scopes = append(c.scopes, g)
				scopeSeen[scope] = true
			}
		}
	}
	if err := c.rebuild(records, prefs); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Catalog) rebuild(records []Record, prefs Preferences) error {
	count := 0
	roomCount := 0
	index := map[string]*indexedStream{}
	locations := map[Location]bool{}
	for _, r := range records {
		if bound, exists := c.visible[r.Location.Partition]; !exists || r.Location.Seq > bound {
			continue
		}
		if r.Group.Kind != "room" {
			count++
		} else {
			roomCount++
		}
		if roomCount > MaxRecords {
			return ErrCapacity
		}
		if count > MaxRecords {
			return ErrCapacity
		}
		if r.Location.Partition == "" || r.Location.Seq == 0 || r.ReceivedAt.IsZero() || locations[r.Location] {
			return ErrInvalid
		}
		locations[r.Location] = true
		if !r.Pending {
			continue
		}
		ref, err := r.Group.StreamRef(c.member)
		if err != nil {
			return err
		}
		if r.Group.Kind != "room" {
			found := false
			for _, d := range c.descriptors {
				if d.Provider == r.Group.Provider && slices.Contains(d.Channels, r.Group.Channel) {
					found = true
				}
			}
			if !found {
				return ErrUnsupported
			}
		}
		s := index[ref]
		if s == nil {
			g := r.Group
			source := g.SourceKey(c.member)
			label, truncated := bounded(g.Facts.Label, 256)
			if label == "" {
				label = "未命名上下文"
				truncated = true
			}
			sourceLabel := g.Facts.SourceLabel
			if g.Kind != "room" {
				// Always retain canonical host/account discrimination even for aliases.
				accountLabel := g.Facts.AccountLabel
				if accountLabel == "" {
					accountLabel = source[len(source)-12:]
				}
				sourceLabel = sourceLabel + " [" + g.Instance + " / " + accountLabel + "]"
			}
			sourceLabel, sourceTruncated := bounded(sourceLabel, 320)
			if sourceTruncated {
				sourceLabel += " [" + source[len(source)-16:] + "]"
			}
			hint := ""
			if truncated || sourceTruncated {
				hint = "标签已截断或缺失；正式读取核对来源"
			}
			if g.Precision == "source" {
				hint = "来源级归组：" + g.Missing
			}
			attention := prefs.Attention[source]
			if g.Kind == "room" || attention == "" {
				attention = "normal"
			}
			if attention != "normal" && attention != "high" && attention != "low" {
				return ErrInvalid
			}
			shown := attention
			if shown == "normal" {
				shown = ""
			}
			s = &indexedStream{summary: StreamSummary{Ref: ref, Kind: g.Kind, Label: label, SourceLabel: sourceLabel, Hint: hint, Attention: shown}, group: g, source: source, account: g.AccountRef(c.member), oldest: r.ReceivedAt}
			index[ref] = s
		}
		if r.ReceivedAt.Before(s.oldest) {
			s.oldest = r.ReceivedAt
		}
		if r.Blocked != "" {
			s.summary.Blocked, _ = bounded(r.Blocked, 256)
		}
		// Retain only bounded searchable facts and excerpts, never whole payloads.
		originalExcerpt := r.Excerpt
		r.Excerpt, _ = bounded(r.Excerpt, 2048)
		r.Incomplete = r.Incomplete || len(originalExcerpt) > 2048
		original := r.Group.Facts.Summary
		r.Group.Facts.Summary, _ = bounded(original, 1024)
		r.Incomplete = r.Incomplete || len(original) > 1024
		s.records = append(s.records, r)
		s.summary.Pending++
	}
	streams := make([]*indexedStream, 0, len(index))
	for _, s := range index {
		sort.Slice(s.records, func(i, j int) bool { return s.records[i].Location.Seq < s.records[j].Location.Seq })
		streams = append(streams, s)
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].summary.Ref < streams[j].summary.Ref })
	// Isolate hot settings mutations from existing and future snapshots.
	b, _ := json.Marshal(prefs)
	var copy Preferences
	_ = json.Unmarshal(b, &copy)
	c.streams, c.byRef, c.prefs = streams, index, copy
	return nil
}
func (c *Catalog) Rebuild(records []Record, prefs Preferences) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rebuild(records, prefs)
}
func bounded(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	for n > 0 && n < len(s) && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n] + "…", true
}
func token() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (c *Catalog) validTurn(turn uint64) error {
	if turn != c.turn {
		return ErrIdentity
	}
	return nil
}
func cursorOnly(q Query) bool {
	turn, cursor := q.Turn, q.Cursor
	q.Turn, q.Cursor = 0, ""
	q.views = false
	return turn != 0 && cursor != "" && q == (Query{})
}
func familyFields(family string) []string {
	switch family {
	case "object":
		return []string{"project", "project_id", "object_type", "iid", "object_ref", "channel"}
	case "conversation":
		return []string{"chat", "chat_id", "thread_id"}
	case "room":
		return []string{"entry_kind", "event_type", "operation_id"}
	}
	return nil
}
func specialFields(q Query) map[string]string {
	return map[string]string{"project": q.Project, "project_id": string(q.ProjectID), "object_type": q.ObjectType, "iid": string(q.IID), "object_ref": q.ObjectRef, "chat": q.Chat, "chat_id": q.ChatID, "thread_id": q.ThreadID, "entry_kind": q.EntryKind, "event_type": q.EventType, "operation_id": q.OperationID, "channel": q.Channel}
}
func (c *Catalog) validate(q Query, overview bool) error {
	if err := c.validTurn(q.Turn); err != nil {
		return err
	}
	for _, id := range []json.Number{q.ProjectID, q.IID} {
		if id != "" {
			n, err := strconv.ParseInt(string(id), 10, 64)
			if err != nil || n < 1 || strconv.FormatInt(n, 10) != string(id) {
				return ErrInvalid
			}
		}
	}
	if q.ObjectType != "" && q.ObjectType != "merge_request" && q.ObjectType != "issue" {
		return ErrUnsupported
	}
	if q.EntryKind != "" && q.EntryKind != "message" && q.EntryKind != "event" {
		return ErrUnsupported
	}
	if q.Limit < 0 || q.Limit > 20 {
		return ErrInvalid
	}
	if q.Kind != "" && q.Kind != "room" && q.Kind != "notification" {
		return ErrUnsupported
	}
	if q.State != "" && q.State != "readable" && q.State != "blocked" && q.State != "all" {
		return ErrUnsupported
	}
	if q.Sort != "" && q.Sort != "attention" && q.Sort != "oldest" {
		return ErrUnsupported
	}
	if q.Attention != "" && q.Attention != "normal" && q.Attention != "high" && q.Attention != "low" {
		return ErrUnsupported
	}
	if overview && !q.views && (q.State != "" || q.Sort != "" || q.Attention != "" || q.Text != "" || q.ObjectType != "" || q.IID != "" || q.ObjectRef != "" || q.ThreadID != "" || q.EntryKind != "" || q.EventType != "" || q.OperationID != "" || q.Channel != "") {
		return ErrUnsupported
	}
	if q.Kind == "room" && (q.Provider != "" || q.AccountRef != "") {
		return ErrConflict
	}
	family := ""
	for k, v := range specialFields(q) {
		if v == "" {
			continue
		}
		f := ""
		for _, candidate := range []string{"object", "conversation", "room"} {
			if slices.Contains(familyFields(candidate), k) {
				f = candidate
			}
		}
		if family != "" && family != f {
			return ErrConflict
		}
		family = f
	}
	if family == "room" && (q.Kind == "notification" || q.Provider != "" || q.AccountRef != "") || family != "" && family != "room" && q.Kind == "room" {
		return ErrConflict
	}
	if q.AccountRef != "" {
		group, ok := c.accounts[q.AccountRef]
		if !ok {
			return ErrIdentity
		}
		if q.Provider != "" && group.Provider != q.Provider {
			return ErrConflict
		}
		if family != "" {
			for _, d := range c.descriptors {
				if d.Provider == group.Provider && d.Family != family {
					return ErrConflict
				}
			}
		}
	}
	if q.Provider != "" {
		found := false
		for _, d := range c.descriptors {
			if d.Provider == q.Provider {
				found = true
				if family != "" && family != d.Family {
					return ErrConflict
				}
				if q.Channel != "" && !slices.Contains(d.Channels, q.Channel) {
					return ErrUnsupported
				}
			}
		}
		if !found {
			return ErrUnsupported
		}
	}
	return nil
}

type compiledQuery struct {
	Query
	exact  map[string]string
	words  []string
	family string
}

func compileQuery(q Query) compiledQuery {
	result := compiledQuery{Query: q, exact: map[string]string{}, words: strings.Fields(strings.ToLower(q.Text))}
	for field, value := range specialFields(q) {
		if value != "" {
			result.exact[field] = value
			for _, family := range []string{"object", "conversation", "room"} {
				if slices.Contains(familyFields(family), field) {
					result.family = family
				}
			}
		}
	}
	return result
}
func factValue(f Facts, channel, field string) string {
	switch field {
	case "project":
		return f.Project
	case "project_id":
		return f.ProjectID
	case "object_type":
		return f.ObjectType
	case "iid":
		return f.IID
	case "object_ref":
		return f.ObjectRef
	case "chat":
		return f.Chat
	case "chat_id":
		return f.ChatID
	case "thread_id":
		return f.ThreadID
	case "entry_kind":
		return f.EntryKind
	case "event_type":
		return f.EventType
	case "operation_id":
		return f.OperationID
	case "channel":
		return channel
	}
	return ""
}
func (c *Catalog) matches(s *indexedStream, q compiledQuery, overview bool) (bool, bool) {
	g := s.group
	v := s.summary
	unknown := false
	if q.Kind != "" && q.Kind != g.Kind || q.Provider != "" && q.Provider != g.Provider || q.AccountRef != "" && q.AccountRef != s.account {
		return false, false
	}
	if q.family != "" {
		family := "room"
		for _, d := range c.descriptors {
			if d.Provider == g.Provider {
				family = d.Family
			}
		}
		if family != q.family {
			return false, false
		}
	}
	attention := v.Attention
	if attention == "" {
		attention = "normal"
	}
	if q.Attention != "" && q.Attention != attention {
		return false, false
	}
	if q.within != "" && !c.withinGroup(q.within, g) {
		return false, false
	}
	if !overview {
		state := q.State
		if state == "" {
			state = "readable"
		}
		if state == "readable" && v.Blocked != "" || state == "blocked" && v.Blocked == "" {
			return false, false
		}
		attention := v.Attention
		if attention == "" {
			attention = "normal"
		}
		if q.Attention != "" && q.Attention != attention {
			return false, false
		}
	}
	for _, r := range s.records {
		f := r.Group.Facts
		match := true
		missing := false
		for field, want := range q.exact {
			actual := factValue(f, r.Group.Channel, field)
			if actual != want {
				match = false
				if actual == "" {
					missing = true
				}
			}
		}
		if match && len(q.words) > 0 {
			text := strings.ToLower(f.Label + " " + f.SourceLabel + " " + f.Project + " " + f.Chat + " " + f.Summary)
			for _, word := range q.words {
				if !strings.Contains(text, word) {
					match = false
				}
			}
		}
		if match {
			return true, false
		}
		unknown = unknown || missing || r.Incomplete || f.Incomplete
	}
	return false, unknown
}
func (c *Catalog) selectStreams(q Query, overview bool) ([]*indexedStream, string, error) {
	if q.within != "" {
		if _, ok := c.ranges[q.within]; !ok {
			return nil, "", ErrIdentity
		}
	}
	if err := c.validate(q, overview); err != nil {
		return nil, "", err
	}
	// A saved name and native ID must describe the same range.
	for _, pair := range [][2]string{{q.Project, string(q.ProjectID)}, {q.Chat, q.ChatID}} {
		if pair[0] == "" || pair[1] == "" {
			continue
		}
		seen, consistent := false, false
		for _, r := range c.ranges {
			g := r.group
			if q.within != "" && !c.withinGroup(q.within, g) || q.AccountRef != "" && q.AccountRef != g.AccountRef(c.member) || q.Provider != "" && q.Provider != g.Provider {
				continue
			}
			name, id := g.Facts.Project, g.Facts.ProjectID
			if q.Chat != "" {
				name, id = g.Facts.Chat, g.Facts.ChatID
			}
			seen = seen || name == pair[0] || id == pair[1]
			consistent = consistent || name == pair[0] && id == pair[1]
		}
		if seen && !consistent {
			return nil, "", ErrConflict
		}
	}
	result := []*indexedStream{}
	filter := compileQuery(q)
	missing := 0
	for _, s := range c.streams {
		yes, unknown := c.matches(s, filter, overview)
		if yes {
			result = append(result, s)
		} else if unknown {
			missing++
		}
	}
	if q.IID != "" {
		scopes := map[string]bool{}
		options := []map[string]string{}
		// Resolve scope from saved native facts, independent of pending counts,
		// keyword matches, channel predicates or a previous read in this turn.
		for _, g := range c.scopes {
			if q.within != "" && !c.withinProject(q.within, g) {
				continue
			}
			if q.Provider != "" && q.Provider != g.Provider || q.AccountRef != "" && q.AccountRef != g.AccountRef(c.member) || q.Project != "" && q.Project != g.Facts.Project || q.ProjectID != "" && string(q.ProjectID) != g.Facts.ProjectID {
				continue
			}
			scope := g.SourceKey(c.member) + "/" + g.Facts.ProjectID
			if scopes[scope] {
				continue
			}
			scopes[scope] = true
			if len(options) < 5 {
				label, _ := bounded(g.Facts.SourceLabel+" ["+g.Instance+" / "+g.Facts.AccountLabel+"]", 320)
				option := map[string]string{"account_ref": g.AccountRef(c.member), "source_label": label, "project": g.Facts.Project, "project_id": g.Facts.ProjectID}
				if q.views {
					option["scope_ref"] = g.ProjectScopeRef(c.member)
				}
				options = append(options, option)
			}
		}
		if len(scopes) > 1 {
			encoded, _ := json.Marshal(options)
			return nil, "", fmt.Errorf("%w: %d saved account/project scopes; choose account_ref and project_id from these saved options: %s", ErrAmbiguous, len(scopes), encoded)
		}
	}

	notice := ""
	if len(result) == 0 {
		base := q
		base.Text, base.ObjectType, base.IID, base.ObjectRef, base.Channel, base.ThreadID = "", "", "", "", "", ""
		base.State, base.Attention = "all", ""
		available := 0
		for _, s := range c.streams {
			if yes, _ := c.matches(s, compileQuery(base), overview); yes {
				available++
			}
		}
		if available > 0 {
			notice = fmt.Sprintf("input_no_match: scope has %d pending streams; additional predicates did not match. Remove query or inspect state=all; do not translate names or infer remote absence.", available)
		} else {
			notice = "input_scope_empty: no pending streams in this exact local scope; names are exact, not semantic. This does not prove remote absence."
		}
	}
	if missing > 0 {
		notice += fmt.Sprintf(" %d flows have missing or truncated saved facts; remove exact filters or read their source-level streams. Search is local only.", missing)
	}
	return result, notice, nil
}
func oldest(a, b *indexedStream) bool {
	if !a.oldest.Equal(b.oldest) {
		return a.oldest.Before(b.oldest)
	}
	return a.summary.Ref < b.summary.Ref
}
func rank(s *indexedStream) int {
	switch s.summary.Attention {
	case "high":
		return 0
	case "low":
		return 2
	}
	return 1
}
func (c *Catalog) ordered(input []*indexedStream, q Query, limit int) ([]StreamSummary, []int) {
	rest := slices.Clone(input)
	result := []StreamSummary{}
	if q.Sort == "oldest" || q.Provider != "" || q.AccountRef != "" {
		sort.Slice(rest, func(i, j int) bool {
			if q.Sort != "oldest" && rank(rest[i]) != rank(rest[j]) {
				return rank(rest[i]) < rank(rest[j])
			}
			return oldest(rest[i], rest[j])
		})
		for _, s := range rest {
			result = append(result, s.summary)
		}
		return result, nil
	}
	ends := []int{}
	sources := []string{}
	seen := map[string]bool{}
	for _, s := range rest {
		if s.group.Kind != "room" && !seen[s.source] {
			sources = append(sources, s.source)
			seen[s.source] = true
		}
	}
	slices.Sort(sources)
	start := 0
	for i, k := range sources {
		if k == c.prefs.LastSource {
			start = (i + 1) % len(sources)
		}
	}
	ring := map[string]int{}
	for i := range sources {
		ring[sources[(i+start)%len(sources)]] = i
	}
	for len(rest) > 0 {
		page := []*indexedStream{}
		counts := map[string]int{}
		last := map[int]int{0: -1, 1: -1, 2: -1}
		for i, s := range rest {
			if s.group.Kind == "room" {
				page = append(page, s)
				rest = append(rest[:i], rest[i+1:]...)
				break
			}
		}
		slots := limit - len(page)
		for n := 0; n < slots && len(rest) > 0; n++ {
			fair := n == slots-1
			best := -1
			for i, s := range rest {
				quota := []int{3, 2, 1}[rank(s)]
				if counts[s.source] >= quota {
					continue
				}
				if best < 0 {
					best = i
					continue
				}
				b := rest[best]
				less := false
				if fair {
					unused, bunused := counts[s.source] == 0, counts[b.source] == 0
					if unused != bunused {
						less = unused
					} else if c.prefs.LastExposed[s.source] != c.prefs.LastExposed[b.source] {
						less = c.prefs.LastExposed[s.source] < c.prefs.LastExposed[b.source]
					} else if ring[s.source] != ring[b.source] {
						less = ring[s.source] < ring[b.source]
					} else {
						less = oldest(s, b)
					}
				} else if rank(s) != rank(b) {
					less = rank(s) < rank(b)
				} else {
					size := len(sources)
					ri := (ring[s.source] - last[rank(s)] - 1 + size) % max(1, size)
					rb := (ring[b.source] - last[rank(b)] - 1 + size) % max(1, size)
					if ri != rb {
						less = ri < rb
					} else {
						less = oldest(s, b)
					}
				}
				if less {
					best = i
				}
			}
			if best < 0 {
				break
			}
			s := rest[best]
			page = append(page, s)
			counts[s.source]++
			last[rank(s)] = ring[s.source]
			rest = append(rest[:best], rest[best+1:]...)
		}
		if len(page) == 0 {
			break
		}
		for _, s := range page {
			result = append(result, s.summary)
		}
		ends = append(ends, len(result))
	}
	return result, ends
}
func (c *Catalog) put(p catalogPage) string {
	if len(c.pages) >= 512 {
		c.pages = map[string]catalogPage{}
	}
	id := token()
	c.pages[id] = p
	return id
}
func (c *Catalog) list(q Query) (List, error) {
	purpose := "list"
	if q.views {
		purpose = "views_list"
	}
	limit := q.Limit
	if limit == 0 {
		limit = 10
	}
	var p catalogPage
	if q.Cursor != "" {
		if !cursorOnly(q) || c.validTurn(q.Turn) != nil {
			return List{}, ErrConflict
		}
		var ok bool
		p, ok = c.pages[q.Cursor]
		if !ok || p.kind != purpose {
			return List{}, ErrExpired
		}
		limit = p.query.Limit
	} else {
		selected, notice, err := c.selectStreams(q, false)
		if err != nil {
			return List{}, err
		}
		c.queries++
		if c.queries > 4 {
			c.pages = map[string]catalogPage{}
			c.queries = 1
		}
		q.Limit = limit
		ordered, ends := c.ordered(selected, q, limit)
		p = catalogPage{kind: purpose, scopeRef: c.resultScope(q), query: q, streams: ordered, ends: ends, total: len(selected), notice: notice, at: c.now()}
	}
	end := min(p.offset+limit, len(p.streams))
	for _, boundary := range p.ends {
		if boundary > p.offset {
			end = min(end, boundary)
			break
		}
	}
	out := List{ScopeRef: p.scopeRef, Matched: p.total, Streams: []StreamSummary{}, Notice: p.notice}
	for i := p.offset; i < end; i++ {
		out.Streams = append(out.Streams, p.streams[i])
		b, _ := json.Marshal(out)
		if len(b) > PageBytes-512 {
			out.Streams = out.Streams[:len(out.Streams)-1]
			end = i
			break
		}
	}
	if end < len(p.streams) {
		p.offset = end
		out.Next = c.put(p)
	}
	return out, nil
}
func (c *Catalog) List(q Query) (List, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out, err := c.list(q)
	if err != nil {
		return out, err
	}
	b, _ := json.Marshal(out)
	if c.discoveryBytes+len(b) > DiscoveryTurnBytes {
		return List{Matched: out.Matched, Streams: []StreamSummary{}, Stop: "directory_budget_exhausted"}, nil
	}
	c.discoveryBytes += len(b)
	return out, nil
}
func (c *Catalog) Overview(q Query) (Overview, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.overview(q)
}
func (c *Catalog) overview(q Query) (Overview, error) {
	purpose := "overview"
	if q.views {
		purpose = "stats"
	}
	limit := q.Limit
	if limit == 0 {
		limit = 20
	}
	var p catalogPage
	if q.Cursor != "" {
		if !cursorOnly(q) || c.validTurn(q.Turn) != nil {
			return Overview{}, ErrConflict
		}
		var ok bool
		p, ok = c.pages[q.Cursor]
		if !ok || p.kind != purpose {
			return Overview{}, ErrExpired
		}
		limit = p.query.Limit
	} else {
		c.queries++
		if c.queries > 4 {
			c.pages = map[string]catalogPage{}
			c.queries = 1
		}
		selected, notice, err := c.selectStreams(q, true)
		if err != nil {
			return Overview{}, err
		}
		grouped := map[string]SourceSummary{}
		entries, blocked := 0, 0
		for _, s := range selected {
			src := grouped[s.source]
			src.Kind = s.group.Kind
			src.Provider = s.group.Provider
			src.AccountRef = s.group.AccountRef(c.member)
			src.Label = s.summary.SourceLabel
			if src.Label == "" {
				src.Label = s.summary.Label
			}
			src.Attention = s.summary.Attention
			src.Pending++
			src.Entries += s.summary.Pending
			entries += s.summary.Pending
			if s.summary.Blocked != "" {
				src.Blocked++
				blocked++
			}
			grouped[s.source] = src
		}
		keys := make([]string, 0, len(grouped))
		for k := range grouped {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		q.Limit = limit
		p = catalogPage{kind: purpose, scopeRef: c.resultScope(q), query: q, total: len(selected), entries: entries, blocked: blocked, notice: notice, at: c.now()}
		for _, k := range keys {
			p.sources = append(p.sources, grouped[k])
		}
	}
	end := min(p.offset+limit, len(p.sources))
	out := Overview{ScopeRef: p.scopeRef, ObservedAt: p.at, Pending: p.total, Entries: p.entries, Blocked: p.blocked, Sources: []SourceSummary{}, Notice: p.notice}
	for i := p.offset; i < end; i++ {
		out.Sources = append(out.Sources, p.sources[i])
		b, _ := json.Marshal(out)
		if len(b) > OverviewBytes-512 {
			out.Sources = out.Sources[:len(out.Sources)-1]
			end = i
			break
		}
	}
	if end < len(p.sources) {
		p.offset = end
		out.Next = c.put(p)
	}
	b, _ := json.Marshal(out)
	if c.discoveryBytes+len(b) > DiscoveryTurnBytes {
		return Overview{ObservedAt: out.ObservedAt, Pending: out.Pending, Entries: out.Entries, Blocked: out.Blocked, Sources: []SourceSummary{}, Stop: "directory_budget_exhausted"}, nil
	}
	c.discoveryBytes += len(b)
	return out, nil
}
func (c *Catalog) Preview(q PreviewRequest) (Preview, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.preview(q)
}
func (c *Catalog) preview(q PreviewRequest) (Preview, error) {
	if c.validTurn(q.Turn) != nil {
		return Preview{}, ErrIdentity
	}
	if q.Limit < 0 || q.Limit > 3 {
		return Preview{}, ErrInvalid
	}
	if q.Limit == 0 {
		q.Limit = 1
	}
	s := c.byRef[q.Ref]
	if s == nil {
		if c.knownRefs[q.Ref] {
			return Preview{}, fmt.Errorf("input_stream_empty: no pending entries; use saved history for previously delivered evidence")
		}
		return Preview{}, ErrExpired
	}
	if s.summary.Blocked != "" {
		return Preview{}, fmt.Errorf("input_stream_blocked: %s", s.summary.Blocked)
	}
	out := Preview{Ref: q.Ref, Entries: []Excerpt{}}
	for _, r := range s.records[:min(q.Limit, len(s.records))] {
		text, truncated := bounded(r.Excerpt, 1024)
		out.Entries = append(out.Entries, Excerpt{text, truncated})
		if r.Incomplete || r.Group.Facts.Incomplete {
			out.Notice = "Saved coverage is incomplete; formally read this stream or query saved source history."
		}
	}
	b, _ := json.Marshal(out)
	if len(b) > PreviewBytes || c.previewBytes+len(b) > PreviewTurnBytes {
		return Preview{Ref: q.Ref, Entries: []Excerpt{}, Stop: "preview_budget_exhausted"}, nil
	}
	c.previewBytes += len(b)
	return out, nil
}

// Directory reuses List DTOs. Exposure is returned as a proposal; only the host
// may persist it, once per accepted turn, with preference-version CAS.
func (c *Catalog) Directory() (InputDirectory, Exposure, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directory != nil {
		return *c.directory, c.exposure, nil
	}
	list, err := c.list(Query{Turn: c.turn, Limit: 5})
	if err != nil {
		return InputDirectory{}, Exposure{}, err
	}
	original := slices.Clone(list.Streams)
	d := InputDirectory{Pending: len(c.streams), Streams: list.Streams, Next: list.Next}
	for _, s := range c.streams {
		if s.summary.Blocked != "" {
			d.Blocked++
		}
	}
	for {
		b, _ := json.Marshal(d)
		if len(b) <= DirectoryBytes {
			break
		}
		d.Truncated = true
		removed := false
		for i := range d.Streams {
			if d.Streams[i].Hint != "" {
				d.Streams[i].Hint = ""
				removed = true
			}
		}
		if removed {
			continue
		}
		if len(d.Streams) == 0 {
			break
		}
		d.Streams = d.Streams[:len(d.Streams)-1]
	}
	if len(d.Streams) < len(original) {
		// Byte trimming must not advance the continuation past unseen choices.
		if previous, ok := c.pages[list.Next]; ok {
			previous.offset -= len(original) - len(d.Streams)
			d.Next = c.put(previous)
		} else {
			d.Next = c.put(catalogPage{kind: "list", query: Query{Turn: c.turn, Limit: 5}, streams: original, offset: len(d.Streams), total: list.Matched, at: c.now()})
		}
	}
	exposure := Exposure{Turn: c.turn}
	seen := map[string]bool{}
	for _, s := range d.Streams {
		v := c.byRef[s.Ref]
		if v.group.Kind != "room" {
			exposure.LastSource = v.source
			if !seen[v.source] {
				exposure.Sources = append(exposure.Sources, v.source)
				seen[v.source] = true
			}
		}
	}
	encoded, _ := json.Marshal(d)
	if c.discoveryBytes+len(encoded) > DiscoveryTurnBytes {
		return InputDirectory{}, Exposure{}, ErrCapacity
	}
	c.discoveryBytes += len(encoded)
	c.directory, c.exposure = &d, exposure
	return d, exposure, nil
}
