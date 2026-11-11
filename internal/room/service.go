package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/web"
)

type ServiceInfo struct {
	RoomIdentity
	ServiceEpoch string `json:"service_epoch"`
}

// Only these explicit DTOs cross HTTP. Actor addresses remain in the service.
type apiRequest struct {
	automatic        bool
	automaticInbox   *InboxStatus
	internalPause    bool
	expectedPause    *uint64
	OperationID      string `json:"operation_id,omitempty"`
	FromGeneration   string `json:"from_generation,omitempty"`
	RotationReason   string `json:"rotation_reason,omitempty"`
	ContinueRotation bool   `json:"continue_rotation,omitempty"`
	rotationLocked   bool

	ServiceInfo
	Caller             Caller                        `json:"caller"`
	TurnNumber         uint64                        `json:"turn_number,omitempty"`
	Policy             *PolicyChange                 `json:"policy,omitempty"`
	ActionResolve      *actionResolveRequest         `json:"action_resolve,omitempty"`
	Tool               string                        `json:"tool,omitempty"` // optional exact platform entry for managed actions
	ReferenceAction    *agentActionRequest           `json:"action_request,omitempty"`
	Progress           *agentProgressChange          `json:"progress,omitempty"`
	InstructionStatus  *agentInstructionStatusChange `json:"instruction_status,omitempty"`
	Followup           *followupRequest              `json:"followup,omitempty"`
	Question           *followupQuestionChange       `json:"question,omitempty"`
	FollowupReply      *followupReplyChange          `json:"followup_reply,omitempty"`
	Instruction        *followupInstructionChange    `json:"instruction,omitempty"`
	ConversationPolicy *agentPolicyChange            `json:"conversation_policy,omitempty"`
	AgentID            string                        `json:"agent_id,omitempty"`
	MembershipID       string                        `json:"membership_id,omitempty"`
	Runner             RunnerConfig                  `json:"runner,omitempty"`
	ClientMessageID    string                        `json:"client_message_id,omitempty"`
	Text               string                        `json:"text,omitempty"`
	ReplyTo            uint64                        `json:"reply_to,omitempty"`
	Mode               ReadMode                      `json:"mode,omitempty"`
	Latest             int                           `json:"latest,omitempty"`
	After              *uint64                       `json:"after,omitempty"`
	SnapshotSeq        *uint64                       `json:"snapshot_seq,omitempty"`
	Limit              int                           `json:"limit,omitempty"`
	Peek               bool                          `json:"peek,omitempty"`
	ReadReceipt        string                        `json:"read_receipt,omitempty"`
	Retry              bool                          `json:"retry,omitempty"`
	Cancel             bool                          `json:"cancel,omitempty"`
	Resume             bool                          `json:"resume,omitempty"`
	RestartID          string                        `json:"restart_id,omitempty"`
	PreviousRestartID  string                        `json:"previous_restart_id,omitempty"`
}

type serviceMember struct {
	MemberInfo
	Status       string         `json:"status"`
	Notification *UnreadChanged `json:"notification"`
	Execution    *MemberStatus  `json:"execution,omitempty"`
}

type serviceMembers struct {
	ServiceInfo
	IsDirect   bool               `json:"is_direct"`
	LatestSeq  uint64             `json:"latest_seq"`
	Revision   uint64             `json:"revision"`
	Committing bool               `json:"committing"`
	WriteError string             `json:"write_error,omitempty"`
	Members    []serviceMember    `json:"members"`
	Restart    *discussionRestart `json:"discussion_restart,omitempty"`
}

type apiError struct {
	Error           string  `json:"error"` // retained for existing HTTP clients
	Code            string  `json:"code"`
	Message         string  `json:"message"`
	Field           string  `json:"field,omitempty"`
	ExpectedVersion *uint64 `json:"expected_version,omitempty"`
	ActualVersion   *uint64 `json:"actual_version,omitempty"`
	Hint            string  `json:"hint,omitempty"`
}

