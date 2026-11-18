package frozen

func (c *Catalog) AddOwnedStream(snap Snapshot, summary StreamSummary, records []StoredRecord, codec Codec) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if codec == nil || summary.Kind != "room" || summary.Ref == "" || len(records) > MaxRecords {
		return ErrInvalid
	}
	d := codec.Descriptor()
	native := codec.NewIndex()
	source := summary.Ref
	c.providers[d.Provider] = catalogProvider{snap, native, 1, codec}
	c.scopes[source] = catalogScope{source, source, source, d.Provider, summary.Label, true}
	if len(records) == 0 {
		return nil
	}
	summary.Pending = len(records)
	summary.Attention = "normal"
	c.streams[source] = summary
	for _, rec := range records {
		if rec.Location.Partition != snap.Partition || rec.Location.Seq > snap.Through {
			return ErrIdentity
		}
		p, err := codec.Decode(rec.Schema, rec.Payload)
		if err != nil {
			return err
		}
		if native != nil {
			if err := native.Add(rec); err != nil {
				return err
			}
		}
		c.streamEntries[source] = append(c.streamEntries[source], len(c.entries))
		c.entries = append(c.entries, catalogEntry{location: rec.Location, stream: source, source: source, search: normalizeSearch(summary.Label + " " + p.Text), text: p.Text, scopes: []string{source}, at: rec.ReceivedAt})
	}
	return nil
}
