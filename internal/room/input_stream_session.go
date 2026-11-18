package room

import (
	"context"
	"sync"

	"github.com/go-aie/teamx/internal/inputstream"
)

// inputSession is composed by the new-format Room runtime. Legacy
// Rooms keep their prior entry until the separately authorized upgrade.
// CLI and MCP share this member service and the same durable coordinator.
type inputSession struct {
	receiptMu  sync.Mutex
	receiptErr error // Latest receipt result; diagnostics only, never consumption authority.
	knownMu    sync.Mutex
	known      map[string]bool
	owner      *mcpInboxSession
	unified    bool
	service    *service
	storage    *platformInputStorage
	execution  inputstream.Execution
	images     bool
	validate   func(context.Context) error // Original lifecycle/access/Session gate.
}

func (p *platformInputStorage) executionLocked(turn uint64) (inputstream.Execution, error) {
	if _, err := p.store.runningTurnLocked(p.id, turn); err != nil {
		return inputstream.Execution{}, err
	}
	if p.generation == "" {
		return inputstream.Execution{}, ErrIdentity
	}
	return inputstream.Execution{Generation: p.generation, Turn: turn}, nil
}
func newInputSession(ctx context.Context, svc *service, p *platformInputStorage, generation string, turn uint64, images bool, validate func(context.Context) error) (*inputSession, error) {
	if svc == nil || p == nil || svc.store != p.store || svc.info.RoomIdentity != p.id.RoomIdentity || generation == "" || validate == nil {
		return nil, ErrIdentity
	}
	s := &inputSession{service: svc, storage: p, execution: inputstream.Execution{Generation: generation, Turn: turn}, images: images, validate: validate, unified: svc.unifiedInputs}
	if err := validate(ctx); err != nil {
		return nil, err
	}
	if err := svc.store.initReferenceFollowups(p.id); err != nil {
		return nil, err
	}
	p.discovery.Lock()
	defer p.discovery.Unlock()
	p.catalogs = nil
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return nil, err
	}
	agent, err := p.store.runningTurnLocked(p.id, turn)
	if err != nil {
		return nil, err
	}
	if p.generation != "" && p.generation != generation {
		old, err := p.repository.State(inputstream.Execution{Generation: p.generation, Turn: turn})
		if err == nil && old.Execution.Turn == turn {
			return nil, ErrIdentity
		}
	}
	if err := p.result(p.repository.Begin(s.execution)); err != nil {
		return nil, err
	}
	state, e := p.repository.State(s.execution)
	if e != nil {
		return nil, e
	}
	for _, a := range state.Attempts {
		if a.Kind != "room" || a.Phase != "awaiting_confirmation" {
			continue
		}
		room, e := p.store.loadRoom()
		if e != nil {
			return nil, e
		}
		proof, ok := room.ReadProofs[p.id.MembershipID]
		if ok && proof.Confirmation.ReadReceipt == a.Proof() {
			if e := p.repository.SettleOwned(s.execution, a.ID, a.Proof()); e != nil {
				return nil, p.result(e)
			}
		}
	}
	p.generation = generation
	// A single stable reference, no copied quota or cursor in agent.json. Results
	// and attempts can exceed agent.json's bounded private-state encoding limit.
	agent.InputStateRef = "input-state.json"
	path, _, err := p.store.memberPath(p.id, "agent.json", true)
	if err != nil {
		return nil, err
	}
	if err := p.store.save(path, agent, maxPrivateFileBytes); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *inputSession) check(ctx context.Context) error {
	if s.service.closing.Load() || s.service.restarting.Load() {
		return ErrClosing
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validate(ctx); err != nil {
		return err
	}
	p := s.storage
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return err
	}
	e, err := p.executionLocked(s.execution.Turn)
	if err != nil {
		return err
	}
	if e != s.execution {
		return ErrIdentity
	}
	_, err = p.repository.State(e)
	return p.result(err)
}
func (s *inputSession) acquire(ctx context.Context) (func(), error) {
	p := s.storage
	if !p.work.TryLock() {
		return nil, ErrBusy
	}
	ownerRelease := func() {}
	if s.owner != nil {
		s.owner.gate.Lock()
		if s.owner.closed {
			s.owner.gate.Unlock()
			p.work.Unlock()
			return nil, ErrClosing
		}
		s.owner.active.Add(1)
		s.owner.gate.Unlock()
		ownerRelease = s.owner.active.Done
	}
	release, err := p.store.beginMCPRead(p.id, s.execution.Turn)
	if err == nil {
		err = s.check(ctx)
	}
	if err != nil {
		if release != nil {
			release()
		}
		ownerRelease()
		p.work.Unlock()
		return nil, err
	}
	return sync.OnceFunc(func() { release(); ownerRelease(); p.work.Unlock() }), nil
}
func (s *inputSession) mutate(fn func(*inputstream.Repository) error) error {
	p := s.storage
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if err := p.checkLocked(); err != nil {
		return err
	}
	return p.result(fn(p.repository))
}