type service struct {
	settings                  *systemSettings
	imageMaterials            imageMaterialConfig
	inboxExpiry               notification.ExpiryPolicy
	maintainInbox             atomic.Pointer[inboxMaintenance]
	materialAttachments       *materialAttachments
	referenceResolver         followupResolver
	resolveInboxAdmission     func(context.Context, platform.SourceRef) (json.RawMessage, error)
	inboxAdmissionMu          sync.Mutex
	inboxAdmissionRetry       map[string]time.Time
	inboxAdmissionChecks      map[string]automaticInboxCheck
	referenceCommit           func(context.Context, MemberIdentity, platform.AccessContext, func() (followupEvent, error)) (followupEvent, error)
	referenceSpecs            func(string) []platform.ActionSpec
	inboxMu                   sync.Mutex
	inboxes                   map[string]actorx.Addr
	settleTools               func(context.Context) error // managed platform calls, nil in standalone mode
	beginPlatformMemberChange func(context.Context, MemberIdentity) (func(), error)
	workspaceOperationID      string
	info                      ServiceInfo
	isDirect                  bool // Immutable Room property; excluded from caller identity.
	store                     *Store
	rt                        *actorx.Runtime
	room                      actorx.Addr
	scheduler                 actorx.Addr
	ctx                       context.Context
	cancel                    context.CancelFunc
	// Management has one retained owner even if its HTTP caller leaves.
	management           sync.Mutex
	changes              sync.RWMutex
	restarting           atomic.Bool
	restart              *discussionRestart // guarded by mu
	cleanup              sync.Mutex
	closing              atomic.Bool
	grantsStopped        atomic.Bool
	grantsFrozen         *atomic.Bool
	mu                   sync.Mutex
	targets              map[string]actorx.Addr
	requests             chan struct{}
	endpoint             string
	cliPath              string
	mergeWindow          time.Duration
	dshTurnTimeout       time.Duration
	maxTurns             uint64
	persistentScheduling bool
	closed               bool
	closeError           error
	dshProfiles          map[string]dshProfile
	uiProfiles           []string
	recovered            []recoveredMember
	memberFactory        func(MemberIdentity, RunnerConfig, *AgentSnapshot) actorx.Actor
}

func newService(dir, roomID string, config serviceConfig) (*service, error) {
	return startService(dir, roomID, config, func(s *service, state RoomSnapshot) error {
		if err := s.loadMembers(state); err != nil {
			return err
		}
		return s.attachMembers(state)
	})
}

// startService establishes resource ownership before assembling members. The
// assembly callback also lets in-package tests inject deterministic Backends.
func startService(dir, roomID string, config serviceConfig, assemble func(*service, RoomSnapshot) error) (_ *service, err error) {
	if config.settings == nil {
		config.settings = defaultSystemSettings()
	}
	if _, err := config.imageMaterials.limits(); err != nil {
		return nil, err
	}
	if err := config.inboxExpiry.Resolved().Validate(); err != nil {
		return nil, err
	}
	if config.maxTurns == 0 {
		config.maxTurns = defaultMaxTurns
	}
	store, err := OpenStore(dir, roomID)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = store.Close()
		}
	}()
	state, err := store.LoadRoom()
	if errors.Is(err, os.ErrNotExist) {
		instance, idErr := newOpaqueID()
		if idErr != nil {
			return nil, idErr
		}
		state = RoomSnapshot{Version: formatVersion, RoomIdentity: RoomIdentity{RoomID: roomID, RoomInstance: instance}, Messages: []RoomMessage{}, Members: []MemberState{}}
		err = store.SaveRoom(state)
	}
	if err != nil {
		return nil, err
	}
	if err := store.checkReferenceRoom(state); err != nil {
		return nil, err
	}
	epoch, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := actorx.NewRuntime(ctx, actorx.WithInstanceLimit(2*maxMembers+3))
	if err != nil {
		cancel()
		return nil, err
	}
	defer func() {
		if err != nil {
			cancel()
			_ = rt.Close(context.Background())
		}
	}()
	room, err := NewRoom(store)
	if err != nil {
		return nil, err
	}
	addr, err := rt.Spawn(room)
	if err != nil {
		return nil, err
	}
	grantsFrozen := &atomic.Bool{}
	scheduler, err := rt.Spawn(&turnScheduler{RoomIdentity: state.RoomIdentity, frozen: grantsFrozen})
	if err != nil {
		return nil, err
	}
	s := &service{
		settings:             config.settings,
		workspaceOperationID: config.workspaceOperationID,
		info:                 ServiceInfo{RoomIdentity: state.RoomIdentity, ServiceEpoch: epoch}, isDirect: state.IsDirect, store: store,
		rt: rt, room: addr, scheduler: scheduler, ctx: ctx, cancel: cancel,
		inboxes: make(map[string]actorx.Addr), targets: make(map[string]actorx.Addr), requests: make(chan struct{}, 32),
		endpoint: config.endpoint, cliPath: config.cliPath, mergeWindow: config.mergeWindow,
		dshTurnTimeout:       config.dshTurnTimeout,
		imageMaterials:       config.imageMaterials,
		inboxExpiry:          config.inboxExpiry,
		maxTurns:             config.maxTurns,
		persistentScheduling: config.persistentScheduling,
		grantsFrozen:         grantsFrozen,
		dshProfiles:          config.dshProfiles,
		uiProfiles:           config.uiProfiles,
	}
	if s.uiProfiles == nil {
		s.uiProfiles = profileNames(s.dshProfiles)
	}
	s.memberFactory = s.newMember
	if err := s.loadDiscussionRestart(); err != nil {
		return nil, err
	}
	if err := assemble(s, state); err != nil {
		return nil, err
	}
	return s, nil
}

