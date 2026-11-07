package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/connection"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

const maxRooms = 1000
const maxRoomsFileBytes int64 = 64 << 20

// Management identity has no implicit Room. Room tools must continue to carry
// their full identity; they cannot target this catalog.
type roomsResponse struct {
	Busy         bool                 `json:"busy"`
	ServiceEpoch string               `json:"service_epoch"`
	WriteError   string               `json:"write_error,omitempty"`
	Rooms        []roomRuntimeSummary `json:"rooms"`
	Operator     string               `json:"operator"`
	Teams        []teamSummary        `json:"teams"`
}

type roomSummary struct {
	TeamID string `json:"team_id,omitempty"`
	RoomIdentity
	Name          string     `json:"name"`
	CreateID      string     `json:"create_id"`
	Status        string     `json:"status"` // creating, ready or deleting
	DeletionPhase string     `json:"deletion_phase,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	LastMessageAt *time.Time `json:"last_message_at,omitempty"`
	Error         string     `json:"error,omitempty"`
}

// Runtime observations belong only to the HTTP catalog, never rooms.json.
type roomRuntimeSummary struct {
	roomSummary
	Busy          bool                `json:"busy"`
	Members       []teamMemberSummary `json:"members,omitempty"`
	RuntimeStatus string              `json:"runtime_status"`
	Service       *ServiceInfo        `json:"service,omitempty"`
	RuntimeError  string              `json:"runtime_error,omitempty"`
}

type roomSelection struct {
	TeamID   string `json:"team_id,omitempty"`
	CreateID string `json:"create_id"`
	Name     string `json:"name,omitempty"`
}

type roomCreateRequest struct {
	ServiceEpoch string `json:"service_epoch"`
	Caller       Caller `json:"caller"`
	roomSelection
}

// Freeze member roles and runtime configuration. Inherited credentials stay outside room data.
type managedMember struct {
	MemberState
	Name         string            `json:"name"`
	Instructions string            `json:"instructions"`
	ToolEnv      map[string]string `json:"tool_env,omitempty"`
	Workspace    string            `json:"workspace"` // relative to this Room
}

type managedRoomConfig struct {
	PlatformToolsVersion int              `json:"platform_tools_version,omitempty"`
	Workspace            *workspaceConfig `json:"workspace"`
	WorkspaceVersion     uint64           `json:"workspace_version"`
	TeamID               string           `json:"team_id,omitempty"`
	Runtime              *teamRuntime     `json:"runtime"`
	Version              int              `json:"version"`
	RoomIdentity
	Name           string          `json:"name"`
	OpeningMessage string          `json:"opening_message"`
	MaxTurns       uint64          `json:"max_turns"`
	Members        []managedMember `json:"members"`
}

type roomResource struct {
	Path       string `json:"path"`
	Executable bool   `json:"executable,omitempty"`
	Data       []byte `json:"data"`
}

type roomInitialization struct {
	Config    managedRoomConfig `json:"config"`
	Resources []roomResource    `json:"resources"`
}

type roomIndexEntry struct {
	roomSummary
	Selection      roomSelection       `json:"selection"`
	Initialization *roomInitialization `json:"initialization,omitempty"`
}

type roomsIndex struct {
	OwnedRooms          []RoomIdentity       `json:"owned_rooms,omitempty"`
	Opens               []roomOpen           `json:"opens,omitempty"`
	Shutdowns           []roomShutdown       `json:"shutdowns,omitempty"`
	Version             int                  `json:"version"`
	Rooms               []roomIndexEntry     `json:"rooms"`
	WorkspaceOperations []workspaceOperation `json:"workspace_operations,omitempty"`
}

// The root lock owns the catalog and its live services. Durable
// operations retain an owner independently of an HTTP caller.
type roomManager struct {
	toolCatalog           []tool.Definition
	platformCalls         map[*platformCall]bool
	platformMemberChanges map[MemberIdentity]bool
	connections           *connection.Manager
	oauthFlights          map[string]*oauthRefreshFlight
	oauthLeases           map[string]map[*gitlabTokenLease]bool
	larkAuthSessions      map[string]*larkAuthSession
	gitlabAuthSessions    map[string]*gitlabAuthSession
	connectionProviders   map[string]connectionProvider
	referenceAdapters     map[string]referenceAdapterFactory
	referenceResolvers    map[string]followupResolver
	referenceExecutors    map[string]referenceActionPreparer
	platformProject       func(context.Context, string, string, workspaceSecret) (int64, error)
	notifications         *notification.Manager
	notificationAdapters  map[notification.SourceID]notificationAdapter
	platformHTTP          http.RoundTripper // offline built-in HTTP seam; never used by agent tools
	workspaceRun          platform.Execute  // offline subprocess seam; no public configuration hook
	teams                 map[string]teamDefinition
	teamsDir              string
	mu                    sync.Mutex
	shutdownGate          sync.Mutex
	store                 *Store // reuse atomic snapshot replacement and commit-unknown latch
	epoch                 string
	index                 roomsIndex
	maxTurns              uint64
	config                serviceConfig
	factory               func(string, string, serviceConfig) (*service, error)
	runtimes              map[RoomIdentity]*roomRuntime
	busy                  bool
	closing               bool
	operationDone         chan struct{}
	recoveryStarted       bool
}

func newRoomManager(dir string, maxTurns uint64) (_ *roomManager, err error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := makeDurableDir(abs); err != nil {
		return nil, err
	}
	lock, err := lockDirectory(abs)
	if err != nil {
		return nil, err
	}
	store := &Store{dir: abs, lock: lock, ops: defaultSnapshotOps()}
	defer func() {
		if err != nil {
			_ = store.Close()
		}
	}()
	epoch, err := newOpaqueID()
	if err != nil {
		return nil, err
	}
	if maxTurns == 0 {
		maxTurns = defaultMaxTurns
	}
	m := &roomManager{store: store, epoch: epoch, maxTurns: maxTurns, factory: newService, runtimes: make(map[RoomIdentity]*roomRuntime)}
	m.initPlatform()
	err = readSnapshot(m.indexPath(), &m.index, maxRoomsFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		m.index = roomsIndex{Version: roomsIndexVersion, Rooms: []roomIndexEntry{}}
		err = m.saveIndex(m.index)
	}
	if err != nil {
		return nil, err
	}
	if err := m.validateIndex(); err != nil {
		return nil, fmt.Errorf("%w: rooms.json: %w", ErrInvalid, err)
	}
	for i, op := range m.index.Opens {
		if op.Status == "running" {
			next := m.copyIndex()
			next.Opens[i].Status, next.Opens[i].Error = "failed", "service stopped during open; inspect and explicitly retry"
			if err := m.saveIndex(next); err != nil {
				return nil, err
			}
		}
	}
	for i, op := range m.index.WorkspaceOperations {
		if op.Status == "running" {
			next := m.copyIndex()
			next.WorkspaceOperations[i].Status = "failed"
			next.WorkspaceOperations[i].ErrorCode = "interrupted"
			next.WorkspaceOperations[i].UpdatedAt = time.Now().UTC()
			if err := m.saveIndex(next); err != nil {
				return nil, err
			}
		}
	}
	if _, err := m.readPlatformConnections(); err != nil {
		return nil, err
	}
	for _, entry := range m.index.Rooms {
		if entry.Status == "ready" {
			if _, err := m.readToolBindingsLocked(entry.RoomIdentity); err != nil {
				return nil, err
			}
		}
	}
	if err := m.loadPlatformNotificationsLocked(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *roomManager) indexPath() string        { return filepath.Join(m.store.dir, "rooms.json") }
func (m *roomManager) roomDir(id string) string { return filepath.Join(m.store.dir, "rooms", id) }
func (m *roomManager) close() error {
	ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
	defer cancel()
	return m.shutdown(ctx)
}

func (m *roomManager) saveIndex(next roomsIndex) error {
	if err := m.store.check(true); err != nil {
		return err
	}
	if err := m.store.save(m.indexPath(), next, maxRoomsFileBytes); err != nil {
		return err
	}
	m.index = next
	return nil
}

func (m *roomManager) validateIndex() error {
	if m.index.Version != roomsIndexVersion {
		return ErrVersion
	}
	if m.index.Rooms == nil || len(m.index.Rooms) > maxRooms {
		return ErrInvalid
	}
	ids, instances, requests := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, e := range m.index.Rooms {
		if !safeMembershipID(e.RoomID) || !validText(e.RoomInstance) || ids[e.RoomID] || instances[e.RoomInstance] || requests[e.CreateID] ||
			!safeMembershipID(e.CreateID) || e.Selection.CreateID != e.CreateID || e.Selection.TeamID != e.TeamID ||
			!validText(e.Name) || len(e.Name) > 256 || !validRoomSource(e) ||
			e.CreatedAt.IsZero() || e.UpdatedAt.Before(e.CreatedAt) || (e.Status != "deleting" && e.DeletionPhase != "") {
			return ErrInvalid
		}
		ids[e.RoomID], instances[e.RoomInstance], requests[e.CreateID] = true, true, true
		switch e.Status {
		case "creating":
			if e.Initialization == nil {
				return ErrInvalid
			}
			if err := validateInitialization(e, *e.Initialization); err != nil {
				return err
			}
		case "ready":
			if e.Initialization != nil || e.Error != "" {
				return ErrInvalid
			}
			var c managedRoomConfig
			if err := readSnapshot(filepath.Join(m.roomDir(e.RoomID), "room-config.json"), &c, maxPrivateFileBytes); err != nil {
				return err
			}
			if err := validateInitialization(e, roomInitialization{Config: c}); err != nil {
				return err
			}
		case "deleting":
			// Removal can be partial. Never read the Room config or replay a
			// deletion on startup; the operator must explicitly retry it.
			if e.Initialization != nil || (e.DeletionPhase != "closing" && e.DeletionPhase != "removing") {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	if err := m.validateRuntimesIndex(); err != nil {
		return err
	}
	return m.validateWorkspaceOperations()
}

func validateInitialization(e roomIndexEntry, init roomInitialization) error {
	c := init.Config
	encoded, err := json.Marshal(c)
	if err != nil || int64(len(encoded)+1) > maxPrivateFileBytes {
		return ErrCapacity
	}
	// The catalog owns the current display name. Creation selections and frozen
	// configs retain the original name so creation receipts remain idempotent.
	if c.Version != formatVersion || c.RoomIdentity != e.RoomIdentity || (e.Status == "creating" && c.Name != e.Name) ||
		(e.Selection.Name != "" && c.Name != e.Selection.Name) || c.TeamID != e.TeamID ||
		!validText(c.Name) || len(c.Name) > 256 || c.MaxTurns == 0 || !validText(c.OpeningMessage) || len(c.OpeningMessage) > maxTextBytes ||
		len(c.Members) == 0 || len(c.Members) > maxMembers {
		return ErrInvalid
	}
	if c.Runtime == nil {
		return fmt.Errorf("%w: missing runtime snapshot", ErrInvalid)
	}
	if err := validateWorkspaceSnapshot(c); err != nil {
		return err
	}
	if err := validateTeamRuntime(*c.Runtime); err != nil {
		return err
	}
	snapshot := RoomSnapshot{Version: formatVersion, RoomIdentity: c.RoomIdentity, Messages: []RoomMessage{}, Members: []MemberState{}}
	for _, member := range c.Members {
		if !validText(member.Name) || !validText(member.Instructions) || member.ReadThrough != 0 ||
			member.Runner != managedRunner(c, member.AgentID) {
			return ErrInvalid
		}
		if err := dsh.CheckToolEnv(member.ToolEnv, c.Runtime.Model.APIKeyEnv); err != nil {
			return err
		}
		for key := range member.ToolEnv {
			if isRoomToolEnv(key) || key == "TEAMX_CLI" {
				return ErrIdentity
			}
		}
		snapshot.Members = append(snapshot.Members, member.MemberState)
	}
	if err := snapshot.validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	total := 0
	if len(init.Resources) > 1024 {
		return ErrCapacity
	}
	for _, resource := range init.Resources {
		total += len(resource.Data)
		if !fs.ValidPath(resource.Path) || resource.Path == "." || strings.Contains(resource.Path, "\\") || seen[resource.Path] || total > 4<<20 {
			return ErrInvalid
		}
		seen[resource.Path] = true
	}
	return nil
}

// A repeated create_id must retain the same selection, even if the selected
// team was later removed. Ready Rooms never reload their template.
func (m *roomManager) create(selection roomSelection) (roomSummary, error) {
	selection.Name = strings.TrimSpace(selection.Name)
	if !safeMembershipID(selection.CreateID) || !validSelection(selection) || len(selection.Name) > 256 ||
		(selection.Name != "" && !validText(selection.Name)) {
		return roomSummary{}, ErrRequest
	}
	if err := m.store.check(true); err != nil {
		return roomSummary{}, err
	}
	at := -1
	for i, e := range m.index.Rooms {
		if e.CreateID != selection.CreateID {
			continue
		}
		if e.Selection != selection {
			return roomSummary{}, ErrConflict
		}
		if e.Status == "deleting" {
			return roomSummary{}, ErrConflict
		}
		if e.Status == "ready" {
			return e.roomSummary, nil
		}
		at = i
		break
	}
	if at == -1 {
		if len(m.index.Rooms) >= maxRooms {
			return roomSummary{}, ErrCapacity
		}
		e, err := m.prepare(selection)
		if err != nil {
			return roomSummary{}, err
		}
		next := m.index
		next.Rooms = append(append([]roomIndexEntry{}, next.Rooms...), e)
		if err := m.saveIndex(next); err != nil {
			return roomSummary{}, err
		}
		at = len(next.Rooms) - 1
	}
	e := m.index.Rooms[at]
	if err := m.initialize(e); err != nil {
		next := m.index
		next.Rooms = append([]roomIndexEntry{}, next.Rooms...)
		next.Rooms[at].Error, next.Rooms[at].UpdatedAt = err.Error(), time.Now().UTC()
		return e.roomSummary, errors.Join(err, m.saveIndex(next))
	}
	next := m.index
	next.Rooms = append([]roomIndexEntry{}, next.Rooms...)
	e.Status, e.Error, e.UpdatedAt, e.Initialization = "ready", "", time.Now().UTC(), nil
	next.Rooms[at] = e
	if err := m.saveIndex(next); err != nil {
		return roomSummary{}, err
	}
	return e.roomSummary, nil
}

func (m *roomManager) initialize(e roomIndexEntry) error {
	init := e.Initialization
	if init == nil {
		return ErrInvalid
	}
	dir := m.roomDir(e.RoomID)
	for _, member := range init.Config.Members {
		if err := checkWorkspaceDirectory(dir, member.Workspace); err != nil {
			return err
		}
	}
	store, err := OpenStore(dir, e.RoomID)
	if err != nil {
		return err
	}
	defer store.Close()
	state, loadErr := store.LoadRoom()
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	if loadErr == nil && state.RoomIdentity != e.RoomIdentity {
		return ErrIdentity
	}
	configPath := filepath.Join(dir, "room-config.json")
	// Check existing identity/configuration before touching any Room data.
	var existing managedRoomConfig
	configErr := readSnapshot(configPath, &existing, maxPrivateFileBytes)
	if configErr == nil {
		before, _ := json.Marshal(existing)
		reserved, _ := json.Marshal(init.Config)
		if !bytes.Equal(before, reserved) {
			return ErrIdentity
		}
	} else if !errors.Is(configErr, os.ErrNotExist) {
		return configErr
	} else {
		if loadErr == nil {
			return fmt.Errorf("%w: existing Room has no creation config", ErrInvalid)
		}
		if err := store.save(configPath, init.Config, maxPrivateFileBytes); err != nil {
			return err
		}
	}
	// Explicit retries use reserved identities and frozen resources. An existing
	// Room's history, cursors and execution evidence are never reinitialized.
	for _, resource := range init.Resources {
		path := filepath.Join(dir, "resources", filepath.FromSlash(resource.Path))
		if err := makeDurableDir(filepath.Dir(path)); err != nil {
			return err
		}
		mode := os.FileMode(0600)
		if resource.Executable {
			mode = 0700
		}
		if err := writeRoomResource(path, resource.Data, mode); err != nil {
			return err
		}
	}
	for _, member := range init.Config.Members {
		if err := checkWorkspaceDirectory(dir, member.Workspace); err != nil {
			return err
		}
		if err := makeDurableDir(filepath.Join(dir, filepath.FromSlash(member.Workspace))); err != nil {
			return err
		}
	}
	if errors.Is(loadErr, os.ErrNotExist) {
		state = RoomSnapshot{Version: formatVersion, RoomIdentity: e.RoomIdentity, Messages: []RoomMessage{}, Members: []MemberState{}}
		for _, member := range init.Config.Members {
			state.Members = append(state.Members, member.MemberState)
		}
		return store.SaveRoom(state)
	}
	return nil
}

// Resources join the ready index only after durable atomic file replacement.
func writeRoomResource(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".resource-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func (m *roomManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == gitlabCallbackPath || r.URL.Path == gitlabCompletePath {
		m.serveGitLabAuth(w, r)
		return
	}
	if !sameOrigin(r) {
		writeAPIError(w, ErrIdentity)
		return
	}
	if r.URL.Path == "/v1/ui" && r.Method == http.MethodGet {
		// Bootstrap remains available with an empty catalog and during switching.
		_ = json.NewEncoder(w).Encode(struct {
			Managed bool `json:"managed"`
		}{true})
		return
	}
	if r.URL.Path == "/v1/actions/execute" {
		m.serveActionExecute(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/tools/") {
		m.serveTools(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/platform/connections") {
		m.servePlatformConnections(w, r)
		return
	}
	if r.URL.Path == "/v1/platform/lark/target" || r.URL.Path == "/v1/platform/lark/messages" || r.URL.Path == "/v1/platform/lark/message" {
		m.serveLarkRead(w, r)
		return
	}
	if r.URL.Path == "/v1/platform/lark/chats" {
		m.serveLarkChats(w, r)
		return
	}
	if r.URL.Path == "/v1/platform/attachments" {
		m.servePlatformAttachment(w, r)
		return
	}
	if r.URL.Path == "/v1/platform/notifications" || r.URL.Path == "/v1/platform/notifications/configure" || r.URL.Path == "/v1/platform/notifications/sync" {
		m.servePlatformNotifications(w, r)
		return
	}
	if r.URL.Path == "/v1/workspace" || r.URL.Path == "/v1/workspace/apply" || r.URL.Path == "/v1/workspace/retry" {
		m.serveWorkspace(w, r)
		return
	}
	if m.workspaceHistory(w, r) {
		return
	}
	if r.URL.Path != "/v1/ui" && new(service).serveWeb(w, r) {
		return
	}
	if r.URL.Path == "/v1/rooms/open" {
		m.serveRoomOpen(w, r)
		return
	}
	if r.URL.Path == "/v1/rooms/rename" {
		m.serveRoomRename(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/rooms/") && r.URL.Path != "/v1/rooms/create" && r.URL.Path != "/v1/rooms/delete" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path != "/v1/rooms" && r.URL.Path != "/v1/rooms/create" && r.URL.Path != "/v1/rooms/delete" {
		m.serveRuntime(w, r)
		return
	}
	if (r.URL.Path == "/v1/rooms" && r.Method != http.MethodGet) || (r.URL.Path != "/v1/rooms" && r.Method != http.MethodPost) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !m.mu.TryLock() {
		writeAPIError(w, ErrBusy)
		return
	}
	defer m.mu.Unlock()
	if err := m.store.check(false); err != nil {
		writeAPIError(w, err)
		return
	}
	if r.Method == http.MethodGet {
		rooms := make([]roomRuntimeSummary, 0, len(m.index.Rooms))
		for _, e := range m.index.Rooms {
			summary := roomRuntimeSummary{roomSummary: e.roomSummary, RuntimeStatus: "closed", Busy: m.roomBusyLocked(e.RoomIdentity)}
			if svc, rt := m.runtimeServiceLocked(e.RoomIdentity); rt != nil {
				summary.RuntimeStatus, summary.RuntimeError = rt.status, rt.err
				if rt.status == "ready" && svc != nil && !svc.closing.Load() {
					info := svc.info
					summary.Service = &info
				}
			}
			// Preserve snapshot activity time separately from public message time,
			// without writing the catalog on every message or scheduler update.
			if info, err := os.Stat(filepath.Join(m.roomDir(e.RoomID), "room.json")); err == nil && info.ModTime().After(summary.UpdatedAt) {
				summary.UpdatedAt = info.ModTime().UTC()
			}
			// Public message time is separate from snapshot writes such as read
			// cursor updates. Derive it for inactive rooms and existing data too.
			var snapshot RoomSnapshot
			if err := readSnapshot(filepath.Join(m.roomDir(e.RoomID), "room.json"), &snapshot, maxRoomFileBytes); err == nil &&
				snapshot.RoomIdentity == e.RoomIdentity && len(snapshot.Messages) > 0 {
				last := snapshot.Messages[len(snapshot.Messages)-1].CreatedAt
				summary.LastMessageAt = &last
			}
			var saved managedRoomConfig
			if err := readSnapshot(filepath.Join(m.roomDir(e.RoomID), "room-config.json"), &saved, maxPrivateFileBytes); err == nil && saved.RoomIdentity == e.RoomIdentity {
				for _, member := range saved.Members {
					summary.Members = append(summary.Members, teamMemberSummary{ID: member.AgentID, Name: member.Name})
				}
			}
			rooms = append(rooms, summary)
		}
		response := roomsResponse{ServiceEpoch: m.epoch, Rooms: rooms,
			Operator: humanOperator(), Busy: m.busy, Teams: m.teamSummaries()}
		if m.store.uncertain {
			response.WriteError = ErrCommitUnknown.Error()
		}
		_ = json.NewEncoder(w).Encode(response)
		return
	}
	if m.closing {
		writeAPIError(w, ErrClosing)
		return
	}
	var req struct {
		ServiceEpoch string `json:"service_epoch"`
		Caller       Caller `json:"caller"`
		roomSelection
		RoomIdentity
	}
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
	if (r.URL.Path == "/v1/rooms/create" && req.RoomIdentity != (RoomIdentity{})) ||
		(r.URL.Path == "/v1/rooms/delete" && req.roomSelection != (roomSelection{})) {
		writeAPIError(w, ErrRequest)
		return
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	var result any
	var err error
	if r.URL.Path == "/v1/rooms/delete" {
		result, err = m.startDeletion(req.RoomIdentity)
	} else {
		if req.RoomIdentity != (RoomIdentity{}) {
			writeAPIError(w, ErrRequest)
			return
		}
		if m.busy {
			writeAPIError(w, ErrBusy)
			return
		}
		result, err = m.create(req.roomSelection)
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
