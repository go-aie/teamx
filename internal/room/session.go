package room

import (
	"context"
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
	once sync.Once
	err  error
}

func (s *ownedSession) close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		if session, ok := s.Session.(*dshSession); ok {
			// The actor remains owned until DSH confirms cleanup. Service Wait
			// has its own deadline and retains the Store if this is unfinished.
			s.err = session.closeOwned()
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.err = s.Close(ctx)
	})
	return s.err
}