// Freeze is nonblocking, so a manager can freeze every Room before joining any
// worker. The scheduler checks the same flag before handing off a queued turn.
func (s *service) freeze() {
	s.closing.Store(true)
	if s.grantsFrozen != nil {
		s.grantsFrozen.Store(true)
	}
}

func (s *service) close(ctx context.Context) error {
	s.freeze()
	for !s.cleanup.TryLock() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("cleanup wait incomplete (store still owned): %w", ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer s.cleanup.Unlock()
	if s.closed {
		return s.closeError
	}
	// All admitted mutations finish before Room and member ownership is joined.
	for !s.changes.TryLock() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("request cleanup incomplete (store still owned): %w", ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	s.changes.Unlock()
	if !s.grantsStopped.Load() {
		if _, err := s.scheduler.Call[struct{}](ctx, stopTurnGrants{}); err != nil {
			return fmt.Errorf("scheduler shutdown incomplete (store still owned): %w", err)
		}
		s.grantsStopped.Store(true)
	}
	// A retained rotation may be waiting for an active turn while owning
	// management. Signal cancellation before joining that lifecycle owner.
	s.mu.Lock()
	pending := make(map[string]actorx.Addr, len(s.targets))
	for membership, target := range s.targets {
		pending[membership] = target
	}
	s.mu.Unlock()
	roster, err := s.room.Call[MemberList](ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return err
	}
	for membership, target := range pending {
		if target.IsZero() {
			continue
		}
		// Removed actors are already being canceled by their removal owner.
		for _, member := range roster.Members {
			if member.MembershipID == membership {
				id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: member.AgentID, MembershipID: membership}
				_, _ = target.Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "settle"})
			}
		}
	}
	// Join an admitted add/remove before taking the ownership snapshot.
	for !s.management.TryLock() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("management cleanup incomplete (store still owned): %w", ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	defer s.management.Unlock()

	s.mu.Lock()
	targets := make([]actorx.Addr, 0, len(s.targets))
	for _, target := range s.targets {
		targets = append(targets, target)
	}
	s.mu.Unlock()
	for _, target := range targets {
		_ = target.Stop(nil)
	}
	var cleanupErr error
	for _, target := range targets {
		exit, err := target.Wait(ctx)
		if err != nil {
			return fmt.Errorf("member cleanup incomplete (store still owned): %w", err)
		}
		cleanupErr = errors.Join(cleanupErr, exit.Reason)
	}
	s.inboxMu.Lock()
	for membership := range s.inboxes {
		if err := s.stopInboxLocked(ctx, membership); err != nil {
			if _, owned := s.inboxes[membership]; owned {
				s.inboxMu.Unlock()
				return err
			}
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	s.inboxMu.Unlock()
	_ = s.scheduler.Stop(nil)
	schedulerExit, err := s.scheduler.Wait(ctx)
	if err != nil {
		return fmt.Errorf("scheduler cleanup incomplete (store still owned): %w", err)
	}
	cleanupErr = errors.Join(cleanupErr, schedulerExit.Reason)
	_ = s.room.Stop(nil)
	exit, err := s.room.Wait(ctx)
	if err != nil {
		return fmt.Errorf("Room cleanup incomplete (store still owned): %w", err)
	}
	cleanupErr = errors.Join(cleanupErr, exit.Reason)
	if err := s.rt.Close(ctx); err != nil {
		return fmt.Errorf("cleanup incomplete (store still owned): %w", err)
	}
	s.cancel()
	s.closed = true
	s.closeError = errors.Join(cleanupErr, s.store.Close())
	return s.closeError
}

func (s *service) target(id string) actorx.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.targets[id]
}

func (s *service) setTarget(id string, addr actorx.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if addr.IsZero() {
		delete(s.targets, id)
	} else {
		s.targets[id] = addr
	}
}

func (s *service) members(ctx context.Context) (serviceMembers, error) {
	list, err := s.room.Call[MemberList](ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return serviceMembers{}, err
	}
	result := serviceMembers{ServiceInfo: s.info, IsDirect: list.IsDirect, LatestSeq: list.LatestSeq, Revision: list.Revision,
		Committing: list.Committing, WriteError: list.WriteError, Members: make([]serviceMember, 0, len(list.Members))}
	result.Restart = s.discussionRestartStatus()
	for _, m := range list.Members {
		item := serviceMember{MemberInfo: m, Status: "detached"}
		target := s.target(m.MembershipID)
		if !target.IsZero() {
			id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: m.AgentID, MembershipID: m.MembershipID}
			inspectCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
			status, inspectErr := target.Call[MemberStatus](inspectCtx, InspectMember{id})
			cancel()
			if inspectErr == nil {
				item.Status, item.Notification, item.Execution = status.Status, status.Notification, &status
			} else {
				item.Status = "unavailable"
			}
		}
		result.Members = append(result.Members, item)
	}
	return result, nil
}

