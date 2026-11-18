package room

import (
	"context"
	"encoding/json"
	core "github.com/go-aie/teamx/internal/inputstream"
	frozen "github.com/go-aie/teamx/internal/inputstreamtest/frozen"
	"github.com/go-aie/teamx/internal/platform/notification"
)

func (p *platformInputStorage) legacyCatalog(turn uint64, o core.Observation) (*frozen.Catalog, error) {
	parts := []frozen.CatalogPartition{}
	for provider, snap := range p.repository.ExistingSnapshots() {
		codec := p.store.notificationCapabilities(provider).Input.Codec
		query := frozen.PlatformQueryCodec(codec)
		if approvals, ok := codec.(approvalFixtureCodec); ok {
			query = approvalQueryCodec{approvals}
		}
		if query == nil {
			return nil, core.ErrUnsupported
		}
		part := frozen.CatalogPartition{Snapshot: snap, Codec: query}
		locations, err := p.repository.Scan(snap)
		if err != nil {
			return nil, err
		}
		for _, l := range locations {
			rec, err := p.repository.Read(snap, l)
			if err != nil {
				return nil, err
			}
			part.Records = append(part.Records, rec)
		}
		parts = append(parts, part)
	}
	return frozen.NewSnapshotCatalog(core.Member{RoomID: p.id.RoomID, RoomInstance: p.id.RoomInstance, MembershipID: p.id.MembershipID}, turn, parts, frozen.Observation{Bounds: o.Bounds, ObservedAt: o.ObservedAt, Excluded: o.Excluded, Blocked: o.Blocked, Preferences: frozen.Preferences{LastSource: o.Preferences.LastSource, Version: o.Preferences.Version, Attention: o.Preferences.Attention, LastExposed: o.Preferences.LastExposed}})
}
func (s *inputSession) legacyCatalog(ctx context.Context) (*frozen.Catalog, error) {
	if err := s.check(ctx); err != nil {
		return nil, err
	}
	p := s.storage
	p.store.mu.Lock()
	o, err := p.repository.ConsumptionObservation(s.execution)
	if err != nil {
		p.store.mu.Unlock()
		return nil, err
	}
	o.Preferences, err = p.store.inputObservationLocked(p.id)
	if err != nil {
		p.store.mu.Unlock()
		return nil, err
	}
	o.Blocked = map[core.Location]string{}
	for _, snap := range p.repository.ExistingSnapshots() {
		ls, err := p.repository.Scan(snap)
		if err != nil {
			p.store.mu.Unlock()
			return nil, err
		}
		for _, l := range ls {
			if o.Excluded[l] {
				continue
			}
			e, err := p.store.businessInputLocked(p.id, 0, &l)
			if err != nil {
				p.store.mu.Unlock()
				return nil, err
			}
			ad, err := p.store.inboxAdmissionLocked(e)
			if err != nil {
				p.store.mu.Unlock()
				return nil, err
			}
			if !ad.Verified {
				o.Blocked[l] = "source_admission_unverified"
			} else if ad.Decision == notification.AdmissionExcluded {
				o.Excluded[l] = true
			}
		}
	}
	c, err := p.legacyCatalog(s.execution.Turn, o)
	p.store.mu.Unlock()
	if err != nil || !s.unified {
		return c, err
	}
	state, err := p.store.LoadRoom()
	if err != nil {
		return nil, err
	}
	var through uint64
	for _, m := range state.Members {
		if m.MembershipID == p.id.MembershipID {
			through = m.ReadThrough
		}
	}
	records := []core.StoredRecord{}
	for _, m := range state.Messages {
		if m.Seq <= through || !roomDeliverable(m, p.id) {
			continue
		}
		b, _ := json.Marshal(m)
		records = append(records, core.StoredRecord{Location: core.Location{Partition: roomPartitionRef(p.id.RoomIdentity), Seq: m.Seq}, Schema: 1, Payload: b, ReceivedAt: m.CreatedAt})
	}
	err = c.AddOwnedStream(core.Snapshot{Partition: roomPartitionRef(p.id.RoomIdentity), Revision: state.Revision, Through: state.LatestSeq, Reclaimed: state.ClearedThrough}, frozen.StreamSummary{Ref: roomStreamRef(p.id), Kind: "room", Label: "当前 Room", Hint: "有待交付的人类消息；有待交付的交互事件"}, records, roomInputCodec{})
	return c, err
}
