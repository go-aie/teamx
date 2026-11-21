package room

import (
	"context"
	"errors"
	"time"

	"github.com/go-aie/teamx/agent"
)

// Private capability contracts describe existing operations. A Backend that only
// implements OpenSession remains usable by bounded members and test fixtures.
type ownedSessionCloser interface{ closeOwned() error }
type backendRunCheck interface{ CheckRun() error }
type modelChangingBackend interface {
	switchModel(SessionOptions, string, string, modelSessionSnapshot) (Backend, error)
}
type backendRecoveryPolicy interface {
	recoverableFailure(error) bool
	settledOpenFailure(SessionOptions, error) bool
}

// storedBackend reads the same adapter-owned reference and settlement evidence
// for startup, closed Rooms, rotation reconciliation and model application.
// Calls neither launch a process nor admit a model turn.
type storedBackend interface {
	rotatingBackend
	checkResume(SessionOptions, string) error
	checkReset(SessionOptions, string) error
	activity(MemberIdentity, bool) (backendActivity, error)
	switchPreparedSession(SessionOptions, string, string, *modelSessionSnapshot) (Backend, error)
}

type sessionReference = agent.Reference

// Keep historical diagnostic text so persisted error recognition remains valid.
var errBackendPaused = errors.New("DSH member paused; inspect backend failure before continuing")
var errBackendDisabled = errors.New("DSH backend disabled; replacement execution is forbidden")

type pendingBackendClose struct{ error }

func (e *pendingBackendClose) Unwrap() error { return e.error }

// A settled close may still carry a resume/checkpoint failure. This receipt
// permits checking rotation, never replaying or reusing an original Session.
type settledBackendClose struct{ error }

func (e *settledBackendClose) Unwrap() error { return e.error }
func backendCleanupSettled(err error) bool {
	var closed *settledBackendClose
	var pending *pendingBackendClose
	return err == nil || !errors.As(err, &pending) && errors.As(err, &closed)
}

func recoverableBackendFailure(backend Backend, err error) bool {
	policy, ok := backend.(backendRecoveryPolicy)
	return ok && policy.recoverableFailure(err)
}
func settledSessionOpenFailure(backend Backend, opts SessionOptions, err error) bool {
	policy, ok := backend.(backendRecoveryPolicy)
	return ok && policy.settledOpenFailure(opts, err)
}

// The checkpoint owner is unchanged. The Actor needs no private backend files,
// Store mutex or directory access. finishMCPTurn only retires turn resources;
// formal input confirmation remains in its existing proof/owner protocol.
type memberStateStore interface {
	SaveAgent(AgentSnapshot) error
	loadScheduling(MemberIdentity) (memberScheduling, error)
	saveScheduling(memberScheduling) error
	loadRotation(MemberIdentity) (*rotationRecord, error)
	saveRotation(rotationRecord) error
	finishMCPTurn(MemberIdentity, uint64)
	continueInputRecovery(MemberIdentity, func() time.Time) error
}

// Cancellation classification is supplied by the executing backend. Plain
// fixtures retain the conservative context-only behavior.
func backendTurnCancellation(backend Backend, err error) bool {
	if policy, ok := backend.(interface{ settledCancellation(error) bool }); ok {
		return policy.settledCancellation(err)
	}
	return cancellationOnly(err, func(err error) bool { return err == context.Canceled })
}
func cancellationOnly(err error, allowed func(error) bool) bool {
	if !errors.Is(err, context.Canceled) {
		return false
	}
	var walk func(error) bool
	walk = func(err error) bool {
		if allowed(err) {
			return true
		}
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				if !walk(child) {
					return false
				}
			}
			return true
		case interface{ Unwrap() error }:
			return walk(wrapped.Unwrap())
		}
		return false
	}
	return walk(err)
}

type backendRecovery struct {
	reference        sessionReference
	resume, recovery bool
	blocked, notice  string
}

type activeSessionOutput interface{ currentActivity() *backendDiagnostic }
type backendActivity struct {
	last, current *backendDiagnostic
	updated       *time.Time
	error         string
}