func (s *service) add(req apiRequest) (MemberInfo, error) {
	if !s.management.TryLock() {
		return MemberInfo{}, ErrBusy
	}
	defer s.management.Unlock()
	return s.addLocked(req)
}

func (s *service) addLocked(req apiRequest) (MemberInfo, error) {
	if s.closing.Load() {
		return MemberInfo{}, ErrClosing
	}
	if !validText(req.AgentID) {
		return MemberInfo{}, ErrRequest
	}
	if !req.Runner.valid() {
		return MemberInfo{}, ErrRequest
	}
	if _, ok := s.dshProfiles[req.Runner.Profile]; !ok {
		return MemberInfo{}, fmt.Errorf("%w: DSH profile %q unavailable; configure serve --dsh-config", ErrRequest, req.Runner.Profile)
	}
	if s.endpoint == "" || s.cliPath == "" {
		return MemberInfo{}, fmt.Errorf("DSH member CLI environment unavailable")
	}
	// Callers can reconcile a timed-out add by listing or retrying its AgentID.
	list, err := s.room.Call[MemberList](s.ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return MemberInfo{}, err
	}
	for _, m := range list.Members {
		if m.AgentID == req.AgentID {
			if m.Runner == req.Runner && !s.target(m.MembershipID).IsZero() {
				return m, nil
			}
			return MemberInfo{}, ErrConflict
		}
	}
	if list.IsDirect {
		return MemberInfo{}, fmt.Errorf("%w: direct conversation membership is fixed", ErrConflict)
	}
	if len(list.Members) >= maxMembers {
		return MemberInfo{}, ErrCapacity
	}
	generation, err := newOpaqueID()
	if err != nil {
		return MemberInfo{}, err
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: generation}
	target, err := s.spawnMember(id, req.Runner, nil)
	if err != nil {
		return MemberInfo{}, err
	}
	s.setTarget(generation, target)
	member, err := s.room.Call[MemberInfo](s.ctx, AddMember{MemberIdentity: id, Runner: req.Runner, Target: target})
	if err != nil {
		// No request deadline can abandon a successful registration. Shutdown
		// joins the whole Runtime if the service lifetime has ended.
		_ = target.Stop(nil)
		_, _ = target.Wait(s.ctx)
		s.setTarget(generation, actorx.Addr{})
	}
	return member, err
}

