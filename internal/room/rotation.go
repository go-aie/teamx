package room

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/go-aie/actorx"
)

type rotatingBackend interface {
	reference(SessionOptions) (sessionReference, bool, error)
	sessionGeneration(SessionOptions) (string, error)
	checkRotationClosed(SessionOptions, string, bool) error
	switchSession(SessionOptions, string, string) (Backend, error)
}

// Service admission remains owned across a timed-out HTTP call. Actor callbacks
// never wait for platform calls, a service mutex, or Session cleanup.
type rotationLease struct {
	begin     func(context.Context) error
	release   func()
	once      sync.Once
	gateOnce  sync.Once
	gateDone  chan struct{}
	gateError error
}

func (l *rotationLease) start(ctx context.Context) {
	if l == nil {
		return
	}
	l.gateOnce.Do(func() { go func() { l.gateError = l.begin(ctx); close(l.gateDone) }() })
}
func (l *rotationLease) wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.start(ctx)
	select {
	case <-l.gateDone:
		return l.gateError
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (l *rotationLease) close() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		// A pre-admission failure may close a lease before platform work started.
		l.gateOnce.Do(func() { close(l.gateDone) })
		select {
		case <-l.gateDone:
			l.release()
		default:
			go func() { <-l.gateDone; l.release() }()
		}
	})
}

type memberRotation struct{ session *ownedSession }

func (s *service) rotateSession(ctx context.Context, req apiRequest) (MemberStatus, error) {
	status, err := s.control(ctx, req, "rotate")
	if err != nil {
		return status, err
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if status.Rotation != nil {
			switch status.Rotation.Status {
			case "completed":
				return status, nil
			case "failed":
				return status, fmt.Errorf("rotation failed: %s", status.Rotation.Error)
			}
		}
		select {
		case <-wait.Done():
			return status, fmt.Errorf("rotation incomplete; query or continue the same operation_id: %w", wait.Err())
		case <-ticker.C:
		}
		status, err = s.target(req.MembershipID).Call[MemberStatus](wait, InspectMember{id})
		if err != nil {
			return status, fmt.Errorf("rotation outcome unknown; inspect member list: %w", err)
		}
	}
}

