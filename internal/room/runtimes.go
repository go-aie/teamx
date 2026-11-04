package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
)

const roomsIndexVersion = 2
const maxRoomOpens = 10000

// mu protects the registry and each operation slot. A slot is reserved before
// assembly; only its worker closes operationDone. Long work runs without mu.
type roomRuntime struct {
	service          *service
	status           string
	err              string
	busy             bool
	resuming         bool // initial scheduling may call tools before opening finishes
	operationDone    chan struct{}
	operationContext context.Context
	operationCancel  context.CancelFunc
}

// All helpers below require mu. Execution ownership and lifecycle exclusion
// always come from the complete Room identity.
func (m *roomManager) roomBusyLocked(id RoomIdentity) bool {
	rt := m.runtimes[id]
	return m.busy || rt != nil && rt.busy
}

func (m *roomManager) reserveRuntimeLocked(id RoomIdentity) *roomRuntime {
	rt := m.runtimes[id]
	if rt == nil {
		rt = &roomRuntime{status: "closed"}
		m.runtimes[id] = rt
	}
	rt.busy, rt.operationDone = true, make(chan struct{})
	rt.operationContext, rt.operationCancel = context.WithCancel(context.Background())
	return rt
}

func (m *roomManager) setRuntimeServiceLocked(id RoomIdentity, svc *service) {
	rt := m.runtimes[id]
	if rt == nil {
		rt = &roomRuntime{}
		m.runtimes[id] = rt
	}
	rt.service = svc
}

// The caller owns this Room's slot. Stop new grants before joining canceled
// tools/queries; no actor or process wait may hold the catalog gate.
func (m *roomManager) stopRoomGrants(ctx context.Context, owner RoomIdentity) error {
	m.mu.Lock()
	svc, _ := m.runtimeServiceLocked(owner)
	m.mu.Unlock()
	if svc == nil || svc.grantsStopped.Load() {
		return nil
	}
	_, err := svc.scheduler.Call[struct{}](ctx, stopTurnGrants{})
	if err == nil {
		svc.grantsStopped.Store(true)
	}
	return err
}

func (m *roomManager) finishRuntimeLocked(rt *roomRuntime, err error) {
	if rt.operationCancel != nil {
		rt.operationCancel()
	}
	rt.busy, rt.resuming = false, false
	rt.err = ""
	if err != nil {
		rt.status, rt.err = "failed", err.Error()
	} else if rt.service != nil {
		rt.status = "ready"
	} else {
		rt.status = "closed"
	}
	close(rt.operationDone)
}

type roomOpen struct {
	RoomIdentity
	OpenID   string       `json:"open_id"`
	Sequence uint64       `json:"sequence,omitempty"`
	Status   string       `json:"status"`
	Service  *ServiceInfo `json:"service,omitempty"`
	Error    string       `json:"error,omitempty"`
}

type roomOpenRequest struct {
	RoomIdentity
	OpenID       string `json:"open_id"`
	ServiceEpoch string `json:"service_epoch"`
	Caller       Caller `json:"caller"`
}

func appendOwner(owners []RoomIdentity, owner RoomIdentity) []RoomIdentity {
	for _, existing := range owners {
		if existing == owner {
			return owners
		}
	}
	return append(owners, owner)
}

func (m *roomManager) migrateIndex() error {
	if m.index.Version == roomsIndexVersion && m.index.OwnedRoomID == "" {
		return nil
	}
	next := m.copyIndex()
	next.Version = roomsIndexVersion
	// ActiveRoomID remains a legacy UI default, never an inferred owner.
	return m.saveIndex(next)
}

func (m *roomManager) validateRuntimesIndex() error {
	sequences := map[uint64]bool{}
	for _, op := range m.index.Activations {
		if op.Sequence != 0 {
			if sequences[op.Sequence] {
				return ErrInvalid
			}
			sequences[op.Sequence] = true
		}
	}
	owners := map[RoomIdentity]bool{}
	for _, owner := range m.index.OwnedRooms {
		if owners[owner] {
			return ErrInvalid
		}
		found := false
		for _, e := range m.index.Rooms {
			found = found || e.RoomIdentity == owner && e.Status != "creating"
		}
		if !found {
			return ErrIdentity
		}
		owners[owner] = true
	}
	stops := map[RoomIdentity]bool{}
	for _, record := range m.index.Shutdowns {
		if !owners[record.RoomIdentity] || stops[record.RoomIdentity] {
			return ErrIdentity
		}
		stops[record.RoomIdentity] = true
	}
	if len(m.index.Opens) > maxRoomOpens {
		return ErrCapacity
	}
	seen := map[string]bool{}
	for _, op := range m.index.Opens {
		if op.Sequence != 0 {
			if sequences[op.Sequence] {
				return ErrInvalid
			}
			sequences[op.Sequence] = true
		}
		if !safeMembershipID(op.OpenID) || seen[op.OpenID] ||
			(op.Status != "running" && op.Status != "completed" && op.Status != "failed") {
			return ErrInvalid
		}
		seen[op.OpenID] = true
		found := false
		for _, e := range m.index.Rooms {
			found = found || e.RoomIdentity == op.RoomIdentity && e.Status != "creating"
		}
		if !found || op.Service != nil && (op.Service.RoomIdentity != op.RoomIdentity || !validText(op.Service.ServiceEpoch)) ||
			op.Status == "completed" && op.Service == nil {
			return ErrIdentity
		}
	}
	return nil
}

