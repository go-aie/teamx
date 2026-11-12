package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const schedulingVersion = 2

// Separate from the worker's agent.json: pause/run can change intent while a
// turn is executing. A terminal turn must never overwrite a newer pause.
type memberScheduling struct {
	Version int `json:"version"`
	MemberIdentity
	Enabled           bool   `json:"enabled"`
	RecoveryAttempted bool   `json:"recovery_attempted,omitempty"`
	Used              uint64 `json:"used"`
	SessionGeneration string `json:"session_generation"`
	SessionStartUsed  uint64 `json:"session_start_used"`
	Limit             uint64 `json:"limit,omitempty"` // Absent in older snapshots: use the Room's original limit.
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
	if (state.Version != formatVersion && state.Version != schedulingVersion) || state.MemberIdentity != id {
		return state, ErrIdentity
	}
	if state.Version == schedulingVersion {
		var fields map[string]json.RawMessage
		if err := readSnapshot(path, &fields, maxPrivateFileBytes); err != nil {
			return state, err
		}
		for _, key := range []string{"enabled", "used", "session_generation", "session_start_used"} {
			if len(fields[key]) == 0 || string(fields[key]) == "null" {
				return state, fmt.Errorf("%w: missing collaboration %s", ErrInvalid, key)
			}
		}
		if state.SessionStartUsed > state.Used || (state.SessionGeneration != "" && !validSessionGeneration(state.SessionGeneration)) {
			return state, fmt.Errorf("%w: collaboration baseline", ErrInvalid)
		}
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
		m.enabled = enabled
		return nil
	}
	version := m.schedulingVersion
	if version == 0 {
		version = formatVersion
	}
	if m.automaticRotation() {
		limit = 0
	}
	err := m.Store.saveScheduling(memberScheduling{Version: version, MemberIdentity: m.Options.MemberIdentity, Enabled: enabled, RecoveryAttempted: m.recoveryAttempted, Used: used, Limit: limit, SessionGeneration: m.sessionGeneration, SessionStartUsed: m.sessionStartUsed})
	if err != nil {
		m.fault, m.paused = true, true
		m.lastError = "collaboration checkpoint failed: " + err.Error()
	} else {
		m.enabled = enabled
	}
	return err
}

// Only an operator enable grants more turns. Automatic resume and rotation do
// not replenish the allowance. Commit the limit before publishing it in memory.
func (m *MemberAgent) resumeScheduling(replenish bool) error {
	limit := m.turnLimit
	if !m.automaticRotation() && replenish && m.turnsUsed >= limit {
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
		opts := SessionOptions{MemberIdentity: member.id, StatePath: filepath.Join(s.store.dir, "agents", member.id.MembershipID, "session.json")}
		ref, _, refErr := (dshBackend{store: s.store}).reference(opts)
		if refErr != nil {
			return refErr
		}
		state = memberScheduling{Version: formatVersion, MemberIdentity: member.id, Enabled: agent.TurnNumber == 0 && ref.Directory == "", Used: agent.TurnNumber}
		if member.automatic && agent.TurnNumber == 0 && ref.Directory == "" {
			state.Version = schedulingVersion
			state.SessionGeneration, err = (dshBackend{store: s.store}).sessionGeneration(opts)
			if err != nil {
				return err
			}
		}
		if err := s.store.saveScheduling(state); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if state.Used < agent.TurnNumber {
		return fmt.Errorf("%w: collaboration budget behind agent checkpoint", ErrInvalid)
	}
	if member.automatic && state.Version == formatVersion {
		state, err = convertScheduling(state)
		if err != nil {
			return err
		}
		state.Limit = 0
		if err := s.store.saveScheduling(state); err != nil {
			return err
		}
	}
	member.used = state.Used
	member.limit = max(s.maxTurns, state.Limit)
	safe := agent.Turn == nil || member.contextReset || ((agent.Turn.Status == "completed" || agent.Turn.Status == "interrupted") && member.backendResume)
	member.resume = state.Enabled && safe && member.blocked == "" && (member.automatic || state.Used < member.limit)
	member.recovery = member.recovery && state.Enabled && !state.RecoveryAttempted
	return nil
}

// Upgrade old scheduling without inventing a baseline for an executed Session.
// Empty generation means the old Session must be rotated before S3 admission.
func convertScheduling(state memberScheduling) (memberScheduling, error) {
	if state.Version != formatVersion || state.SessionGeneration != "" || state.SessionStartUsed != 0 {
		return state, ErrVersion
	}
	state.Version = schedulingVersion
	return state, nil
}

func (m *MemberAgent) initializeScheduling() error {
	if _, ok := m.Backend.(rotatingBackend); !ok {
		m.schedulingReady = true
		return nil
	}
	m.PersistentScheduling = true // Standalone DSH members persist counts as well.
	state, err := m.Store.loadScheduling(m.Options.MemberIdentity)
	if errors.Is(err, os.ErrNotExist) {
		// Only untouched members have a trustworthy first-generation baseline.
		state = memberScheduling{Version: formatVersion, MemberIdentity: m.Options.MemberIdentity, Used: max(m.turnsUsed, m.state.TurnNumber), Limit: m.turnLimit}
		var ref dshReference
		var exists bool
		if m.Options.StatePath != "" {
			var refErr error
			ref, exists, refErr = (dshBackend{store: m.Store}).reference(m.Options)
			if refErr != nil {
				return refErr
			}
		}
		if m.state.TurnNumber == 0 && m.turnsUsed == 0 && !exists && !ref.Pending && ref.Directory == "" {
			generation, err := m.Backend.(rotatingBackend).sessionGeneration(m.Options)
			if err != nil {
				return err
			}
			state.Version, state.SessionGeneration = schedulingVersion, generation
		}
		err = m.Store.saveScheduling(state)
	}
	if err != nil {
		return err
	}
	if m.automaticRotation() && state.Version == formatVersion {
		state, err = convertScheduling(state)
		if err != nil {
			return err
		}
		state.Limit = 0
		if err = m.Store.saveScheduling(state); err != nil {
			return err
		}
	}
	m.schedulingVersion, m.sessionGeneration, m.sessionStartUsed = state.Version, state.SessionGeneration, state.SessionStartUsed
	m.enabled, m.recoveryAttempted = state.Enabled, state.RecoveryAttempted
	m.turnsUsed, m.turnLimit = state.Used, max(m.turnLimit, state.Limit)
	m.schedulingReady = true
	generation, err := m.Backend.(rotatingBackend).sessionGeneration(m.Options)
	if err != nil {
		return err
	}
	m.currentGeneration = generation
	if state.Version == schedulingVersion {
		if (state.SessionGeneration == "" && !m.automaticRotation()) || (state.SessionGeneration != "" && state.SessionGeneration != generation) {
			m.BackendBlocked = "Session generation and collaboration baseline require reconciliation"
		}
	}
	return nil
}

func (s *service) recoverMemberRotation(id MemberIdentity, agent AgentSnapshot) (*rotationRecord, error) {
	r, err := s.store.loadRotation(id)
	if err != nil || r == nil {
		return r, err
	}
	state, err := s.store.loadScheduling(id)
	if errors.Is(err, os.ErrNotExist) {
		return r, fmt.Errorf("%w: rotation has no collaboration checkpoint", ErrInvalid)
	}
	if err != nil {
		return r, err
	}
	if state.Used < agent.TurnNumber {
		return r, ErrConflict
	}
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(s.store.dir, "agents", id.MembershipID, "session.json")}
	return r, reconcileRotation(s.store, opts, r, &state)
}
