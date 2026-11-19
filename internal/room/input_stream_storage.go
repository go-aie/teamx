package room

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// platformInputStorage is the private body and consumer engine of one owner.
// Store.mu protects only the finite local persistence/association boundary.
type platformInputStorage struct {
	historyMu         sync.Mutex
	historyHandoff    sync.RWMutex
	history           *historyQueries
	admissions        map[inputstream.Location]notification.Admission
	retired           atomic.Bool
	projectionDirty   atomic.Bool
	service           atomic.Pointer[service]
	admissionProvider string
	admissionAfter    uint64
	maintenanceNext   int
	owner             actorx.Addr
	operations        atomic.Uint64
	projection        atomic.Pointer[inputProjection]
	catalogs          []*inputstream.Catalog
	generation        string
	store             *Store
	id                MemberIdentity
	repository        *inputstream.Repository
	specs             []inputstream.PartitionSpec
}

// Called by the member owner. Cap retained immutable cursor views per execution.
func (p *platformInputStorage) keepInputCatalog(c *inputstream.Catalog) {
	p.catalogs = append(p.catalogs, c)
	if len(p.catalogs) > 4 {
		p.catalogs[0] = nil
		p.catalogs = p.catalogs[1:]
	}
}

type inputPersistence struct{ store *Store }

func (p inputPersistence) SaveInputJSON(path string, v any) error {
	err := p.store.save(path, v, 16<<20)
	if errors.Is(err, ErrCommitUnknown) {
		return errors.Join(inputstream.ErrCommitUnknown, err)
	}
	return err
}

// Project the existing static source assembly; there is no input-only registry.
// Multiple channels of a provider share the same codec and physical partition.
func inputStorageCodecs(adapters map[notification.SourceID]notificationAdapter, capabilities func(string) notification.Capabilities) ([]inputstream.Codec, error) {
	providers := map[string]inputstream.Codec{}
	channels := map[string][]string{}
	for id, a := range adapters {
		if a.Source == nil || a.Source.ID() != id {
			return nil, inputstream.ErrInvalid
		}
		cap := capabilities(id.Provider).Input
		if cap == nil {
			continue
		}
		if cap.Codec == nil || cap.EncodeStoredInput == nil {
			return nil, inputstream.ErrInvalid
		}
		d := cap.Codec.Descriptor()
		if d.Provider != id.Provider {
			return nil, inputstream.ErrIdentity
		}
		if old := providers[id.Provider]; old != nil && !reflect.DeepEqual(old.Descriptor(), d) {
			return nil, inputstream.ErrConflict
		}
		providers[id.Provider] = cap.Codec
		channels[id.Provider] = append(channels[id.Provider], id.Channel)
	}
	names := []string{}
	for p := range providers {
		names = append(names, p)
	}
	sort.Strings(names)
	out := []inputstream.Codec{}
	for _, p := range names {
		sort.Strings(channels[p])
		out = append(out, assembledInputCodec{providers[p], channels[p]})
	}
	return out, nil
}

type assembledInputCodec struct {
	inputstream.Codec
	channels []string
}

