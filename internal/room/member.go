package room

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
)

const defaultMaxTurns uint64 = 16

// MemberAgent owns scheduling and agent.json. The worker owns session.json
// through Session. Neither worker mutates the Actor's live state.
//
//actorx:actor
type MemberAgent struct {
	Options              SessionOptions
	Backend              Backend
	Store                *Store
	Room                 actorx.Addr
	MergeWindow          time.Duration
	Restored             *AgentSnapshot // Loaded and validated before Spawn; starts paused.
	MaxTurns             uint64         // Cumulative limit for backends without automatic rotation.
	SessionMaxTurns      uint64
	RequestRotation      func(context.Context, MemberIdentity, string) error
	RequestRecovery      func(context.Context, MemberIdentity, string) error
	RecoveryPending      bool // Startup candidate; armed only after HTTP is serving.
	recoveryArmed        bool
	recoveryAttempted    bool
	enabled              bool // Durable user intent, independent of fault pause.
	rotationRequest      bool
	rotationNext         time.Time
	BackendBlocked       string // Restored backend cannot safely open a new session.
	PersistentScheduling bool
	RestoredTurnsUsed    uint64
	RestoredTurnLimit    uint64
	ExecutionFrozen      *atomic.Bool                               // shared service shutdown boundary
	Inbox                func(context.Context) (InboxStatus, error) // Bounded, asynchronous reconciliation.
	inbox                *InboxStatus
	inboxPolling         bool
	inboxNext            time.Time

	owner             actorx.Context
	latest            *UnreadChanged
	state             AgentSnapshot
	session           *ownedSession
	work              *memberTurn
	active            bool
	paused            bool
	retry             bool
	fault             bool
	lastError         string
	due               time.Time
	cancel            context.CancelFunc
	now               func() time.Time
	turnsUsed         uint64
	turnLimit         uint64
	rotation          *SessionRotation
	rotationWork      *memberRotation
	rotationRecord    *rotationRecord
	rotationLease     *rotationLease
	schedulingVersion int
	schedulingReady   bool
	pauseRevision     uint64
	currentGeneration string
	sessionGeneration string
	sessionStartUsed  uint64
	rotationEnable    bool

	scheduler     actorx.Addr
	target        actorx.Addr
	request       *requestTurn
	requestNumber uint64
	checking      bool
	checked       *checkedTurn
	releasing     map[turnGranted]struct{}
}

func (m *MemberAgent) Started(ctx actorx.Context, _ actorx.Started) error {
	m.owner = ctx.WithContext(context.WithoutCancel(ctx))
	m.paused = true // New members start only after an explicit run request.
	m.turnsUsed = m.RestoredTurnsUsed
	m.lastError = m.BackendBlocked
	if m.MaxTurns == 0 {
		m.MaxTurns = defaultMaxTurns
	}
	m.turnLimit = max(m.MaxTurns, m.RestoredTurnLimit)
	m.state = AgentSnapshot{Version: formatVersion, MemberIdentity: m.Options.MemberIdentity}
	if m.Restored != nil {
		m.state = *m.Restored
		if m.state.MemberIdentity != m.Options.MemberIdentity {
			return ErrIdentity
		}
		if err := m.state.validate(); err != nil {
			return err
		}
		if m.state.Turn != nil && m.state.Turn.Status == "running" {
			return fmt.Errorf("running checkpoint must be recovered before Spawn")
		}
	}
	if m.now == nil {
		m.now = time.Now
	}
	if err := m.initializeScheduling(); err != nil && !errors.Is(err, ErrIdentity) {
		return err
	}
	if _, capable := m.Backend.(rotatingBackend); m.schedulingReady && capable {
		if r, err := m.Store.loadRotation(m.Options.MemberIdentity); err != nil {
			return err
		} else if r != nil {
			m.rotationRecord = r
			if r.Phase == "completed" {
				m.rotationStatus("completed")
			} else {
				m.rotationStatus("failed")
				m.BackendBlocked = "rotation requires reconciliation"
			}
		}
	}

	return m.maintain()
}

func (m *MemberAgent) Notify(_ actorx.Context, msg UnreadChanged) error {
	if msg.MemberIdentity != m.Options.MemberIdentity {
		return ErrIdentity
	}
	if m.latest == nil || msg.Revision > m.latest.Revision {
		copy := msg
		m.latest = &copy
		if m.due.IsZero() {
			m.due = m.now().Add(m.MergeWindow)
		}
	}
	m.schedule()
	return nil
}

