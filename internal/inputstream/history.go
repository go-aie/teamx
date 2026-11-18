package inputstream

import (
	"sort"
	"time"
)

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
