// Package runtime owns durable remote execution facts and screen admission.
// Process and native MCP I/O always run outside the metadata mutex.
package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/computer"
	"github.com/go-aie/teamx/internal/persistence"
)

const maxRecords = 256
const maxOperations = 1024
const snapshotLimit = 32 << 20

type Resource struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Epoch uint64 `json:"epoch"`
}

// Driver is private to the managed Runtime. It must prove Stop by checking the
// entire screen process range, including GUI-created descendants.
type Driver interface {
	Validate(string, json.RawMessage) error
	Start(context.Context, Resource) error
	Tools(context.Context, Resource) (computer.Contract, error)
	Call(context.Context, Resource, string, json.RawMessage) (json.RawMessage, error)
	Stop(context.Context, Resource) error
}
type record struct {
	Resource   Resource                      `json:"resource"`
	Binding    computer.Binding              `json:"binding"`
	Status     string                        `json:"status"`
	Retired    bool                          `json:"retired"`
	Closed     []computer.Scope              `json:"closed,omitempty"`
	Operations map[string]computer.Operation `json:"operations"`
}
type state struct {
	Admissions      map[string]computer.MemberAdmission `json:"admissions,omitempty"`
	Version         int                                 `json:"version"`
	Instance        string                              `json:"instance"`
	Owner           computer.Owner                      `json:"owner"`
	Service         string                              `json:"service"`
	ClaimExpected   string                              `json:"claim_expected"`
	Lease           string                              `json:"lease"`
	RetiredServices map[string]bool                     `json:"retired_services"`
	Screens         map[string]*record                  `json:"screens"`
}
type cached struct {
	Result  json.RawMessage
	Expires time.Time
}
type active struct {
	done   chan struct{}
	scope  computer.Scope
	cancel context.CancelFunc
	err    error
}
type Engine struct {
	mu       sync.Mutex
	state    state
	path     string
	writer   *persistence.Writer
	unlock   func() error
	driver   Driver
	contract computer.Contract
	boot     string
	max      int
	active   map[string]*active
	results  map[string]cached
	closed   bool
	closing  bool
	controls map[string]*humanControl
	views    map[string]map[*viewStream]bool
}

