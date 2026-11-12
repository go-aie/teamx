package room

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Recovery changes a Session generation, never the user's scheduling intent.
// One automatic attempt is allowed until a turn succeeds or the user enables
// collaboration again. Settlement and action receipts are checked separately.
func (m *MemberAgent) canRecoverSession() bool {
	return m.automaticRotation() && m.enabled && m.RecoveryPending && !m.recoveryAttempted && !m.fault && !m.active &&
		(m.ExecutionFrozen == nil || !m.ExecutionFrozen.Load()) && m.rotationWork == nil &&
		(m.rotationRecord == nil || m.rotationRecord.Phase == "completed")
}

func recoverableDSHFailure(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, dsh.ErrAuthentication) || errors.Is(err, dsh.ErrModelNotFound) || errors.Is(err, dsh.ErrRateLimited) {
		return false
	}
	var status *dsh.HTTPStatusError
	if errors.As(err, &status) {
		return status.StatusCode >= 500
	}
	return errors.Is(err, dsh.ErrUnavailable) || errors.Is(err, dsh.ErrUncertain) || errors.Is(err, dsh.ErrModelUnavailable)
}

// Legacy snapshots retain only safe diagnostics, so recognize only known
// transient failures. An arbitrary saved error never authorizes replacement.
func recoverableDSHHistory(agent AgentSnapshot) bool {
	if agent.Turn == nil || agent.Turn.Status == "completed" || agent.Turn.Status == "interrupted" {
		return true
	}
	err := agent.Turn.Error
	if strings.Contains(err, "HTTP 4") {
		return false
	}
	for _, permanent := range []error{dsh.ErrAuthentication, dsh.ErrModelNotFound, dsh.ErrRateLimited} {
		if strings.Contains(err, permanent.Error()) {
			return false
		}
	}
	return strings.Contains(err, "cannot inspect owned DSH processes") || strings.Contains(err, dsh.ErrUnavailable.Error()) ||
		strings.Contains(err, dsh.ErrModelUnavailable.Error())
}

func (s *service) requestMemberRecovery(ctx context.Context, id MemberIdentity, from string) error {
	if id.RoomIdentity != s.info.RoomIdentity {
		return ErrIdentity
	}
	_, err := s.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID,
		FromGeneration: from, RotationReason: "backend_recovery", Resume: true, automatic: true, recovering: true}, "rotate")
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
