package inputstream

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
)

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
type StreamSummary struct {
	Hint        string `json:"hint,omitempty"`
	Ref         string `json:"stream_ref"`
	Kind        string `json:"kind"`
	Label       string `json:"label"`
	SourceLabel string `json:"source_label,omitempty"`
	Pending     int    `json:"pending_entries"`
	Attention   string `json:"attention,omitempty"`
	Blocked     string `json:"-"`
}
type catalogEntry struct {
	location                             Location
	source                               string
	at                                   time.Time
	labelTruncated, fallback, incomplete bool
}
type catalogPage struct {
	all           []StreamSummary
	offset, limit int
	ends          []int
}
type Catalog struct {
	mu             sync.Mutex
	member         Member
	turn           uint64
	nonce          string
	observedAt     time.Time
	streams        map[string]StreamSummary
	entries        []catalogEntry
	streamEntries  map[string][]int
	preferences    Preferences
	pages          map[string]catalogPage
	directory      *Directory
	exposure       Exposure
	discoveryBytes int
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
	c := &Catalog{member: r.member, turn: turn, nonce: hex.EncodeToString(nonce[:]), observedAt: o.ObservedAt, streams: map[string]StreamSummary{}, streamEntries: map[string][]int{}, preferences: o.Preferences, pages: map[string]catalogPage{}}
	if c.observedAt.IsZero() {
		c.observedAt = time.Now()
	}
	c.preferences.Attention = cloneMap(o.Preferences.Attention)
	c.preferences.LastExposed = cloneMap(o.Preferences.LastExposed)
	blocked := map[string]bool{}
	known := make(map[Location]bool, len(o.Excluded)+len(o.Blocked))
	for _, p := range r.parts {
		for _, rec := range p.records {
			_, excluded := o.Excluded[rec.Location]
			_, blocked := o.Blocked[rec.Location]
			if excluded || blocked {
				known[rec.Location] = true
			}
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
		for _, seq := range sortedSeqs(p) {
			rec := p.records[seq]
			if seq > bound.Through || o.Excluded[rec.Location] {
				continue
			}
			v := p.projections[seq]
			ref := p.streamRefs[seq]
			source := attentionSourceRef(r.member, provider, rec.Group.SourceKey(provider, rec.Channel))
			if o.Blocked[rec.Location] != "" || v.Blocked != "" {
				blocked[ref] = true
			}
			attention := c.preferences.Attention[source]
			if attention == "" {
				attention = "normal"
			}
			if attention != "normal" && attention != "high" && attention != "low" {
				return nil, ErrInvalid
			}
			label, cut := boundedText(v.Label, 200)
			sourceLabel, sourceCut := boundedText(v.SourceLabel, 200)
			c.streamEntries[ref] = append(c.streamEntries[ref], len(c.entries))
			c.entries = append(c.entries, catalogEntry{location: rec.Location, source: source, at: rec.ReceivedAt, labelTruncated: cut || sourceCut, fallback: rec.Group.Precision == "source", incomplete: v.Incomplete})
			s := c.streams[ref]
			s.Ref = ref
			s.Kind = "notification"
			s.Label = label
			s.SourceLabel = sourceLabel
			s.Pending++
			s.Attention = attention
			c.streams[ref] = s
		}
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
	for ref := range c.streams {
		if blocked[ref] {
			delete(c.streams, ref)
			continue
		}
		s := c.streams[ref]
		s.Hint = c.hint(c.streamEntries[ref])
		c.streams[ref] = s
	}
	return c, nil
}
func (c *Catalog) AddRoom(snap Snapshot, summary StreamSummary, records []StoredRecord) error {
	return c.AddExternal(snap, summary, records)
}
func (c *Catalog) AddExternal(snap Snapshot, summary StreamSummary, records []StoredRecord) error {
	return c.addExternal(snap, summary, records, summary.Ref)
}
func (c *Catalog) AddShared(snap Snapshot, summary StreamSummary, records []StoredRecord, provider string) error {
	if summary.Kind != "shared" || provider == "" {
		return ErrInvalid
	}
	if len(records) == 0 {
		return nil
	}
	source := attentionSourceRef(c.member, provider, records[0].Group.SourceKey(provider, records[0].Channel))
	return c.addExternal(snap, summary, records, source)
}
func (c *Catalog) addExternal(snap Snapshot, summary StreamSummary, records []StoredRecord, source string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if (summary.Kind != "room" && summary.Kind != "shared") || summary.Ref == "" || len(records) > MaxRecords {
		return ErrInvalid
	}
	if len(records) == 0 {
		return nil
	}
	summary.Pending = len(records)
	summary.Attention = "normal"
	if summary.Kind == "shared" && c.preferences.Attention[source] != "" {
		summary.Attention = c.preferences.Attention[source]
	}
	if summary.Attention != "normal" && summary.Attention != "high" && summary.Attention != "low" {
		return ErrInvalid
	}
	label, cut := boundedText(summary.Label, 200)
	sourceLabel, sourceCut := boundedText(summary.SourceLabel, 200)
	summary.Label = label
	summary.SourceLabel = sourceLabel
	c.streams[summary.Ref] = summary
	for _, rec := range records {
		if rec.Location.Partition != snap.Partition || rec.Location.Seq > snap.Through {
			return ErrIdentity
		}
		c.streamEntries[summary.Ref] = append(c.streamEntries[summary.Ref], len(c.entries))
		c.entries = append(c.entries, catalogEntry{location: rec.Location, source: source, at: rec.ReceivedAt, labelTruncated: cut || sourceCut, fallback: rec.Group.Precision == "source"})
	}
	if summary.Kind == "shared" {
		summary.Hint = c.hint(c.streamEntries[summary.Ref])
		c.streams[summary.Ref] = summary
	}
	return nil
}
func (c *Catalog) newCursor(p catalogPage) string {
	if len(c.pages) >= 512 {
		return ""
	}
	ref := Ref("catalog_", c.nonce, c.turn, p.offset, len(c.pages))
	c.pages[ref] = p
	return ref
}
func (c *Catalog) list(q ListRequest, maximum int) (Directory, error) {
	limit := q.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 20 || q.Cursor != "" && q.Limit != 0 {
		return Directory{}, ErrInvalid
	}
	var p catalogPage
	if q.Cursor != "" {
		var ok bool
		p, ok = c.pages[q.Cursor]
		if !ok {
			return Directory{}, ErrExpired
		}
	} else {
		refs := []string{}
		for ref := range c.streams {
			refs = append(refs, ref)
		}
		sort.Slice(refs, func(i, j int) bool {
			a, b := c.streams[refs[i]], c.streams[refs[j]]
			if rank(a.Attention) != rank(b.Attention) {
				return rank(a.Attention) < rank(b.Attention)
			}
			ae, be := c.entries[c.streamEntries[a.Ref][0]], c.entries[c.streamEntries[b.Ref][0]]
			if !ae.at.Equal(be.at) {
				return ae.at.Before(be.at)
			}
			return a.Ref < b.Ref
		})
		refs, ends := c.orderPages(refs, limit, false)
		p = catalogPage{limit: limit, ends: ends}
		for _, ref := range refs {
			s := c.streams[ref]
			if s.Attention == "normal" {
				s.Attention = ""
			}
			p.all = append(p.all, s)
		}
	}
	out := Directory{Pending: len(p.all), Streams: []StreamSummary{}}
	end := p.offset
	pageEnd := len(p.all)
	for _, e := range p.ends {
		if e > p.offset {
			pageEnd = e
			break
		}
	}
	for end < pageEnd && end-p.offset < p.limit {
		out.Streams = append(out.Streams, p.all[end])
		b, _ := json.Marshal(out)
		if len(b) > maximum-512 {
			out.Streams = out.Streams[:len(out.Streams)-1]
			break
		}
		end++
	}
	if end == p.offset && end < len(p.all) {
		return Directory{}, ErrCapacity
	}
	if end < len(p.all) {
		p.offset = end
		out.Next = c.newCursor(p)
		if out.Next == "" {
			return Directory{}, ErrCapacity
		}
	}
	b, _ := json.Marshal(out)
	if c.discoveryBytes+len(b) > DiscoveryTurnBytes {
		return Directory{Pending: out.Pending, Streams: []StreamSummary{}, Stop: "discovery_budget_exhausted"}, nil
	}
	c.discoveryBytes += len(b)
	return out, nil
}
func (c *Catalog) Directory() (Directory, Exposure, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directory != nil {
		return *c.directory, c.exposure, nil
	}
	out, err := c.list(ListRequest{Limit: 5}, DirectoryBytes)
	if err != nil {
		return out, Exposure{}, err
	}
	exposure := Exposure{Turn: c.turn}
	seen := map[string]bool{}
	for _, s := range out.Streams {
		if s.Kind == "room" {
			continue
		}
		source := c.entries[c.streamEntries[s.Ref][0]].source
		if !seen[source] {
			seen[source] = true
			exposure.Sources = append(exposure.Sources, source)
		}
		exposure.LastSource = source
	}
	c.directory = &out
	c.exposure = exposure
	return out, exposure, nil
}
func (c *Catalog) ReadableList(q ListRequest) (Directory, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.list(q, PageBytes)
}

func (r *Repository) StreamAt(l Location) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for provider, p := range r.parts {
		if p.state.Spec.Ref == l.Partition {
			rec, ok := p.records[l.Seq]
			if !ok {
				return "", ErrCorrupt
			}
			return rec.Group.StreamRef(r.member, provider, rec.Channel), nil
		}
	}
	return "", ErrIdentity
}

// InputContext is a local label fallback for a known opaque reference after
// context loss. It exposes no scopes, query API or native identity coordinates.
func (r *Repository) InputContext(e Execution, stream string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.execution(e); err != nil {
		return "", err
	}
	for provider, p := range r.parts {
		bound, ok := r.coordinator.Bounds[provider]
		if !ok {
			continue
		}
		seqs := p.streamSeqs[stream]
		if len(seqs) > 0 && seqs[0] <= bound.Through {
			v := p.projections[seqs[0]]
			return strings.TrimSpace(v.SourceLabel + " / " + v.Label), nil
		}
	}
	return "", ErrIdentity
}
