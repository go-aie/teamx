package room

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/web"
	"github.com/pelletier/go-toml/v2"
)

func validSpaceName(name string) bool {
	return strings.TrimSpace(name) == name && len([]rune(name)) > 0 && len([]rune(name)) <= 64 && !strings.ContainsFunc(name, unicode.IsControl)
}

type spaceChangeInput struct {
	RequestID          string `json:"request_id"`
	Name               string `json:"name"`
	ExpectedVersion    uint64 `json:"expected_version"`
	ModelSourceSpaceID string `json:"model_source_space_id,omitempty"`
}

func (s *spaceServer) serveDirectory(w http.ResponseWriter, r *http.Request) {
	if web.Serve(w, r) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	scope, _ := r.Context().Value(spaceContextKey{}).(authorizedSpace)
	if s.access.authorizeSubject(scope.Subject) != nil {
		writeAPIError(w, ErrIdentity)
		return
	}
	if path != "/v1/spaces" && !strings.HasPrefix(path, "/v1/spaces/") {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet && path == "/v1/spaces" {
		s.mu.Lock()
		defer s.mu.Unlock()
		type view struct {
			SpaceRecord
			LifecycleBusy  bool            `json:"lifecycle_busy"`
			LifecycleError string          `json:"lifecycle_error,omitempty"`
			State          string          `json:"runtime_state"`
			Capabilities   map[string]bool `json:"capabilities"`
		}
		spaces := make([]view, 0, len(s.directory.Spaces))
		for _, record := range s.directory.Spaces {
			if record.OwnerUserID != scope.Subject.UserID {
				continue
			}
			state := record.Status
			lifecycleError := ""
			lifecycleBusy := false
			if record.Status == "active" {
				state = "starting"
			}
			if entry := s.entries[record.ID]; entry != nil && record.Status == "active" {
				entry.mu.RLock()
				if entry.err != nil {
					state = "failed"
				} else if entry.runtime != nil {
					state = entry.runtime.State()
					if entry.listener == nil {
						state = "starting"
					} else {
						entry.listener.mu.RLock()
						if entry.listener.handler == nil {
							state = "starting"
						}
						entry.listener.mu.RUnlock()
					}
				}
				entry.mu.RUnlock()
			}
			if entry := s.entries[record.ID]; entry != nil {
				lifecycleError = entry.lifecycleError
				lifecycleBusy = entry.lifecycleBusy
			}
			legacy := record.ID == s.access.space.SpaceID
			gitlab, lark, scion := false, false, false
			if catalog := s.assembly.config.platformCatalog; catalog != nil {
				_, gitlab = catalog.Types().LookupType("gitlab")
				_, lark = catalog.Types().LookupType("lark")
				_, scion = catalog.Types().LookupType("scion")
			}
			if s.access.users != nil {
				gitlab = gitlab && s.gitlabApplications != nil
			}
			spaces = append(spaces, view{record, lifecycleBusy, lifecycleError, state, map[string]bool{"gitlab_application_management": s.access.users == nil, "gitlab": gitlab, "lark": (legacy || s.assembly.config.hostTools.restricted) && lark, "scion": legacy && scion, "computer": !s.assembly.config.hostTools.restricted, "managed_repositories": !s.assembly.config.hostTools.restricted}})
			spaces[len(spaces)-1].Capabilities["space_reset"] = s.spaceResetAvailable()
			// Personal deployments expose the existing Space management UI.
			// Hosted deployments retain their APIs while keeping the UI hidden.
			spaces[len(spaces)-1].Capabilities["space_management"] = s.access.users == nil
			spaces[len(spaces)-1].Capabilities["native_lark_login_preserved"] = runtime.GOOS == "darwin" && s.access.users == nil && !s.assembly.config.hostTools.restricted
			spaces[len(spaces)-1].Capabilities["reset_default_model"] = s.access.users != nil
			if record.resetting() || s.resetBusy[record.ID] && record.Reset != nil {
				spaces[len(spaces)-1].LifecycleBusy = s.resetBusy[record.ID]
				if s.resetBusy[record.ID] && errors.Is(s.writer.checkWriter(true), ErrCommitUnknown) {
					op := *record.Reset
					op.Code = "commit_unknown"
					op.Message = "重置登记结果尚未确定，请重启服务后核对并继续原操作。"
					spaces[len(spaces)-1].Reset = &op
				}
				if record.resetting() {
					spaces[len(spaces)-1].State = "resetting"
				}
			}
		}
		_ = writeJSON(w, map[string]any{"version": 1, "can_create": true, "default_space_id": s.access.defaultSpace(scope.Subject), "spaces": spaces})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input spaceChangeInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		writeAPIError(w, ErrRequest)
		return
	}
	if spaceLifecyclePath(path) {
		if input.ModelSourceSpaceID != "" {
			writeAPIError(w, ErrRequest)
			return
		}
		if strings.HasSuffix(path, "/reset") {
			s.serveSpaceReset(w, r, input)
			return
		}
		s.serveSpaceLifecycle(w, r, input)
		return
	}
	if !validSpaceName(input.Name) {
		writeAPIError(w, ErrRequest)
		return
	}
	if path == "/v1/spaces" {
		record, err := s.create(input, scope.Subject)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_ = writeJSON(w, record)
		return
	}
	if input.ModelSourceSpaceID != "" {
		writeAPIError(w, ErrRequest)
		return
	}
	id := strings.TrimPrefix(path, "/v1/spaces/")
	if !validSpaceID(id) {
		writeAPIError(w, ErrRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		writeAPIError(w, ErrClosing)
		return
	}
	for i, record := range s.directory.Spaces {
		if record.ID != id || record.OwnerUserID != scope.Subject.UserID {
			continue
		}
		if record.resetting() || s.resetBusy[id] {
			writeAPIError(w, ErrBusy)
			return
		}
		if record.Version != input.ExpectedVersion {
			writeAPIError(w, ErrConflict)
			return
		}
		copyDirectory := s.directory
		copyDirectory.Spaces = append([]SpaceRecord{}, s.directory.Spaces...)
		record.Name = input.Name
		record.Version++
		copyDirectory.Spaces[i] = record
		if err := s.writer.save(filepath.Join(s.assembly.roots.Home, "spaces.json"), copyDirectory, maxPrivateFileBytes); err != nil {
			writeAPIError(w, err)
			return
		}
		s.directory = copyDirectory
		_ = writeJSON(w, record)
		return
	}
	writeAPIError(w, ErrIdentity)
}

func (s *spaceServer) create(input spaceChangeInput, subjects ...userSubject) (SpaceRecord, error) {
	subject := s.access.subject()
	if len(subjects) > 0 {
		subject = subjects[0]
	}
	if s.access.authorizeSubject(subject) != nil {
		return SpaceRecord{}, ErrIdentity
	}
	if !validSpaceID(input.RequestID) || input.ExpectedVersion != 0 {
		return SpaceRecord{}, ErrRequest
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return SpaceRecord{}, ErrClosing
	}
	for _, record := range s.directory.Spaces {
		if record.ID == input.RequestID {
			s.mu.Unlock()
			if record.OwnerUserID != subject.UserID {
				return SpaceRecord{}, ErrIdentity
			}
			if record.Name != input.Name || record.ModelSourceSpaceID != input.ModelSourceSpaceID {
				return SpaceRecord{}, ErrConflict
			}
			return record, nil
		}
	}
	if s.access.users == nil && len(s.directory.Spaces) >= 32 {
		s.mu.Unlock()
		return SpaceRecord{}, ErrBusy
	}
	home := filepath.Join(s.assembly.roots.Home, "spaces", input.RequestID)
	record := SpaceRecord{ID: input.RequestID, OwnerUserID: subject.UserID, Name: input.Name, Status: "active", Version: 1, Roots: spaceRoots{home, filepath.Join(home, "data"), filepath.Join(home, "teams")}}
	record.ModelSourceSpaceID = input.ModelSourceSpaceID
	next := s.directory
	next.Spaces = append(append([]SpaceRecord{}, next.Spaces...), record)
	if err := s.assembly.config.executionResources.checkSpaces(next.Spaces); err != nil {
		s.mu.Unlock()
		return SpaceRecord{}, err
	}
	if err := s.validateDirectory(next); err != nil {
		s.mu.Unlock()
		return SpaceRecord{}, err
	}
	s.claims.mu.Lock()
	if err := s.claims.checkNewSpaceLocked(record); err != nil {
		s.claims.mu.Unlock()
		s.mu.Unlock()
		return SpaceRecord{}, err
	}
	// The durable reservation is published before resources. A crash resumes this
	// exact SpaceID at startup; an uncertain commit never creates another Space.
	if err := s.writer.save(filepath.Join(s.assembly.roots.Home, "spaces.json"), next, maxPrivateFileBytes); err != nil {
		s.claims.mu.Unlock()
		s.mu.Unlock()
		return SpaceRecord{}, err
	}
	s.directory = next
	s.claims.directory.Spaces = append(append([]SpaceRecord{}, s.claims.directory.Spaces...), record)
	s.claims.mu.Unlock()
	s.startLocked(record, nil, spaceAddress(strings.TrimPrefix(s.access.internal, "http://")))
	s.mu.Unlock()
	return record, nil
}

type spaceAddress string

func (a spaceAddress) Network() string { return "tcp" }
func (a spaceAddress) String() string  { return string(a) }

func (s *spaceServer) initializeSpace(record SpaceRecord) error {
	path := filepath.Join(record.Roots.Home, "config.toml")
	if _, err := os.Lstat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := makeDurableDir(record.Roots.Home); err != nil {
		return err
	}
	// Start with an empty catalog. An explicitly nominated owned model source
	// enters ordinary first-use validation; no model is published by copying.
	// Environment, platform profiles, Computer and personal templates stay separate.
	values := map[string]any{"server": map[string]any{"data_dir": "./data", "teams_dir": "./teams"}, "models": modelCatalog{SchemaVersion: 1, Version: 1}}
	if s.assembly.defaults != nil {
		runtime := *s.assembly.defaults
		runtime.Model = dsh.ModelConfig{}
		values["runtime"] = runtime
	}
	data, err := toml.Marshal(values)
	if err != nil {
		return err
	}
	return installMissing(path, data)
}