func (s *service) toolEnv(id MemberIdentity) []string {
	env := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		if !isRoomToolEnv(entry) {
			env = append(env, entry)
		}
	}
	return append(env, "TEAMX_ENDPOINT="+s.endpoint, "TEAMX_ROOM_ID="+id.RoomID, "TEAMX_ROOM_INSTANCE="+id.RoomInstance,
		"TEAMX_SERVICE_EPOCH="+s.info.ServiceEpoch, "TEAMX_AGENT_ID="+id.AgentID, "TEAMX_MEMBERSHIP_ID="+id.MembershipID)
}

func (s *service) control(ctx context.Context, req apiRequest, action string) (MemberStatus, error) {
	if s.closing.Load() {
		return MemberStatus{}, ErrClosing
	}
	list, err := s.room.Call[MemberList](ctx, ListMembers{s.info.RoomIdentity})
	if err != nil {
		return MemberStatus{}, err
	}
	for _, member := range list.Members {
		if member.AgentID == req.AgentID && member.MembershipID == req.MembershipID {
			if action == "rotate" {
				if _, ok := s.dshProfiles[member.Runner.Profile]; !ok {
					return MemberStatus{}, fmt.Errorf("%w: DSH profile unavailable", ErrRequest)
				}
			}
			id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}

			reset := action == "rotate"
			if action == "enable" {
				execution, err := s.target(member.MembershipID).Call[MemberStatus](ctx, InspectMember{id})
				if err != nil {
					return MemberStatus{}, err
				}
				if execution.Active {
					return MemberStatus{}, ErrBusy
				}
				reset = execution.SessionResetRequired
			}
			var lease *rotationLease
			var rotationReplenish bool
			if reset {
				execution, err := s.target(member.MembershipID).Call[MemberStatus](ctx, InspectMember{id})
				if err != nil {
					return MemberStatus{}, err
				}
				if action == "rotate" && req.OperationID != "" && execution.Rotation != nil && execution.Rotation.OperationID == req.OperationID {
					r := execution.Rotation
					rotationReplenish = r.Replenish
					reason := req.RotationReason
					if reason == "" {
						reason = "manual"
					}
					if r.FromGeneration != req.FromGeneration || r.Reason != reason {
						return execution, ErrConflict
					}
					// Actor validates all fixed parameters even on an observation/retry.
					if r.Status != "failed" || !req.ContinueRotation {
						return s.target(member.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: "rotate", OperationID: req.OperationID, FromGeneration: req.FromGeneration, Reason: req.RotationReason, Cancel: req.Cancel, Resume: req.Resume, Replenish: r.Replenish})
					}
				}
				if !req.rotationLocked && !s.management.TryLock() {
					return execution, ErrBusy
				}
				releaseManagement := func() {
					if !req.rotationLocked {
						s.management.Unlock()
					}
				}
				var releasePlatform func()
				lease = &rotationLease{gateDone: make(chan struct{}), release: func() {
					if releasePlatform != nil {
						releasePlatform()
					}
					releaseManagement()
				}}
				lease.begin = func(taskCtx context.Context) error {
					if s.closing.Load() {
						return ErrClosing
					}
					if s.beginPlatformMemberChange == nil {
						return nil
					}
					var err error
					releasePlatform, err = s.beginPlatformMemberChange(taskCtx, id)
					return err
				}
			}

			state := UnreadChanged{MemberIdentity: id, Revision: list.Revision, LatestSeq: list.LatestSeq,
				NewestUnreadSeq: member.NewestUnreadSeq, UnreadCount: member.UnreadCount}
			callCtx := ctx
			if lease != nil && s.ctx != nil {
				callCtx = s.ctx
			}
			result, err := s.target(member.MembershipID).Call[MemberStatus](callCtx, ControlMember{MemberIdentity: id, Action: action,
				automatic: req.automatic, automaticInbox: req.automaticInbox, Retry: req.Retry, Cancel: req.Cancel, Resume: req.Resume, State: state, OperationID: req.OperationID, FromGeneration: req.FromGeneration, Reason: req.RotationReason, ContinueRotation: req.ContinueRotation, Replenish: rotationReplenish, lease: lease, internalPause: req.internalPause, expectedPause: req.expectedPause})
			if err != nil {
				lease.close()
			}
			return result, err
		}
	}
	return MemberStatus{}, ErrIdentity
}