func Open(ctx context.Context, dir string, max int, contract computer.Contract, driver Driver) (*Engine, error) {
	if max < 1 || max > maxRecords || driver == nil {
		return nil, computer.Fail(computer.Invalid, "invalid Runtime capacity or driver")
	}
	verified, err := computer.NewContract(contract.Provider, contract.Versions, contract.Tools)
	if err != nil || verified.Digest != contract.Digest {
		return nil, computer.Fail(computer.ContractMismatch, "invalid pinned contract")
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err = os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	unlock, err := lockDirectory(dir)
	if err != nil {
		return nil, err
	}
	e := &Engine{path: filepath.Join(dir, "runtime.json"), writer: persistence.NewWriter(persistence.DefaultOperations()), unlock: unlock, driver: driver, contract: contract.Clone(), boot: computer.NewID(), max: max, active: map[string]*active{}, results: map[string]cached{}}
	e.controls = map[string]*humanControl{}
	e.views = map[string]map[*viewStream]bool{}
	e.state = state{Version: 1, Instance: computer.NewID(), Lease: computer.NewID(), RetiredServices: map[string]bool{}, Screens: map[string]*record{}}
	if err = persistence.ReadJSON(e.path, &e.state, snapshotLimit); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = unlock()
		return nil, err
	}
	if e.state.Version != 1 || e.state.Screens == nil || e.state.RetiredServices == nil || len(e.state.Screens) > maxRecords {
		_ = unlock()
		return nil, computer.Fail(computer.Invalid, "invalid Runtime snapshot")
	}

	if e.state.Admissions == nil {
		e.state.Admissions = map[string]computer.MemberAdmission{}
	}
	for key, a := range e.state.Admissions {
		if a.Binding.Validate() != nil || a.Binding.Key() != key || (a.Retired && a.Enabled) {
			_ = unlock()
			return nil, computer.Fail(computer.Invalid, "invalid persisted member admission")
		}
	}
	indexes := map[int]bool{}
	for id, s := range e.state.Screens {
		_, identityErr := hex.DecodeString(id)
		if identityErr != nil || s == nil || s.Resource.ID != id || len(id) != 32 || s.Resource.Index < 0 || s.Resource.Index >= maxRecords || indexes[s.Resource.Index] || s.Operations == nil || len(s.Operations) > maxOperations || len(s.Closed) > maxOperations || s.Binding.Validate() != nil {
			_ = unlock()
			return nil, computer.Fail(computer.Invalid, "invalid persisted screen ownership or allocation")
		}
		indexes[s.Resource.Index] = true
	}
	for i := 0; i < len(e.state.Screens); i++ {
		if !indexes[i] {
			_ = unlock()
			return nil, computer.Fail(computer.Invalid, "non-contiguous screen allocation")
		}
	}
	// Invalidate service leases before doing remote recovery. A killed Runtime's
	// accepted operations are unknown, never replayed, even if the tool returned.
	if e.state.Service != "" {
		e.state.RetiredServices[e.state.Service] = true
	}
	e.state.Service = ""
	e.state.Lease = computer.NewID()
	for _, s := range e.state.Screens {
		for id, op := range s.Operations {
			if op.State == "pending" {
				op.State = "unknown"
				s.Operations[id] = op
			}
		}
		s.Status = "blocked"
	}
	if err = e.save(); err != nil {
		_ = unlock()
		return nil, err
	}
	for _, s := range e.state.Screens {
		// A failed recovery only blocks this screen. Other screens remain usable.
		stopCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		stopErr := driver.Stop(stopCtx, s.Resource)
		cancel()
		if stopErr == nil {
			s.Status = "stopped"
			settle(s)
		}
		if err = e.save(); err != nil {
			_ = unlock()
			return nil, err
		}
	}
	return e, nil
}
func (e *Engine) save() error { return e.writer.Save(e.path, e.state, snapshotLimit) }
func settle(s *record) {
	for id, op := range s.Operations {
		if !op.Settled {
			op.State = "unknown"
			op.Settled = true
			s.Operations[id] = op
		}
	}
}
func (e *Engine) Inspect(ctx context.Context) (computer.Info, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed || e.closing {
		return computer.Info{}, computer.Fail(computer.Unavailable, "Runtime closed")
	}
	running := 0
	for _, s := range e.state.Screens {
		if s.Status != "stopped" {
			running++
		}
	}
	return computer.Info{MemberAdmission: true, Owner: e.state.Owner, InstanceID: e.state.Instance, BootID: e.boot, Lease: e.state.Lease, MaxScreens: e.max, Running: running, Contract: e.contract.Clone()}, nil
}

