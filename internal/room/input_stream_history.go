package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
)

const inputHistoryPageSize = 25

type sharedHistoryStatus struct {
	Records int    `json:"records"`
	Pending int    `json:"pending"`
	Bytes   uint64 `json:"bytes"`
}

type inputHistoryStatus struct {
	Latest    string `json:"latest"`
	Retention string `json:"retention"`
	Pending   int    `json:"pending"`
	Reclaimed bool   `json:"reclaimed,omitempty"`
}

type inputHistoryCursor struct {
	SharedGrant string                          `json:"shared_grant,omitempty"`
	Owner       MemberIdentity                  `json:"owner"`
	Bounds      map[string]inputstream.Snapshot `json:"bounds"`
	Offset      int                             `json:"offset"`
}
type inputHistoryNavigation struct {
	Latest string `json:"latest"`
	Self   string `json:"self"`
	Newer  string `json:"newer,omitempty"`
	Older  string `json:"older,omitempty"`
}

func (c inputHistoryCursor) at(offset int) string {
	c.Offset = offset
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Caller holds Store.mu. Operator inspection sees retained evidence, including
// items excluded from automatic input, without changing consumption or links.
func (p *platformInputStorage) historyStatusLocked(bounds map[string]inputstream.Snapshot) (InboxStatus, error) {
	out := InboxStatus{MemberIdentity: p.id, Limits: DefaultInboxLimits()}
	usage, err := p.repository.InspectUsage()
	if err != nil {
		return out, err
	}
	pending, err := p.repository.PendingLocations(nil)
	if err != nil {
		return out, err
	}
	latest, retention := map[string]uint64{}, map[string]uint64{}
	for provider, snap := range bounds {
		latest[provider] = snap.Through
		if snap.Reclaimed > 0 {
			retention[provider] = snap.Reclaimed
		}
	}
	if shared := p.store.sharedHandle.Load(); shared != nil {
		v := shared.view.Load()
		if g, err := v.grant(inputMember(p.id)); err == nil {
			c, err := p.sharedConsumption(g, v.root.Inbox, false)
			if err != nil {
				return out, err
			}
			out.Shared = &sharedHistoryStatus{}
			latest["_shared"] = v.snapshot.Through
			if v.snapshot.Reclaimed > 0 {
				retention["_shared"] = v.snapshot.Reclaimed
			}
			for seq, rec := range v.records {
				if _, err := v.entry(p.id, rec.Location); err != nil {
					continue
				}
				out.Shared.Records++
				encoded, _ := json.Marshal(rec)
				out.Shared.Bytes += uint64(len(encoded) + 1)
				if seq > c.Through[v.streams[seq]] && c.Skipped[seq] == "" {
					out.Shared.Pending++
				}
			}
		}
	}
	out.RetainedEntries, out.Bytes = int(usage.Records), int64(usage.Bytes)
	out.History = &inputHistoryStatus{inputstream.Ref("history_", p.id, latest), inputstream.Ref("retention_", p.id, retention), len(pending), len(retention) > 0}
	if out.Shared != nil {
		out.History.Pending += out.Shared.Pending
	}
	return out, nil
}

func (p *platformInputStorage) inspectHistory() (InboxStatus, error) {
	if p.retired.Load() {
		return InboxStatus{}, ErrIdentity
	}
	if err := p.store.check(false); err != nil {
		return InboxStatus{}, err
	}
	v := p.projection.Load()
	if v == nil {
		return InboxStatus{}, ErrBusy
	}
	out := v.history
	if out.History != nil {
		copy := *out.History
		out.History = &copy
	}
	return out, v.err
}

func (p *platformInputStorage) previewHistory(cursor string) (inboxPreviewPage, error) {
	return inputOwnerValue(context.Background(), p, "preview_history", func(ctx context.Context) (inboxPreviewPage, error) { return p.previewHistoryOwned(cursor) })
}
func (p *platformInputStorage) previewHistoryOwned(cursor string) (inboxPreviewPage, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return inboxPreviewPage{}, err
	}
	bounds := p.repository.ExistingSnapshots()
	c := inputHistoryCursor{Owner: p.id, Bounds: bounds}
	shared := p.store.sharedHandle.Load()
	var sharedView *sharedInputView
	if shared != nil {
		sharedView = shared.view.Load()
		if g, err := sharedView.grant(inputMember(p.id)); err == nil {
			bounds["_shared"] = sharedView.snapshot
			c.SharedGrant = g.ID
		}
	}
	if cursor != "" {
		if len(cursor) > 32<<10 {
			return inboxPreviewPage{}, ErrRequest
		}
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		var saved inputHistoryCursor
		if err != nil || inputstream.StrictDecode(data, &saved) != nil || saved.Owner != p.id || saved.Bounds == nil ||
			saved.Offset < 0 || saved.Offset > inputstream.MaxRecords || saved.Offset%inputHistoryPageSize != 0 {
			return inboxPreviewPage{}, ErrRequest
		}
		c = saved
	}
	privateBounds := map[string]inputstream.Snapshot{}
	for provider, snap := range c.Bounds {
		if provider != "_shared" {
			privateBounds[provider] = snap
		}
	}
	unpin, err := p.repository.PinSnapshots(privateBounds)
	if errors.Is(err, inputstream.ErrExpired) {
		return inboxPreviewPage{}, ErrInboxExpired
	}
	if err != nil {
		return inboxPreviewPage{}, err
	}
	defer unpin()
	locations, err := p.repository.History(privateBounds)
	if errors.Is(err, inputstream.ErrExpired) {
		return inboxPreviewPage{}, ErrInboxExpired
	}
	if err != nil {
		return inboxPreviewPage{}, err
	}
	if bound, ok := c.Bounds["_shared"]; ok {
		if sharedView == nil {
			return inboxPreviewPage{}, ErrInboxExpired
		}
		g, err := sharedView.grant(inputMember(p.id))
		if err != nil || g.ID != c.SharedGrant || bound.Partition != sharedView.snapshot.Partition || bound.Through > sharedView.snapshot.Through || bound.Reclaimed != sharedView.snapshot.Reclaimed {
			return inboxPreviewPage{}, ErrInboxExpired
		}
		for seq, rec := range sharedView.records {
			if seq <= bound.Through {
				if _, err := sharedView.entry(p.id, rec.Location); err == nil {
					locations = append(locations, rec.Location)
				}
			}
		}
		times := map[inputstream.Location]time.Time{}
		for _, l := range locations {
			e, err := p.store.businessInputLocked(p.id, 0, &l)
			if err != nil {
				return inboxPreviewPage{}, err
			}
			times[l] = e.ReceivedAt
		}
		sort.Slice(locations, func(i, j int) bool {
			a, b := locations[i], locations[j]
			if !times[a].Equal(times[b]) {
				return times[a].After(times[b])
			}
			if a.Partition != b.Partition {
				return a.Partition < b.Partition
			}
			return a.Seq > b.Seq
		})
	}
	if c.Offset > len(locations) {
		return inboxPreviewPage{}, ErrRequest
	}
	end := min(c.Offset+inputHistoryPageSize, len(locations))
	page := InboxPage{MemberIdentity: p.id, Entries: []InboxEntry{}}
	// Render oldest first within each page, as in the existing operator view.
	for i := end - 1; i >= c.Offset; i-- {
		entry, err := p.store.businessInputLocked(p.id, 0, &locations[i])
		if err != nil {
			return inboxPreviewPage{}, err
		}
		page.Entries = append(page.Entries, entry)
	}
	status, err := p.historyStatusLocked(bounds)
	if err != nil {
		return inboxPreviewPage{}, err
	}
	out, err := p.store.projectInboxPreviewLocked(page, status)
	if err != nil {
		return out, err
	}
	fixedLatest := map[string]uint64{}
	for provider, bound := range c.Bounds {
		fixedLatest[provider] = bound.Through
	}
	out.History = &inputHistoryNavigation{Latest: inputstream.Ref("history_", p.id, fixedLatest), Self: c.at(c.Offset)}
	if sharedView != nil && c.SharedGrant != "" {
		g, _ := sharedView.grant(inputMember(p.id))
		consumption, err := p.sharedConsumption(g, sharedView.root.Inbox, false)
		if err != nil {
			return out, err
		}
		for i := range out.Entries {
			e := &out.Entries[i]
			if e.Partition == sharedView.snapshot.Partition {
				e.Shared = true
				e.Consumption = "pending"
				if consumption.Through[sharedView.streams[e.Seq]] >= e.Seq {
					e.Consumption = "confirmed"
				}
				if consumption.Skipped[e.Seq] != "" {
					e.Consumption = "skipped"
				}
			}
		}
	}
	if c.Offset > 0 {
		out.History.Newer = c.at(c.Offset - inputHistoryPageSize)
	}
	if end < len(locations) {
		out.History.Older = c.at(end)
	}
	return out, nil
}
