package room

import (
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/go-aie/teamx/internal/inputstream"
)

const inputHistoryPageSize = 25

type inputHistoryStatus struct {
	Latest    string `json:"latest"`
	Retention string `json:"retention"`
	Pending   int    `json:"pending"`
	Reclaimed bool   `json:"reclaimed,omitempty"`
}

type inputHistoryCursor struct {
	Owner  MemberIdentity                  `json:"owner"`
	Bounds map[string]inputstream.Snapshot `json:"bounds"`
	Offset int                             `json:"offset"`
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
	out.RetainedEntries, out.Bytes = int(usage.Records), int64(usage.Bytes)
	out.History = &inputHistoryStatus{inputstream.Ref("history_", p.id, latest), inputstream.Ref("retention_", p.id, retention), len(pending), len(retention) > 0}
	return out, nil
}

func (p *platformInputStorage) inspectHistory() (InboxStatus, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return InboxStatus{}, err
	}
	return p.historyStatusLocked(p.repository.ExistingSnapshots())
}

func (p *platformInputStorage) previewHistory(cursor string) (inboxPreviewPage, error) {
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return inboxPreviewPage{}, err
	}
	bounds := p.repository.ExistingSnapshots()
	c := inputHistoryCursor{Owner: p.id, Bounds: bounds}
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
	locations, err := p.repository.History(c.Bounds)
	if errors.Is(err, inputstream.ErrExpired) {
		return inboxPreviewPage{}, ErrInboxExpired
	}
	if err != nil {
		return inboxPreviewPage{}, err
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
	if c.Offset > 0 {
		out.History.Newer = c.at(c.Offset - inputHistoryPageSize)
	}
	if end < len(locations) {
		out.History.Older = c.at(end)
	}
	return out, nil
}