func (e *Engine) Screens(ctx context.Context, owner computer.Owner) ([]computer.ScreenInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	// Connection settlement consumes inventory as durable evidence, so an
	// uncertain Runtime commit must not be presented as a verified stop.
	if err := e.writer.Check(true); err != nil {
		return nil, err
	}
	if e.closed || e.closing {
		return nil, computer.Fail(computer.Unavailable, "Runtime closed")
	}
	if e.state.Owner != (computer.Owner{}) && owner != e.state.Owner {
		return nil, computer.Fail(computer.Stale, "Computer belongs to another installation or owner")
	}
	out := []computer.ScreenInfo{}
	for _, r := range e.state.Screens {
		if !r.Retired {
			out = append(out, e.screen(r).Info())
		}
	}
	return out, nil
}
func (e *Engine) Restore(info computer.ScreenInfo) computer.Screen { return &screen{e: e, info: info} }
func (e *Engine) Claim(ctx context.Context, c computer.Claim) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := c.Validate(); err != nil {
		return "", err
	}
	if e.closed || e.closing {
		return "", computer.Fail(computer.Unavailable, "Runtime closed")
	}
	if e.state.Owner != (computer.Owner{}) && e.state.Owner != c.Owner {
		return "", computer.Fail(computer.Stale, "Computer belongs to another installation or owner")
	}
	if e.state.RetiredServices[c.Service] {
		return "", computer.Fail(computer.Stale, "service generation fenced out")
	}
	if e.state.Service == c.Service && (e.state.Lease == c.ExpectedLease || e.state.ClaimExpected == c.ExpectedLease) {
		return e.state.Lease, nil
	}
	if c.ExpectedLease != e.state.Lease {
		return "", computer.Fail(computer.Stale, "ownership compare-and-swap failed")
	}
	if len(e.state.RetiredServices) >= maxOperations {
		return "", computer.Fail(computer.Capacity, "service fencing history full")
	}
	// A CAS handoff fences the previous service immediately. Pending work stays
	// owned by its screen and must still drain/stop before that screen can admit
	// another call. It must not prevent recovery of other independent screens.
	if e.state.Service != "" {
		e.state.RetiredServices[e.state.Service] = true
	}
	for id := range e.views {
		e.cancelViews(id, false)
	}
	e.controls = map[string]*humanControl{}
	e.state.Owner = c.Owner
	e.state.Service = c.Service
	e.state.ClaimExpected = c.ExpectedLease
	e.state.Lease = computer.NewID()
	for _, r := range e.state.Screens {
		r.Binding.Service = c.Service
		r.Binding.Lease = e.state.Lease
	}
	if err := e.save(); err != nil {
		return "", err
	}
	return e.state.Lease, nil
}
func (e *Engine) binding(b computer.Binding) error {
	if err := b.Validate(); err != nil {
		return err
	}
	if e.closed || e.closing {
		return computer.Fail(computer.Unavailable, "Runtime closed")
	}
	if err := e.writer.Check(true); err != nil {
		return err
	}
	if b.Owner != e.state.Owner || b.Service != e.state.Service || b.Lease != e.state.Lease {
		return computer.Fail(computer.Stale, "service ownership or lease revoked")
	}
	return nil
}

