package room

import (
	"errors"
	"fmt"
	"os"
)

// Separate from the worker's agent.json: pause/run can change intent while a
// turn is executing. A terminal turn must never overwrite a newer pause.
type memberScheduling struct {
	Version int `json:"version"`
	MemberIdentity
	Enabled bool   `json:"enabled"`
	Used    uint64 `json:"used"`
	Limit   uint64 `json:"limit,omitempty"` // Absent in older snapshots: use the Room's original limit.
}

func (s *Store) loadScheduling(id MemberIdentity) (memberScheduling, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _, err := s.memberPath(id, "collaboration.json", false)
	if err != nil {
		return memberScheduling{}, err
	}
	var state memberScheduling
	if err := readSnapshot(path, &state, maxPrivateFileBytes); err != nil {
		return state, err
	}
	if state.Version != formatVersion || state.MemberIdentity != id {
		return state, ErrIdentity
	}
	return state, nil
}

func (s *Store) saveScheduling(state memberScheduling) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, _, err := s.memberPath(state.MemberIdentity, "collaboration.json", true)
	if err != nil {
		return err
	}
	return s.save(path, state, maxPrivateFileBytes)
}

func (m *MemberAgent) persistScheduling(enabled bool, used uint64) error {
	return m.persistSchedulingLimit(enabled, used, m.turnLimit)
}

func (m *MemberAgent) persistSchedulingLimit(enabled bool, used, limit uint64) error {
	if !m.PersistentScheduling {
		return nil
	}
	err := m.Store.saveScheduling(memberScheduling{Version: formatVersion, MemberIdentity: m.Options.MemberIdentity, Enabled: enabled, Used: used, Limit: limit})
	if err != nil {
		m.fault, m.paused = true, true
		m.lastError = "collaboration checkpoint failed: " + err.Error()
	}
	return err
}

// Only an operator enable grants more turns. Automatic resume and rotation do
// not replenish the allowance. Commit the limit before publishing it in memory.
func (m *MemberAgent) resumeScheduling(replenish bool) error {
	limit := m.turnLimit
	if replenish && m.turnsUsed >= limit {
		if m.turnsUsed > ^uint64(0)-m.MaxTurns {
			return fmt.Errorf("%w: turn budget overflow", ErrInvalid)
		}
		limit = m.turnsUsed + m.MaxTurns
	}
	if err := m.persistSchedulingLimit(true, m.turnsUsed, limit); err != nil {
		return err
	}
	m.turnLimit, m.paused = limit, false
	return nil
}

func (s *service) restoreScheduling(member *recoveredMember, agent AgentSnapshot) error {
	state, err := s.store.loadScheduling(member.id)
	if errors.Is(err, os.ErrNotExist) {
		// Only an untouched membership may acquire the new-Room enable intent.
		// Legacy executed members have no durable pause proof and remain paused.
		state = memberScheduling{Version: formatVersion, MemberIdentity: member.id, Enabled: agent.TurnNumber == 0, Used: agent.TurnNumber}
		if err := s.store.saveScheduling(state); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if state.Used < agent.TurnNumber {
		return fmt.Errorf("%w: collaboration budget behind agent checkpoint", ErrInvalid)
	}
	member.used = state.Used
	member.limit = max(s.maxTurns, state.Limit)
	safe := agent.Turn == nil || (agent.Turn.Status == "completed" && (member.backendResume || member.contextReset))
	member.resume = state.Enabled && safe && member.blocked == "" && state.Used < member.limit
	return nil
}
