package room

import (
	"context"
	"errors"
	"time"

	"github.com/go-aie/actorx"
)

// AgentInbox owns one Membership's durable deliveries. The Store lock and an
// in-process reservation enforce a single writer; process exclusion comes from
// OpenStore. Service composition must Stop and Wait before closing the Store.
//
//actorx:actor
type AgentInbox struct {
	id          MemberIdentity
	dir         string
	store       *Store
	limits      InboxLimits
	receipts    map[string]inboxIndex
	latest      uint64
	bytes       int64
	committing  bool
	previewing  bool
	writeErr    error
	started     bool
	consumption inboxConsumption

	save    func(InboxEntry) error
	preview func(uint64, uint64, int) (InboxPage, error)
}

// SpawnAgentInbox reserves the Membership before loading, so even construction
// cannot race a previous writer. Zero limits reuse persisted limits or defaults.
// The Room service owns its lazy creation, recovery and lifecycle settlement.
func SpawnAgentInbox(rt *actorx.Runtime, store *Store, id MemberIdentity, limits InboxLimits) (actorx.Addr, error) {
	if err := validateMemberHeader(formatVersion, id); err != nil {
		return actorx.Addr{}, ErrIdentity
	}
	if limits != (InboxLimits{}) && !limits.valid() {
		return actorx.Addr{}, ErrRequest
	}
	a := &AgentInbox{id: id, store: store, receipts: make(map[string]inboxIndex)}
	if _, loaded := store.activeInboxes.LoadOrStore(id.MembershipID, a); loaded {
		return actorx.Addr{}, ErrBusy
	}
	if err := store.loadInbox(a, limits); err != nil {
		store.activeInboxes.CompareAndDelete(id.MembershipID, a)
		return actorx.Addr{}, err
	}
	if err := store.loadInboxConsumption(a); err != nil {
		store.activeInboxes.CompareAndDelete(id.MembershipID, a)
		return actorx.Addr{}, err
	}
	a.save = func(e InboxEntry) error { return store.saveInboxEntry(a, e) }
	a.preview = func(after, snapshot uint64, limit int) (InboxPage, error) {
		return store.previewInbox(a, after, snapshot, limit)
	}
	addr, err := rt.Spawn(a)
	if err != nil {
		store.activeInboxes.CompareAndDelete(id.MembershipID, a)
	}
	return addr, err
}

func (a *AgentInbox) Started(_ actorx.Context, _ actorx.Started) error {
	if a.store == nil || a.save == nil || a.preview == nil || a.started {
		return ErrRequest
	}
	a.started = true
	return nil
}

func (a *AgentInbox) Stopped(_ actorx.Context, _ actorx.Stopped) error {
	// Actorx joins owned tasks before Stopped, including a discarded commit.
	if a.store != nil {
		a.store.activeInboxes.CompareAndDelete(a.id.MembershipID, a)
	}
	return nil
}

func (a *AgentInbox) Receive(ctx actorx.Context, msg ReceiveInbox, reply actorx.Reply[InboxReceipt]) error {
	if msg.MemberIdentity != a.id {
		return ErrIdentity
	}
	if a.writeErr != nil {
		return a.writeErr
	}
	if a.committing {
		return ErrBusy
	}
	if !validInboxItem(msg.Item) {
		return ErrRequest
	}
	digest := inboxDigest(msg.Item)
	if previous, ok := a.receipts[msg.Item.DeliveryID]; ok {
		if previous.digest != digest {
			// Newly preserved candidate URLs must not poison redelivery of an older
			// receipt. Keep its original evidence and compare all other exact fields.
			legacy := msg.Item
			legacy.Source.URL = ""
			if previous.hasSourceURL || inboxDigest(legacy) != previous.digest {
				return ErrConflict
			}
		}
		reply.Complete(InboxReceipt{MemberIdentity: a.id, Seq: previous.seq, DeliveryID: msg.Item.DeliveryID}, nil)
		return nil
	}
	entry := InboxEntry{Version: 1, MemberIdentity: a.id, Seq: a.latest + 1, Item: msg.Item, ReceivedAt: time.Now().UTC()}
	entry.Item.CreatedAt = entry.Item.CreatedAt.UTC()
	size, err := inboxEntrySize(entry)
	if err != nil {
		return ErrRequest
	}
	if len(a.receipts) >= a.limits.Entries || size > a.limits.EntryBytes || a.bytes+size > a.limits.Bytes {
		return ErrCapacity
	}
	a.committing = true
	_, err = actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(ctx context.Context) (struct{}, error) {
		if err := ctx.Err(); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, a.save(entry)
	}, func(_ struct{}, err error) bool {
		a.committing = false
		if err != nil {
			if errors.Is(err, ErrCommitUnknown) {
				a.writeErr = err
			}
			reply.Complete(InboxReceipt{}, err)
			return true
		}
		a.latest, a.bytes = entry.Seq, a.bytes+size
		a.receipts[entry.Item.DeliveryID] = inboxIndex{entry.Seq, digest, entry.Item.Source.URL != ""}
		reply.Complete(InboxReceipt{MemberIdentity: a.id, Seq: entry.Seq, DeliveryID: entry.Item.DeliveryID}, nil)
		return true
	}, nil)
	if err != nil {
		a.committing = false
	}
	return err
}

func (a *AgentInbox) Preview(ctx actorx.Context, msg PreviewInbox, reply actorx.Reply[InboxPage]) error {
	if msg.MemberIdentity != a.id {
		return ErrIdentity
	}
	if a.previewing {
		return ErrBusy
	}
	snapshot := a.latest
	if msg.SnapshotSeq != nil {
		snapshot = *msg.SnapshotSeq
	}
	if msg.Limit < 0 || msg.Limit > a.limits.PageSize || snapshot > a.latest || msg.After > snapshot {
		return ErrRequest
	}
	limit := msg.Limit
	if limit == 0 {
		limit = a.limits.PageSize
	}
	a.previewing = true
	_, err := actorx.StartTask(ctx, func(ctx context.Context) (InboxPage, error) {
		if err := ctx.Err(); err != nil {
			return InboxPage{}, err
		}
		return a.preview(msg.After, snapshot, limit)
	}, func(page InboxPage, err error) bool {
		a.previewing = false
		reply.Complete(page, err)
		return true
	}, nil)
	if err != nil {
		a.previewing = false
	}
	return err
}

func (a *AgentInbox) Inspect(_ actorx.Context, msg InspectInbox) (InboxStatus, error) {
	if msg.MemberIdentity != a.id {
		return InboxStatus{}, ErrIdentity
	}
	return a.inboxStatus(), nil
}

func (a *AgentInbox) inboxStatus() InboxStatus {
	status := InboxStatus{MemberIdentity: a.id, LatestSeq: a.latest, ConsumedThrough: a.consumption.Through, Bytes: a.bytes, Limits: a.limits, Committing: a.committing, Previewing: a.previewing}
	if a.consumption.Batch != nil {
		b := *a.consumption.Batch
		status.Batch = &b
	}
	if a.writeErr != nil {
		status.WriteError = a.writeErr.Error()
	}
	return status
}
