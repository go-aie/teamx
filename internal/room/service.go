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
)

type ServiceInfo struct {
	RoomIdentity
	ServiceEpoch string `json:"service_epoch"`
}

// Only these explicit DTOs cross HTTP. Actor addresses remain in the service.
type apiRequest struct {
	ServiceInfo
	Caller            Caller       `json:"caller"`
	AgentID           string       `json:"agent_id,omitempty"`
	MembershipID      string       `json:"membership_id,omitempty"`
	Runner            RunnerConfig `json:"runner,omitempty"`
	ClientMessageID   string       `json:"client_message_id,omitempty"`
	Text              string       `json:"text,omitempty"`
	ReplyTo           uint64       `json:"reply_to,omitempty"`
	Mode              ReadMode     `json:"mode,omitempty"`
	Latest            int          `json:"latest,omitempty"`
	After             *uint64      `json:"after,omitempty"`
	SnapshotSeq       *uint64      `json:"snapshot_seq,omitempty"`
	Limit             int          `json:"limit,omitempty"`
	Peek              bool         `json:"peek,omitempty"`
	ReadReceipt       string       `json:"read_receipt,omitempty"`
	Retry             bool         `json:"retry,omitempty"`
	Cancel            bool         `json:"cancel,omitempty"`
	Resume            bool         `json:"resume,omitempty"`
	RestartID         string       `json:"restart_id,omitempty"`
	PreviousRestartID string       `json:"previous_restart_id,omitempty"`
}

type serviceMember struct {
	MemberInfo
	Status       string         `json:"status"`
	Notification *UnreadChanged `json:"notification"`
	Execution    *MemberStatus  `json:"execution,omitempty"`
}

type serviceMembers struct {
	ServiceInfo
	LatestSeq  uint64             `json:"latest_seq"`
	Revision   uint64             `json:"revision"`
	Committing bool               `json:"committing"`
	WriteError string             `json:"write_error,omitempty"`
	Members    []serviceMember    `json:"members"`
	Restart    *discussionRestart `json:"discussion_restart,omitempty"`
}

type apiError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

type service struct {
	workspaceOperationID string
	info                 ServiceInfo
	store                *Store
	rt                   *actorx.Runtime
	room                 actorx.Addr
	scheduler            actorx.Addr
	ctx                  context.Context
	cancel               context.CancelFunc
	// Management has one retained owner even if its HTTP caller leaves.
	management           sync.Mutex
	changes              sync.RWMutex
	restarting           atomic.Bool
	restart              *discussionRestart // guarded by mu
	cleanup              sync.Mutex
	closing              atomic.Bool
	grantsStopped        bool
	mu                   sync.Mutex
	targets              map[string]actorx.Addr
	requests             chan struct{}
	endpoint             string
	cliPath              string
	mergeWindow          time.Duration
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
	epoch, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := actorx.NewRuntime(ctx, actorx.WithInstanceLimit(maxMembers+3))
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
	scheduler, err := rt.Spawn(&turnScheduler{RoomIdentity: state.RoomIdentity})
	if err != nil {
		return nil, err
	}
	s := &service{
		workspaceOperationID: config.workspaceOperationID,
		info:                 ServiceInfo{RoomIdentity: state.RoomIdentity, ServiceEpoch: epoch}, store: store,
		rt: rt, room: addr, scheduler: scheduler, ctx: ctx, cancel: cancel,
		targets: make(map[string]actorx.Addr), requests: make(chan struct{}, 32),
		endpoint: config.endpoint, cliPath: config.cliPath, mergeWindow: config.mergeWindow,
		maxTurns:             config.maxTurns,
		persistentScheduling: config.persistentScheduling,
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

func (s *service) close(ctx context.Context) error {
	s.closing.Store(true)
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
	if !s.grantsStopped {
		if _, err := s.scheduler.Call[struct{}](ctx, stopTurnGrants{}); err != nil {
			return fmt.Errorf("scheduler shutdown incomplete (store still owned): %w", err)
		}
		s.grantsStopped = true
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
	result := serviceMembers{ServiceInfo: s.info, LatestSeq: list.LatestSeq, Revision: list.Revision,
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
			state := UnreadChanged{MemberIdentity: id, Revision: list.Revision, LatestSeq: list.LatestSeq,
				NewestUnreadSeq: member.NewestUnreadSeq, UnreadCount: member.UnreadCount}
			return s.target(member.MembershipID).Call[MemberStatus](ctx, ControlMember{MemberIdentity: id, Action: action,
				Retry: req.Retry, Cancel: req.Cancel, Resume: req.Resume, State: state})
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
	id := MemberIdentity{RoomIdentity: s.info.RoomIdentity, AgentID: req.AgentID, MembershipID: req.MembershipID}
	removed, err := s.room.Call[MemberInfo](s.ctx, RemoveMember{id})
	if err != nil {
		return MemberInfo{}, err
	}
	return removed, s.settleMemberRemoval(req.MembershipID)
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
	if s.serveWeb(w, r) {
		return
	}
	if r.URL.Path == "/v1/info" && r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(s.info)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req apiRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, ErrRequest)
		return
	}
	if dec.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.ServiceInfo != s.info {
		writeAPIError(w, ErrIdentity)
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
		if r.URL.Path != "/v1/send" && r.URL.Path != "/v1/read" && r.URL.Path != "/v1/read/confirm" {
			writeAPIError(w, ErrIdentity)
			return
		}
	}
	var result any
	var err error
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
		if s.restarting.Load() {
			writeAPIError(w, fmt.Errorf("%w: discussion restart in progress", ErrBusy))
			return
		}
	}
	switch r.URL.Path {
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
	case "/v1/member/activity":
		w.Header().Set("Cache-Control", "no-store")
		result, err = s.memberActivity(r.Context(), req)
	case "/v1/member/run":
		result, err = s.control(r.Context(), req, "run")
	case "/v1/member/pause":
		result, err = s.control(r.Context(), req, "pause")
	case "/v1/member/session/rotate":
		result, err = s.rotateSession(r.Context(), req)
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
	case errors.Is(err, ErrCapacity):
		code, status = "capacity", http.StatusConflict
	case errors.Is(err, ErrRequest):
		code, status = "request", http.StatusBadRequest
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(apiError{Error: err.Error(), Code: code})
}
