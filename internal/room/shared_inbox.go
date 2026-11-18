//go:generate go run github.com/go-aie/actorx/cmd/actorx-gen

package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/gitlab"
	"github.com/go-aie/teamx/internal/platform/notification"
)

const sharedProjectInbox = "project"

func sharedInboxIdentity(id RoomIdentity) inputstream.Inbox {
	return inputstream.Inbox{RoomID: id.RoomID, RoomInstance: id.RoomInstance, ID: sharedProjectInbox}
}
func inputMember(id MemberIdentity) inputstream.Member {
	return inputstream.Member{RoomID: id.RoomID, RoomInstance: id.RoomInstance, MembershipID: id.MembershipID}
}

type sharedInputView struct {
	root     inputstream.SharedRoot
	snapshot inputstream.Snapshot
	records  map[uint64]inputstream.StoredRecord
	streams  map[uint64]string
}
type sharedInboxMaintenance struct{ run func(context.Context) error }

type sharedMaintenanceStatus struct {
	Partition string
	First     uint64
	Source    string
}

type sharedInputStorage struct {
	maintenanceStatus atomic.Pointer[sharedMaintenanceStatus]
	service           atomic.Pointer[service]
	dirty             atomic.Bool
	maintenance       sync.Mutex
	store             *Store
	repository        *inputstream.Repository
	owner             actorx.Addr
	view              atomic.Pointer[sharedInputView]
	operations        atomic.Uint64
}

// InboxActor owns the shared body, Grant transitions and persistent protections.
// Waiting on member/source evidence happens outside its mutation slot.
//
//actorx:actor
type InboxActor struct {
	p             *sharedInputStorage
	busy, closing bool
	queue         []sharedInputOperation
	quiesce       []actorx.Reply[struct{}]
}
type sharedInputOperation struct {
	ID    uint64
	run   func() (any, error)
	reply actorx.Reply[any]
}
type quiesceSharedInputs struct{}

