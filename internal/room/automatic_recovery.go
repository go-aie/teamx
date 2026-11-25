package room

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"
)

// Retry metadata belongs to scheduling, independently of a private Session.
// Backoff survives restart; successful work clears it. Attempts saturate so a
// long provider outage cannot overflow or produce increasingly frequent calls.
type sessionRecovery struct {
	Stage    string    `json:"stage"`
	Attempts int       `json:"attempts"`
	NextAt   time.Time `json:"next_at"`
	Error    string    `json:"error,omitempty"`
	Blocked  bool      `json:"blocked,omitempty"`
}

func recoveryDelay(attempts int) time.Duration {
	delays := [...]time.Duration{time.Second, 15 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}
	return delays[min(max(attempts, 0), len(delays)-1)]
}

func transientSessionError(err error) bool {
	for _, permanent := range []error{ErrInvalid, ErrIdentity, ErrVersion, ErrRequest, ErrCommitUnknown, ErrStoreClosed} {
		if errors.Is(err, permanent) {
			return false
		}
	}
	var network net.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrBusy) || errors.As(err, &network) && (network.Timeout() || network.Temporary())
}

func (m *MemberAgent) deferSessionChange(stage, diagnostic string, retry bool) {
	if m.sessionRecovery == nil {
		m.sessionRecovery = &sessionRecovery{}
	}
	r := m.sessionRecovery
	r.Stage, r.Error, r.Blocked = stage, diagnostic, !retry
	r.Attempts = min(r.Attempts+1, 64)
	r.NextAt = m.now().Add(recoveryDelay(r.Attempts - 1))
	m.rotationNext = r.NextAt
	if !retry {
		m.RecoveryPending = false
		m.paused = true
	} else if stage == "rotation" {
		m.recoveryArmed = true
	}
	_ = m.persistScheduling(m.enabled, m.turnsUsed)
}

func (m *MemberAgent) recordRecoveryAttempt() {
	if m.sessionRecovery == nil {
		m.sessionRecovery = &sessionRecovery{Stage: "backend"}
	}
	r := m.sessionRecovery
	r.Attempts = min(r.Attempts+1, 64)
	r.NextAt = m.now().Add(recoveryDelay(r.Attempts))
	m.rotationNext = r.NextAt
}

// Recovery changes a Session generation, never the user's scheduling intent.
// Every attempt still verifies settlement and saved action outcomes.
func (m *MemberAgent) canRecoverSession() bool {
	return m.automaticRotation() && m.enabled && m.RecoveryPending && !m.fault && !m.active &&
		(m.sessionRecovery == nil || !m.sessionRecovery.Blocked) &&
		(m.ExecutionFrozen == nil || !m.ExecutionFrozen.Load()) && m.rotationWork == nil &&
		(m.rotationRecord == nil || m.rotationRecord.Phase == "completed")
}

func (s *service) requestMemberRecovery(ctx context.Context, id MemberIdentity, from string) error {
	if id.RoomIdentity != s.info.RoomIdentity {
		return ErrIdentity
	}
	req := apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID,
		FromGeneration: from, RotationReason: "backend_recovery", Resume: true, automatic: true, recovering: true}
	// Resume the original operation after a temporary lifecycle failure. Never
	// allocate another target or discard an uncertain reference publication.
	r, err := s.store.loadRotation(id)
	if err != nil {
		return err
	}
	if r != nil && r.Automatic && r.Phase != "completed" {
		req.OperationID, req.FromGeneration, req.RotationReason = r.OperationID, r.From, r.Reason
		req.ContinueRotation, req.recovering = true, r.Recovery
		req.Cancel, req.Resume = r.Cancel, r.Resume
	}
	_, err = s.control(ctx, req, "rotate")
	return err
}

// Called after the platform lifecycle gate has cancelled and joined admitted
// calls. Unknown receipts must be resolved before work can be reoffered.
func (s *service) checkSessionRecovery(id MemberIdentity) error {
	agent, err := s.store.LoadAgent(id)
	if err != nil {
		return err
	}
	var turn TurnRecord
	if agent.Turn != nil {
		turn = *agent.Turn
	}
	facts, err := s.store.recoveryBusinessContext(id, turn)
	if err != nil {
		return err
	}
	if facts.unknownActions > 0 {
		return fmt.Errorf("automatic Session recovery blocked: %d action outcomes require inspection", facts.unknownActions)
	}
	return nil
}
