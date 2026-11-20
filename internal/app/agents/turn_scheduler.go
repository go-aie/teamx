//go:generate go run github.com/go-aie/actorx/cmd/actorx-gen

package agents

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/identity"
)

// All addresses are local. Send performs bounded mailbox admission; handlers
// never wait for another actor's reply. Request numbers are monotonic within a
// member incarnation and are reused on retry.
type RequestTurn struct {
	identity.MemberIdentity
	ID     uint64
	Target actorx.Addr
}

type TurnGranted struct {
	identity.MemberIdentity
	ID uint64
}

type CancelTurnRequest struct{ TurnGranted }
type ReleaseTurn struct{ TurnGranted }
type StopTurnGrants struct{}

// InspectTurnScheduler returns a copy at the actor's message boundary. Callers
// may observe settlement without accessing or mutating the grant owner's state.
type InspectTurnScheduler struct{}

type TurnSchedulerState struct {
	Holders map[identity.MemberIdentity]TurnGranted
	Queue   []TurnGranted
	Closing bool
}

type turnApplicant struct {
	RequestTurn
	pending bool
}

// TurnScheduler owns one Room's FIFO and handoff responsibility. A grant stays
// held even if its delivery fails. Retrying the same grant cannot create a
// second turn, and only its exact identity/number can release it.
//
//actorx:actor
type TurnScheduler struct {
	identity.RoomIdentity
	owner   actorx.Context
	members map[identity.MemberIdentity]*turnApplicant
	queue   []*turnApplicant
	holders map[identity.MemberIdentity]*turnApplicant
	mode    SchedulingMode
	closing bool
	frozen  *atomic.Bool // service admission boundary; nil in isolated actor tests
}

// NewTurnScheduler creates one Room's grant owner. The service shares frozen
// with its shutdown admission gate; nil is allowed for isolated runtimes.
// Spawn validates mode before admitting requests. Configuration is immutable
// after spawning, and only the actor accesses its queues and holders.
func NewTurnScheduler(id identity.RoomIdentity, mode SchedulingMode, frozen *atomic.Bool) *TurnScheduler {
	return &TurnScheduler{RoomIdentity: id, mode: mode, frozen: frozen}
}

func (s *TurnScheduler) Started(ctx actorx.Context, _ actorx.Started) error {
	mode, err := s.mode.Effective()
	if err != nil {
		return err
	}
	s.mode = mode
	s.holders = make(map[identity.MemberIdentity]*turnApplicant)
	s.owner = ctx.WithContext(context.WithoutCancel(ctx))
	s.members = make(map[identity.MemberIdentity]*turnApplicant)
	return s.maintain()
}

func (s *TurnScheduler) Request(_ actorx.Context, msg RequestTurn) error {
	if s.closing || s.frozen != nil && s.frozen.Load() {
		return nil
	}
	if msg.RoomIdentity != s.RoomIdentity || msg.ID == 0 || msg.Target.IsZero() {
		return identity.ErrMismatch
	}
	a := s.members[msg.MemberIdentity]
	if a == nil {
		if len(s.members) >= MaxMembers {
			return ErrCapacity
		}
		a = &turnApplicant{}
		s.members[msg.MemberIdentity] = a
	} else if msg.Target != a.Target || msg.ID <= a.ID || s.holders[msg.MemberIdentity] == a {
		// Completed/canceled request numbers remain retired until actor exit.
		return nil
	}
	s.removeQueued(a)
	a.RequestTurn, a.pending = msg, true
	s.queue = append(s.queue, a)
	s.grant()
	return nil
}

func (s *TurnScheduler) Cancel(_ actorx.Context, msg CancelTurnRequest) error {
	if a := s.members[msg.MemberIdentity]; a != nil && a.ID == msg.ID {
		s.removeQueued(a)
		// Cancellation alone is not evidence that the holder has settled.
	}
	return nil
}

func (s *TurnScheduler) Release(_ actorx.Context, msg ReleaseTurn) error {
	if a := s.members[msg.MemberIdentity]; a != nil && a.ID == msg.ID {
		s.removeQueued(a)
		delete(s.holders, msg.MemberIdentity)
		s.grant()
	}
	return nil
}

func (s *TurnScheduler) Quiesce(_ actorx.Context, _ StopTurnGrants) (struct{}, error) {
	s.closing = true
	for _, a := range s.queue {
		a.pending = false
	}
	s.queue = nil
	return struct{}{}, nil
}

func (s *TurnScheduler) Inspect(_ actorx.Context, _ InspectTurnScheduler) (TurnSchedulerState, error) {
	state := TurnSchedulerState{Closing: s.closing, Holders: make(map[identity.MemberIdentity]TurnGranted, len(s.holders))}
	for id, a := range s.holders {
		state.Holders[id] = TurnGranted{a.MemberIdentity, a.ID}
	}
	for _, a := range s.queue {
		state.Queue = append(state.Queue, TurnGranted{a.MemberIdentity, a.ID})
	}
	return state, nil
}

func (s *TurnScheduler) removeQueued(a *turnApplicant) {
	if a.pending {
		s.queue = slices.DeleteFunc(s.queue, func(item *turnApplicant) bool { return item == a })
		a.pending = false
	}
}

func (s *TurnScheduler) grant() {
	if s.closing || s.frozen != nil && s.frozen.Load() {
		return
	}
	for len(s.queue) > 0 && (s.mode == SchedulingParallel || len(s.holders) == 0) {
		a := s.queue[0]
		s.queue = s.queue[1:]
		a.pending = false
		s.holders[a.MemberIdentity] = a
	}
	for _, a := range s.holders {
		// Admission failure retains this holder. One member's full mailbox
		// must not prevent other parallel members from receiving their grants.
		_ = a.Target.Send(s.owner, TurnGranted{a.MemberIdentity, a.ID})
	}
}

func (s *TurnScheduler) maintain() error {
	// One timer/scan per Room, independent of queue length. Wait with an
	// already canceled context only observes completed exits; it never waits
	// for a live actor or treats a cancellation deadline as settlement.
	targets := make(map[identity.MemberIdentity]actorx.Addr, len(s.members))
	for id, a := range s.members {
		targets[id] = a.Target
	}
	_, err := actorx.StartTask(s.owner, func(ctx context.Context) ([]identity.MemberIdentity, error) {
		timer := time.NewTimer(25 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		probe, cancel := context.WithCancel(ctx)
		cancel()
		var dead []identity.MemberIdentity
		for id, target := range targets {
			if _, err := target.Wait(probe); err == nil {
				dead = append(dead, id)
			}
		}
		return dead, nil
	}, func(dead []identity.MemberIdentity, err error) bool {
		if err == nil {
			for _, id := range dead {
				a := s.members[id]
				s.removeQueued(a)
				delete(s.holders, id)
				delete(s.members, id)
			}
			s.grant()
			if err := s.maintain(); err != nil {
				s.owner.Stop(fmt.Errorf("turn scheduler maintenance unavailable: %w", err))
			}
		}
		return true
	}, nil)
	return err
}
