package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-aie/actorx"
)

type recoveredMember struct {
	automatic     bool
	id            MemberIdentity
	state         *AgentSnapshot
	resume        bool
	recovery      bool
	notice        string
	blocked       string
	backendResume bool
	contextReset  bool
	used          uint64
	limit         uint64
}

// Validate all current members before spawning any of them. The Room table is
// authoritative: orphan directories are deliberately never enumerated.
func (s *service) loadMembers(state RoomSnapshot) error {
	for _, member := range state.Members {
		id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		if s.cliPath == "" || s.endpoint == "" {
			return fmt.Errorf("restore %s: member CLI environment unavailable", member.AgentID)
		}
		recovered := recoveredMember{id: id, automatic: true}
		agent, err := s.store.LoadAgent(id)
		if errors.Is(err, os.ErrNotExist) {
			agent = AgentSnapshot{Version: formatVersion, MemberIdentity: id}
			recovered.notice = "missing agent checkpoint initialized; member paused"
		} else if err != nil {
			return fmt.Errorf("restore %s agent: %w", member.AgentID, err)
		}
		rotation, rotationErr := s.recoverMemberRotation(id, agent)
		if rotationErr != nil {
			recovered.blocked = "rotation recovery blocked: " + rotationErr.Error()
		}
		opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(s.store.dir, "agents", id.MembershipID, "session.json")}
		rotationReady := rotationErr == nil && (rotation == nil || rotation.Phase == "completed")
		backend, err := s.inspectBackendRecovery(opts, member.Runner, agent, rotationReady)
		if err != nil {
			return fmt.Errorf("restore %s DSH reference: %w", member.AgentID, err)
		}
		recovered.backendResume, recovered.recovery = backend.resume, backend.recovery && rotationErr == nil
		// Rotation reconciliation failures take precedence unless the adapter
		// has a more specific backend/configuration failure, as before.
		if backend.blocked != "" {
			recovered.blocked = backend.blocked
		}
		if backend.notice != "" {
			recovered.notice = backend.notice
		}
		if backend.reference.Pending && backend.blocked == "" {
			recovered.contextReset = rotation != nil && rotation.Phase == "completed" || s.workspaceOperationID != "" && backend.reference.Directory == sessionGenerationForOperation(s.workspaceOperationID)
			recovered.notice = "explicit rotation committed; private context reset; fresh Session will open on explicit run"
			if recovered.contextReset {
				recovered.notice = "rotation completed; private context reset; fresh Session will open lazily for new work"
			}
		}

		if agent.Turn != nil {
			switch agent.Turn.Status {
			case "running":
				agent.Turn.Status = "interrupted"
				agent.Turn.Error = "service stopped before turn settlement; tool effects may have occurred"
				recovered.notice = "running turn marked interrupted; original turn will not be replayed"
			case "interrupted":
				recovered.notice = "interrupted turn retained; original turn will not be replayed"
			default:
				recovered.resume = recovered.backendResume && agent.Turn.Status == "completed"
			}
		}
		if recovered.blocked != "" {
			recovered.notice = recovered.blocked + "; member paused"
		}
		// DSH counts survive normal restart in standalone mode too.
		if err := s.restoreScheduling(&recovered, agent); err != nil {
			return fmt.Errorf("restore %s collaboration: %w", member.AgentID, err)
		}
		if recovered.recovery {
			recovered.notice = "old Session settled; automatic recovery pending; private context will reset"
		} else if recovered.resume && recovered.backendResume {
			recovered.notice = "normal shutdown verified; enabled scheduling restored; original Session will resume lazily"
		}
		recovered.state = &agent
		s.recovered = append(s.recovered, recovered)
	}
	// Validate all members before committing recovery or creating execution resources.
	for _, member := range s.recovered {
		if err := s.store.SaveAgent(*member.state); err != nil {
			return fmt.Errorf("restore %s checkpoint: %w", member.id.AgentID, err)
		}
	}
	return nil
}