func (m *MemberAgent) Inspect(_ actorx.Context, msg InspectMember) (MemberStatus, error) {
	if msg.MemberIdentity != m.Options.MemberIdentity {
		return MemberStatus{}, ErrIdentity
	}
	return m.status(), nil
}

func (m *MemberAgent) status() MemberStatus {
	status := "idle"
	switch {
	case m.rotation != nil && (m.rotation.Status == "waiting" || m.rotation.Status == "closing"):
		status = "rotating"
	case m.fault:
		status = "checkpoint_failed"
	case m.active:
		status = "running"
	case m.request != nil:
		status = "waiting"
	case !m.automaticRotation() && m.turnsUsed >= m.turnLimit:
		status = "budget_exhausted"
	case m.BackendBlocked != "":
		status = "backend_blocked"
	case m.paused:
		status = "paused"
	}
	// Turn records the last attempt, which may remain interrupted or failed
	// after scheduling is explicitly resumed. It is not the current status.
	state := m.state
	if state.Turn != nil {
		copy := *state.Turn
		state.Turn = &copy
	}
	result := MemberStatus{CurrentGeneration: m.currentGeneration, PauseRevision: m.pauseRevision, Status: status, Active: m.active, Paused: m.paused || m.rotation != nil && m.rotation.Status == "failed", Agent: &state, Error: m.lastError,
		SessionResetRequired: m.BackendBlocked != "", SessionGeneration: m.sessionGeneration, SessionStartUsed: m.sessionStartUsed,
		TurnBudget: &TurnBudget{Limit: m.turnLimit, Used: m.turnsUsed, Grant: m.MaxTurns}}
	if m.automaticRotation() {
		result.TurnBudget = nil
		result.SessionTurns = &SessionTurns{Limit: m.SessionMaxTurns, Total: m.turnsUsed, BaselineKnown: m.sessionGeneration != ""}
		if result.SessionTurns.BaselineKnown {
			result.SessionTurns.Used = m.turnsUsed - m.sessionStartUsed
		}
	}
	if m.inbox != nil {
		copy := *m.inbox
		result.Inbox = &copy
	}
	if m.work != nil {
		if reason := m.work.cleanupReason.Load(); reason != nil {
			result.Error = *reason
		}
	}
	if m.rotation != nil {
		copy := *m.rotation
		result.Rotation = &copy
	}
	if m.latest != nil {
		copy := *m.latest
		result.Notification = &copy
	}
	return result
}

func (m *MemberAgent) Control(ctx actorx.Context, msg ControlMember) (MemberStatus, error) {
	if msg.MemberIdentity != m.Options.MemberIdentity {
		return MemberStatus{}, ErrIdentity
	}
	// Shutdown cancels retained work without changing durable collaboration intent.
	if msg.Action == "settle" {
		m.withdrawTurn()
		if m.cancel != nil {
			m.cancel()
		}
		return m.status(), nil
	}
	if !m.schedulingReady {
		if err := m.initializeScheduling(); err != nil {
			return MemberStatus{}, err
		}
	}
	switch msg.Action {
	case "recover":
		if !msg.automatic {
			return MemberStatus{}, ErrRequest
		}
		m.recoveryArmed = true
		m.schedule()
	case "rotate":
		if err := m.prepareRotation(msg); err != nil {
			return m.status(), err
		}

	case "pause":
		if !msg.internalPause {
			m.pauseRevision++
		}
		m.rotationEnable = false
		if err := m.persistScheduling(false, m.turnsUsed); err != nil {
			return MemberStatus{}, err
		}
		m.paused, m.retry = true, false
		m.withdrawTurn()
		if msg.Cancel && m.cancel != nil {
			m.lastError = "cancellation requested; waiting for turn settlement"
			m.cancel()
		}
	case "run", "enable":
		if msg.expectedPause != nil && *msg.expectedPause != m.pauseRevision {
			return m.status(), nil
		}
		if m.rotationWork != nil || (m.rotationRecord != nil && m.rotationRecord.Phase != "completed") {
			return MemberStatus{}, ErrBusy
		}
		if msg.State.MemberIdentity != m.Options.MemberIdentity {
			return MemberStatus{}, ErrIdentity
		}
		if m.fault {
			return MemberStatus{}, fmt.Errorf("checkpoint failure requires inspection and service recovery: %s", m.lastError)
		}
		if msg.Action == "enable" && m.active {
			return MemberStatus{}, ErrBusy
		}
		if msg.Action == "enable" {
			m.recoveryAttempted = false
			m.RecoveryPending, m.recoveryArmed = false, false
		}
		if m.active && msg.Retry {
			return MemberStatus{}, ErrBusy
		}
		if m.BackendBlocked != "" {
			if msg.Action != "enable" {
				return MemberStatus{}, fmt.Errorf("%s", m.BackendBlocked)
			}
			// The same settlement checks as explicit rotation remain mandatory.
			status, err := m.Control(ctx, ControlMember{MemberIdentity: msg.MemberIdentity, Action: "rotate", Resume: true, Replenish: true, lease: msg.lease})
			if err == nil {
				m.rotationEnable = true
			}
			return status, err
		}
		if !m.automaticRotation() && m.turnsUsed >= m.turnLimit && msg.Action != "enable" {
			return MemberStatus{}, ErrTurnBudget
		}
		if backend, ok := m.Backend.(interface{ CheckRun() error }); ok && m.session == nil && !m.active {
			if err := backend.CheckRun(); err != nil {
				return MemberStatus{}, err
			}
		}
		if err := m.resumeScheduling(msg.Action == "enable"); err != nil {
			return MemberStatus{}, err
		}
		m.retry = msg.Retry
		if msg.Action == "enable" && !m.active && m.state.Turn != nil && (m.state.Turn.Status == "failed" || m.state.Turn.Status == "interrupted") {
			m.retry = true
		}
		if !m.active {
			// Resuming acknowledges the settled error; Turn retains its history.
			m.lastError = ""
		}
		if err := m.Notify(ctx, msg.State); err != nil {
			return MemberStatus{}, err
		}
	default:
		return MemberStatus{}, ErrRequest
	}
	return m.status(), nil
}