func (m *MemberAgent) rotationStatus(status string) {
	r := m.rotationRecord
	m.rotation = &SessionRotation{Cancel: r.Cancel, Resume: r.Resume, Replenish: r.Replenish, Status: status, OperationID: r.OperationID, Reason: r.Reason, FromGeneration: r.From, ToGeneration: r.To, BaseUsed: r.BaseUsed, Phase: r.Phase, Error: r.Error, FailedStep: r.FailedStep, ContextReset: r.Phase == "completed"}
}
func (m *MemberAgent) prepareRotation(msg ControlMember) error {
	if m.ModelGate != "" && msg.modelTarget == nil {
		return ErrBusy
	}
	backend, ok := m.Backend.(rotatingBackend)
	if !ok {
		return fmt.Errorf("%w: session rotation requires a capable backend", ErrRequest)
	}
	if msg.modelTarget != nil {
		if _, ok := m.Backend.(modelChangingBackend); !ok || msg.Cancel || msg.Resume || msg.Replenish || msg.automatic {
			return ErrRequest
		}
		if err := msg.modelTarget.validate(m.Options.MemberIdentity); err != nil {
			return err
		}
	}

	if msg.automatic {
		// Recheck current Room/Inbox facts after asynchronous lifecycle admission.
		if msg.State.MemberIdentity != m.Options.MemberIdentity {
			return ErrIdentity
		}
		if m.latest == nil || msg.State.Revision >= m.latest.Revision {
			state := msg.State
			m.latest = &state
		}
		if msg.automaticInbox != nil {
			if msg.automaticInbox.MemberIdentity != m.Options.MemberIdentity {
				return ErrIdentity
			}
			status := *msg.automaticInbox
			m.inbox = &status
		}
		eligible := m.automaticRotation() && m.ready() && m.needsRotation()
		if msg.recovering {
			eligible = m.canRecoverSession()
		}
		if !eligible || m.active || msg.FromGeneration != m.currentGeneration {
			return ErrConflict
		}
	}
	operationID := msg.OperationID
	if operationID == "" {
		var err error
		operationID, err = newOpaqueID()
		if err != nil {
			return err
		}
	}
	if !validRestartID(operationID) {
		return ErrRequest
	}
	reason := msg.Reason
	if reason == "" {
		reason = "manual"
	}
	if !validText(reason) || len(reason) > 256 {
		return ErrRequest
	}
	current, err := backend.sessionGeneration(m.Options)
	if err != nil {
		return err
	}
	from := msg.FromGeneration
	if from == "" {
		from = current
	}
	if r := m.rotationRecord; r != nil && r.OperationID == operationID {
		var retryTarget *modelSessionSnapshot
		if msg.modelTarget != nil {
			snapshot := msg.modelTarget.clone()
			snapshot.Generation = r.To
			retryTarget = &snapshot
		}
		if !reflect.DeepEqual(r.ModelTarget, retryTarget) {
			return ErrConflict
		}
		if r.From != from || r.Reason != reason || r.Cancel != msg.Cancel || r.Resume != msg.Resume || r.Replenish != msg.Replenish {
			return ErrConflict
		}
		if r.Phase == "completed" || m.rotationWork != nil || m.rotation.Status == "waiting" || !msg.ContinueRotation {
			return nil
		}
		if m.fault {
			return fmt.Errorf("checkpoint failure requires service recovery: %s", m.lastError)
		}
		if m.turnsUsed != r.BaseUsed || (current != r.From && current != r.To) {
			return ErrConflict
		}
		m.rotationEnable = r.Replenish
		m.rotationLease = msg.lease
		r.Error, r.FailedStep = "", ""
		m.rotationStatus("waiting")
		if r.ModelTarget == nil {
			m.rotationLease.start(m.owner)
		}
		m.rotate()
		return nil
	}
	if m.fault {
		return fmt.Errorf("checkpoint failure requires service recovery: %s", m.lastError)
	}
	if msg.ContinueRotation {
		return fmt.Errorf("%w: no matching rotation to continue", ErrConflict)
	}
	if checker, ok := m.Backend.(backendRunCheck); ok && msg.Resume {
		if err := checker.CheckRun(); err != nil {
			return err
		}
	}
	if m.rotationRecord != nil && m.rotationRecord.Phase != "completed" {
		return ErrBusy
	}
	if m.rotationWork != nil {
		return ErrBusy
	}
	if from != current {
		return fmt.Errorf("%w: stale from_generation; current=%s", ErrConflict, current)
	}
	if m.schedulingVersion == schedulingVersion && m.sessionGeneration != "" && m.sessionGeneration != current {
		return fmt.Errorf("%w: Session baseline disagrees with reference", ErrConflict)
	}
	target, err := newOpaqueID()
	if err != nil {
		return err
	}
	r := &rotationRecord{Version: rotationVersion, MemberIdentity: m.Options.MemberIdentity, OperationID: operationID, Reason: reason, From: from, To: sessionGenerationForOperation(target), BaseUsed: m.turnsUsed, Phase: "prepared", Cancel: msg.Cancel, Resume: msg.Resume, Replenish: msg.Replenish}
	if msg.modelTarget != nil {
		snapshot := msg.modelTarget.clone()
		snapshot.Generation = r.To
		r.ModelTarget = &snapshot
	}
	r.Recovery = msg.recovering
	if msg.recovering {
		// Commit a bounded recovery allowance before any lifecycle operation.
		// A restart must not turn a persistent failure into an endless loop.
		m.recoveryAttempted = true
		if err := m.persistScheduling(m.enabled, m.turnsUsed); err != nil {
			return err
		}
	}
	// Freeze first, including already admitted attempts in BaseUsed. Even an
	// uncertain prepared write leaves this member blocked until reconciliation.
	m.withdrawTurn()
	m.rotationRecord = r

	m.rotationStatus("waiting")
	// Default manual pause must already be durable if prepared commits and the
	// process stops before the next write. A later explicit run intent is saved
	// only after prepared has durably frozen this operation.
	if !msg.automatic && msg.modelTarget == nil && !msg.Resume {
		if err := m.persistScheduling(false, m.turnsUsed); err != nil {
			m.rotationFailure("intent_before_prepare", err)
			return err
		}
		m.paused, m.retry = true, false
	}
	if err := m.Store.saveRotation(*r); err != nil {
		m.rotationFailure("prepared", err)
		return err
	}
	if !msg.automatic && msg.Resume {
		if err := m.persistScheduling(true, m.turnsUsed); err != nil {
			m.rotationFailure("intent", err)
			return err
		}
	}
	if !msg.automatic && msg.modelTarget == nil {
		m.paused, m.retry = !msg.Resume, false
		m.RecoveryPending, m.recoveryArmed = false, false
	}
	m.rotationEnable = msg.Replenish
	m.rotationLease = msg.lease
	if msg.Cancel && m.cancel != nil {
		m.cancel()
	}
	if r.ModelTarget == nil {
		m.rotationLease.start(m.owner)
	}
	m.rotate()
	return nil
}
func (m *MemberAgent) rotationFailure(step string, err error) {
	m.rotationRecord.Error, m.rotationRecord.FailedStep = err.Error(), step
	// Best effort diagnostic only; never advance a phase after an uncertain save.
	if step != "intent_before_prepare" {
		_ = m.Store.saveRotation(*m.rotationRecord)
	}
	m.rotationStatus("failed")
	m.lastError = err.Error()
	m.BackendBlocked = "rotation incomplete; continue the same operation: " + err.Error()
	m.rotationLease.close()
	m.rotationLease = nil
}