func (c assembledInputCodec) Descriptor() inputstream.Descriptor {
	d := c.Codec.Descriptor()
	d.Channels = append([]string(nil), c.channels...)
	return d
}
func (c assembledInputCodec) ValidateStoredRecord(rec inputstream.StoredRecord) error {
	if v, ok := c.Codec.(inputstream.StoredRecordValidator); ok {
		return v.ValidateStoredRecord(rec)
	}
	return nil
}
func (s *Store) openPlatformInputStorage(id MemberIdentity, adapters map[notification.SourceID]notificationAdapter) (*platformInputStorage, error) {
	s.inputOwners.mu.Lock()
	defer s.inputOwners.mu.Unlock()
	if s.inputOwners.closed {
		return nil, ErrClosing
	}
	// Composition happens before acquiring the original Store lock.
	codecs, err := inputStorageCodecs(adapters, s.notificationCapabilities)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(true); err != nil {
		return nil, err
	}
	if _, _, err := s.memberPath(id, "agent.json", false); err != nil {
		return nil, err
	}
	owner := inputstream.Member{RoomID: id.RoomID, RoomInstance: id.RoomInstance, MembershipID: id.MembershipID}
	specs := []inputstream.PartitionSpec{}
	for _, codec := range codecs {
		specs = append(specs, codec.Descriptor().Spec(owner))
	}
	if old := s.platformInputs[id.MembershipID]; old != nil {
		if old.retired.Load() {
			return nil, ErrIdentity
		}
		if old.id != id || !reflect.DeepEqual(old.specs, specs) {
			return nil, ErrConflict
		}
		return old, nil
	}
	repo, err := inputstream.OpenRepository(s.dir, owner, codecs, inputPersistence{s})
	if err != nil {
		return nil, err
	}
	handle := &platformInputStorage{store: s, id: id, repository: repo, specs: specs}
	if state, exists, err := repo.CurrentState(); err != nil {
		return nil, err
	} else if exists {
		handle.generation = state.Execution.Generation
	}
	if s.inputOwners.runtime == nil {
		s.inputOwners.runtime, err = actorx.NewRuntime(context.Background(), actorx.WithInstanceLimit(maxMembers+1))
		if err != nil {
			return nil, err
		}
	}
	handle.owner, err = s.inputOwners.runtime.Spawn(&MemberInputActor{p: handle})
	if err != nil {
		return nil, err
	}
	if s.platformInputs == nil {
		s.platformInputs = map[string]*platformInputStorage{}
	}
	s.platformInputs[id.MembershipID] = handle
	s.inputHandles.Store(id.MembershipID, handle)
	return handle, nil
}
func (p *platformInputStorage) checkLocked() error {
	if err := p.store.check(true); err != nil {
		return err
	}
	_, _, err := p.store.memberPath(p.id, "agent.json", false)
	return err
}
func (p *platformInputStorage) result(err error) error {
	if errors.Is(err, inputstream.ErrCommitUnknown) {
		p.store.markUncertain()
	}
	return err
}

// Receive is local delivery to an already frozen target. It never acknowledges
// the source; the caller acknowledges only after this durable receipt returns.
func (p *platformInputStorage) Receive(item notification.Item, receivedAt time.Time) (inputstream.Receipt, error) {
	// Freeze nested native/content fields before the request can wait in a mailbox.
	encoded, err := json.Marshal(item)
	if err != nil {
		return inputstream.Receipt{}, ErrInvalid
	}
	if len(encoded) > inputstream.MaxRecordBytes {
		return inputstream.Receipt{}, inputstream.ErrCapacity
	}
	var frozen notification.Item
	if err := json.Unmarshal(encoded, &frozen); err != nil {
		return inputstream.Receipt{}, err
	}
	return inputOwnerValue(context.Background(), p, "receive", func(ctx context.Context) (inputstream.Receipt, error) {
		return p.receiveInput(frozen, receivedAt)
	})
}
func (p *platformInputStorage) receiveInput(item notification.Item, receivedAt time.Time) (inputstream.Receipt, error) {
	s := p.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return inputstream.Receipt{}, err
	}
	if !validInboxItem(item) {
		return inputstream.Receipt{}, ErrInvalid
	}
	if !item.Route.Target.Valid() || item.Route.Target.MembershipID != p.id.MembershipID || item.Route.Target.AgentID != p.id.AgentID || item.Route.Version == 0 {
		return inputstream.Receipt{}, ErrIdentity
	}
	cap := s.notificationCapabilities(item.Source.Provider).Input
	if cap == nil || cap.EncodeStoredInput == nil {
		return inputstream.Receipt{}, inputstream.ErrUnsupported
	}
	request, err := cap.EncodeStoredInput(item)
	if err != nil {
		return inputstream.Receipt{}, err
	}
	request.ReceivedAt = receivedAt
	receipt, err := p.engine().Body.AppendOnce(item.Source.Provider, request)
	return receipt, p.result(err)
}
func (s *Store) platformInputHandle(id MemberIdentity) *platformInputStorage {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.platformInputs[id.MembershipID]
	if p != nil && p.id == id && p.generation != "" {
		return p
	}
	return nil
}