func (s *service) remove(req apiRequest) (MemberInfo, error) {
	if !s.management.TryLock() {
		return MemberInfo{}, ErrBusy
	}
	defer s.management.Unlock()
	return s.removeLocked(req)
}

func (s *service) removeLocked(req apiRequest) (MemberInfo, error) {
	if s.closing.Load() {
		return MemberInfo{}, ErrClosing
	}
	if s.isDirect {
		return MemberInfo{}, fmt.Errorf("%w: delete the direct conversation instead of removing its agent", ErrConflict)
	}
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}

	if s.beginPlatformMemberChange != nil {
		release, err := s.beginPlatformMemberChange(s.ctx, id)
		if err != nil {
			return MemberInfo{}, err
		}
		defer release()
	}
	s.inboxMu.Lock()
	removed, err := s.room.Call[MemberInfo](s.ctx, RemoveMember{id})
	if err != nil {
		s.inboxMu.Unlock()
		return MemberInfo{}, err
	}
	inboxErr := s.stopInboxLocked(s.ctx, req.MembershipID)
	// A member-owned reconciliation task may be waiting for inboxMu. Release
	// it before joining the member; the Room table now rejects all old calls.
	s.inboxMu.Unlock()
	return removed, errors.Join(inboxErr, s.settleMemberRemoval(req.MembershipID))
}