// This small owned timer also retries task admission without requiring a new
// notification. One timer plus one turn bounds the member's work allocation.
func (m *MemberAgent) maintain() error {
	_, err := actorx.StartTask(m.owner, func(ctx context.Context) (struct{}, error) {
		timer := time.NewTimer(25 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return struct{}{}, ctx.Err()
		case <-timer.C:
			return struct{}{}, nil
		}
	}, func(_ struct{}, err error) bool {
		if err == nil {
			if err := m.maintain(); err != nil {
				m.owner.Stop(fmt.Errorf("member maintenance unavailable: %w", err))
			} else {
				m.pollInbox()
				m.schedule()
			}
		}
		return true
	}, nil)
	return err
}

type memberTurn struct {
	session *ownedSession
	state   AgentSnapshot
	started bool
	ended   bool
	// The worker publishes only an immutable diagnostic while joining cleanup;
	// all scheduling fields remain owned by the actor.
	cleanupReason atomic.Pointer[string]
}

func (m *MemberAgent) BindScheduler(_ actorx.Context, msg bindTurnScheduler) (struct{}, error) {
	if msg.Scheduler.IsZero() || msg.Target.IsZero() || !m.scheduler.IsZero() {
		return struct{}{}, ErrRequest
	}
	m.scheduler, m.target = msg.Scheduler, msg.Target
	return struct{}{}, nil
}

func (m *MemberAgent) ready() bool {
	return (m.ExecutionFrozen == nil || !m.ExecutionFrozen.Load()) && !m.active && !m.paused && !m.fault && m.BackendBlocked == "" &&
		m.rotationWork == nil && (m.rotation == nil || (m.rotation.Status != "waiting" && m.rotation.Status != "closing")) &&
		(m.rotationRecord == nil || m.rotationRecord.Phase == "completed") && (m.automaticRotation() || m.turnsUsed < m.turnLimit) && (m.roomReady() || m.inboxReady()) && !m.now().Before(m.due)
}

func (m *MemberAgent) roomReady() bool {
	return m.latest != nil && m.latest.UnreadCount > 0 && (m.retry || m.latest.NewestUnreadSeq > m.state.LastOfferedUnreadSeq)
}

func (m *MemberAgent) inboxReady() bool {
	i := m.inbox
	return i != nil && i.WriteError == "" && (!i.AutomaticChecked || i.AutomaticReady && !i.AutomaticBlocked) && i.LatestSeq > i.inputThrough() &&
		(m.retry || m.state.LastOfferedInboxSeq == 0 || i.inputThrough() >= m.state.LastOfferedInboxSeq || i.inputThrough() > m.state.LastOfferedInboxThrough)
}

