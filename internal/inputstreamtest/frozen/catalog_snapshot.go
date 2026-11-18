package frozen

// CatalogPartition is a caller-owned immutable snapshot. It supports domain
// backends and isolated contract fixtures using the same query implementation;
// it contains no consumption state and grants no write authority.
type CatalogPartition struct {
	Snapshot Snapshot
	Codec    Codec
	Records  []StoredRecord
}

func NewSnapshotCatalog(owner Member, turn uint64, parts []CatalogPartition, o Observation) (*Catalog, error) {
	if !owner.Valid() || len(parts) > 64 {
		return nil, ErrIdentity
	}
	r := &Repository{member: owner, parts: map[string]*partition{}, codecs: map[string]Codec{}}
	count := 0
	for _, part := range parts {
		if part.Codec == nil {
			return nil, ErrInvalid
		}
		d := part.Codec.Descriptor()
		if r.parts[d.Provider] != nil {
			return nil, ErrConflict
		}
		p := &partition{state: rootState{Spec: d.Spec(owner), Revision: part.Snapshot.Revision, Latest: part.Snapshot.Through, Reclaimed: part.Snapshot.Reclaimed}, records: map[uint64]StoredRecord{}, projections: map[uint64]Projection{}, search: map[uint64]string{}, native: part.Codec.NewIndex()}
		p.state.Spec.Ref = part.Snapshot.Partition
		for _, rec := range part.Records {
			count++
			if count > MaxRecords || rec.Location.Partition != part.Snapshot.Partition || rec.Location.Seq > part.Snapshot.Through || p.records[rec.Location.Seq].Location.Seq != 0 {
				return nil, ErrInvalid
			}
			v, err := part.Codec.Decode(rec.Schema, rec.Payload)
			if err != nil {
				return nil, err
			}
			if err := v.Validate(); err != nil {
				return nil, err
			}
			p.records[rec.Location.Seq] = rec
			p.projections[rec.Location.Seq] = v
			p.search[rec.Location.Seq] = normalizeSearch(v.Label + " " + v.SourceLabel + " " + v.Text)
			p.state.Scopes = mergeScopes(p.state.Scopes, d.Provider, rec.Channel, v, rec.Location.Seq)
			if p.native != nil {
				if err := p.native.Add(rec); err != nil {
					return nil, err
				}
			}
		}
		r.parts[d.Provider] = p
		r.codecs[d.Provider] = part.Codec
	}
	return r.Catalog(turn, o)
}