// Keep failed or unfinished cleanup owned so a batch retry cannot open a new
// team while an already unregistered member is still executing.
func (s *service) settleMemberRemoval(membershipID string) error {
	target := s.target(membershipID)
	if !target.IsZero() {
		_ = target.Stop(nil)
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		defer cancel()
		exit, err := target.Wait(ctx)
		if err != nil {
			return fmt.Errorf("member removed; cleanup incomplete: %w", err)
		}
		if !dsh.CleanupSettled(exit.Reason) {
			return fmt.Errorf("member removed; cleanup failed: %w", exit.Reason)
		}
		s.setTarget(membershipID, actorx.Addr{})
	}
	return nil
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if s.closing.Load() {
		writeAPIError(w, ErrClosing)
		return
	}
	if !sameOrigin(r) {
		writeAPIError(w, ErrIdentity)
		return
	}
	select {
	case s.requests <- struct{}{}:
		defer func() { <-s.requests }()
	default:
		writeAPIError(w, ErrBusy)
		return
	}
	if s.serveUI(w, r) || web.Serve(w, r) {
		return
	}
	if r.URL.Path == "/v1/info" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(struct {
			ServiceInfo
			settingsView
			IsDirect bool `json:"is_direct"`
		}{s.info, s.settings.snapshot().settingsView, s.isDirect})
		return
	}
	if r.URL.Path == "/v1/settings" || r.URL.Path == "/v1/settings/save" {
		serveSystemSettings(w, r, s.settings, s.info)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req apiRequest
	bodyLimit := int64(128 << 10)
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, bodyLimit))
	if err != nil {
		writeAPIError(w, invalidField("", "request body exceeds limit or could not be read"))
		return
	}
	if err := decodeRequestJSON(string(data), &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.ServiceInfo != s.info {
		writeAPIError(w, &requestDiagnostic{cause: ErrIdentity, Field: "binding", Message: "Room instance or service binding is unavailable", Hint: "Use the current bound Room context; do not reuse an old service identity."})
		return
	}
	if !validText(req.Caller.ID) || (req.Caller.Kind != "agent" && req.Caller.Kind != "human") ||
		(req.Caller.Kind == "human" && req.Caller.MembershipID != "") {
		writeAPIError(w, ErrIdentity)
		return
	}
	// Validate member tools even for observation endpoints. A removed or old
	// generation must not regain access via a query that omits a Room call.
	if req.Caller.Kind == "agent" {
		list, err := s.room.Call[MemberList](r.Context(), ListMembers{s.info.RoomIdentity})
		if err != nil {
			writeAPIError(w, err)
			return
		}
		found := false
		for _, m := range list.Members {
			if m.AgentID == req.Caller.ID && m.MembershipID == req.Caller.MembershipID {
				found = true
				break
			}
		}
		if !found {
			writeAPIError(w, ErrIdentity)
			return
		}
		if !agentRuntimePath(r.URL.Path) {
			writeAPIError(w, ErrIdentity)
			return
		}

	}
	var result any
	err = nil
	// Drain already admitted mutations before a restart establishes its
	// boundary. Observation remains available while the background owner works.
	if r.URL.Path != "/v1/discussion/restart" && r.URL.Path != "/v1/member/list" && r.URL.Path != "/v1/member/activity" &&
		!(r.URL.Path == "/v1/read" && req.Caller.Kind == "human") {
		s.changes.RLock()
		defer s.changes.RUnlock()
		if s.closing.Load() {
			writeAPIError(w, ErrClosing)
			return
		}
		if s.restarting.Load() && r.URL.Path != "/v1/member/pause" && r.URL.Path != "/v1/member/session/status" {
			writeAPIError(w, fmt.Errorf("%w: discussion restart in progress", ErrBusy))
			return
		}
	}
	switch r.URL.Path {
	case "/v1/matters/list", "/v1/matters/read", "/v1/matters/update", "/v1/matters/sources", "/v1/matters/source", "/v1/matters/resolve", "/v1/matters/ensure", "/v1/matters/notify", "/v1/matters/reply", "/v1/matters/instruct", "/v1/matters/instruction-status":
		result, err = s.memberFollowups(r.Context(), req, r.URL.Path)
	case "/v1/policy/read", "/v1/policy/update", "/v1/policy/publish":
		result, err = s.memberPolicy(r.Context(), req, map[string]string{"/v1/policy/read": "read", "/v1/policy/update": "update", "/v1/policy/publish": "publish"}[r.URL.Path])
	case "/v1/inbox/read", "/v1/inbox/confirm":
		result, err = s.memberInbox(r.Context(), req, map[string]string{"/v1/inbox/read": "read", "/v1/inbox/confirm": "confirm"}[r.URL.Path])
	case "/v1/discussion/restart":
		result, err = s.startDiscussionRestart(req)
	case "/v1/messages/clear":
		if req.SnapshotSeq == nil {
			err = ErrRequest
		} else {
			result, err = s.room.Call[ClearReceipt](r.Context(), ClearMessages{RoomIdentity: req.RoomIdentity,
				Caller: req.Caller, ThroughSeq: *req.SnapshotSeq})
		}
	case "/v1/member/add":
		result, err = s.add(req)
	case "/v1/member/remove":
		result, err = s.remove(req)
	case "/v1/member/list":
		result, err = s.members(r.Context())
	case "/v1/member/inbox":
		w.Header().Set("Cache-Control", "no-store")
		result, err = s.previewInbox(r.Context(), req)
	case "/v1/member/inbox/status":
		w.Header().Set("Cache-Control", "no-store")
		result, err = s.inspectInbox(r.Context(), req)
	case "/v1/member/activity":
		w.Header().Set("Cache-Control", "no-store")
		result, err = s.memberActivity(r.Context(), req)
	case "/v1/member/run":
		// Operator intent: continue, replenishing exhausted turns and safely
		// rotating a disabled Session when necessary. Internal run stays strict.
		result, err = s.control(r.Context(), req, "enable")
	case "/v1/member/pause":
		result, err = s.control(r.Context(), req, "pause")
	case "/v1/member/session/status":
		id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
		result, err = s.target(req.MembershipID).Call[MemberStatus](r.Context(), InspectMember{id})
	case "/v1/member/session/rotate":
		if !validRestartID(req.OperationID) || !validSessionGeneration(req.FromGeneration) {
			err = fmt.Errorf("%w: rotation requires operation_id and from_generation", ErrRequest)
		} else {
			result, err = s.rotateSession(r.Context(), req)
		}
	case "/v1/send":
		result, err = s.room.Call[PostReceipt](r.Context(), PostMessage{RoomIdentity: req.RoomIdentity, Caller: req.Caller,
			ClientMessageID: req.ClientMessageID, Text: req.Text, ReplyTo: req.ReplyTo})
	case "/v1/read":
		result, err = s.room.Call[ReadPage](r.Context(), ReadMessages{RoomIdentity: req.RoomIdentity, Caller: req.Caller,
			Mode: req.Mode, Latest: req.Latest, After: req.After, SnapshotSeq: req.SnapshotSeq, Limit: req.Limit, Peek: req.Peek})
	case "/v1/read/confirm":
		if req.Caller.Kind != "agent" {
			err = ErrIdentity
			break
		}
		result, err = s.room.Call[ReadConfirmation](r.Context(), ConfirmRead{MemberIdentity: MemberIdentity{
			RoomIdentity: req.RoomIdentity, AgentID: req.Caller.ID, MembershipID: req.Caller.MembershipID}, ReadReceipt: req.ReadReceipt})
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}

func writeAPIError(w http.ResponseWriter, err error) {
	code, status := "failed", http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrVersion):
		code, status = "schema_switch_required", http.StatusConflict
	case errors.Is(err, ErrClosing):
		code, status = "closing", http.StatusServiceUnavailable
	case errors.Is(err, ErrTurnBudget):
		code, status = "turn_budget", http.StatusConflict
	case errors.Is(err, ErrBusy), errors.Is(err, actorx.ErrFull):
		code, status = "busy", http.StatusConflict
	case errors.Is(err, ErrIdentity):
		code, status = "identity", http.StatusForbidden
	case errors.Is(err, ErrConflict):
		code, status = "conflict", http.StatusConflict
	case errors.Is(err, ErrReadExpired):
		code, status = "read_expired", http.StatusConflict
	case errors.Is(err, ErrInboxExpired):
		code, status = "inbox_expired", http.StatusConflict
	case errors.Is(err, ErrCapacity):
		code, status = "capacity", http.StatusConflict
	case errors.Is(err, ErrRequest):
		code, status = "request", http.StatusBadRequest
	}
	// Only authored diagnostics may disclose detail. Wrapped filesystem/platform
	// errors can contain private paths, credentials or untrusted remote text.
	message, hint := "operation failed", "Outcome may be uncertain; query saved/remote state before any write retry."
	for _, known := range []error{ErrVersion, ErrClosing, ErrTurnBudget, ErrBusy, ErrIdentity, ErrConflict, ErrReadExpired, ErrInboxExpired, ErrCapacity, ErrRequest} {
		if errors.Is(err, known) {
			message, hint = known.Error(), ""
			break
		}
	}
	switch code {
	case "schema_switch_required":
		hint = "Preserve existing evidence and inspect its schema before switching; do not clear data."
	case "identity":
		hint = "Check the current Room/member binding and caller permission; do not reuse another membership."
	case "request":
		hint = "Use the command's --help for its exact parameters and constraints."
	case "conflict":
		hint = "Read the affected record and reassess; reconcile uncertain writes before any resend."
	}
	failure := apiError{Error: message, Code: code, Message: message, Hint: hint}
	var diagnostic *requestDiagnostic
	if errors.As(err, &diagnostic) {
		failure.Error, failure.Message = diagnostic.Error(), diagnostic.Message
		failure.Field, failure.ExpectedVersion, failure.ActualVersion, failure.Hint = diagnostic.Field, diagnostic.ExpectedVersion, diagnostic.ActualVersion, diagnostic.Hint
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(failure)
}