func (m *MemberAgent) pollInbox() {
	if m.Inbox == nil || m.inboxPolling || m.now().Before(m.inboxNext) {
		return
	}
	m.inboxPolling = true
	m.inboxNext = m.now().Add(250 * time.Millisecond)
	_, err := actorx.StartTask(m.owner, m.Inbox, func(status InboxStatus, err error) bool {
		m.inboxPolling = false
		if err == nil {
			changed := m.inbox == nil || m.inbox.LatestSeq != status.LatestSeq || m.inbox.ConsumedThrough != status.ConsumedThrough || m.inbox.ExpiredThrough != status.ExpiredThrough || m.inbox.SkippedThrough != status.SkippedThrough || m.inbox.AutomaticReady != status.AutomaticReady || m.inbox.AutomaticBlocked != status.AutomaticBlocked
			m.inbox = &status
			if changed && m.due.IsZero() {
				m.due = m.now().Add(m.MergeWindow)
			}
		} else {
			m.inbox = nil
			m.lastError = "inbox reconciliation: " + err.Error()
		}
		m.schedule()
		return true
	}, nil)
	if err != nil {
		m.inboxPolling = false
		m.lastError = err.Error()
	}
}

// A failed release admission retains responsibility and blocks another request.
func (m *MemberAgent) flushRelease() bool {
	for grant := range m.releasing {
		if err := m.scheduler.Send(m.owner, releaseTurn{grant}); err != nil {
			m.lastError = "execution slot release pending: " + err.Error()
			return false
		}
		delete(m.releasing, grant)
	}
	return true
}

func (m *MemberAgent) queueRelease(grant turnGranted) {
	if m.releasing == nil {
		m.releasing = make(map[turnGranted]struct{})
	}
	m.releasing[grant] = struct{}{}
	m.flushRelease()
}

func (m *MemberAgent) releaseTurn() {
	if m.request != nil {
		grant := turnGranted{m.request.MemberIdentity, m.request.ID}
		if m.checked != nil && m.checked.request == m.request {
			m.checked = nil
		}
		m.request = nil
		m.queueRelease(grant)
	}
}

func (m *MemberAgent) withdrawTurn() {
	if m.request != nil && !m.active {
		_ = m.scheduler.Send(m.owner, cancelTurnRequest{turnGranted{m.request.MemberIdentity, m.request.ID}})
		// No execution was admitted, so we can also acknowledge a grant that
		// may already be in transit. Its eventual delivery is harmless.
		m.releaseTurn()
	}
}

func (m *MemberAgent) schedule() {
	if !m.flushRelease() {
		return
	}
	if m.rotation != nil && (m.rotation.Status == "waiting" || m.rotation.Status == "closing") {
		m.withdrawTurn()
		m.rotate()
		return
	}
	if m.recoveryArmed && m.canRecoverSession() {
		m.withdrawTurn()
		m.requestSessionChange(m.RequestRecovery, true)
		return
	}
	if !m.automaticRotation() && !m.active && m.turnsUsed >= m.turnLimit {
		m.paused, m.retry = true, false
	}
	if !m.ready() {
		if !m.active && !m.checking {
			m.withdrawTurn()
		}
		return
	}
	if m.automaticRotation() && m.needsRotation() {
		m.withdrawTurn()
		m.requestRotation()
		return
	}
	if m.rotationRequest {
		return
	}
	if m.checked != nil {
		_ = m.target.Send(m.owner, startGrantedTurn{m.checked.request.ID})
		return
	}
	if m.scheduler.IsZero() || m.checking {
		return
	}
	if m.request == nil {
		m.requestNumber++
		m.request = &requestTurn{MemberIdentity: m.Options.MemberIdentity, ID: m.requestNumber, Target: m.target}
	}
	if err := m.scheduler.Send(m.owner, *m.request); err != nil {
		m.lastError = "execution slot request pending: " + err.Error()
	}
}

