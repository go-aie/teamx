package room

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/computer"
)

// A capability is a Session fact, separate from both the model and the mutable
// member preference. Transport credentials and screen epochs are not capabilities.
type computerCapability struct {
	Enabled        bool   `json:"enabled"`
	ComputerID     string `json:"computer_id,omitempty"`
	Provider       string `json:"provider,omitempty"`
	InstanceID     string `json:"instance_id,omitempty"`
	ContractDigest string `json:"tool_contract_digest,omitempty"`
	Images         bool   `json:"images,omitempty"`
}

func (c computerCapability) digest() string {
	if !c.Enabled {
		return ""
	}
	return computer.Hash(struct {
		Capability computerCapability
		Management json.RawMessage
	}{c, computerStatusDefinition})
}
func computerSnapshotPath(opts SessionOptions, generation string) string {
	return filepath.Join(filepath.Dir(opts.StatePath), generation+".computer.json")
}
func loadComputerCapability(opts SessionOptions, generation string) (computerCapability, error) {
	var c computerCapability
	err := readSnapshot(computerSnapshotPath(opts, generation), &c, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	return c, err
}

type computerFence struct {
	MemberIdentity
	// Only unsettled execution scopes, never parameters, images or native results.
	Scopes []computer.Scope `json:"scopes"`
}

func checkComputerFence(path, generation string) error {
	var f computerFence
	err := readSnapshot(path, &f, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if validateMemberHeader(formatVersion, f.MemberIdentity) != nil || filepath.Base(filepath.Dir(path)) != f.MembershipID {
		return ErrIdentity
	}
	for _, s := range f.Scopes {
		if generation == "" || s.Session == generation {
			return &pendingBackendClose{computer.Fail(computer.Unknown, "Computer execution settlement is pending")}
		}
	}
	return nil
}
func checkComputerSettled(opts SessionOptions, generation string) error {
	return checkComputerFence(filepath.Join(filepath.Dir(opts.StatePath), "computer-execution.json"), generation)
}

type computerResource struct {
	AdmissionClosed bool `json:"admission_closed,omitempty"`
	Retired         bool `json:"retired_pending_cleanup,omitempty"`
	Rebuilt         bool `json:"rebuilt,omitempty"`
	MemberIdentity
	Directory string              `json:"directory"` // service-owned member state directory
	Config    computerConfig      `json:"connection"`
	Screen    computer.ScreenInfo `json:"screen"`
	Unknown   bool                `json:"unknown,omitempty"`
}
type computerResources struct {
	Version int                `json:"version"`
	Members []computerResource `json:"members"`
}
type computerMemberExecution struct {
	mu         sync.Mutex // admission and local facts only; never held over provider IO
	settle     sync.Mutex
	resource   computerResource
	screen     computer.Screen
	scopes     []computer.Scope
	active     map[computer.Scope]int
	done       chan struct{}
	blocked    bool
	humanUntil time.Time
}
type computerMembers struct {
	mu         sync.Mutex
	connection *computerController
	manager    *roomManager
	service    *service // standalone service composition
	path       string
	records    computerResources
	members    map[MemberIdentity]*computerMemberExecution
	host       *computer.Service
	changes    map[string]memberComputerChange
}

func newComputerMembers(c *computerController, m *roomManager, svc *service) (*computerMembers, error) {
	x := &computerMembers{connection: c, manager: m, service: svc, path: filepath.Join(filepath.Dir(c.path), "members.json"), records: computerResources{Version: 1}, members: map[MemberIdentity]*computerMemberExecution{}}
	if err := readSnapshot(x.path, &x.records, 4<<20); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if x.records.Version != 1 || len(x.records.Members) > 4096 {
		return nil, ErrInvalid
	}
	for _, r := range x.records.Members {
		if validateMemberHeader(formatVersion, r.MemberIdentity) != nil || !filepath.IsAbs(r.Directory) || r.Screen.Binding.Member != r.MemberIdentity || r.Screen.Binding.Owner != c.owner || x.members[r.MemberIdentity] != nil {
			return nil, ErrInvalid
		}
		e := &computerMemberExecution{resource: r, active: map[computer.Scope]int{}, done: make(chan struct{}), blocked: true}
		var f computerFence
		if err := readSnapshot(filepath.Join(r.Directory, "computer-execution.json"), &f, maxPrivateFileBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		if len(f.Scopes) > 0 && f.MemberIdentity != r.MemberIdentity {
			return nil, ErrIdentity
		}
		e.scopes = f.Scopes
		x.members[r.MemberIdentity] = e
	}
	if err := x.loadChanges(); err != nil {
		return nil, err
	}
	host, err := computer.OpenService(filepath.Join(filepath.Dir(c.path), "operations"))
	if err != nil {
		return nil, err
	}
	x.host = host
	return x, nil
}
func (x *computerMembers) persistResource(e *computerMemberExecution) error {
	// Caller holds e.mu. The persisted inventory is immutable during each save.
	x.mu.Lock()
	defer x.mu.Unlock()
	found := false
	for i, r := range x.records.Members {
		if r.MemberIdentity == e.resource.MemberIdentity {
			x.records.Members[i] = e.resource
			found = true
		}
	}
	if !found {
		x.records.Members = append(x.records.Members, e.resource)
	}
	return x.connection.writer.Save(x.path, x.records, 4<<20)
}
func (x *computerMembers) entry(id MemberIdentity) *computerMemberExecution {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.members[id]
}
func (x *computerMembers) blocked(id MemberIdentity) bool {
	if x == nil {
		return false
	}
	if x.memberPending(id) {
		return true
	}
	e := x.entry(id)
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	x.connection.mu.Lock()
	applying := x.connection.pendingLocked() != nil
	x.connection.mu.Unlock()
	return e.blocked || e.resource.Retired || applying && e.resource.Screen.Status != "stopped"
}
func (s *service) computerMembers() *computerMembers {
	if s.settings == nil || s.settings.computer == nil {
		return nil
	}
	return s.settings.computer.members
}
func (s *service) computerReady(id MemberIdentity) bool { return !s.computerMembers().blocked(id) }
func (b dshBackend) computerMembers() *computerMembers {
	if b.settings == nil || b.settings.computer == nil {
		return nil
	}
	return b.settings.computer.members
}

// Managed rooms own preferences in their frozen member records. Standalone
// rooms and subsequently added memberships use the same fields in a private
// member record, without editing model or role configuration.
type computerMemberPreferences struct {
	MemberIdentity
	Enabled         bool   `json:"computer_enabled"`
	SettingsVersion uint64 `json:"computer_settings_version"`
}

func computerPreferencesPath(dir string, id MemberIdentity) string {
	return filepath.Join(dir, "agents", id.MembershipID, "computer-settings.json")
}
func (x *computerMembers) preference(id MemberIdentity, dir string) (managedMember, error) {
	if dir == "" || validateMemberHeader(formatVersion, id) != nil {
		return managedMember{}, ErrIdentity
	}
	state, err := readPublishedRoom(dir, id.RoomIdentity)
	if err != nil {
		return managedMember{}, err
	}
	var live *MemberState
	for i := range state.Members {
		m := &state.Members[i]
		if m.AgentID == id.AgentID && m.MembershipID == id.MembershipID {
			live = m
			break
		}
	}
	if live == nil {
		return managedMember{}, ErrIdentity
	}
	var saved managedRoomConfig
	err = readSnapshot(filepath.Join(dir, "room-config.json"), &saved, maxPrivateFileBytes)
	if err == nil {
		if saved.RoomIdentity != id.RoomIdentity {
			return managedMember{}, ErrIdentity
		}
		for _, m := range saved.Members {
			if m.AgentID == id.AgentID && m.MembershipID == id.MembershipID {
				return m, nil
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return managedMember{}, err
	}
	var pref computerMemberPreferences
	err = readSnapshot(computerPreferencesPath(dir, id), &pref, maxPrivateFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		return managedMember{MemberState: *live}, nil
	}
	if err != nil {
		return managedMember{}, err
	}
	if pref.MemberIdentity != id {
		return managedMember{}, ErrIdentity
	}
	return managedMember{MemberState: *live, ComputerEnabled: pref.Enabled, ComputerSettingsVersion: pref.SettingsVersion}, nil
}
func (x *computerMembers) roomPreferences(dir string, id RoomIdentity) ([]managedMember, error) {
	state, err := readPublishedRoom(dir, id)
	if err != nil {
		return nil, err
	}
	members := make([]managedMember, 0, len(state.Members))
	for _, live := range state.Members {
		member, err := x.preference(MemberIdentity{RoomIdentity: id, AgentID: live.AgentID, MembershipID: live.MembershipID}, dir)
		if err != nil {
			return nil, err
		}
		members = append(members, member)
	}
	return members, nil
}

func (x *computerMembers) desired(id MemberIdentity, dir string, images bool) (computerCapability, error) {
	if x == nil {
		return computerCapability{}, nil
	}
	c := x.connection.current.Load()
	if !c.Enabled {
		return computerCapability{}, nil
	}
	m, err := x.preference(id, dir)
	if err != nil {
		return computerCapability{}, err
	}
	if !m.ComputerEnabled {
		return computerCapability{}, nil
	}
	return computerCapability{true, x.connection.owner.ComputerID, c.Provider, c.InstanceID, c.ContractDigest, images}, nil
}
func (x *computerMembers) prepare(ctx context.Context, opts SessionOptions, generation string, images, recover bool) (computerCapability, error) {
	if x == nil {
		return loadComputerCapability(opts, generation)
	}
	if x.blocked(opts.MemberIdentity) {
		return computerCapability{}, computer.Fail(computer.Unknown, "Computer recovery or capability application is pending")
	}
	desired, err := x.desired(opts.MemberIdentity, filepath.Dir(filepath.Dir(filepath.Dir(opts.StatePath))), images)
	if err != nil {
		return desired, err
	}
	if recover {
		frozen, err := loadComputerCapability(opts, generation)
		if err != nil {
			return frozen, err
		}
		if frozen != desired {
			return frozen, computer.Fail(computer.ContractMismatch, "Computer capability requires settled Session rotation")
		}
		return frozen, nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.StatePath), 0700); err != nil {
		return desired, err
	}
	return desired, x.connection.writer.Save(computerSnapshotPath(opts, generation), desired, maxPrivateFileBytes)
}
func (x *computerMembers) screen(ctx context.Context, id MemberIdentity, dir string) (*computerMemberExecution, error) {
	e := x.entry(id)
	if e != nil {
		e.mu.Lock()
		if e.resource.Retired {
			e.mu.Unlock()
			return nil, computer.Fail(computer.Stale, "Member screen is retired")
		}
		s := e.screen
		e.mu.Unlock()
		if s != nil {
			return e, nil
		}
	}
	// Use the same connection/capacity entrance as controlled operations. Claims
	// are CAS handoffs, serialized with screen restoration and global application.
	c := x.connection
	c.memberEnsure.Lock()
	defer c.memberEnsure.Unlock()
	e = x.entry(id)
	if e != nil {
		e.mu.Lock()
		if e.resource.Retired {
			e.mu.Unlock()
			return nil, computer.Fail(computer.Stale, "Member screen is retired")
		}
		s := e.screen
		e.mu.Unlock()
		if s != nil {
			return e, nil
		}
	}
	preference, err := x.preference(id, filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		return nil, err
	}
	if !preference.ComputerEnabled {
		return nil, computer.Refuse(computer.Disabled, "Member Computer is disabled")
	}
	cfg := *c.current.Load()
	p, info, err := c.checkTarget(ctx, cfg)
	if err != nil {
		return nil, err
	}
	serviceGeneration := c.runtimeService(cfg, info)
	lease, err := p.Claim(ctx, computer.Claim{Owner: c.owner, Service: serviceGeneration, ExpectedLease: info.Lease})
	if err != nil {
		return nil, err
	}
	binding := computer.Binding{Owner: c.owner, Member: id, Service: serviceGeneration, Lease: lease, Admission: computer.AdmissionVersion{Connection: cfg.Version, Member: preference.ComputerSettingsVersion}}
	if err = p.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: binding, Enabled: true}); err != nil {
		return nil, err
	}
	screen, err := c.ensureScreen(ctx, binding)
	if err != nil {
		return nil, err
	}
	if e == nil {
		e = &computerMemberExecution{active: map[computer.Scope]int{}, done: make(chan struct{})}
		x.mu.Lock()
		x.members[id] = e
		x.mu.Unlock()
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resource = computerResource{MemberIdentity: id, Directory: dir, Config: cfg, Screen: screen.Info(), Unknown: e.resource.Unknown, Rebuilt: e.resource.Screen.Epoch > 0 && e.resource.Screen.Epoch != screen.Info().Epoch}
	e.screen = screen
	if err = x.persistResource(e); err != nil {
		e.blocked = true
		return nil, err
	}
	return e, nil
}
func (x *computerMembers) beginTurn(ctx context.Context, opts SessionOptions, generation string, turn uint64, cap computerCapability) error {
	if x == nil || !cap.Enabled {
		return nil
	}
	if x.blocked(opts.MemberIdentity) {
		return computer.Fail(computer.Unknown, "Computer recovery is pending")
	}
	desired, err := x.desired(opts.MemberIdentity, filepath.Dir(filepath.Dir(filepath.Dir(opts.StatePath))), cap.Images)
	if err != nil {
		return err
	}
	if desired != cap {
		return computer.Fail(computer.ContractMismatch, "Computer capability changed; rotate Session")
	}
	e, err := x.screen(ctx, opts.MemberIdentity, filepath.Dir(opts.StatePath))
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.blocked {
		return computer.Fail(computer.Unknown, "Computer screen is blocked")
	}
	scope := computer.Scope{Session: generation, Turn: strconv.FormatUint(turn, 10)}
	if slices.Contains(e.scopes, scope) {
		return computer.Fail(computer.Unknown, "Previous Computer turn has not settled")
	}
	e.scopes = append(e.scopes, scope)
	if err = x.saveFence(e); err != nil {
		e.blocked = true
	}
	return err
}
func (x *computerMembers) saveFence(e *computerMemberExecution) error {
	return x.connection.writer.Save(filepath.Join(e.resource.Directory, "computer-execution.json"), computerFence{e.resource.MemberIdentity, append([]computer.Scope(nil), e.scopes...)}, maxPrivateFileBytes)
}
func (x *computerMembers) call(ctx context.Context, id MemberIdentity, scope computer.Scope, cap computerCapability, callID, tool string, args json.RawMessage) (json.RawMessage, error) {
	if x == nil || !cap.Enabled {
		return nil, computer.Fail(computer.Disabled, "Computer is disabled")
	}
	if x.memberPending(id) {
		return nil, computer.Fail(computer.Disabled, "Computer capability is applying")
	}
	e := x.entry(id)
	if e == nil {
		return nil, computer.Fail(computer.Unavailable, "Computer turn is not prepared")
	}
	e.mu.Lock()
	if time.Now().Before(e.humanUntil) {
		e.mu.Unlock()
		return nil, computer.Refuse(computer.HumanControl, "Human takeover is pending or active. Finish the turn; do not retry this action.")
	}
	if e.screen == nil && !e.blocked && !e.resource.Retired && slices.Contains(e.scopes, scope) {
		directory := e.resource.Directory
		e.mu.Unlock()
		if _, err := x.screen(ctx, id, directory); err != nil {
			return nil, err
		}
		e.mu.Lock()
	}
	if x.memberPending(id) {
		e.mu.Unlock()
		return nil, computer.Fail(computer.Disabled, "Computer capability is applying")
	}
	if e.blocked || e.resource.Retired || !slices.Contains(e.scopes, scope) || e.screen == nil {
		e.mu.Unlock()
		return nil, computer.Fail(computer.Stale, "Computer execution scope is closed")
	}
	if e.active[scope] > 0 {
		e.mu.Unlock()
		return nil, computer.Fail(computer.Busy, "Computer screen has an active call")
	}
	// All scopes of one member share one admission gate.
	for _, n := range e.active {
		if n > 0 {
			e.mu.Unlock()
			return nil, computer.Fail(computer.Busy, "Computer screen is busy")
		}
	}
	screen := e.screen
	e.active[scope]++
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.active[scope]--; close(e.done); e.done = make(chan struct{}); e.mu.Unlock() }()
	contract, err := screen.Tools(ctx)
	if err != nil {
		return nil, err
	}
	t, ok := contract.Tool(tool)
	if !ok || contract.Digest != cap.ContractDigest || t.RequiresImages && !cap.Images {
		return nil, computer.Fail(computer.ContractMismatch, "Tool is outside the frozen Computer capability")
	}
	call := computer.Call{ID: computer.Hash(struct {
		Member        MemberIdentity
		Service, Call string
		Scope         computer.Scope
	}{id, x.connection.service.ServiceEpoch, callID, scope}), Scope: scope, Tool: tool, Arguments: args, ContractDigest: cap.ContractDigest}
	out, err := x.host.Call(ctx, screen, call)
	if err != nil {
		return nil, err
	}
	if !out.ResultAvailable {
		return nil, computer.Fail(computer.Unknown, "Operation was accepted; native result unavailable. Observe current state; do not repeat the action.")
	}
	if !json.Valid(out.Result) || len(out.Result) > computer.ResponseLimit-1024 {
		return nil, computer.Fail(computer.Invalid, "Native result exceeds the supported response contract")
	}
	return out.Result, nil
}

// Settlement never infers remote completion from HTTP cancellation or local
// process exit. A failed Drain requires a verified screen stop; unknown website
// effects remain unknown, even when execution has been proven stopped.
func (x *computerMembers) settleScope(ctx context.Context, id MemberIdentity, scope computer.Scope, stop bool) error {
	if x == nil {
		return nil
	}
	e := x.entry(id)
	if e == nil {
		return nil
	}
	e.settle.Lock()
	defer e.settle.Unlock()
	e.mu.Lock()
	e.blocked = true
	var matching []computer.Scope
	for _, s := range e.scopes {
		if scope.Matches(s) {
			matching = append(matching, s)
		}
	}
	screen := e.screen
	r := e.resource
	e.mu.Unlock()
	if len(matching) == 0 && (!stop || r.Screen.Status == "stopped") {
		e.mu.Lock()
		e.blocked = false
		e.mu.Unlock()
		return nil
	}
	if screen == nil {
		p, info, err := x.connection.inspect(ctx, r.Config)
		if err != nil {
			return err
		}
		if info.InstanceID != r.Config.InstanceID {
			return computer.Fail(computer.Stale, "Original Computer instance unavailable")
		}
		inventory, err := p.Screens(ctx, x.connection.owner)
		if err != nil {
			return err
		}
		found := false
		for _, s := range inventory {
			if s.ID == r.Screen.ID && s.Binding.Key() == r.Screen.Binding.Key() {
				r.Screen = s
				found = true
			}
		}
		if !found {
			return computer.Fail(computer.Stale, "Original screen is missing")
		}
		// Startup claims fence every earlier host service before restoration.
		serviceGeneration := x.connection.runtimeService(r.Config, info)
		lease, err := p.Claim(ctx, computer.Claim{Owner: x.connection.owner, Service: serviceGeneration, ExpectedLease: info.Lease})
		if err != nil {
			return err
		}
		// Rebind only management authority. Restore must never create or restart
		// processes while reconciling a disabled or unknown execution.
		r.Screen.Binding.Service = serviceGeneration
		r.Screen.Binding.Lease = lease
		screen = p.Restore(r.Screen)
	}
	var err error
	if scope.Session == "" && !stop {
		for _, specific := range matching {
			if err = screen.Drain(ctx, specific); err != nil {
				break
			}
		}
	} else {
		err = screen.Drain(ctx, scope)
	}
	unknown := false
	if err != nil || stop {
		unknown = err != nil
		if stopErr := screen.Stop(ctx); stopErr != nil {
			return errors.Join(err, stopErr)
		}
	}
	if unknown {
		if err := x.rememberUnknown(e, screen.Info()); err != nil {
			return err
		}
	}
	for _, p := range x.host.Pending() {
		if p.Screen.ID != screen.Info().ID || !scope.Matches(p.Scope) {
			continue
		}
		fact, err := screen.Operation(ctx, p.ID)
		if computer.Is(err, computer.Unknown) {
			if stopErr := screen.Stop(ctx); stopErr != nil {
				return errors.Join(err, stopErr)
			}
			unknown = true
			if err = x.rememberUnknown(e, screen.Info()); err != nil {
				return err
			}
			if err = x.host.SettleStopped(screen, scope); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if fact.Operation.State == "unknown" {
			unknown = true
			if err = x.rememberUnknown(e, screen.Info()); err != nil {
				return err
			}
		}
		if _, err = x.host.Reconcile(ctx, screen, p.ID); err != nil {
			return err
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.resource.Screen = screen.Info()
	if stop || unknown {
		e.resource.Screen.Status = "stopped"
	}
	e.resource.Unknown = e.resource.Unknown || unknown
	e.scopes = slices.DeleteFunc(e.scopes, func(s computer.Scope) bool { return scope.Matches(s) })
	// Joining host handlers is independent of the remote execution proof.
	for {
		busy := false
		for s, n := range e.active {
			if scope.Matches(s) && n > 0 {
				busy = true
			}
		}
		if !busy {
			break
		}
		done := e.done
		e.mu.Unlock()
		select {
		case <-ctx.Done():
			e.mu.Lock()
			return ctx.Err()
		case <-done:
		}
		e.mu.Lock()
	}
	if err := x.saveFence(e); err != nil {
		return err
	}
	if err := x.persistResource(e); err != nil {
		return err
	}
	e.screen = nil // next use revalidates current transport and epoch
	e.blocked = false
	return nil
}
func (x *computerMembers) finish(id MemberIdentity, generation string, turn uint64) error {
	if x == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return x.settleScope(ctx, id, computer.Scope{Session: generation, Turn: strconv.FormatUint(turn, 10)}, false)
}
func (x *computerMembers) recover(ctx context.Context) {
	x.mu.Lock()
	ids := make([]MemberIdentity, 0, len(x.members))
	for id := range x.members {
		ids = append(ids, id)
	}
	x.mu.Unlock()
	var workers sync.WaitGroup
	slots := make(chan struct{}, 8)
	for _, id := range ids {
		e := x.entry(id)
		e.mu.Lock()
		needed := e.blocked
		e.mu.Unlock()
		if !needed {
			continue
		}
		workers.Add(1)
		go func(id MemberIdentity) {
			defer workers.Done()
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slots }()
			wait, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()
			_ = x.settleScope(wait, id, computer.Scope{}, false)
		}(id)
	}
	workers.Wait()
}

func (x *computerMembers) refreshConnection(config computerConfig) error {
	x.mu.Lock()
	var entries []*computerMemberExecution
	for _, e := range x.members {
		entries = append(entries, e)
	}
	x.mu.Unlock()
	for _, e := range entries {
		e.mu.Lock()
		if e.resource.Config.InstanceID == config.InstanceID && e.resource.Config != config {
			e.resource.Config = config
			e.screen = nil // the prior immutable transport is for reconciliation only
			if err := x.persistResource(e); err != nil {
				e.blocked = true
				e.mu.Unlock()
				return err
			}
		}
		e.mu.Unlock()
	}
	return nil
}

func (x *computerMembers) rememberUnknown(e *computerMemberExecution, screen computer.ScreenInfo) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resource.Unknown = true
	e.resource.Screen = screen
	e.resource.Screen.Status = "stopped"
	return x.persistResource(e)
}
func (x *computerMembers) turnNotice(id MemberIdentity) string {
	if x == nil {
		return ""
	}
	e := x.entry(id)
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	notice := ""
	if e.resource.Rebuilt {
		notice += "\nComputer screen was rebuilt. Discard old element handles and observe the current page or desktop before acting."
	}
	if e.resource.Unknown {
		notice += "\nAn earlier Computer action has an unknown website outcome. Execution has stopped; that is not proof of business success. Inspect current state and do not repeat that action without resolving its outcome."
	}
	return notice
}

func (x *computerMembers) retire(ctx context.Context, id MemberIdentity) error {
	x.connection.memberEnsure.Lock()
	defer x.connection.memberEnsure.Unlock()
	if err := x.closeAdmission(ctx, id, computerConfig{}, 0, 0, true); err != nil {
		return err
	}
	if err := x.settleScope(ctx, id, computer.Scope{}, true); err != nil {
		return err
	}
	e := x.entry(id)
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.resource.Retired = true
	return x.persistResource(e)
}

// Display-only local projection, deliberately computed outside connection.mu.
// It neither constructs a provider nor supplies an authorization decision.
func (x *computerMembers) enabledCount() int {
	type reference struct {
		id  RoomIdentity
		dir string
	}
	var rooms []reference
	if x.manager != nil {
		x.manager.mu.Lock()
		for _, r := range x.manager.index.Rooms {
			if r.Status == "ready" {
				rooms = append(rooms, reference{r.RoomIdentity, x.manager.roomDir(r.RoomID)})
			}
		}
		x.manager.mu.Unlock()
	} else if x.service != nil {
		rooms = append(rooms, reference{x.service.info.RoomIdentity, x.service.store.dir})
	}
	count := 0
	for _, r := range rooms {
		members, err := x.roomPreferences(r.dir, r.id)
		if err != nil {
			continue
		}
		for _, m := range members {
			if m.ComputerEnabled {
				count++
			}
		}
	}

	return count
}

// Close remote admission before inventory. An acknowledged policy change and
// Runtime's serialized start records prove that no older request can arrive
// after an empty inventory and create an untracked desktop.
func (x *computerMembers) closeAdmission(ctx context.Context, id MemberIdentity, source computerConfig, connectionVersion, memberVersion uint64, retired bool) error {
	e := x.entry(id)
	current := *x.connection.current.Load()
	if source.Version == 0 {
		source = current
	}
	differentInstance := false
	if e != nil {
		e.mu.Lock()
		r, scopes := e.resource, len(e.scopes)
		e.mu.Unlock()
		if !source.Enabled {
			if r.AdmissionClosed {
				return nil
			}
			source = r.Config
		}
		differentInstance = r.Config.InstanceID != source.InstanceID
		if differentInstance && (!r.AdmissionClosed || r.Screen.Status != "stopped" || scopes != 0) {
			return computer.Fail(computer.Busy, "Previous Computer execution must settle first")
		}
	} else if !source.Enabled {
		return nil
	}
	if retired && e == nil {
		used := false
		x.mu.Lock()
		for _, op := range x.changes {
			if op.MemberIdentity == id && op.Enabled {
				used = true
				break
			}
		}
		x.mu.Unlock()
		if !used {
			if pref, err := x.preference(id, x.memberDirectory(id)); err == nil {
				used = pref.ComputerEnabled
			}
		}
		if !used {
			return nil
		}
	}
	if connectionVersion == 0 {
		connectionVersion = current.Version
	}
	if memberVersion == 0 {
		if pref, err := x.preference(id, x.memberDirectory(id)); err == nil {
			memberVersion = pref.ComputerSettingsVersion
		}
	}
	provider, info, err := x.connection.inspect(ctx, source)
	if err != nil {
		return err
	}
	if info.InstanceID != source.InstanceID {
		return computer.Fail(computer.Stale, "Original Computer instance unavailable")
	}
	serviceGeneration := x.connection.runtimeService(source, info)
	lease, err := provider.Claim(ctx, computer.Claim{Owner: x.connection.owner, Service: serviceGeneration, ExpectedLease: info.Lease})
	if err != nil {
		return err
	}
	binding := computer.Binding{Owner: x.connection.owner, Member: id, Service: serviceGeneration, Lease: lease, Admission: computer.AdmissionVersion{Connection: connectionVersion, Member: memberVersion}}
	if err = provider.SetMemberAdmission(ctx, computer.MemberAdmission{Binding: binding, Retired: retired}); err != nil {
		return err
	}
	if e == nil || differentInstance {
		inventory, err := provider.Screens(ctx, x.connection.owner)
		if err != nil {
			return err
		}
		for _, screen := range inventory {
			if screen.Binding.Member != id {
				continue
			}
			directory := filepath.Join(x.memberDirectory(id), "agents", id.MembershipID)
			if err = os.MkdirAll(directory, 0700); err != nil {
				return err
			}
			if e == nil {
				e = &computerMemberExecution{resource: computerResource{MemberIdentity: id, Directory: directory, Config: source, Screen: screen}, active: map[computer.Scope]int{}, done: make(chan struct{}), blocked: true}
				x.mu.Lock()
				x.members[id] = e
				x.mu.Unlock()
			} else {
				e.mu.Lock()
				e.resource.Config = source
				e.resource.Screen = screen
				e.resource.Rebuilt = true
				e.screen = nil
				e.blocked = true
				e.mu.Unlock()
			}
			break
		}
	}
	if e != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.resource.AdmissionClosed = true
		return x.persistResource(e)
	}
	return nil
}
