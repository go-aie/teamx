package inputstream

import (
	"sort"
	"time"
)

// RetainedStream is a rebuildable metadata view, independent of delivery,
// admission, preferences and business associations. Locations are in native
// publication order. Payloads are read only after a caller selects a page.
type RetainedStream struct {
	Ref, Provider string
	Projection    Projection
	Snapshot      Snapshot
	Locations     []Location
	Latest        time.Time
}

// RetainedProjection uses the already validated metadata index, including when
// a shared reader must choose its label from a frozen recipient-visible row.
func (r *Repository) RetainedProjection(l Location) (Projection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return Projection{}, ErrCommitUnknown
	}
	for _, p := range r.parts {
		if p.state.Spec.Ref != l.Partition {
			continue
		}
		if l.Seq <= p.state.Reclaimed {
			return Projection{}, ErrExpired
		}
		v, ok := p.projections[l.Seq]
		if !ok {
			return Projection{}, ErrCorrupt
		}
		v.Group.Context = append([]string(nil), v.Group.Context...)
		return v, nil
	}
	return Projection{}, ErrIdentity
}

func (r *Repository) RetainedStreams() ([]RetainedStream, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	out := []RetainedStream{}
	for provider, p := range r.parts {
		for ref, seqs := range p.streamSeqs {
			v := RetainedStream{Ref: ref, Provider: provider, Snapshot: snapshot(p)}
			for _, seq := range seqs {
				if seq <= p.state.Reclaimed {
					continue
				}
				rec, ok := p.records[seq]
				if !ok {
					return nil, ErrCorrupt
				}
				v.Locations = append(v.Locations, rec.Location)
				v.Projection = p.projections[seq]
				if rec.ReceivedAt.After(v.Latest) {
					v.Latest = rec.ReceivedAt
				}
			}
			if len(v.Locations) > 0 {
				v.Projection.Group.Context = append([]string(nil), v.Projection.Group.Context...)
				out = append(out, v)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out, nil
}

// History returns retained positions in newest-received order. It reads the
// published index without decoding bodies or consulting Agent checkpoints.
func (r *Repository) History(bounds map[string]Snapshot) ([]Location, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uncertain {
		return nil, ErrCommitUnknown
	}
	type row struct {
		location Location
		received time.Time
	}
	var rows []row
	for provider, bound := range bounds {
		p := r.parts[provider]
		if p == nil || p.state.Spec.Ref != bound.Partition || bound.Through > p.state.Latest || bound.Reclaimed > bound.Through {
			return nil, ErrIdentity
		}
		if bound.Reclaimed != p.state.Reclaimed {
			return nil, ErrExpired
		}
		for seq, rec := range p.records {
			if seq <= bound.Through {
				rows = append(rows, row{rec.Location, rec.ReceivedAt})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if !a.received.Equal(b.received) {
			return a.received.After(b.received)
		}
		if a.location.Partition != b.location.Partition {
			return a.location.Partition < b.location.Partition
		}
		return a.location.Seq > b.location.Seq
	})
	out := make([]Location, len(rows))
	for i, row := range rows {
		out[i] = row.location
	}
	return out, nil
}