func (m *MemberAgent) Granted(_ actorx.Context, msg turnGranted) error {
	if msg.MemberIdentity != m.Options.MemberIdentity {
		return ErrIdentity
	}
	if m.request == nil || m.request.ID != msg.ID {
		// Exact numbers protect a later holder against a delayed grant.
		m.queueRelease(msg)
		return nil
	}
	if m.active || m.checking || m.checked != nil {
		return nil
	}
	if !m.ready() || m.needsRotation() {
		m.withdrawTurn()
		m.schedule()
		return nil
	}
	request, room, id := m.request, m.Room, m.Options.MemberIdentity
	_, err := actorx.StartTask(m.owner, func(ctx context.Context) (MemberList, error) {
		return room.Call[MemberList](ctx, ListMembers{id.RoomIdentity})
	}, func(list MemberList, err error) bool {
		m.checking = false
		if m.request != request {
			m.schedule()
			return true
		}
		m.checked = &checkedTurn{request: request, list: list, err: err}
		m.schedule()
		return true
	}, nil)
	if err != nil {
		m.lastError = err.Error()
		m.releaseTurn()
		return nil
	}
	m.checking = true
	return nil
}

type startGrantedTurn struct{ ID uint64 }

type checkedTurn struct {
	request *requestTurn
	list    MemberList
	err     error
}

func (m *MemberAgent) StartGranted(_ actorx.Context, msg startGrantedTurn) error {
	checked := m.checked
	if checked == nil || checked.request.ID != msg.ID {
		return nil
	}
	m.checked = nil
	if m.request != checked.request {
		m.schedule()
		return nil
	}
	list, err, id := checked.list, checked.err, m.Options.MemberIdentity
	var member *MemberInfo
	if err == nil {
		for i := range list.Members {
			if list.Members[i].AgentID == id.AgentID && list.Members[i].MembershipID == id.MembershipID {
				member = &list.Members[i]
				break
			}
		}
		if member == nil {
			err = ErrIdentity
		}
	}
	if err != nil {
		m.lastError = err.Error()
		if errors.Is(err, ErrIdentity) {
			m.paused = true
		}
		m.releaseTurn()
		return nil
	}
	input := UnreadChanged{MemberIdentity: id, Revision: list.Revision, LatestSeq: list.LatestSeq,
		NewestUnreadSeq: member.NewestUnreadSeq, UnreadCount: member.UnreadCount}
	if m.latest == nil || input.Revision >= m.latest.Revision {
		m.latest = &input
	}
	if !m.ready() || m.needsRotation() {
		m.withdrawTurn()
		m.schedule()
		return nil
	}
	if m.latest.Revision > input.Revision {
		// A newer notification arrived while querying. Requeue rather than
		// offer an older snapshot or reserve this turn indefinitely.
		m.releaseTurn()
		m.schedule()
		return nil
	}
	if m.inboxReady() {
		i := m.inbox
		through := i.inputThrough()
		end := min(i.LatestSeq, through+inboxBatchSize)
		if i.Batch != nil && i.Batch.SnapshotSeq > through {
			end = i.Batch.SnapshotSeq
		}
		input.Inbox = &InboxInput{Through: through, SnapshotSeq: end}
	}
	m.startTurn(input, list.LatestSeq, member.ReadThrough)
	return nil
}