// Exact identity lookup never assembles or follows the UI selection.
func (m *roomManager) runtimeServiceLocked(id RoomIdentity) (*service, *roomRuntime) {
	if rt := m.runtimes[id]; rt != nil {
		return rt.service, rt
	}
	return nil, nil
}

func (m *roomManager) serveRoomOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		m.mu.Lock()
		var result *roomOpen
		for _, op := range m.index.Opens {
			if op.OpenID == q.Get("open_id") {
				result = &op
				break
			}
		}
		m.mu.Unlock()
		if result != nil {
			_ = json.NewEncoder(w).Encode(result)
			return
		}
		writeAPIError(w, ErrRequest)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req roomOpenRequest
	if !decodeToolRequest(w, r, &req) {
		return
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	m.mu.Lock()
	op, err := m.startOpen(req.OpenID, req.RoomIdentity)
	m.mu.Unlock()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(op)
}

func (m *roomManager) startOpen(id string, target RoomIdentity) (roomOpen, error) {
	if !safeMembershipID(id) {
		return roomOpen{}, ErrRequest
	}
	if m.closing {
		return roomOpen{}, ErrClosing
	}
	if err := m.store.check(true); err != nil {
		return roomOpen{}, err
	}
	for _, op := range m.index.Opens {
		if op.OpenID == id {
			if op.RoomIdentity != target {
				return roomOpen{}, ErrConflict
			}
			return op, nil
		}
	}
	if m.busy { // shared connection settlement still owns the global gate
		return roomOpen{}, ErrBusy
	}
	if err := m.checkWorkspaceOperations(target); err != nil {
		return roomOpen{}, err
	}
	svc, rt := m.runtimeServiceLocked(target)
	if rt != nil && rt.busy || svc != nil && (svc.closing.Load() || svc.restarting.Load() || rt != nil && rt.status == "failed") {
		return roomOpen{}, ErrBusy
	}
	found := false
	for _, e := range m.index.Rooms {
		found = found || e.RoomIdentity == target && e.Status == "ready"
	}
	if !found {
		return roomOpen{}, ErrIdentity
	}
	if len(m.index.Opens) >= maxRoomOpens {
		return roomOpen{}, ErrCapacity
	}
	sequence, err := m.nextRuntimeOperationSequence()
	if err != nil {
		return roomOpen{}, err
	}
	op := roomOpen{RoomIdentity: target, OpenID: id, Sequence: sequence, Status: "running"}
	if svc != nil {
		info := svc.info
		op.Status, op.Service = "completed", &info
	}
	next := m.copyIndex()
	next.Opens = append(next.Opens, op)
	// Durable ownership precedes workspace preparation and service assembly.
	next.OwnedRooms = appendOwner(next.OwnedRooms, target)
	if svc == nil {
		next.forgetShutdown(target)
	}
	if err := m.saveIndex(next); err != nil {
		return roomOpen{}, err
	}
	if svc == nil {
		rt = m.reserveRuntimeLocked(target)
		rt.status = "opening"
		go m.runOpen(op, rt)
	}
	return op, nil
}

