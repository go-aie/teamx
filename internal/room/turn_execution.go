package room

import "path/filepath"

func (s *service) executionBindingForTurn(id MemberIdentity, turn uint64) (turnExecution, error) {
	state, err := s.store.LoadAgent(id)
	if err != nil {
		return turnExecution{}, err
	}
	if state.Turn == nil || state.Turn.Number != turn || state.Turn.Status != "running" || turn == 0 {
		return turnExecution{}, ErrIdentity
	}
	// Backends without Sessions still get a stable per-Room execution namespace;
	// real DSH always resolves its actual persisted generation.
	generation := "local_" + id.MembershipID
	ref, exists, err := readSessionReference(filepath.Join(s.store.dir, "agents", id.MembershipID, "session.json"), id)
	if err != nil {
		return turnExecution{}, err
	}
	if exists {
		if ref.Pending || ref.Directory == "" {
			return turnExecution{}, ErrIdentity
		}
		generation = ref.Directory
	}
	return turnExecution{Generation: generation, Turn: turn}, nil
}