func (m *MemberAgent) startTurn(input UnreadChanged, snapshotSeq, readThrough uint64) {
	if m.automaticRotation() {
		generation, err := m.Backend.(rotatingBackend).sessionGeneration(m.Options)
		if err != nil || generation != m.sessionGeneration || m.needsRotation() {
			m.withdrawTurn()
			if err != nil || (m.sessionGeneration != "" && generation != m.sessionGeneration) {
				m.BackendBlocked = "Session generation changed before admission"
			}
			m.schedule()
			return
		}
	}
	if m.turnsUsed == ^uint64(0) {
		m.fault = true
		m.lastError = "cumulative turn count overflow"
		m.releaseTurn()
		return
	}
	// Reserve budget before execution. A crash or failed task admission may
	// conservatively consume one attempt, but can never renew the allowance.
	if err := m.persistScheduling(m.enabled, m.turnsUsed+1); err != nil {
		m.releaseTurn()
		return
	}
	opts, backend, store := m.Options, m.Backend, m.Store
	work := &memberTurn{session: m.session, state: m.state}
	// Capture immutable inputs before transferring the Session to the task.
	cancel, err := actorx.StartTask(m.owner, func(ctx context.Context) (_ struct{}, runErr error) {
		work.state.TurnNumber++
		work.state.LastOfferedUnreadSeq = input.NewestUnreadSeq
		work.state.Turn = &TurnRecord{Number: work.state.TurnNumber, Status: "running", StartReadThrough: readThrough,
			SnapshotSeq: snapshotSeq, OfferedUnreadSeq: input.NewestUnreadSeq}
		if input.Inbox != nil {
			work.state.LastOfferedInboxSeq, work.state.LastOfferedInboxThrough = input.Inbox.SnapshotSeq, input.Inbox.Through
			work.state.Turn.InboxStart, work.state.Turn.InboxSnapshotSeq = input.Inbox.Through, input.Inbox.SnapshotSeq
		}
		if err := store.SaveAgent(work.state); err != nil {
			return struct{}{}, err
		}
		work.started = true
		// Terminal checkpoint runs on failure and cancellation too. It does not
		// make external tool effects transactional or safe to replay.
		defer func() {
			if v := recover(); v != nil {
				runErr = fmt.Errorf("backend panic: %v", v)
				// A backend panic may abandon activity; join cleanup before
				// returning the Room slot, and forbid Session reuse.
				runErr = errors.Join(runErr, errDSHDisabled)
			}
			if errors.Is(runErr, errDSHDisabled) {
				reason := "waiting for backend cleanup: " + runErr.Error()
				work.cleanupReason.Store(&reason)
				runErr = errors.Join(runErr, work.session.close())
			}
			work.state.Turn.Status = "completed"
			if runErr != nil {
				work.state.Turn.Status = "failed"
				work.state.Turn.Error = runErr.Error()
			}
			if ctx.Err() != nil {
				work.state.Turn.Status = "interrupted"
				work.state.Turn.Error = ctx.Err().Error()
				runErr = errors.Join(runErr, ctx.Err())
			}
			err := store.SaveAgent(work.state)
			work.ended = err == nil
			runErr = errors.Join(runErr, err)
		}()
		if work.session == nil {
			session, err := backend.OpenSession(ctx, opts)
			if session != nil {
				work.session = &ownedSession{Session: session}
			}
			if err != nil {
				reason := "waiting for failed Session open cleanup: " + err.Error()
				work.cleanupReason.Store(&reason)
				closeErr := work.session.close()
				work.session = nil
				return struct{}{}, errors.Join(err, closeErr)
			}
			if work.session == nil {
				return struct{}{}, fmt.Errorf("backend returned no Session")
			}
		}
		return struct{}{}, work.session.Run(ctx, input)
	}, func(_ struct{}, err error) bool {
		m.active, m.cancel = false, nil
		m.work = nil
		m.session = work.session
		m.state = work.state
		m.fault = !work.started || !work.ended || errors.Is(err, ErrCommitUnknown)
		m.lastError = ""
		if err != nil {
			m.lastError = err.Error()
			if errors.Is(err, errDSHPaused) {
				m.paused, m.retry = true, false
			}
			if errors.Is(err, errDSHDisabled) {
				m.BackendBlocked = m.lastError
			}
			if errors.Is(err, errDSHPaused) && work.session != nil && recoverableDSHFailure(err) {
				m.RecoveryPending, m.recoveryArmed = true, true
				m.rotationNext = m.now().Add(time.Second)
			}
		} else {
			m.RecoveryPending = false
			if m.recoveryAttempted {
				m.recoveryAttempted = false
				_ = m.persistScheduling(m.enabled, m.turnsUsed)
			}
		}
		m.releaseTurn()
		m.schedule()
		return true
	}, func(struct{}) {
		if err := work.session.close(); err != nil {
			fmt.Fprintln(os.Stderr, "discarded Session cleanup failed:", err)
		}
	})
	if err != nil {
		m.lastError = err.Error() // Pending state is retained for maintenance.
		_ = m.persistScheduling(m.enabled, m.turnsUsed)
		m.releaseTurn()
		return
	}
	m.session = nil
	m.work = work
	// Count admitted attempts, including failed or canceled turns. Failed task
	// admission consumes nothing, and run/retry never renews the allowance.
	m.turnsUsed++
	m.active, m.cancel, m.retry = true, cancel, false
	m.due = time.Time{}
}

func (m *MemberAgent) Stopped(_ actorx.Context, _ actorx.Stopped) error {
	defer m.releaseTurn()
	defer m.rotationLease.close()
	if m.rotationWork != nil {
		return m.rotationWork.session.close()
	}
	if m.work != nil {
		// Wait joins the discarded task before Stopped. Preserve its cleanup
		// error in Exit as well as diagnostics, so assembly cannot hide it.
		return m.work.session.close()
	}
	return m.session.close()
}