// Called only after the turn completion callback has adopted all resources.
func (m *MemberAgent) rotate() {
	if m.rotation == nil || m.rotation.Status != "waiting" || m.active || m.rotationWork != nil {
		return
	}
	if m.fault {
		m.rotationFailure("checkpoint", errors.New("checkpoint failure requires service recovery"))
		return
	}
	backend := m.Backend.(rotatingBackend)
	opts := m.Options
	record := *m.rotationRecord
	lease := m.rotationLease
	work := &memberRotation{session: m.session}
	type result struct {
		backend Backend
		record  rotationRecord
		step    string
	}
	_, err := actorx.StartTask(m.owner, func(ctx context.Context) (result, error) {
		r := result{record: record, step: "platform_settlement"}
		if err := lease.wait(ctx); err != nil {
			return r, err
		}
		if r.record.Phase == "prepared" {
			r.step = "old_cleanup"
			if err := work.session.close(); !backendCleanupSettled(err) {
				return r, fmt.Errorf("old Session cleanup failed: %w", err)
			}
			if err := ctx.Err(); err != nil {
				return r, err
			}
			if err := backend.checkRotationClosed(opts, record.From, work.session != nil); err != nil {
				return r, err
			}
			r.step = "old_closed"
			closed := r.record
			closed.Phase = "old_closed"
			if err := m.Store.saveRotation(closed); err != nil {
				return r, err
			}
			r.record = closed
		}
		r.step = "session_reference"
		var next Backend
		var err error
		if record.ModelTarget != nil {
			next, err = backend.(modelChangingBackend).switchModel(opts, record.From, record.To, *record.ModelTarget)
		} else {
			next, err = backend.switchSession(opts, record.From, record.To)
		}
		r.backend = next
		return r, err
	}, func(r result, err error) bool {
		defer m.observeStatus()
		m.rotationWork = nil
		// Identity checks also protect future asynchronous automatic callbacks.
		if m.rotationRecord == nil || m.rotationRecord.OperationID != record.OperationID || m.rotationRecord.To != record.To {
			lease.close()
			return true
		}
		m.rotationRecord = &r.record
		if err != nil {
			m.session = work.session
			m.rotationFailure(r.step, err)
			return true
		}
		m.session = nil
		m.currentGeneration = record.To
		m.Backend = r.backend
		m.schedulingVersion, m.sessionGeneration, m.sessionStartUsed = schedulingVersion, record.To, record.BaseUsed
		if record.Recovery {
			// A newer explicit pause always wins over asynchronous cleanup.
			m.paused = !m.enabled
			m.retry = m.enabled
			m.RecoveryPending = false
		}
		if err := m.persistScheduling(m.enabled, m.turnsUsed); err != nil {
			m.rotationFailure("collaboration_baseline", err)
			return true
		}
		completed := r.record
		completed.Phase = "completed"
		if err := m.Store.saveRotation(completed); err != nil {
			m.rotationFailure("completed", err)
			return true
		}
		m.rotationRecord = &completed
		m.rotationStatus("completed")
		if record.ModelTarget != nil {
			m.ModelGate = ""
		}
		m.BackendBlocked, m.lastError = "", ""
		lease.close()
		m.rotationLease = nil
		if !m.paused && m.rotationEnable {
			if err := m.resumeScheduling(true); err != nil {
				m.lastError = err.Error()
			}
			m.retry = !m.paused
		}
		m.schedule()
		return true
	}, func(result) {
		defer lease.close()
		if err := work.session.close(); err != nil {
			m.owner.Stop(errors.Join(errors.New("rotation discard cleanup failed"), err))
		}
	})
	if err != nil {
		m.rotationFailure("task_admission", err)
		return
	}
	m.session = nil
	m.rotationWork = work
	m.rotation.Status = "closing"
}
