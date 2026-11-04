package room

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Virtual private state is validated by the fixture assembly, never by the
// production DSH recovery path. Actor scheduling/checkpoints are shared.
func (s *service) loadFixtureMembers(state RoomSnapshot) error {
	for _, member := range state.Members {
		id := MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
		if !strings.HasPrefix(member.Runner.Profile, "fixture-") {
			return fmt.Errorf("fixture profile required")
		}
		if _, err := scriptActions(strings.TrimPrefix(member.Runner.Profile, "fixture-")); err != nil {
			return err
		}
		recovered := recoveredMember{id: id}
		agent, err := s.store.LoadAgent(id)
		if errors.Is(err, os.ErrNotExist) {
			agent = AgentSnapshot{Version: formatVersion, MemberIdentity: id}
			recovered.notice = "missing agent checkpoint initialized; member paused"
		} else if err != nil {
			return err
		}
		if _, err := s.store.loadVirtualSession(id); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("restore %s session: %w", member.AgentID, err)
			}
			if agent.Turn != nil && agent.Turn.Status == "completed" {
				return fmt.Errorf("%w: completed fixture turn has no Session state", ErrInvalid)
			}
		}
		if agent.Turn != nil {
			switch agent.Turn.Status {
			case "running":
				agent.Turn.Status = "interrupted"
				agent.Turn.Error = "service stopped before turn settlement; tool effects may have occurred"
			case "interrupted":
			default:
				recovered.resume = true
			}
		}
		recovered.state = &agent
		s.recovered = append(s.recovered, recovered)
	}
	for _, member := range s.recovered {
		if err := s.store.SaveAgent(*member.state); err != nil {
			return err
		}
	}
	return nil
}
