package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

type serviceConfig struct {
	imageMaterials       imageMaterialConfig
	workspaceOperationID string // verified managed configuration only
	endpoint             string
	cliPath              string
	mergeWindow          time.Duration
	dshTurnTimeout       time.Duration
	maxTurns             uint64
	persistentScheduling bool
	dshProfiles          map[string]dshProfile
	uiProfiles           []string
}

type recoveredMember struct {
	id            MemberIdentity
	state         *AgentSnapshot
	resume        bool
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
		recovered := recoveredMember{id: id}
		agent, err := s.store.LoadAgent(id)
		if errors.Is(err, os.ErrNotExist) {
			agent = AgentSnapshot{Version: formatVersion, MemberIdentity: id}
			recovered.notice = "missing agent checkpoint initialized; member paused"
		} else if err != nil {
			return fmt.Errorf("restore %s agent: %w", member.AgentID, err)
		}
		opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(s.store.dir, "agents", id.MembershipID, "session.json")}
		ref, exists, err := (dshBackend{store: s.store}).reference(opts)
		if err != nil {
			return fmt.Errorf("restore %s DSH reference: %w", member.AgentID, err)
		}
		if _, ok := s.dshProfiles[member.Runner.Profile]; !ok {
			recovered.blocked = "DSH profile unavailable: " + member.Runner.Profile
		} else if exists {
			dir := ref.Directory
			if err := dsh.CheckResume(filepath.Join(filepath.Dir(opts.StatePath), dir)); err != nil {
				recovered.blocked = "DSH recovery blocked: " + err.Error()
			} else {
				recovered.backendResume = true
				recovered.notice = "DSH original session will resume lazily using current tool identity"
			}
		} else if ref.Pending {
			recovered.contextReset = s.workspaceOperationID != "" && ref.Directory == filepath.Base(dshStateDir(opts.StatePath))+"-"+s.workspaceOperationID
			recovered.notice = "explicit rotation committed; private context reset; fresh Session will open on explicit run"
			if recovered.contextReset {
				recovered.notice = "workspace applied; private context reset; fresh Session will open lazily for new work"
			}
		} else if agent.Turn != nil {
			recovered.blocked = "DSH recovery blocked: backend reference missing for recorded turn"
		}
		if agent.Turn != nil {
			switch agent.Turn.Status {
			case "running":
				agent.Turn.Status = "interrupted"
				agent.Turn.Error = "service stopped before turn settlement; tool effects may have occurred"
				recovered.notice = "running turn marked interrupted; member paused"
			case "interrupted":
				recovered.notice = "interrupted turn retained; member paused"
			default:
				recovered.resume = recovered.backendResume && agent.Turn.Status == "completed"
			}
		}
		if recovered.blocked != "" {
			recovered.notice = recovered.blocked + "; member paused"
		}
		if s.persistentScheduling {
			if err := s.restoreScheduling(&recovered, agent); err != nil {
				return fmt.Errorf("restore %s collaboration: %w", member.AgentID, err)
			}
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
	backend := dshBackend{store: s.store, profile: s.dshProfiles[runner.Profile], turnTimeout: s.dshTurnTimeout, prepare: s.prepareDSHMaterials}
	blocked := ""
	var used, limit uint64
	for _, recovered := range s.recovered {
		if recovered.id == id {
			blocked = recovered.blocked
			used = recovered.used
			limit = recovered.limit
			backend.recover = recovered.backendResume
		}
	}
	return &MemberAgent{Options: opts, Store: s.store, Room: s.room, Restored: restored,
		Inbox:   func(ctx context.Context) (InboxStatus, error) { return s.memberInboxStatus(ctx, id) },
		Backend: backend, MergeWindow: s.mergeWindow, MaxTurns: s.maxTurns, BackendBlocked: blocked, PersistentScheduling: s.persistentScheduling, RestoredTurnsUsed: used, RestoredTurnLimit: limit, ExecutionFrozen: s.grantsFrozen}
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
	// A batch interrupted by shutdown may have rotated only some members.
	// Keep the whole team paused until the operator resolves or retries it.
	if restart := s.discussionRestartStatus(); restart != nil && restart.Status != "completed" {
		return nil
	}
	for _, member := range s.recovered {
		if member.resume {
			_, err := s.control(ctx, apiRequest{AgentID: member.id.AgentID, MembershipID: member.id.MembershipID}, "run")
			if err != nil {
				return fmt.Errorf("resume %s: %w", member.id.AgentID, err)
			}
		}
	}
	return nil
}
