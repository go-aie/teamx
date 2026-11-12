package room

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// Published catalog and admission gates shared with managed services. Each
// generation retains an immutable execution snapshot across catalog changes.
type modelExecution struct {
	mu          sync.RWMutex
	Gates       map[MemberIdentity]string
	Catalog     modelCatalog
	Credentials *modelCredentials
	Validations map[string]modelValidation
}

func loadModelExecution(c startupConfig) (*modelExecution, error) {
	if c.Models == nil || len(c.Models.Entries) == 0 {
		return nil, ErrRequest
	}
	if c.Server.DataDir != nil {
		var index roomsIndex
		if err := readSnapshot(filepath.Join(*c.Server.DataDir, "rooms.json"), &index, maxRoomsFileBytes); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, room := range index.Rooms {
			var runtime *teamRuntime
			if room.Initialization != nil {
				runtime = room.Initialization.Config.Runtime
			} else if room.Status == "ready" {
				var saved managedRoomConfig
				if !safeMembershipID(room.RoomID) {
					return nil, ErrIdentity
				}
				if err := readSnapshot(filepath.Join(*c.Server.DataDir, "rooms", room.RoomID, "room-config.json"), &saved, maxPrivateFileBytes); err != nil {
					return nil, err
				}
				if saved.RoomIdentity != room.RoomIdentity {
					return nil, ErrIdentity
				}
				runtime = saved.Runtime
			} else {
				continue
			}
			if runtime == nil || !runtime.ManagedModels {
				return nil, errors.New("existing rooms require model migration before catalog execution")
			}
		}
	}
	credentials, err := newModelCredentials(filepath.Dir(c.path))
	if err != nil {
		return nil, err
	}
	execution := &modelExecution{Catalog: c.Models.clone(), Credentials: credentials, Validations: map[string]modelValidation{}}
	for _, entry := range c.Models.Entries {
		var receipt modelValidation
		path := filepath.Join(filepath.Dir(credentials.dir), "model-validations", entry.executionDigest()+".json")
		if err := readSnapshot(path, &receipt, maxPrivateFileBytes); err != nil {
			continue // Management remains available; only dependent executions are blocked.
		}
		if !modelValidationMatches(entry, receipt) {
			continue
		}
		receipt.EntryID, receipt.Revision = entry.ID, entry.Revision
		execution.Validations[entry.ID] = receipt
	}
	return execution, nil
}

func (e *modelExecution) target(id MemberIdentity, selection modelSelection, version uint64, profile dshProfile) (modelSessionSnapshot, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	entry, err := e.Catalog.resolve(selection)
	if err != nil {
		return modelSessionSnapshot{}, err
	}
	return newModelSessionSnapshot(e.Catalog, memberModelReference{MemberIdentity: id, Selection: selection, Version: version}, "session.json.dsh", profile, e.Validations[entry.ID])
}

func (e *modelExecution) catalog() modelCatalog {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Catalog.clone()
}

func (e *modelExecution) hasValidation(id string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	_, ok := e.Validations[id]
	return ok
}
func (e *modelExecution) gate(id MemberIdentity) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.Gates[id]
}
func (e *modelExecution) setGate(id MemberIdentity, op string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.Gates == nil {
		e.Gates = map[MemberIdentity]string{}
	}
	if op == "" {
		delete(e.Gates, id)
	} else {
		e.Gates[id] = op
	}
}

// Read all publication material before the config replace. The publish callback
// itself performs no I/O and is safe when another config writer reconciles it.
func (e *modelExecution) publication(c modelCatalog) (func(), error) {
	receipts := map[string]modelValidation{}
	for _, entry := range c.Entries {
		var r modelValidation
		if err := readSnapshot(filepath.Join(filepath.Dir(e.Credentials.dir), "model-validations", entry.executionDigest()+".json"), &r, maxPrivateFileBytes); err != nil {
			continue
		}
		r.EntryID, r.Revision = entry.ID, entry.Revision
		if !modelValidationMatches(entry, r) {
			continue
		}
		receipts[entry.ID] = r
	}
	frozen := c.clone()
	return func() { e.mu.Lock(); defer e.mu.Unlock(); e.Catalog = frozen; e.Validations = receipts }, nil
}