func (a *InboxActor) Started(ctx actorx.Context, _ actorx.Started) error {
	return a.startMaintenance(ctx)
}
func (a *InboxActor) startMaintenance(owner actorx.Context) error {
	_, err := actorx.StartTask(owner, func(ctx context.Context) (struct{}, error) {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return struct{}{}, ctx.Err()
		case <-timer.C:
		}
		svc := a.p.service.Load()
		if svc == nil || svc.closing.Load() || svc.restarting.Load() {
			return struct{}{}, nil
		}
		hook := svc.maintainShared.Load()
		if hook == nil {
			return struct{}{}, nil
		}
		return struct{}{}, hook.run(ctx)
	}, func(_ struct{}, _ error) bool {
		if !a.closing && owner.Err() == nil {
			if err := a.startMaintenance(owner); err != nil {
				owner.Stop(err)
			}
		}
		return true
	}, nil)
	return err
}
func (a *InboxActor) Operate(ctx actorx.Context, op sharedInputOperation, reply actorx.Reply[any]) error {
	if op.ID == 0 || op.run == nil {
		return ErrIdentity
	}
	if a.closing {
		return ErrClosing
	}
	if len(a.queue) >= 64 {
		return ErrBusy
	}
	op.reply = reply
	a.queue = append(a.queue, op)
	a.dispatch(ctx)
	return nil
}
func (a *InboxActor) dispatch(ctx actorx.Context) {
	if a.busy {
		return
	}
	if len(a.queue) == 0 {
		for _, r := range a.quiesce {
			r.Complete(struct{}{}, nil)
		}
		a.quiesce = nil
		return
	}
	op := a.queue[0]
	a.queue[0] = sharedInputOperation{}
	a.queue = a.queue[1:]
	a.busy = true
	_, err := actorx.StartTask(ctx.WithContext(context.WithoutCancel(ctx)), func(context.Context) (any, error) {
		if err := a.p.store.check(true); err != nil {
			return nil, err
		}
		v, err := op.run()
		if errors.Is(err, inputstream.ErrCommitUnknown) {
			a.p.store.markUncertain()
		}
		if err == nil {
			err = a.p.publish()
		}
		return v, err
	}, func(v any, err error) bool { a.busy = false; op.reply.Complete(v, err); a.dispatch(ctx); return true }, nil)
	if err != nil {
		a.busy = false
		op.reply.Complete(nil, err)
		a.dispatch(ctx)
	}
}
func (a *InboxActor) Quiesce(_ actorx.Context, _ quiesceSharedInputs, reply actorx.Reply[struct{}]) error {
	a.closing = true
	if !a.busy && len(a.queue) == 0 {
		reply.Complete(struct{}{}, nil)
	} else {
		a.quiesce = append(a.quiesce, reply)
	}
	return nil
}
func sharedOwnerValue[T any](ctx context.Context, p *sharedInputStorage, fn func() (T, error)) (T, error) {
	var zero T
	v, err := p.owner.Call[any](ctx, sharedInputOperation{ID: p.operations.Add(1), run: func() (any, error) { return fn() }})
	if err != nil {
		return zero, err
	}
	out, ok := v.(T)
	if !ok {
		return zero, ErrInvalid
	}
	return out, nil
}
func (p *sharedInputStorage) publish() error {
	root, err := p.repository.SharedState()
	if err != nil {
		return err
	}
	snap, err := p.repository.Snapshot("gitlab")
	if err != nil {
		return err
	}
	v := &sharedInputView{root: root, snapshot: snap}
	old := p.view.Load()
	if old != nil && old.snapshot.Through == snap.Through && old.snapshot.Reclaimed == snap.Reclaimed {
		v.records = old.records
		v.streams = old.streams
	} else {
		v.records = map[uint64]inputstream.StoredRecord{}
		v.streams = map[uint64]string{}
		locations, err := p.repository.Scan(snap)
		if err != nil {
			return err
		}
		for _, l := range locations {
			rec, err := p.repository.Read(snap, l)
			if err != nil {
				return err
			}
			v.records[l.Seq] = rec
			physical, err := p.repository.StreamAt(l)
			if err != nil {
				return err
			}
			v.streams[l.Seq] = physical
		}
	}
	p.view.Store(v)
	return nil
}
func (s *Store) sharedInputs(create bool) (*sharedInputStorage, error) {
	s.inputOwners.mu.Lock()
	defer s.inputOwners.mu.Unlock()
	if s.inputOwners.closed {
		return nil, ErrClosing
	}
	if s.sharedInput != nil {
		return s.sharedInput, nil
	}
	inbox := sharedInboxIdentity(RoomIdentity{RoomID: s.roomID, RoomInstance: s.instance})
	if !create {
		if _, err := os.Stat(s.dir + "/shared-inboxes/" + inbox.ID + "/partition.json"); errors.Is(err, os.ErrNotExist) {
			return nil, nil
		} else if err != nil {
			return nil, err
		}
	}
	if err := s.check(true); err != nil {
		return nil, err
	}
	codec := gitlab.InputCapability().Codec
	repo, err := inputstream.OpenSharedRepository(s.dir, inbox, "project-events", codec, inputPersistence{s})
	if err != nil {
		return nil, err
	}
	p := &sharedInputStorage{store: s, repository: repo}
	if err = p.publish(); err != nil {
		return nil, err
	}
	if s.inputOwners.runtime == nil {
		s.inputOwners.runtime, err = actorx.NewRuntime(context.Background(), actorx.WithInstanceLimit(maxMembers+1))
		if err != nil {
			return nil, err
		}
	}
	p.owner, err = s.inputOwners.runtime.Spawn(&InboxActor{p: p})
	if err != nil {
		return nil, err
	}
	s.sharedInput = p
	s.sharedHandle.Store(p)
	return p, nil
}
func (v *sharedInputView) grant(m inputstream.Member) (inputstream.SharedGrant, error) {
	for _, g := range v.root.Grants {
		if g.Member == m && g.Status == "active" {
			return g, nil
		}
	}
	return inputstream.SharedGrant{}, ErrIdentity
}
func (v *sharedInputView) entry(id MemberIdentity, l inputstream.Location) (InboxEntry, error) {
	g, err := v.grant(inputMember(id))
	if err != nil {
		return InboxEntry{}, err
	}
	if l.Partition != v.snapshot.Partition {
		return InboxEntry{}, ErrIdentity
	}
	if l.Seq <= v.snapshot.Reclaimed {
		return InboxEntry{}, inputstream.ErrExpired
	}
	rec, ok := v.records[l.Seq]
	if !ok {
		return InboxEntry{}, inputstream.ErrCorrupt
	}
	allowed := false
	for _, recipient := range rec.Recipients {
		if recipient == g.ID {
			allowed = true
		}
	}
	if !allowed {
		return InboxEntry{}, ErrIdentity
	}
	item, err := gitlab.InputCapability().DecodeStoredInput(rec)
	if err != nil {
		return InboxEntry{}, err
	}
	return InboxEntry{Version: formatVersion, MemberIdentity: id, Location: &l, Seq: l.Seq, Item: item, ReceivedAt: rec.ReceivedAt, sourceRef: inputstream.Ref("src_", v.root.Inbox, inputMember(id), g.ID, l, item.DeliveryID)}, nil
}
func (s *service) receiveSharedInbox(ctx context.Context, item notification.Item) error {
	if !s.unifiedInputs || item.Route.Target.InboxID != sharedProjectInbox || item.Source.Provider != "gitlab" || item.Source.Channel != "events" || !validInboxItem(item) {
		return ErrIdentity
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return ErrInvalid
	}
	if len(encoded) > inputstream.MaxRecordBytes {
		return inputstream.ErrCapacity
	}
	var frozen notification.Item
	if err := json.Unmarshal(encoded, &frozen); err != nil {
		return err
	}
	item = frozen
	p, err := s.store.sharedInputs(false)
	if err != nil {
		return err
	}
	if p == nil {
		return ErrIdentity
	}
	_, err = sharedOwnerValue(ctx, p, func() (inputstream.Receipt, error) {
		q, err := gitlab.InputCapability().EncodeStoredInput(item)
		if err != nil {
			return inputstream.Receipt{}, err
		}
		q.ReceivedAt = time.Now()
		return p.repository.AppendOnce("gitlab", q)
	})
	if err == nil {
		s.sharedInputsChanged(p)
	}
	return err
}
func (s *service) sharedInputsChanged(p *sharedInputStorage) {
	for _, g := range p.view.Load().root.Grants {
		if g.Status != "active" {
			continue
		}
		if v, ok := s.store.inputHandles.Load(g.Member.MembershipID); ok {
			member := v.(*platformInputStorage)
			member.projectionDirty.Store(true)
		}
	}
	s.store.changed("inbox", MemberIdentity{RoomIdentity: s.info.RoomIdentity})
}
