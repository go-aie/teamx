package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
)

const defaultAssistantCreateID = "system-default-assistant"

// This is a creation/deletion receipt, not a second Agent state store. The
// membership and its enable intent belong to the referenced Room as usual.
type defaultAssistantRecord struct {
	RoomIdentity
	CreateID  string `json:"create_id"`
	Status    string `json:"status"` // pending, completed; deleting/deleted retain legacy deletion intent
	ErrorCode string `json:"error_code,omitempty"`
	Error     string `json:"error,omitempty"`
	NextStep  string `json:"next_step,omitempty"`
}

func defaultAssistantSelection() roomSelection {
	return roomSelection{CreateID: defaultAssistantCreateID, IsDirect: true, AgentRoleID: chiefOfStaffRoleID}
}

func (m *roomManager) validateDefaultAssistant() error {
	record := m.index.DefaultAssistant
	var entry *roomIndexEntry
	for i := range m.index.Rooms {
		e := &m.index.Rooms[i]
		if e.CreateID == defaultAssistantCreateID {
			entry = e
		}
	}
	if record == nil {
		if entry != nil {
			return fmt.Errorf("%w: default assistant reservation has no receipt", ErrInvalid)
		}
		return nil // Old catalogs are initialized when the managed server starts.
	}
	if record.CreateID != defaultAssistantCreateID {
		return ErrIdentity
	}
	if record.Status != "pending" && (record.ErrorCode != "" || record.Error != "" || record.NextStep != "") {
		return ErrInvalid
	}
	switch record.ErrorCode {
	case "":
		if record.Error != "" || record.NextStep != "" {
			return ErrInvalid
		}
	case "configuration_required", "initialization_failed":
		if !validText(record.Error) || (record.ErrorCode == "configuration_required" && record.NextStep != "teamx start init") ||
			(record.ErrorCode == "initialization_failed" && record.NextStep != "") {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	if record.RoomIdentity == (RoomIdentity{}) {
		if record.Status != "pending" || entry != nil {
			return ErrIdentity
		}
		return nil // Configuration blocked before a member could be frozen.
	}
	if !safeMembershipID(record.RoomID) || !validText(record.RoomInstance) {
		return ErrIdentity
	}
	if record.Status == "deleted" {
		if entry != nil {
			return ErrIdentity
		}
		for _, e := range m.index.Rooms {
			if e.RoomIdentity == record.RoomIdentity {
				return ErrIdentity
			}
		}
		return nil
	}
	if entry == nil || entry.RoomIdentity != record.RoomIdentity || entry.Selection != defaultAssistantSelection() {
		return ErrIdentity
	}
	if !(record.Status == "pending" && entry.Status == "creating" ||
		record.Status == "completed" && entry.Status == "ready" ||
		record.Status == "deleting" && entry.Status == "deleting") {
		return ErrInvalid
	}
	return nil
}

// Called under the catalog lock after startup configuration has been loaded.
// All reservation identities and the frozen role/runtime join the receipt in
// one commit. Retries never search by name or adopt a manually created DM.
func (m *roomManager) ensureDefaultAssistantLocked() (*defaultAssistantRecord, error) {
	if m.closing {
		return m.index.DefaultAssistant, ErrClosing
	}
	if m.busy {
		return m.index.DefaultAssistant, ErrBusy
	}
	if err := m.store.check(true); err != nil {
		return m.index.DefaultAssistant, err
	}
	if err := m.validateDefaultAssistant(); err != nil {
		return m.index.DefaultAssistant, err
	}
	record := m.index.DefaultAssistant
	if record != nil && record.Status != "pending" {
		return record, nil // Includes deletion intent, even before data removal.
	}
	runtime := m.defaultRuntime
	if record != nil && record.RoomIdentity != (RoomIdentity{}) {
		for _, e := range m.index.Rooms {
			if e.RoomIdentity == record.RoomIdentity {
				runtime = e.Initialization.Config.Runtime
			}
		}
	}
	var configErr error
	if runtime == nil {
		configErr = fmt.Errorf("runtime is missing in config.toml")
	} else if configErr = validateTeamRuntime(*runtime); configErr == nil {
		configErr = checkManagedRuntimeAvailability(runtime.profile())
	}
	if configErr != nil {
		return m.defaultAssistantFailureLocked("configuration_required", configErr)
	}
	if record == nil || record.RoomIdentity == (RoomIdentity{}) {
		if len(m.index.Rooms) >= maxRooms {
			return m.defaultAssistantFailureLocked("initialization_failed", ErrCapacity)
		}
		e, err := m.prepare(defaultAssistantSelection())
		if err != nil {
			return m.defaultAssistantFailureLocked("initialization_failed", err)
		}
		next := m.copyIndex()
		next.Rooms = append(next.Rooms, e)
		next.DefaultAssistant = &defaultAssistantRecord{CreateID: defaultAssistantCreateID, Status: "pending", RoomIdentity: e.RoomIdentity}
		if err := m.saveIndex(next); err != nil {
			return m.index.DefaultAssistant, err
		}
	}
	if _, err := m.createRoom(defaultAssistantSelection()); err != nil {
		return m.defaultAssistantFailureLocked("initialization_failed", err)
	}
	// A retry after startup joins normal runtime recovery; it cannot re-enable
	// a completed, paused, failed or deleted instance.
	if m.recoveryStarted {
		owner := m.index.DefaultAssistant.RoomIdentity
		if m.runtimes[owner] == nil {
			for _, e := range m.index.Rooms {
				if e.RoomIdentity == owner {
					rt := m.reserveRuntimeLocked(owner)
					rt.status = "opening"
					go m.restoreRuntime(e, rt)
				}
			}
		}
	}
	return m.index.DefaultAssistant, nil
}

func (m *roomManager) defaultAssistantFailureLocked(code string, cause error) (*defaultAssistantRecord, error) {
	next := m.copyIndex()
	if next.DefaultAssistant == nil {
		next.DefaultAssistant = &defaultAssistantRecord{CreateID: defaultAssistantCreateID, Status: "pending"}
	}
	next.DefaultAssistant.ErrorCode, next.DefaultAssistant.Error = code, cause.Error()
	next.DefaultAssistant.NextStep = ""
	if code == "configuration_required" {
		next.DefaultAssistant.NextStep = "teamx start init"
	}
	err := m.saveIndex(next)
	return m.index.DefaultAssistant, errors.Join(cause, err)
}

// Only a still-private default creation writes initial intent. No runtime or
// backend is opened here; the regular lazy scheduler waits for real input.
func (m *roomManager) initializeDefaultAssistantScheduling(e roomIndexEntry) error {
	if e.CreateID != defaultAssistantCreateID {
		return nil
	}
	record := m.index.DefaultAssistant
	if record == nil || record.Status != "pending" || record.RoomIdentity != e.RoomIdentity || e.Initialization == nil {
		return ErrIdentity
	}
	store, err := OpenStore(m.roomDir(e.RoomID), e.RoomID)
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.LoadRoom()
	if err != nil {
		return err
	}
	if err := validateManagedSnapshot(e.Initialization.Config, state); err != nil {
		return err
	}
	member := state.Members[0]
	id := MemberIdentity{RoomIdentity: e.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	opts := SessionOptions{MemberIdentity: id, StatePath: filepath.Join(store.dir, "agents", id.MembershipID, "session.json")}
	generation, err := (dshBackend{store: store}).sessionGeneration(opts)
	if err != nil {
		return err
	}
	initial := memberScheduling{Version: schedulingVersion, MemberIdentity: id, Enabled: true, SessionGeneration: generation}
	intent, err := store.loadScheduling(id)
	if err == nil {
		// A crash may have committed intent before the ready index. Retain it.
		if intent != initial {
			return fmt.Errorf("%w: unexpected default assistant initial intent", ErrInvalid)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return store.saveScheduling(initial)
}

func (m *roomManager) serveDefaultAssistantRetry(w http.ResponseWriter, r *http.Request) {
	m.serveDefaultAssistantInitialization(w, r, false)
}

func (m *roomManager) serveDefaultAssistantRestore(w http.ResponseWriter, r *http.Request) {
	m.serveDefaultAssistantInitialization(w, r, true)
}

// Explicit restoration creates a fresh instance. Only a confirmed tombstone
// can be replaced; startup/retry and stale browser tabs cannot restore it.
func (m *roomManager) restoreDefaultAssistantLocked(previous RoomIdentity) (*defaultAssistantRecord, error) {
	if m.closing {
		return nil, ErrClosing
	}
	if m.roomBusyLocked(previous) {
		return nil, ErrBusy
	}
	if err := m.store.check(true); err != nil {
		return nil, err
	}
	if err := m.validateDefaultAssistant(); err != nil {
		return nil, err
	}
	record := m.index.DefaultAssistant
	if record == nil || record.Status != "deleted" || previous != record.RoomIdentity {
		return nil, ErrConflict
	}
	next := m.copyIndex()
	next.DefaultAssistant = &defaultAssistantRecord{CreateID: defaultAssistantCreateID, Status: "pending"}
	if err := m.saveIndex(next); err != nil {
		return nil, err
	}
	// Persist the human's restoration intent before ordinary initialization.
	// A crash or missing runtime resumes this request through the same path.
	return m.ensureDefaultAssistantLocked()
}

func (m *roomManager) serveDefaultAssistantInitialization(w http.ResponseWriter, r *http.Request, restore bool) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ServiceEpoch string `json:"service_epoch"`
		Caller       Caller `json:"caller"`
		RoomIdentity
	}
	if !decodeToolRequest(w, r, &req) {
		return
	}
	if req.ServiceEpoch != m.epoch || req.Caller.Kind != "human" || !validText(req.Caller.ID) || req.Caller.MembershipID != "" {
		writeAPIError(w, ErrIdentity)
		return
	}
	if !restore && req.RoomIdentity != (RoomIdentity{}) {
		writeAPIError(w, ErrRequest)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var record *defaultAssistantRecord
	var err error
	if restore {
		record, err = m.restoreDefaultAssistantLocked(req.RoomIdentity)
	} else {
		record, err = m.ensureDefaultAssistantLocked()
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(record)
}
