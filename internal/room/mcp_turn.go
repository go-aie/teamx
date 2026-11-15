package room

import "sync"

// mcpTurnOperations is an in-memory operation barrier, not a delivery ledger.
type mcpTurnOperations struct {
	mu      sync.Mutex
	turns   map[MemberIdentity]*mcpTurnOperation
	inboxes map[MemberIdentity]*mcpInboxSession
}

type mcpTurnOperation struct {
	number  uint64
	closing bool
	mode    string
	active  sync.WaitGroup
}

func (s *Store) beginMCPRead(id MemberIdentity, turn uint64) (func(), error) {
	s.mcpOperations.mu.Lock()
	defer s.mcpOperations.mu.Unlock()
	if turn == 0 {
		return nil, ErrRequest
	}
	s.mu.Lock()
	_, err := s.runningTurnLocked(id, turn)
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if s.mcpOperations.turns == nil {
		s.mcpOperations.turns = make(map[MemberIdentity]*mcpTurnOperation)
	}
	op := s.mcpOperations.turns[id]
	if op == nil || op.number != turn {
		op = &mcpTurnOperation{number: turn}
		s.mcpOperations.turns[id] = op
	}
	if op.closing {
		return nil, ErrClosing
	}
	op.active.Add(1)
	return sync.OnceFunc(op.active.Done), nil
}

// Stop admission before waiting, and wait before writing the terminal turn.
// No Store file mutex is held while a read/confirm is completing.
func (s *Store) finishMCPTurn(id MemberIdentity, turn uint64) {
	s.mcpOperations.mu.Lock()
	if s.mcpOperations.turns == nil {
		s.mcpOperations.turns = make(map[MemberIdentity]*mcpTurnOperation)
	}
	op := s.mcpOperations.turns[id]
	if op == nil || op.number != turn {
		op = &mcpTurnOperation{number: turn}
		s.mcpOperations.turns[id] = op
	}
	op.closing = true
	s.mcpOperations.mu.Unlock()
	op.active.Wait()
}
