package room

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Hide concrete adapter/session types while exercising the real member entry
// points. Forwarding keeps the adapter's files and failure semantics intact.
type lifecycleTestCapabilities interface {
	Backend
	backendRunCheck
	rotatingBackend
	backendRecoveryPolicy
	modelChangingBackend
	settledCancellation(error) bool
}

type hiddenLifecycleBackend struct{ lifecycleTestCapabilities }
type hiddenLifecycleSession struct {
	Session
	ownedSessionCloser
}

func (b hiddenLifecycleBackend) OpenSession(ctx context.Context, opts SessionOptions) (Session, error) {
	s, err := b.lifecycleTestCapabilities.OpenSession(ctx, opts)
	if s == nil {
		return nil, err
	}
	return hiddenLifecycleSession{s, s.(ownedSessionCloser)}, err
}
func (b hiddenLifecycleBackend) switchSession(opts SessionOptions, from, to string) (Backend, error) {
	next, err := b.lifecycleTestCapabilities.switchSession(opts, from, to)
	if err != nil {
		return nil, err
	}
	return hiddenLifecycleBackend{next.(lifecycleTestCapabilities)}, nil
}
func (b hiddenLifecycleBackend) switchModel(opts SessionOptions, from, to string, target modelSessionSnapshot) (Backend, error) {
	next, err := b.lifecycleTestCapabilities.switchModel(opts, from, to, target)
	if err != nil {
		return nil, err
	}
	return hiddenLifecycleBackend{next.(lifecycleTestCapabilities)}, nil
}

type settlingTestSession struct {
	entered, release chan struct{}
	closes, owned    atomic.Int32
}

func (*settlingTestSession) Run(context.Context, UnreadChanged) error { return nil }
func (s *settlingTestSession) Close(context.Context) error {
	s.closes.Add(1)
	return errors.New("bounded close cannot prove owned settlement")
}
func (s *settlingTestSession) closeOwned() error {
	s.owned.Add(1)
	close(s.entered)
	<-s.release
	return nil
}

func TestOwnedSessionWaitsForLifecycleSettlement(t *testing.T) {
	session := &settlingTestSession{entered: make(chan struct{}), release: make(chan struct{})}
	owned := &ownedSession{Session: session}
	results := make(chan error, 2)
	go func() { results <- owned.close() }()
	<-session.entered
	go func() { results <- owned.close() }()
	select {
	case err := <-results:
		t.Fatal("ownership released before settlement", err)
	default:
	}
	close(session.release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if session.owned.Load() != 1 || session.closes.Load() != 0 {
		t.Fatal("owned close was bypassed or repeated", session.owned.Load(), session.closes.Load())
	}
}

func TestBackendClosePreservesSettlementAndDiagnostics(t *testing.T) {
	failure := errors.New("checkpoint unavailable")
	for _, input := range []error{
		nil, failure, dsh.ErrCleanupPending,
		&dsh.ClosedError{Err: failure},
		errors.Join(&dsh.ClosedError{Err: failure}, dsh.ErrCleanupPending),
		errors.Join(&dsh.ClosedError{Err: failure}, context.Canceled),
	} {
		output := dshCloseResult(input)
		if backendCleanupSettled(output) != dsh.CleanupSettled(input) {
			t.Fatal("component cleanup fact changed", input, output)
		}
		if input != nil && (output.Error() != input.Error() || !errors.Is(output, input)) {
			t.Fatal("diagnostic chain changed", input, output)
		}
	}
	joined := errors.Join(dshCloseResult(&dsh.ClosedError{Err: failure}), dshCloseResult(dsh.ErrCleanupPending))
	if backendCleanupSettled(joined) {
		t.Fatal("a settled resource hid another resource's pending cleanup")
	}
}
