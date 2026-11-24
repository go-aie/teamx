package room

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Backend and Session define the lifecycle used by TeamX Room members.
type Backend interface {
	OpenSession(context.Context, SessionOptions) (Session, error)
}

type Session interface {
	Run(context.Context, UnreadChanged) error
	Close(context.Context) error
}

type SessionOptions struct {
	MemberIdentity
	StatePath string
	CLIPath   string
	CLIEnv    []string
}

// Lifecycle and discard can converge on the same resource after a panic.
type ownedSession struct {
	Session
	mu        sync.Mutex
	attempted bool
	err       error
}

func (s *ownedSession) close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attempted {
		var pending *pendingBackendClose
		if !errors.As(s.err, &pending) {
			return s.err
		}
	}
	s.attempted = true
	if session, ok := s.Session.(ownedSessionCloser); ok {
		s.err = session.closeOwned()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.err = s.Close(ctx)
	}

	return s.err
}
