package room

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
)

func validSpaceStatus(status string) bool {
	return status == "active" || status == "archiving" || status == "suspended"
}
func spaceLifecyclePath(path string) bool {
	if !strings.HasPrefix(path, "/v1/spaces/") {
		return false
	}
	id, action, ok := strings.Cut(strings.TrimPrefix(path, "/v1/spaces/"), "/")
	return ok && validSpaceID(id) && (action == "archive" || action == "restore")
}
func spaceControlPath(path string) bool {
	return path == "/v1/spaces" || spaceLifecyclePath(path) ||
		strings.HasPrefix(path, "/v1/spaces/") && !strings.Contains(strings.TrimPrefix(path, "/v1/spaces/"), "/") ||
		strings.HasPrefix(path, "/v1/access/") || strings.HasPrefix(path, "/ui/") || path == "/space-loading"
}

// Caller holds s.mu. The directory is the sole durable lifecycle authority.
// A failed/unknown publication never admits work or discards the old owner.
func (s *spaceServer) saveSpaceLocked(index int, record SpaceRecord) error {
	next := s.directory
	next.Spaces = append([]SpaceRecord{}, next.Spaces...)
	next.Spaces[index] = record
	if err := s.writer.save(filepath.Join(s.assembly.roots.Home, "spaces.json"), next, maxPrivateFileBytes); err != nil {
		return err
	}
	s.directory = next
	return nil
}

func (s *spaceServer) changeSpaceStatus(id, action string, version uint64, subjects ...userSubject) (SpaceRecord, error) {
	owner := s.access.subject().UserID
	if len(subjects) > 0 {
		if s.access.authorizeSubject(subjects[0]) != nil {
			return SpaceRecord{}, ErrIdentity
		}
		owner = subjects[0].UserID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return SpaceRecord{}, ErrClosing
	}
	for i, record := range s.directory.Spaces {
		if record.ID != id || (owner != "" && record.OwnerUserID != owner) {
			continue
		}
		entry := s.entries[id]
		if action == "archive" && record.Status == "suspended" || action == "restore" && record.Status == "active" {
			return record, nil
		}
		if record.Version != version {
			return SpaceRecord{}, ErrConflict
		}
		if entry != nil && entry.lifecycleBusy {
			if action == "restore" {
				return SpaceRecord{}, ErrBusy
			}
			return record, nil
		}
		if action == "restore" {
			if s.access.users != nil {
				user, ok := s.access.users.user(record.OwnerUserID)
				if !ok || user.Status != "active" {
					return SpaceRecord{}, ErrIdentity
				}
			}
			// archiving retains an unsettled owner (or an interrupted operation).
			// Only an explicit archive retry may finish it before restore.
			if record.Status != "suspended" {
				return SpaceRecord{}, ErrBusy
			}
			if entry != nil {
				select {
				case <-entry.done:
				default:
					return SpaceRecord{}, ErrBusy
				}
				entry.mu.RLock()
				runtime := entry.runtime
				entry.mu.RUnlock()
				if runtime != nil && runtime.State() != "stopped" {
					return SpaceRecord{}, ErrBusy
				}
			}
			record.Status = "active"
			record.Version++
			next := append([]SpaceRecord{}, s.directory.Spaces...)
			next[i] = record
			if err := s.assembly.config.executionResources.checkSpaces(next); err != nil {
				return SpaceRecord{}, err
			}
			if err := s.saveSpaceLocked(i, record); err != nil {
				return SpaceRecord{}, err
			}
			delete(s.entries, id)
			s.startLocked(record, nil, spaceAddress(strings.TrimPrefix(s.access.internal, "http://")))
			return record, nil
		}
		if action != "archive" {
			return SpaceRecord{}, ErrRequest
		}
		if record.Status == "active" {
			record.Status = "archiving"
			record.Version++
			if err := s.saveSpaceLocked(i, record); err != nil {
				if errors.Is(err, ErrCommitUnknown) && entry != nil {
					// Conservative admission fence until restart reconciles the durable
					// directory. Retain handles and operation evidence in this entry.
					entry.revoke()
					entry.lifecycleError = "归档登记结果尚未确定，请重启后核对。"
				}
				return SpaceRecord{}, err
			}
		}
		if entry == nil {
			// After process restart no old Go objects can still own work. Do not
			// open a business manager just to finish an interrupted archive.
			record.Status = "suspended"
			record.Version++
			if err := s.saveSpaceLocked(i, record); err != nil {
				return SpaceRecord{}, err
			}
			return record, nil
		}
		entry.lifecycleBusy, entry.lifecycleError = true, ""
		entry.revoke()
		s.lifecycle.Add(1)
		go func() {
			defer s.lifecycle.Done()
			ctx, cancel := context.WithTimeout(context.Background(), serviceShutdownTimeout)
			defer cancel()
			err := s.settleSpace(ctx, entry)
			s.mu.Lock()
			defer s.mu.Unlock()
			entry.lifecycleBusy = false
			if err == nil {
				for index, current := range s.directory.Spaces {
					if current.ID == id && current.Status == "archiving" {
						current.Status = "suspended"
						current.Version++
						err = s.saveSpaceLocked(index, current)
						break
					}
				}
			}
			if err != nil {
				entry.lifecycleError = "空间尚未结清，请重试归档。"
			}
		}()
		return record, nil
	}
	return SpaceRecord{}, ErrIdentity
}

func (entry *spaceServerEntry) revoke() {
	entry.cancel()
	entry.mu.RLock()
	defer entry.mu.RUnlock()
	if entry.runtime != nil {
		entry.runtime.config.access.revokeSpace()
		entry.runtime.cancel()
	}
	if entry.listener != nil {
		_ = entry.listener.Close()
	}
}

func (s *spaceServer) settleSpace(ctx context.Context, entry *spaceServerEntry) error {
	// The Serve owner first joins admitted HTTP/setup handlers and its own
	// cleanup. A timeout retains the same entry; subsequent retry closes it.
	select {
	case <-entry.done:
	case <-ctx.Done():
		return ctx.Err()
	}
	entry.mu.RLock()
	runtime := entry.runtime
	entry.mu.RUnlock()
	if runtime != nil {
		return runtime.Close(ctx)
	}
	return nil
}

func (a *serverAccess) revokeSpace() {
	a.revoked.Store(true)
	a.mu.Lock()
	clear(a.executions)
	a.mu.Unlock()
	auth := a.authority()
	auth.mu.Lock()
	defer auth.mu.Unlock()
	for lease := range auth.updateLeases {
		if lease.spaceID == a.space.SpaceID {
			delete(auth.updateLeases, lease)
			close(lease.done)
		}
	}
}

func (s *spaceServer) serveSpaceLifecycle(w http.ResponseWriter, r *http.Request, input spaceChangeInput) {
	if input.Name != "" || input.RequestID != "" || input.ExpectedVersion == 0 {
		writeAPIError(w, ErrRequest)
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/spaces/"), "/")
	scope, _ := r.Context().Value(spaceContextKey{}).(authorizedSpace)
	record, err := s.changeSpaceStatus(id, action, input.ExpectedVersion, scope.Subject)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
	_ = writeJSON(w, record)
}