func (m *roomManager) runOpen(op roomOpen, rt *roomRuntime) {
	svc := rt.service
	var runErr error
	if svc == nil {
		config, err := m.activationConfig(op.RoomIdentity)
		runErr = err
		if runErr == nil {
			var saved managedRoomConfig
			runErr = readSnapshot(filepath.Join(m.roomDir(op.RoomID), "room-config.json"), &saved, maxPrivateFileBytes)
			if runErr == nil {
				_, runErr = m.ensureWorkspaces(rt.operationContext, saved, false)
			}
		}
		if runErr == nil {
			svc, runErr = m.factory(m.roomDir(op.RoomID), op.RoomID, config)
			if svc != nil {
				svc.settleTools = func(ctx context.Context) error { return m.waitTools(ctx, &op.RoomIdentity) }
			}
			if runErr == nil && svc.info.RoomIdentity != op.RoomIdentity {
				runErr = ErrIdentity
			}
		}
		// Publish ownership before restoring existing enable intent. Repeated
		// opens never resume members, reset budgets or retry interrupted turns.
		if runErr == nil {
			m.mu.Lock()
			if m.closing {
				runErr = ErrClosing
			} else {
				rt.service, rt.resuming = svc, true
			}
			m.mu.Unlock()
		}
		if runErr == nil {
			runErr = svc.resumeMembers(rt.operationContext)
		}
	}
	m.mu.Lock()
	if runErr == nil && m.closing {
		runErr = ErrClosing
	}
	if runErr == nil {
		next := m.copyIndex()
		for i := range next.Opens {
			if op.OpenID != "" && next.Opens[i].OpenID == op.OpenID {
				info := svc.info
				next.Opens[i].Status, next.Opens[i].Service = "completed", &info
			}
		}
		runErr = m.saveIndex(next)
	}
	if runErr == nil {
		rt.service = svc
		m.finishRuntimeLocked(rt, nil)
		m.notifications.WakeRoomLocked(op.RoomIdentity)
		m.mu.Unlock()
		return
	}
	rt.resuming = false
	if svc != nil {
		svc.closing.Store(true)
	}
	m.cancelToolsLocked(func(c *toolCall) bool { return c.owner == op.RoomIdentity })
	m.mu.Unlock()
	if svc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
		cleanupErr := errors.Join(m.waitTools(ctx, &op.RoomIdentity), svc.close(ctx))
		runErr = errors.Join(runErr, cleanupErr)
		cancel()
		svc.store.mu.Lock()
		released := svc.store.lock == nil
		svc.store.mu.Unlock()
		if released && cleanupErr == nil {
			svc = nil
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rt.service = svc
	rt.err = runErr.Error()
	next := m.copyIndex()
	for i := range next.Opens {
		if op.OpenID != "" && next.Opens[i].OpenID == op.OpenID {
			next.Opens[i].Status, next.Opens[i].Error = "failed", runErr.Error()
		}
	}
	if err := m.saveIndex(next); err != nil {
		rt.err = errors.Join(runErr, err).Error()
		// Keep the receipt diagnostic available even if persistence is latched.
		m.index.Opens = next.Opens
	}
	m.finishRuntimeLocked(rt, errors.New(rt.err))
}

func (m *roomManager) serveRuntime(w http.ResponseWriter, r *http.Request) {
	var info ServiceInfo
	var req apiRequest
	if r.Method == http.MethodGet {
		q := r.URL.Query()
		if !validText(q.Get("caller_id")) {
			writeAPIError(w, ErrIdentity)
			return
		}
		info = ServiceInfo{RoomIdentity: RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}, ServiceEpoch: q.Get("service_epoch")}
	} else {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 128<<10))
		if err != nil || json.Unmarshal(data, &req) != nil {
			writeAPIError(w, ErrRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		info = req.ServiceInfo
	}
	m.mu.Lock()
	svc, rt := m.runtimeServiceLocked(info.RoomIdentity)
	busy := m.roomBusyLocked(info.RoomIdentity)
	closing := m.closing
	m.mu.Unlock()
	if closing {
		writeAPIError(w, ErrClosing)
		return
	}
	if svc == nil || info != svc.info {
		writeAPIError(w, ErrIdentity)
		return
	}
	tools := r.URL.Path == "/v1/send" || r.URL.Path == "/v1/read" || r.URL.Path == "/v1/read/confirm"
	if busy && (!tools || req.Caller.Kind != "agent") {
		writeAPIError(w, ErrBusy)
		return
	}
	if r.URL.Path == "/v1/member/add" || r.URL.Path == "/v1/member/remove" {
		writeAPIError(w, fmt.Errorf("%w: managed rooms retain their saved roster", ErrRequest))
		return
	}
	if r.URL.Path == "/v1/discussion/restart" {
		m.mu.Lock()
		current, _ := m.runtimeServiceLocked(info.RoomIdentity)
		if m.closing || m.roomBusyLocked(info.RoomIdentity) || current != svc {
			m.mu.Unlock()
			writeAPIError(w, ErrBusy)
			return
		}
		rt = m.reserveRuntimeLocked(info.RoomIdentity)
		m.mu.Unlock()
		svc.ServeHTTP(w, r)
		go func() {
			svc.management.Lock()
			svc.management.Unlock()
			m.mu.Lock()
			defer m.mu.Unlock()
			m.finishRuntimeLocked(rt, nil)
			m.notifications.WakeRoomLocked(info.RoomIdentity)
		}()
		return
	}
	svc.ServeHTTP(w, r)
}
