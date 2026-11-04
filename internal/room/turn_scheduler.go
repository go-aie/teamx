package room

import (
	"context"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
)

// All addresses are local. Send performs bounded mailbox admission; handlers
// never wait for another actor's reply. Request numbers are monotonic within a
// member incarnation and are reused on retry.
type requestTurn struct {
	MemberIdentity
	ID     uint64
	Target actorx.Addr
}

type turnGranted struct {
	MemberIdentity
	ID uint64
}

type cancelTurnRequest struct{ turnGranted }
type releaseTurn struct{ turnGranted }
type stopTurnGrants struct{}
type bindTurnScheduler struct{ Scheduler, Target actorx.Addr }

type turnApplicant struct {
	requestTurn
	pending bool
}

// turnScheduler owns one Room's FIFO and handoff responsibility. A grant stays
// held even if its delivery fails. Retrying the same grant cannot create a
// second turn, and only its exact identity/number can release it.
//
//actorx:actor
type turnScheduler struct {
	RoomIdentity
	owner   actorx.Context
	members map[MemberIdentity]*turnApplicant
	queue   []*turnApplicant
	holder  *turnApplicant
	closing bool
	frozen  *atomic.Bool // service admission boundary; nil in isolated actor tests
}

func (s *turnScheduler) Started(ctx actorx.Context, _ actorx.Started) error {
	s.owner = ctx.WithContext(context.WithoutCancel(ctx))
	s.members = make(map[MemberIdentity]*turnApplicant)
	return s.maintain()
}

func (s *turnScheduler) Request(_ actorx.Context, msg requestTurn) error {
	if s.closing || s.frozen != nil && s.frozen.Load() {
		return nil
	}
	if msg.RoomIdentity != s.RoomIdentity || msg.ID == 0 || msg.Target.IsZero() {
		return ErrIdentity
	}
	a := s.members[msg.MemberIdentity]
	if a == nil {
		if len(s.members) >= maxMembers {
			return ErrCapacity
		}
		a = &turnApplicant{}
		s.members[msg.MemberIdentity] = a
	} else if msg.Target != a.Target || msg.ID <= a.ID || a == s.holder {
		// Completed/canceled request numbers remain retired until actor exit.
		return nil
	}
	s.removeQueued(a)
	a.requestTurn, a.pending = msg, true
	s.queue = append(s.queue, a)
	s.grant()
	return nil
}

func (s *turnScheduler) Cancel(_ actorx.Context, msg cancelTurnRequest) error {
	if a := s.members[msg.MemberIdentity]; a != nil && a.ID == msg.ID {
		s.removeQueued(a)
		// Cancellation alone is not evidence that the holder has settled.
	}
	return nil
}

func (s *turnScheduler) Release(_ actorx.Context, msg releaseTurn) error {
	if a := s.members[msg.MemberIdentity]; a != nil && a.ID == msg.ID {
		s.removeQueued(a)
		if s.holder == a {
			s.holder = nil
		}
		s.grant()
	}
	return nil
}

func (s *turnScheduler) Quiesce(_ actorx.Context, _ stopTurnGrants) (struct{}, error) {
	s.closing = true
	for _, a := range s.queue {
		a.pending = false
	}
	s.queue = nil
	return struct{}{}, nil
}

func (s *turnScheduler) removeQueued(a *turnApplicant) {
	if a.pending {
		s.queue = slices.DeleteFunc(s.queue, func(item *turnApplicant) bool { return item == a })
		a.pending = false
	}
}

func (s *turnScheduler) grant() {
	if s.closing || s.frozen != nil && s.frozen.Load() {
		return
	}
	if s.holder == nil && len(s.queue) > 0 {
		s.holder = s.queue[0]
		s.queue = s.queue[1:]
		s.holder.pending = false
	}
	if a := s.holder; a != nil {
		// Admission failure and admission without handling both retain this
		// holder. The maintenance tick reconciles and retries the same grant.
		_ = a.Target.Send(s.owner, turnGranted{a.MemberIdentity, a.ID})
	}
}

func (s *turnScheduler) maintain() error {
	// One timer/scan per Room, independent of queue length. Wait with an
	// already canceled context only observes completed exits; it never waits
	// for a live actor or treats a cancellation deadline as settlement.
	targets := make(map[MemberIdentity]actorx.Addr, len(s.members))
	for id, a := range s.members {
		targets[id] = a.Target
	}
	_, err := actorx.StartTask(s.owner, func(ctx context.Context) ([]MemberIdentity, error) {
		timer := time.NewTimer(25 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
		probe, cancel := context.WithCancel(ctx)
		cancel()
		var dead []MemberIdentity
		for id, target := range targets {
			if _, err := target.Wait(probe); err == nil {
				dead = append(dead, id)
			}
		}
		return dead, nil
	}, func(dead []MemberIdentity, err error) bool {
		if err == nil {
			for _, id := range dead {
				a := s.members[id]
				s.removeQueued(a)
				if s.holder == a {
					s.holder = nil
				}
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

// Spawn first, then bind the reply address before attaching/enabling the member.
// Test observers may use the same assembly without becoming scheduled agents.
func spawnScheduledMember(rt *actorx.Runtime, member actorx.Actor, scheduler actorx.Addr) (actorx.Addr, error) {
	addr, err := rt.Spawn(member)
	if err != nil {
		return addr, err
	}
	if _, ok := member.(*MemberAgent); ok {
		_, err = addr.Call[struct{}](context.Background(), bindTurnScheduler{Scheduler: scheduler, Target: addr})
		if err != nil {
			_ = addr.Stop(err)
			_, _ = addr.Wait(context.Background())
		}
	}
	return addr, err
}