func (s *service) newMember(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(s.store.dir, "agents", id.MembershipID, "session.json"), CLIPath: s.cliPath, CLIEnv: s.toolEnv(id)}
	resumeBackend := false

	blocked := ""
	recovery := false
	var used, limit uint64
	for _, recovered := range s.recovered {
		if recovered.id == id {
			blocked = recovered.blocked
			recovery = recovered.recovery
			used = recovered.used
			limit = recovered.limit
			resumeBackend = recovered.backendResume
		}
	}
	gate := ""
	if s.models != nil {
		gate = s.models.gate(id)
	}
	member := &MemberAgent{AdditionalWorkReady: func() bool { return s.additionalWorkReady(id) }, ObserveTurnFinished: s.calendarTurnFinished, ExecutionBinding: func(turn uint64) (turnExecution, error) { return s.executionBindingForTurn(id, turn) }, ObserveEnabled: func() bool { return s.updates.observesRoom(s.info) }, Observe: func() { s.invalidate("members", id); s.invalidate("activity", id) }, ModelGate: gate, Options: opts, Store: s.store, Room: s.room, Restored: restored,
		Inbox:   func(ctx context.Context) (InboxStatus, error) { return s.memberInboxStatus(ctx, id) },
		Backend: s.executionBackend(runner, resumeBackend), MergeWindow: s.mergeWindow, MaxTurns: s.maxTurns, SessionMaxTurns: s.maxTurns, RequestRotation: s.requestMemberRotation, RequestRecovery: s.requestMemberRecovery, RecoveryPending: recovery, BackendBlocked: blocked, PersistentScheduling: true, RestoredTurnsUsed: used, RestoredTurnLimit: limit, ExecutionFrozen: s.grantsFrozen}
	if s.unifiedInputs {
		member.Inbox = nil
		member.Inputs = func(ctx context.Context) (unifiedInputStatus, error) {
			v, err := s.inputStatus(ctx, id)
			if err == nil && v.Recovery != nil {
				if e := s.checkSessionRecovery(id); e != nil {
					v.Ready = false
					v.Blocked = e.Error()
				}
			}
			return v, err
		}
		member.ClaimInputRecovery = func(ctx context.Context) error { return s.claimInputRecovery(ctx, id) }
		member.state.InputStateRef = "input-state.json"
	}
	return member
}

func (s *service) spawnMember(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) (actorx.Addr, error) {
	return spawnScheduledMember(s.rt, s.memberFactory(id, runner, restored), s.scheduler)
}

func (s *service) attachMembers(state RoomSnapshot) error {
	for i, member := range state.Members {
		recovered := s.recovered[i]
		target, err := s.spawnMember(recovered.id, member.Runner, recovered.state)
		if err != nil {
			return fmt.Errorf("restore %s actor: %w", member.AgentID, err)
		}
		s.setTarget(member.MembershipID, target)
		if _, err := s.room.Call[MemberInfo](s.ctx, AttachMember{MemberIdentity: recovered.id, Target: target}); err != nil {
			return fmt.Errorf("restore %s address: %w", member.AgentID, err)
		}
	}
	return nil
}

// Call after HTTP starts, before publishing readiness. Recovered actors were
// paused throughout reattachment; their tools now have a live endpoint and the
// new ServiceEpoch. Only settled turns resume automatic notification handling.
func (s *service) resumeMembers(ctx context.Context) error {
	s.calendarRunning.Store(true)
	// A batch interrupted by shutdown may have rotated only some members.
	// Keep the whole team paused until the operator resolves or retries it.
	if restart := s.discussionRestartStatus(); restart != nil && restart.Status != "completed" {
		return nil
	}
	for _, member := range s.recovered {
		if s.models != nil && s.models.gate(member.id) != "" {
			continue
		}
		if member.recovery {
			if _, err := s.control(ctx, apiRequest{AgentID: member.id.AgentID, MembershipID: member.id.MembershipID, automatic: true}, "recover"); err != nil {
				return fmt.Errorf("recover %s: %w", member.id.AgentID, err)
			}
		}
		if member.resume {
			_, err := s.control(ctx, apiRequest{AgentID: member.id.AgentID, MembershipID: member.id.MembershipID, Retry: member.contextReset && member.state.Turn != nil && member.state.Turn.Status != "completed"}, "run")
			if err != nil {
				return fmt.Errorf("resume %s: %w", member.id.AgentID, err)
			}
		}
	}
	return nil
}
