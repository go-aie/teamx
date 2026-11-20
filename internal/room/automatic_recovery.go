package room

import (
	"context"
	"fmt"
)

// Recovery changes a Session generation, never the user's scheduling intent.
// One automatic attempt is allowed until a turn succeeds or the user enables
// collaboration again. Settlement and action receipts are checked separately.
func (m *MemberAgent) canRecoverSession() bool {
	return m.automaticRotation() && m.enabled && m.RecoveryPending && !m.recoveryAttempted && !m.fault && !m.active &&
		(m.ExecutionFrozen == nil || !m.ExecutionFrozen.Load()) && m.rotationWork == nil &&
		(m.rotationRecord == nil || m.rotationRecord.Phase == "completed")
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
