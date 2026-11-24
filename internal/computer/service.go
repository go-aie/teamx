package computer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-aie/teamx/internal/persistence"
)

// Pending is a host association, never a copy of native arguments or results.
// Room/session owners persist their execution-scope linkage separately.
type Pending struct {
	Screen ScreenInfo `json:"screen"`
	ID     string     `json:"id"`
	Scope  Scope      `json:"scope"`
	Tool   string     `json:"tool"`
	Digest string     `json:"digest"`
}
type hostState struct {
	Version int                `json:"version"`
	Pending map[string]Pending `json:"pending"`
}

// Service is the provider-independent controlled entry point. Its caller owns
// the service-generation lease. The service exclusively locks its state
// directory and performs no IO until explicitly opened; normal TeamX startup does not construct it.
type Service struct {
	mu     sync.Mutex
	path   string
	writer *persistence.Writer
	unlock func() error
	closed bool
	state  hostState
}

func OpenService(dir string) (*Service, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	unlock, err := lockServiceDirectory(dir)
	if err != nil {
		return nil, err
	}
	s := &Service{unlock: unlock, path: filepath.Join(dir, "pending.json"), writer: persistence.NewWriter(persistence.DefaultOperations()), state: hostState{Version: 1, Pending: map[string]Pending{}}}
	if err := persistence.ReadJSON(s.path, &s.state, 4<<20); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = unlock()
		return nil, err
	}
	if s.state.Version != 1 || s.state.Pending == nil || len(s.state.Pending) > 1024 {
		_ = unlock()
		return nil, Fail(Invalid, "invalid host association snapshot")
	}
	return s, nil
}
func (s *Service) Pending() []Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Pending, 0, len(s.state.Pending))
	for _, p := range s.state.Pending {
		out = append(out, p)
	}
	return out
}
func (s *Service) save() error { return s.writer.Save(s.path, s.state, 4<<20) }
func (s *Service) Call(ctx context.Context, screen Screen, c Call) (Outcome, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Outcome{}, persistence.ErrClosed
	}
	contract, err := screen.Tools(ctx)
	if err != nil {
		return Outcome{}, err
	}
	tool, ok := contract.Tool(c.Tool)
	if !ok || c.ContractDigest != contract.Digest {
		return Outcome{}, Fail(ContractMismatch, "call is outside frozen native contract")
	}
	if tool.ReadOnly {
		return screen.Call(ctx, c)
	}
	info := screen.Info()
	key := info.ID + "/" + c.ID
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return Outcome{}, persistence.ErrClosed
	}
	if old, exists := s.state.Pending[key]; exists {
		if old.Digest != c.Digest() || old.Screen != info {
			s.mu.Unlock()
			return Outcome{}, Fail(Invalid, "pending operation identity changed")
		}
		s.mu.Unlock()
		// A repeated host invocation is reconciliation only, even if the first
		// transport failed before the Runtime received the request.
		return s.Reconcile(ctx, screen, c.ID)
	}
	if len(s.state.Pending) >= 1024 {
		s.mu.Unlock()
		return Outcome{}, Fail(Capacity, "host pending association limit reached")
	}
	s.state.Pending[key] = Pending{Screen: info, ID: c.ID, Scope: c.Scope, Tool: c.Tool, Digest: c.Digest()}
	if err = s.save(); err != nil {
		s.mu.Unlock()
		return Outcome{}, err
	}
	s.mu.Unlock()
	outcome, err := screen.Call(ctx, c)
	if err != nil {
		var refused *AdmissionRefusal
		if errors.As(err, &refused) {
			return outcome, errors.Join(err, s.rejected(key, c.Digest()))
		}
		// A verified admission refusal accepted no operation. Transport errors,
		// cancellations and unknown outcomes keep the original association.
		for _, code := range []Code{Disabled, Invalid, Capacity, Busy, HumanControl, Stale, ContractMismatch} {
			if Is(err, code) {
				return outcome, errors.Join(err, s.rejected(key, c.Digest()))
			}
		}
		return outcome, err
	}
	return outcome, s.finish(screen, c.ID, outcome)
}
func (s *Service) rejected(key, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return persistence.ErrClosed
	}
	if p, ok := s.state.Pending[key]; ok && p.Digest == digest {
		delete(s.state.Pending, key)
		return s.save()
	}
	return nil
}

func (s *Service) Reconcile(ctx context.Context, screen Screen, id string) (Outcome, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return Outcome{}, persistence.ErrClosed
	}
	outcome, err := screen.Operation(ctx, id)
	if err != nil {
		return outcome, err
	}
	return outcome, s.finish(screen, id, outcome)
}
func (s *Service) finish(screen Screen, id string, outcome Outcome) error {
	if !outcome.Operation.Settled {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return persistence.ErrClosed
	}
	key := screen.Info().ID + "/" + id
	if old, exists := s.state.Pending[key]; exists {
		if old.Digest != outcome.Operation.Digest {
			return Fail(Invalid, "remote operation association mismatch")
		}
		delete(s.state.Pending, key)
		return s.save()
	}
	return nil
}
func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	if err := s.writer.Close(ctx); err != nil {
		return err
	}
	s.closed = true
	return s.unlock()
}

// SettleStopped retires host associations after the application has obtained a
// successful Screen.Stop for this exact identity and drained scope. It records
// no business success and is never used to retry the corresponding actions.
func (s *Service) SettleStopped(screen Screen, scope Scope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return persistence.ErrClosed
	}
	for key, p := range s.state.Pending {
		if p.Screen.ID == screen.Info().ID && p.Screen.Binding.Key() == screen.Info().Binding.Key() && scope.Matches(p.Scope) {
			delete(s.state.Pending, key)
		}
	}
	return s.save()
}
