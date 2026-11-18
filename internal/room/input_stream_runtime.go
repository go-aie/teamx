package room

import (
	"context"
	"errors"
	"github.com/go-aie/teamx/internal/platform/notification"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/go-aie/teamx/internal/inputstream"
)

// Session generation and membership are already authenticated by mcpExecution.
// A cached service is per turn, while the durable repository is per member.
func (s *mcpInboxSession) inputFor(ctx context.Context, turn uint64, generation string) (*inputSession, error) {
	s.inputMu.Lock()
	defer s.inputMu.Unlock()
	if s.input != nil && s.input.execution.Turn == turn {
		return s.input, nil
	}
	p, err := s.service.inputStorage(s.id)
	if err != nil {
		return nil, err
	}
	input, err := newInputSession(ctx, s.service, p, generation, turn, s.images, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.gate.Lock()
		closed := s.closed
		s.gate.Unlock()
		if closed {
			return ErrClosing
		}
		s.service.store.mu.Lock()
		defer s.service.store.mu.Unlock()
		path, _, err := s.service.store.memberPath(s.id, "session.json", false)
		if err != nil {
			return err
		}
		ref, exists, err := readDSHReference(path, s.id)
		if err != nil {
			return err
		}
		if !exists || ref.Pending || ref.Directory != generation {
			return ErrIdentity
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	input.owner = s
	s.input = input
	return input, nil
}
func inputSessionFrom(ctx context.Context) (*inputSession, error) {
	if s, ok := ctx.Value(inputSessionKey{}).(*inputSession); ok {
		return s, s.check(ctx)
	}
	return nil, ErrIdentity
}

func (s *service) inputStorage(id MemberIdentity) (*platformInputStorage, error) {
	return s.store.openPlatformInputStorage(id, s.inputAdapters)
}

func (r *Store) roomInputProtected(through uint64) error {
	if through > 0 {
		var disk RoomSnapshot
		if err := readSnapshot(filepath.Join(r.dir, "room.json"), &disk, maxRoomFileBytes); err == nil && through > disk.ClearedThrough {
			for _, m := range disk.Members {
				if r.platformInputs[m.MembershipID] == nil {
					_, err := os.Stat(filepath.Join(r.dir, "agents", m.MembershipID, "input-state.json"))
					if err == nil {
						return ErrBusy
					}
					if !errors.Is(err, os.ErrNotExist) {
						return err
					}
				}
			}
		}
	}

	for _, p := range r.platformInputs {
		state, ok, err := p.repository.CurrentState()
		if err != nil {
			return err
		}
		if !ok || state.Closed {
			continue
		}
		for _, a := range state.Attempts {
			if a.Kind == "room" && a.Phase != "failed_before_output" && a.From < through {
				return ErrBusy
			}
		}
	}
	return nil
}

type unifiedInputStatus struct {
	Pending  int
	Ready    bool
	Recovery *inputstream.InputStall
	Blocked  string
	Progress string
}

type inputRecoveryView struct {
	Pending  int       `json:"pending"`
	Attempts int       `json:"attempts"`
	Since    time.Time `json:"since"`
	NextAt   time.Time `json:"next_at"`
	Blocked  string    `json:"blocked,omitempty"`
}

func (s *service) inputStatus(ctx context.Context, id MemberIdentity) (unifiedInputStatus, error) {
	out := unifiedInputStatus{}
	p, err := s.inputStorage(id)
	if err != nil {
		return out, err
	}
	if hook := s.maintainInbox.Load(); hook != nil {
		if err := hook.run(ctx, id); err != nil {
			return out, err
		}
	}
	p.store.mu.Lock()
	locations, err := p.repository.PendingLocations(nil)
	p.store.mu.Unlock()
	if err != nil {
		return out, err
	}
	admissions := map[inputstream.Location]notification.Admission{}
	for _, l := range locations {
		p.store.mu.Lock()
		e, err := p.store.businessInputLocked(id, 0, &l)
		p.store.mu.Unlock()
		if err != nil {
			return out, err
		}
		ad, err := s.resolveStoredInputAdmission(ctx, e)
		if err != nil {
			return out, err
		}
		admissions[l] = ad
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	readable := 0
	counts := map[string]int{}
	blockedStreams := map[string]bool{}
	readyStreams := map[string]bool{}
	for _, l := range locations {
		ad := admissions[l]
		if ad.Verified && ad.Decision == notification.AdmissionExcluded {
			err := p.repository.ExcludeLocal(l, "admission_excluded:"+ad.ReasonCode)
			if err != nil && !errors.Is(err, inputstream.ErrBusy) {
				return out, p.result(err)
			}
			continue
		}
		stream, err := p.repository.StreamAt(l)
		if err != nil {
			return out, err
		}
		blockedStreams[stream] = blockedStreams[stream] || !ad.Verified
		readyStreams[stream] = true
		counts[stream]++
	}
	for stream := range readyStreams {
		if !blockedStreams[stream] {
			readable++
			out.Pending += counts[stream]
		}
	}
	out.Progress = inputstream.Ref("pending_", locations, admissionsFingerprint(admissions))
	state, exists, err := p.repository.CurrentState()
	if err != nil {
		return out, err
	}
	if !exists {
		out.Ready = readable > 0
		return out, nil
	}
	agent, err := p.store.loadInputAgentLocked(id)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if agent.Turn == nil || agent.Turn.Status != "completed" {
		return out, nil
	}
	if state.Stall != nil || state.Used == 0 && out.Pending > 0 {
		out.Recovery, err = p.repository.ReconcileStall(state.Execution, time.Now(), inboxRecoveryBackoff[:])
		if err != nil {
			return out, err
		}
	}
	if out.Recovery != nil {
		out.Ready = readable > 0 && out.Recovery.Attempts < 3 && !out.Recovery.Next.IsZero() && !time.Now().Before(out.Recovery.Next)
	} else {
		out.Ready = readable > 0
	}
	return out, nil
}
func (s *Store) loadInputAgentLocked(id MemberIdentity) (AgentSnapshot, error) {
	var a AgentSnapshot
	path, _, err := s.memberPath(id, "agent.json", false)
	if err != nil {
		return a, err
	}
	err = readSnapshot(path, &a, maxPrivateFileBytes)
	return a, err
}
func (s *service) claimInputRecovery(ctx context.Context, id MemberIdentity) error {
	if err := s.checkSessionRecovery(id); err != nil {
		return err
	}
	p, err := s.inputStorage(id)
	if err != nil {
		return err
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	state, ok, err := p.repository.CurrentState()
	if err != nil {
		return err
	}
	if !ok || state.Stall == nil {
		return nil
	}
	return p.repository.ClaimStall(state.Execution, time.Now(), inboxRecoveryBackoff[:])
}

func (m *MemberAgent) continueInput() error {
	if m.active || m.paused || m.fault || m.ModelGate != "" || m.BackendBlocked != "" || m.rotationWork != nil || m.rotationRequest || (m.rotationRecord != nil && m.rotationRecord.Phase != "completed") || (m.ExecutionFrozen != nil && m.ExecutionFrozen.Load()) {
		return ErrBusy
	}
	if !m.automaticRotation() && m.turnsUsed >= m.turnLimit {
		return ErrTurnBudget
	}
	if m.inputStatus == nil || m.inputStatus.Recovery == nil || m.inputStatus.Blocked != "" || m.state.Turn == nil {
		return ErrConflict
	}
	p := m.Store.platformInputHandle(m.Options.MemberIdentity)
	if p == nil {
		return ErrIdentity
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	state, ok, err := p.repository.CurrentState()
	if err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	if err := p.repository.ContinueStall(state.Execution, m.now()); err != nil {
		return err
	}
	m.inputStatus.Ready = true
	m.lastError = ""
	return nil
}

func admissionsFingerprint(a map[inputstream.Location]notification.Admission) []string {
	out := []string{}
	for l, v := range a {
		out = append(out, inputstream.Ref("admission_", l, v))
	}
	sort.Strings(out)
	return out
}