// SetMemberAdmission closes admission before inventory or process settlement.
// EnsureScreen records accepted starts under this same mutex, so an acknowledged
// closure also fences late HTTP requests whose earlier response was lost.
func (e *Engine) SetMemberAdmission(ctx context.Context, a computer.MemberAdmission) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.binding(a.Binding); err != nil {
		return err
	}
	if a.Retired && a.Enabled {
		return computer.Fail(computer.Invalid, "retired member cannot be enabled")
	}
	key := a.Binding.Key()
	if old, ok := e.state.Admissions[key]; ok {
		if old.Retired && a.Enabled {
			return computer.Fail(computer.Stale, "member admission permanently retired")
		}
		av, bv := a.Binding.Admission, old.Binding.Admission
		older := av.Connection < bv.Connection || av.Connection == bv.Connection && av.Member < bv.Member
		if older && !a.Retired {
			return computer.Fail(computer.Stale, "member admission version superseded")
		}
		if av == bv && a.Enabled != old.Enabled && !a.Retired {
			return computer.Fail(computer.Stale, "member admission version already decided")
		}
		if older {
			a.Binding.Admission = bv
		}
		a.Retired = a.Retired || old.Retired
	} else if len(e.state.Admissions) >= 4096 {
		return computer.Fail(computer.Capacity, "member admission history full")
	}
	if !a.Enabled {
		for id, r := range e.state.Screens {
			if r.Binding.Key() == key {
				e.cancelViews(id, false)
			}
		}
	}
	e.state.Admissions[key] = a
	return e.save()
}
func (e *Engine) admit(b computer.Binding) error {
	a, ok := e.state.Admissions[b.Key()]
	if !ok && b.Admission == (computer.AdmissionVersion{}) {
		return nil
	}
	if !ok || !a.Enabled || a.Retired || a.Binding.Admission != b.Admission {
		return computer.Fail(computer.Stale, "member admission closed or superseded")
	}
	return nil
}
func (e *Engine) EnsureScreen(ctx context.Context, b computer.Binding) (computer.Screen, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	e.mu.Lock()
	if err := e.binding(b); err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if err := e.admit(b); err != nil {
		e.mu.Unlock()
		return nil, err
	}
	for _, r := range e.state.Screens {
		if r.Retired && r.Binding.Owner == b.Owner && r.Binding.Member.RoomID == b.Member.RoomID && r.Binding.Member.RoomInstance == b.Member.RoomInstance {
			// Membership replacement retires one member, while a different active
			// RoomInstance tombstones the entire old Room incarnation.
			for _, current := range e.state.Screens {
				if !current.Retired && current.Binding.Owner == b.Owner && current.Binding.Member.RoomID == b.Member.RoomID && current.Binding.Member.RoomInstance != b.Member.RoomInstance {
					e.mu.Unlock()
					return nil, computer.Fail(computer.Stale, "Room incarnation retired")
				}
			}
		}
	}
	var s *record
	for _, r := range e.state.Screens {
		if r.Binding.Key() == b.Key() {
			s = r
			break
		}
	}
	if s != nil && s.Retired {
		e.mu.Unlock()
		return nil, computer.Fail(computer.Stale, "member incarnation retired")
	}
	if s != nil && s.Status == "running" {
		s.Binding = b
		out := e.screen(s)
		e.mu.Unlock()
		return out, nil
	}
	if s != nil && s.Status == "starting" {
		a := e.active[s.Resource.ID]
		e.mu.Unlock()
		if a == nil {
			return nil, computer.Fail(computer.Busy, "screen start requires reconciliation")
		}
		select {
		case <-a.done:
			if a.err != nil {
				return nil, a.err
			}
			return e.EnsureScreen(ctx, b)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if s != nil && s.Status != "stopped" {
		e.mu.Unlock()
		return nil, computer.Fail(computer.Busy, "screen stopping or blocked")
	}
	running := 0
	for _, r := range e.state.Screens {
		if r.Status != "stopped" {
			running++
		}
		if !r.Retired && supersedes(b, r.Binding) && r.Status != "stopped" {
			e.mu.Unlock()
			return nil, computer.Fail(computer.Stale, "previous member incarnation must be stopped first")
		}
	}
	if running >= e.max {
		e.mu.Unlock()
		return nil, computer.Fail(computer.Capacity, "running screen limit reached")
	}
	if s == nil {
		if len(e.state.Screens) >= maxRecords {
			e.mu.Unlock()
			return nil, computer.Fail(computer.Capacity, "persistent screen limit reached")
		}
		for _, r := range e.state.Screens {
			if supersedes(b, r.Binding) {
				r.Retired = true
			}
		}
		s = &record{Resource: Resource{ID: computer.NewID(), Index: len(e.state.Screens)}, Operations: map[string]computer.Operation{}}
		e.state.Screens[s.Resource.ID] = s
	}
	s.Binding = b
	s.Resource.Epoch++
	s.Status = "starting"
	s.Closed = nil
	resource := s.Resource
	if err := e.save(); err != nil {
		e.mu.Unlock()
		return nil, err
	}
	a := &active{done: make(chan struct{})}
	e.active[resource.ID] = a
	e.mu.Unlock()
	// Start has a finite budget independent of a disconnected management client.
	startCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	err := e.driver.Start(startCtx, resource)
	if err == nil {
		var catalog computer.Contract
		catalog, err = e.driver.Tools(startCtx, resource)
		if err == nil && catalog.Digest != e.contract.Digest {
			err = computer.Fail(computer.ContractMismatch, "screen native catalog differs from pinned contract")
		}
	}
	cancel()
	var cleanupErr error
	if err != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		cleanupErr = e.driver.Stop(stopCtx, resource)
		stopCancel()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.active, resource.ID)
	a.err = errors.Join(err, cleanupErr)
	close(a.done)
	if err == nil {
		s.Status = "running"
	} else if cleanupErr == nil {
		s.Status = "stopped"
	} else {
		s.Status = "blocked"
	}
	if saveErr := e.save(); saveErr != nil {
		return nil, saveErr
	}
	if err != nil {
		return nil, errors.Join(err, cleanupErr)
	}
	return e.screen(s), nil
}

// A Room incarnation change retires every prior member of that Room, even if
// the new Room uses a different set of Agent names.
func supersedes(next, old computer.Binding) bool {
	return next.Owner == old.Owner && next.Member.RoomID == old.Member.RoomID && (next.Member.RoomInstance != old.Member.RoomInstance || (next.Member.AgentID == old.Member.AgentID && next.Member.MembershipID != old.Member.MembershipID))
}

type screen struct {
	e    *Engine
	info computer.ScreenInfo
}

func (e *Engine) screen(s *record) *screen {
	return &screen{e, computer.ScreenInfo{ID: s.Resource.ID, Epoch: s.Resource.Epoch, Status: s.Status, Binding: s.Binding}}
}
func (s *screen) Info() computer.ScreenInfo { return s.info }
func (s *screen) get() (*record, error) {
	if err := s.e.binding(s.info.Binding); err != nil {
		return nil, err
	}
	return s.owned()
}
func (s *screen) owned() (*record, error) {
	r := s.e.state.Screens[s.info.ID]
	if r == nil || r.Retired || r.Binding.Key() != s.info.Binding.Key() || r.Resource.Epoch != s.info.Epoch {
		return nil, computer.Fail(computer.Stale, "screen identity or epoch revoked")
	}
	return r, nil
}

func (s *screen) Tools(ctx context.Context) (computer.Contract, error) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	if _, err := s.get(); err != nil {
		return computer.Contract{}, err
	}
	return s.e.contract.Clone(), nil
}
func (s *screen) Call(ctx context.Context, c computer.Call) (computer.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return computer.Outcome{}, err
	}
	e := s.e
	e.mu.Lock()
	r, err := s.get()
	if err != nil {
		e.mu.Unlock()
		return computer.Outcome{}, err
	}
	if err = e.admit(s.info.Binding); err != nil {
		e.mu.Unlock()
		return computer.Outcome{}, err
	}
	tool, ok := e.contract.Tool(c.Tool)
	var args map[string]json.RawMessage
	if c.ID == "" || len(c.ID) > 256 || c.Scope.Session == "" || len(c.Scope.Session) > 256 || len(c.Scope.Turn) > 256 || !ok || len(c.Arguments) > computer.RequestLimit || json.Unmarshal(c.Arguments, &args) != nil || args == nil {
		e.mu.Unlock()
		return computer.Outcome{}, computer.Fail(computer.Invalid, "invalid native call or execution scope")
	}
	if err = e.driver.Validate(tool.NativeName, c.Arguments); err != nil {
		e.mu.Unlock()
		return computer.Outcome{}, err
	}
	if c.ContractDigest != e.contract.Digest {
		e.mu.Unlock()
		return computer.Outcome{}, computer.Fail(computer.ContractMismatch, "frozen tool contract changed")
	}
	if old, exists := r.Operations[c.ID]; exists {
		if old.Digest != c.Digest() {
			e.mu.Unlock()
			return computer.Outcome{}, computer.Fail(computer.Invalid, "operation ID reused with different arguments or scope")
		}
		out := e.outcome(r, old)
		e.mu.Unlock()
		return out, nil
	}
	for _, scope := range r.Closed {
		if scope.Matches(c.Scope) {
			e.mu.Unlock()
			return computer.Outcome{}, computer.Fail(computer.Stale, "execution scope drained")
		}
	}
	if r.Status != "running" {
		e.mu.Unlock()
		return computer.Outcome{}, computer.Fail(computer.Unavailable, "screen is not running")
	}
	if e.active[r.Resource.ID] != nil {
		e.mu.Unlock()
		return computer.Outcome{}, computer.Fail(computer.Busy, "screen already executing")
	}
	if e.humanHeld(r.Resource.ID) {
		e.mu.Unlock()
		return computer.Outcome{}, computer.Fail(computer.HumanControl, "human takeover is pending or active; finish the turn without repeating this call")
	}
	for _, op := range r.Operations {
		if op.State == "unknown" && !op.Settled {
			e.mu.Unlock()
			return computer.Outcome{}, computer.Fail(computer.Unknown, "stop and verify screen before further calls")
		}
	}
	op := computer.Operation{ID: c.ID, Digest: c.Digest(), Epoch: r.Resource.Epoch, Scope: c.Scope, Tool: c.Tool, State: "pending"}
	if !tool.ReadOnly {
		if len(r.Operations) >= maxOperations {
			e.mu.Unlock()
			return computer.Outcome{}, computer.Fail(computer.Capacity, "screen operation journal full")
		}
		r.Operations[c.ID] = op
		if err = e.save(); err != nil {
			e.mu.Unlock()
			return computer.Outcome{}, err
		}
	}
	callCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	a := &active{done: make(chan struct{}), scope: c.Scope, cancel: cancel}
	e.active[r.Resource.ID] = a
	resource := r.Resource
	// Copy parameter bytes before returning to callers, including canceled callers.
	arguments := append(json.RawMessage(nil), c.Arguments...)
	e.mu.Unlock()
	var out computer.Outcome
	var executionErr error
	go func() {
		result, callErr := e.driver.Call(callCtx, resource, tool.NativeName, arguments)
		cancel()
		if callErr == nil && (len(result) > computer.ResponseLimit || !json.Valid(result)) {
			callErr = computer.Fail(computer.Unavailable, "native response invalid or exceeds 8 MiB; query operation and inspect screen")
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		op.State = "completed"
		op.Settled = true
		if callErr != nil {
			op.State = "unknown"
			op.Settled = false
			r.Status = "blocked"
		}
		if !tool.ReadOnly {
			r.Operations[c.ID] = op
			if saveErr := e.save(); saveErr != nil {
				callErr = errors.Join(callErr, saveErr)
				r.Status = "blocked"
			}
		}
		if callErr == nil {
			e.cache(resource.ID+"/"+c.ID, result)
		}
		out = computer.Outcome{Operation: op, ResultAvailable: callErr == nil, Result: result}
		if callErr != nil {
			out.Result = nil
			executionErr = computer.Fail(computer.Unknown, "native call has no verified terminal response; query operation or stop screen")
		}
		delete(e.active, resource.ID)
		close(a.done)
	}()
	select {
	case <-a.done:
		return out, executionErr
	case <-ctx.Done():
		return computer.Outcome{}, ctx.Err()
	}
}
func (e *Engine) cache(key string, result json.RawMessage) {
	now := time.Now()
	total := 0
	for k, c := range e.results {
		if now.After(c.Expires) {
			delete(e.results, k)
		} else {
			total += len(c.Result)
		}
	}
	if total+len(result) > 16<<20 || len(e.results) >= 64 {
		return
	}
	e.results[key] = cached{append(json.RawMessage(nil), result...), now.Add(time.Minute)}
}
func (e *Engine) outcome(r *record, op computer.Operation) computer.Outcome {
	out := computer.Outcome{Operation: op}
	if c, ok := e.results[r.Resource.ID+"/"+op.ID]; ok && time.Now().Before(c.Expires) {
		out.Result = append(json.RawMessage(nil), c.Result...)
		out.ResultAvailable = true
	}
	return out
}
func (s *screen) Operation(ctx context.Context, id string) (computer.Outcome, error) {
	s.e.mu.Lock()
	defer s.e.mu.Unlock()
	r, err := s.get()
	if err != nil {
		return computer.Outcome{}, err
	}
	op, ok := r.Operations[id]
	if !ok {
		return computer.Outcome{}, computer.Fail(computer.Unknown, "operation not recorded; do not replay")
	}
	return s.e.outcome(r, op), nil
}
func (s *screen) Drain(ctx context.Context, scope computer.Scope) error {
	e := s.e
	e.mu.Lock()
	r, err := s.get()
	if err != nil {
		e.mu.Unlock()
		return err
	}
	found := false
	for _, closed := range r.Closed {
		if closed.Matches(scope) {
			found = true
		}
	}
	if !found {
		if len(r.Closed) >= maxOperations {
			e.mu.Unlock()
			return computer.Fail(computer.Capacity, "drained scope limit reached")
		}
		r.Closed = append(r.Closed, scope)
		if err = e.save(); err != nil {
			e.mu.Unlock()
			return err
		}
	}
	a := e.active[r.Resource.ID]
	e.mu.Unlock()
	if a != nil && (a.scope.Session == "" || scope.Matches(a.scope)) {
		select {
		case <-a.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err = s.get(); err != nil {
		return err
	}
	for _, op := range r.Operations {
		if scope.Matches(op.Scope) && !op.Settled {
			return computer.Fail(computer.Unknown, "remote operation is not settled")
		}
	}
	if r.Status == "blocked" {
		return computer.Fail(computer.Unknown, "screen stop has not been verified")
	}
	return nil
}
func (s *screen) Stop(ctx context.Context) error { return s.stop(ctx, false) }
func (s *screen) stop(ctx context.Context, internal bool) error {
	e := s.e
	e.mu.Lock()
	var r *record
	var err error
	if internal {
		r, err = s.owned()
	} else {
		r, err = s.get()
	}
	if err != nil {
		e.mu.Unlock()
		return err
	}
	if r.Status == "stopped" {
		e.mu.Unlock()
		return nil
	}
	if r.Status == "starting" || r.Status == "stopping" {
		e.mu.Unlock()
		return computer.Fail(computer.Busy, "screen lifecycle operation in progress")
	}
	streams := e.cancelViews(r.Resource.ID, false)
	r.Status = "stopping"
	saveErr := e.save()
	a := e.active[r.Resource.ID]
	if a != nil && a.cancel != nil {
		a.cancel()
	}
	resource := r.Resource
	e.mu.Unlock()
	err = joinViews(ctx, streams)
	if err == nil {
		err = e.driver.Stop(ctx, resource)
	}
	if err == nil && a != nil {
		select {
		case <-a.done:
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		r.Status = "stopped"
		settle(r)
	} else {
		r.Status = "blocked"
	}
	return errors.Join(err, saveErr, e.save())
}

// Resolve reconstructs a managed handle, never a native MCP connection.
func (e *Engine) Resolve(info computer.ScreenInfo) (computer.Screen, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := &screen{e, info}
	r, err := s.get()
	if err != nil {
		return nil, err
	}
	s.info.Status = r.Status
	return s, nil
}

// Close stops owned process ranges; a failed proof retains exclusive directory ownership.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closing = true
	var screens []*screen
	var starts []*active
	for _, r := range e.state.Screens {
		if !r.Retired {
			screens = append(screens, e.screen(r))
			if r.Status == "starting" && e.active[r.Resource.ID] != nil {
				starts = append(starts, e.active[r.Resource.ID])
			}
		}
	}
	e.mu.Unlock()
	for _, a := range starts {
		select {
		case <-a.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var errs []error
	for _, s := range screens {
		if err := s.stop(ctx, true); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.writer.Close(ctx); err != nil {
		return err
	}
	if err := e.unlock(); err != nil {
		return err
	}
	e.closed = true
	return nil
}

var _ computer.Provider = (*Engine)(nil)
