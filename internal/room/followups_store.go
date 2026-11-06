package room

import (
	"path/filepath"
)

func (s *Store) inboxEntryLocked(id MemberIdentity, seq uint64) (InboxEntry, error) {
	var e InboxEntry
	if _, _, err := s.memberPath(id, "agent.json", false); err != nil {
		return e, err
	}
	if err := readSnapshot(filepath.Join(s.dir, "inboxes", id.MembershipID, inboxEntryName(seq)), &e, DefaultInboxLimits().EntryBytes); err != nil {
		return e, err
	}
	if err := e.validate(id); err != nil {
		return e, err
	}
	if e.Seq != seq {
		return e, ErrInvalid
	}
	return e, nil
}
func (s *Store) runningTurnLocked(id MemberIdentity, turn uint64) (AgentSnapshot, error) {
	var agent AgentSnapshot
	path, _, err := s.memberPath(id, "agent.json", true)
	if err != nil {
		return agent, err
	}
	if err := readSnapshot(path, &agent, maxPrivateFileBytes); err != nil {
		return agent, err
	}
	if agent.MemberIdentity != id || agent.validate() != nil {
		return agent, ErrInvalid
	}
	if agent.Turn == nil || agent.Turn.Status != "running" {
		return agent, conflictReason("turn_number", "no running turn for this membership", "Wait for a running turn; do not replay uncertain actions.")
	}
	if turn != 0 && agent.TurnNumber != turn {
		return agent, versionConflict("turn_number", turn, agent.TurnNumber, "Use the runtime current_turn after restoring context; do not replay uncertain actions.")
	}
	return agent, nil
}
func validFollowupStatus(status string) bool {
	switch status {
	case "pending", "in_progress", "waiting_user", "waiting_followup", "completed", "archived", "unknown":
		return true
	}
	return false
}
func handlingText(text string) bool { return validText(text) && len(text) <= 4096 }
