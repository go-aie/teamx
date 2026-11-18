package frozen

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const DirectoryBytes = 8 << 10
const PageBytes = 16 << 10
const StatsBytes = 8 << 10
const PreviewBytes = 8 << 10
const DiscoveryTurnBytes = 128 << 10
const PreviewTurnBytes = 32 << 10
const GuidanceBytes = 1 << 10

type Exposure struct {
	Turn       uint64
	Sources    []string
	LastSource string
}
type InputDirectory struct {
	Pending   int             `json:"pending_streams"`
	Blocked   int             `json:"blocked_streams,omitempty"`
	Streams   []StreamSummary `json:"streams"`
	Next      string          `json:"next_cursor,omitempty"`
	Truncated bool            `json:"truncated,omitempty"`
}

func boundedText(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}
func rank(s string) int {
	switch s {
	case "high":
		return 0
	case "low":
		return 2
	}
	return 1
}

// Pure generic source rotation. Native business facts never affect ranking.
func (c *Catalog) orderPages(refs []string, limit int, unlimited bool) ([]string, []int) {
	if unlimited {
		return refs, nil
	}
	for i, ref := range refs {
		if c.streams[ref].Kind == "room" {
			rest := append(append([]string{}, refs[:i]...), refs[i+1:]...)
			if limit == 1 {
				ordered, ends := c.orderPages(rest, limit, false)
				for i := range ends {
					ends[i]++
				}
				return append([]string{ref}, ordered...), append([]int{1}, ends...)
			}
			ordered, ends := c.orderPages(rest, limit-1, false)
			// Fill the first page with Room, then retain the frozen source pages.
			for i := range ends {
				ends[i]++
			}
			if len(ends) == 0 {
				ends = []int{1}
			}
			return append([]string{ref}, ordered...), ends
		}
	}
	queues := map[string][]string{}
	for _, ref := range refs {
		source := c.entries[c.streamEntries[ref][0]].source
		queues[source] = append(queues[source], ref)
	}
	remaining := len(refs)
	sources := []string{}
	seen := map[string]bool{}
	for _, ref := range refs {
		source := c.entries[c.streamEntries[ref][0]].source
		if !seen[source] {
			sources = append(sources, source)
			seen[source] = true
		}
	}
	sort.Strings(sources)
	start := 0
	for n, s := range sources {
		if s == c.preferences.LastSource {
			start = (n + 1) % len(sources)
		}
	}
	ring := map[string]int{}
	for n := range sources {
		ring[sources[(n+start)%len(sources)]] = n
	}
	sourceOf := func(ref string) string { return c.entries[c.streamEntries[ref][0]].source }
	oldest := func(a, b string) bool {
		ae, be := c.entries[c.streamEntries[a][0]], c.entries[c.streamEntries[b][0]]
		if !ae.at.Equal(be.at) {
			return ae.at.Before(be.at)
		}
		return a < b
	}
	out := []string{}
	ends := []int{}
	for remaining > 0 {
		counts := map[string]int{}
		last := map[int]int{0: -1, 1: -1, 2: -1}
		count := 0
		for n := 0; n < limit && remaining > 0; n++ {
			best := ""
			fair := n == limit-1
			for source, queue := range queues {
				ref := queue[0]
				level := rank(c.streams[ref].Attention)
				if counts[source] >= []int{3, 2, 1}[level] {
					continue
				}
				if best == "" {
					best = source
					continue
				}
				b := queues[best][0]
				bs := sourceOf(b)
				bl := rank(c.streams[b].Attention)
				less := false
				if fair {
					if (counts[source] == 0) != (counts[bs] == 0) {
						less = counts[source] == 0
					} else if c.preferences.LastExposed[source] != c.preferences.LastExposed[bs] {
						less = c.preferences.LastExposed[source] < c.preferences.LastExposed[bs]
					} else if ring[source] != ring[bs] {
						less = ring[source] < ring[bs]
					} else {
						less = oldest(ref, b)
					}
				} else if level != bl {
					less = level < bl
				} else {
					ri := (ring[source] - last[level] - 1 + len(sources)) % len(sources)
					rb := (ring[bs] - last[bl] - 1 + len(sources)) % len(sources)
					if ri != rb {
						less = ri < rb
					} else {
						less = oldest(ref, b)
					}
				}
				if less {
					best = source
				}
			}
			if best == "" {
				break
			}
			ref := queues[best][0]
			source := sourceOf(ref)
			out = append(out, ref)
			counts[source]++
			last[rank(c.streams[ref].Attention)] = ring[source]
			queues[best] = queues[best][1:]
			if len(queues[best]) == 0 {
				delete(queues, best)
			}
			remaining--
			count++
		}
		if count == 0 {
			break
		}
		ends = append(ends, len(out))
	}
	return out, ends
}
func (c *Catalog) hint(indices []int) string {
	fallback, incomplete := false, false
	truncated := false
	var oldest time.Time
	for _, idx := range indices {
		e := c.entries[idx]
		if e.labelTruncated {
			truncated = true
		}
		if e.fallback {
			fallback = true
		}
		if e.incomplete {
			incomplete = true
		}
		if oldest.IsZero() || e.at.Before(oldest) {
			oldest = e.at
		}
	}
	hints := []string{}
	if truncated {
		hints = append(hints, "标签已截断")
	}
	if fallback {
		hints = append(hints, "来源级归组；原生定位信息不足")
	}
	if incomplete {
		hints = append(hints, "本地覆盖不完整")
	}
	if !oldest.IsZero() && !c.observedAt.Before(oldest) {
		hours := int(c.observedAt.Sub(oldest) / time.Hour)
		if hours >= 1 {
			hints = append(hints, "已等待 "+strconv.Itoa(hours)+" 小时")
		}
	}
	return strings.Join(hints, "；")
}
func (c *Catalog) Directory() (InputDirectory, Exposure, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directory != nil {
		return *c.directory, c.exposure, nil
	}
	before := c.discoveryBytes
	list, err := c.list(Query{Limit: 5})
	if err != nil {
		return InputDirectory{}, Exposure{}, err
	}
	original := append([]StreamSummary(nil), list.Streams...)
	out := InputDirectory{Pending: len(c.streams), Streams: list.Streams, Next: list.Next}
	for _, s := range c.streams {
		if s.Blocked != "" {
			out.Blocked++
		}
	}
	for {
		b, _ := json.Marshal(out)
		if len(b) <= DirectoryBytes {
			break
		}
		out.Truncated = true
		trimmed := false
		for i := range out.Streams {
			if out.Streams[i].Hint != "" {
				out.Streams[i].Hint = ""
				trimmed = true
			}
		}
		if !trimmed {
			if len(out.Streams) == 0 {
				return InputDirectory{}, Exposure{}, ErrCapacity
			}
			out.Streams = out.Streams[:len(out.Streams)-1]
		}
	}
	if len(out.Streams) < len(original) {
		var p catalogPage
		if list.Next != "" {
			p = c.pages[list.Next]
			p.offset -= len(original) - len(out.Streams)
		} else {
			p = catalogPage{purpose: "list", list: List{Matched: list.Matched, Streams: original}, offset: len(out.Streams), limit: 5}
		}
		out.Next = c.newCursor(p)
	}
	b, _ := json.Marshal(out)
	if before+len(b) > DiscoveryTurnBytes {
		return InputDirectory{Pending: len(c.streams), Streams: []StreamSummary{}, Truncated: true}, Exposure{}, ErrCapacity
	}
	c.discoveryBytes = before + len(b)
	exposure := Exposure{Turn: c.turn}
	seen := map[string]bool{}
	for _, s := range out.Streams {
		if s.Kind == "room" {
			continue
		}
		source := c.entries[c.streamEntries[s.Ref][0]].source
		if !seen[source] {
			exposure.Sources = append(exposure.Sources, source)
			seen[source] = true
		}
		exposure.LastSource = source
	}
	c.directory = &out
	c.exposure = exposure
	return out, exposure, nil
}
