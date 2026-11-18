package inputstream

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// ImportEntry is one retained delivery with its legacy consumption fact.
// ImportPartition is used only while building an unpublished upgrade tree.
type ImportEntry struct {
	AppendRequest
	Consumed bool
	Excluded bool
}

func (r *Repository) ImportPartition(provider string, reclaimed, latest uint64, entries []ImportEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.codecs[provider]
	if c == nil || r.parts[provider] != nil || r.coordinator != nil || len(r.capacity.Pending) != 0 || r.uncertain {
		return ErrConflict
	}
	if latest == 0 || latest < reclaimed {
		return ErrInvalid
	}
	descriptor := c.Descriptor()
	spec := descriptor.Spec(r.member)
	root := rootState{Version: FormatVersion, Spec: spec, Revision: 1, Latest: latest, Reclaimed: reclaimed,
		Receipts: map[string]Receipt{}, Consumption: true, LocalOnly: true}
	state := consumptionState{Version: 2, Owner: r.member, Partition: spec.Ref, Revision: 1,
		Through: map[string]uint64{}, Skipped: map[uint64]string{}}
	part := &partition{state: root, records: map[uint64]StoredRecord{}, offsets: map[uint64]offset{}, projections: map[uint64]Projection{},
		streamRefs: map[uint64]string{}, streamSeqs: map[string][]uint64{}}
	total := r.usageLocked()
	if uint64(len(entries)) > spec.Policy.MaxRecords || total.Records+uint64(len(entries)) > r.capacity.Limits.Records || len(entries) > spec.Policy.MaxReceipts {
		return &CapacityError{Scope: "import records", Partition: spec.Ref}
	}
	var buffer bytes.Buffer
	var seg segment
	dir := filepath.Join(r.partDir(provider), "records")
	flush := func() error {
		if buffer.Len() == 0 {
			return nil
		}
		seg.Bytes, seg.Hash = int64(buffer.Len()), Digest(buffer.Bytes())
		if err := durableDir(dir); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(dir, seg.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, err = buffer.WriteTo(f)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := syncDir(dir); err != nil {
			return err
		}
		root.Segments = append(root.Segments, seg)
		return nil
	}
	var bytesUsed uint64
	previous := reclaimed
	for _, entry := range entries {
		q := entry.AppendRequest
		if q.Seq <= previous || q.Seq > latest || !validKey(q.DeliveryID) || !validSourceRef(q.SourceRef) ||
			q.DeliveryDigest == "" || !slices.Contains(spec.Channels, q.Channel) || q.CreatedAt.IsZero() || q.ReceivedAt.IsZero() {
			return ErrInvalid
		}
		key := deliveryKey(q.Channel, q.DeliveryID)
		if _, exists := root.Receipts[key]; exists {
			return ErrConflict
		}
		projection, err := decodeProjection(c, descriptor.Schema, q.Payload)
		if err != nil {
			return err
		}
		rec := StoredRecord{Location: Location{spec.Ref, q.Seq}, DeliveryID: q.DeliveryID, Channel: q.Channel,
			Codec: descriptor.Codec, Schema: descriptor.Schema, Group: projection.Group, CreatedAt: q.CreatedAt.UTC(),
			ReceivedAt: q.ReceivedAt.UTC(), Payload: q.Payload, DeliveryDigest: q.DeliveryDigest}
		rec.Digest = rec.checksum()
		if _, err := r.validateRecord(provider, rec); err != nil {
			return err
		}
		line, err := encodeLine(rec)
		if err != nil {
			return err
		}
		bytesUsed += uint64(len(line))
		if len(line) > spec.Policy.MaxRecordBytes || bytesUsed > spec.Policy.MaxBytes || total.Bytes+bytesUsed > r.capacity.Limits.Bytes {
			return &CapacityError{Scope: "import bytes", Partition: spec.Ref, FirstProtected: q.Seq}
		}
		if buffer.Len() > 0 && int64(buffer.Len()+len(line)) > r.segmentBytes {
			if err := flush(); err != nil {
				return err
			}
		}
		if buffer.Len() == 0 {
			seg = segment{Name: fmt.Sprintf("%020d.jsonl", q.Seq), First: q.Seq}
		}
		seg.Last, previous = q.Seq, q.Seq
		part.records[q.Seq] = rec
		part.offsets[q.Seq] = offset{seg.Name, int64(buffer.Len()), len(line)}
		part.index(r.member, rec, projection)
		buffer.Write(line)
		root.Receipts[key] = Receipt{Partition: spec.Ref, Seq: q.Seq, Key: q.DeliveryID, Digest: q.DeliveryDigest,
			Operation: Ref("append_", spec.Ref, q.DeliveryID, q.DeliveryDigest, q.Seq), Channel: q.Channel, SourceRef: q.SourceRef}
		if entry.Excluded {
			state.Skipped[q.Seq] = "legacy_exclusion"
		} else if entry.Consumed {
			state.Through[part.streamRefs[q.Seq]] = q.Seq
		}
	}
	if err := flush(); err != nil {
		return err
	}
	state.Checksum = checksumState(state)
	if err := r.save(r.consumptionPath(provider), state); err != nil {
		return err
	}
	if err := r.saveRoot(provider, root); err != nil {
		return err
	}
	if err := r.saveCapacity(); err != nil {
		return err
	}
	part.state = root
	r.parts[provider] = part
	return nil
}
