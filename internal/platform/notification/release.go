package notification

import (
	"context"

	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform"
)

type SourceReleaseCandidate struct {
	Owner    platform.RoomIdentity
	InboxID  string
	Location inputstream.Location
	Item     Item
	Digest   string
}
type SourceRelease struct {
	Candidate            SourceReleaseCandidate
	Status               string // unknown, held, released
	Reason               string
	Revision, Activation uint64
	Evidence             string
}

// CheckSourceRelease reads immutable committed source facts. Work is bounded by
// the target's fixed candidate page, including disabled and retired namespaces.
// Absence alone never proves release: the provider must prove a monotonic floor
// or a retained observation for that exact native identity.
func (m *Manager) CheckSourceRelease(ctx context.Context, candidates []SourceReleaseCandidate) ([]SourceRelease, error) {
	if len(candidates) > 128 {
		return nil, ErrCapacity
	}
	out := make([]SourceRelease, 0, len(candidates))
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		r := SourceRelease{Candidate: c, Status: "unknown", Reason: "source_evidence_unavailable"}
		item := c.Item
		key := Key{Owner: c.Owner, Source: SourceID{item.Source.Provider, item.Source.Channel}, SubscriptionID: item.Source.SubscriptionID}
		m.mu.Lock()
		p := m.owners[c.Owner]
		closing := m.stopping || m.retired[c.Owner]
		m.mu.Unlock()
		if p == nil || closing || m.host.Check(true) != nil {
			out = append(out, r)
			continue
		}
		r.Activation = p.epoch.Load()
		saved, ok := p.committed.Load().states[key]
		if !ok || c.InboxID != item.Route.Target.InboxKey() || !item.Route.Target.Valid() || item.Route.Version == 0 ||
			item.Route.Version > saved.state.Metadata().Version || c.Location.Partition == "" || c.Location.Seq == 0 || c.Digest == "" ||
			item.Key != item.Source.ItemKey || item.DeliveryID != item.Route.DeliveryID(DeliveryID(key, item.Key)) || saved.state.Metadata().Target != item.Route.Target {
			out = append(out, r)
			continue
		}
		r.Revision = saved.revision
		if pending, ok := saved.state.Notification(item.Key); ok {
			if pending.DeliveryID == item.DeliveryID && pending.Route == item.Route {
				r.Status, r.Reason = "held", "source_outstanding"
			}
		} else if source, ok := saved.state.(interface{ SourceTerminal(Item) (string, bool) }); ok {
			if reason, valid := source.SourceTerminal(item); valid {
				r.Status, r.Reason = "released", reason
				r.Evidence = inputstream.Ref("source_terminal_", key, r.Revision, c)
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func (m *Manager) ReleaseCurrent(r SourceRelease) bool {
	m.mu.Lock()
	p := m.owners[r.Candidate.Owner]
	stopping := m.stopping || m.retired[r.Candidate.Owner]
	m.mu.Unlock()
	return p != nil && !stopping && p.epoch.Load() == r.Activation && m.host.Check(true) == nil
}
