package room

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
)

type workspaceDirectoryRoot struct {
	Path              string `json:"path"`
	Label             string `json:"label"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}
type workspaceDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}
type workspaceDirectoryListing struct {
	CurrentDirectoryError string                    `json:"current_directory_error,omitempty"`
	DirectorySource       string                    `json:"directory_source,omitempty"`
	Roots                 []workspaceDirectoryRoot  `json:"roots"`
	Path                  string                    `json:"path"`
	Parent                *string                   `json:"parent"`
	Entries               []workspaceDirectoryEntry `json:"entries"`
	CanSelect             bool                      `json:"can_select"`
	Truncated             bool                      `json:"truncated"`
	UnavailableReason     string                    `json:"unavailable_reason,omitempty"`
}

func (m *roomManager) workspaceDirectoryRoots() ([]workspaceDirectoryRoot, string) {
	roots := m.workspaceRoots
	defaultHome := roots == nil
	if roots == nil {
		if m.config.hostTools.restricted {
			return []workspaceDirectoryRoot{}, "directory_selection_disabled"
		}
		home, err := os.UserHomeDir()
		if err != nil || !validDirectoryPath(home) || filepath.Dir(home) == home {
			return []workspaceDirectoryRoot{}, "directory_home_unavailable"
		}
		roots = &[]string{home}
	}
	result := []workspaceDirectoryRoot{}
	for _, root := range *roots {
		real, err := m.canonicalWorkspaceDirectory(root, false)
		if err != nil {
			// Protected roots must not become a discovery entry.
			if stringDirectoryError(err) == "directory_not_allowed" {
				continue
			}
			result = append(result, workspaceDirectoryRoot{filepath.Clean(root), filepath.Base(root), stringDirectoryError(err)})
			continue
		}
		covered := false
		for _, prev := range result {
			if prev.UnavailableReason == "" && pathWithin(prev.Path, real) {
				covered = true
			}
		}
		if covered {
			continue
		}
		result = slices.DeleteFunc(result, func(prev workspaceDirectoryRoot) bool {
			return prev.UnavailableReason == "" && pathWithin(real, prev.Path)
		})
		result = append(result, workspaceDirectoryRoot{Path: real, Label: filepath.Base(real)})
	}
	for _, root := range result {
		if root.UnavailableReason == "" {
			return result, ""
		}
	}
	reason := "directory_roots_unavailable"
	if defaultHome {
		reason = "directory_home_unavailable"
	} else if len(*roots) == 0 {
		reason = "directory_selection_disabled"
	}
	return result, reason
}
func stringDirectoryError(err error) string {
	if e, ok := err.(workspaceDirectoryError); ok {
		return string(e)
	}
	return "directory_unavailable"
}

func (m *roomManager) browseWorkspaceDirectory(r *http.Request, requested, current string) (workspaceDirectoryListing, error) {
	return m.browseMemberWorkspaceDirectory(r, requested, current, "", "")
}

// The only protected-path exception is this exact member's managed workspace.
// It grants read-only browsing, never an external-directory binding capability.
func (m *roomManager) browseMemberWorkspaceDirectory(r *http.Request, requested, current, roomDir, workspace string) (workspaceDirectoryListing, error) {
	var managedRoot, managedPath string
	var managedErr error
	if workspace != "" {
		managedPath = filepath.Join(roomDir, workspace)
		if err := checkWorkspaceDirectory(roomDir, workspace); err != nil {
			managedErr = directoryError("directory_path_changed")
		} else if real, err := filepath.EvalSymlinks(managedPath); err != nil {
			managedErr = directoryIOError(err)
		} else {
			managedRoot = real
		}
	}
	canonical := func(path string) (string, bool, error) {
		if !validDirectoryPath(path) {
			return "", false, directoryError("directory_not_allowed")
		}
		if managedErr != nil && pathWithin(managedPath, filepath.Clean(path)) {
			return "", false, managedErr
		}
		if managedRoot != "" {
			if real, err := filepath.EvalSymlinks(path); err == nil && pathWithin(managedRoot, real) {
				if err := readableDirectory(real); err != nil {
					return "", false, err
				}
				return real, true, nil
			}
		}
		real, err := m.canonicalWorkspaceDirectory(path, false)
		return real, false, err
	}
	roots, reason := m.workspaceDirectoryRoots()
	result := workspaceDirectoryListing{Roots: roots, Entries: []workspaceDirectoryEntry{}, UnavailableReason: reason}
	if requested == "" {
		if current != "" {
			if real, _, err := canonical(current); err == nil {
				requested = real
			} else {
				result.CurrentDirectoryError = stringDirectoryError(err)
			}
		}
		if requested == "" {
			for _, root := range roots {
				if root.UnavailableReason == "" {
					requested = root.Path
					break
				}
			}
		}
		if requested == "" && reason != "" {
			return limitWorkspaceDirectoryListing(result), nil
		}
	}
	path, managed, err := canonical(requested)
	if err != nil {
		return result, err
	}
	result.Path, result.CanSelect, result.DirectorySource = path, true, "external"
	if managed {
		result.DirectorySource, result.CanSelect, result.UnavailableReason = "managed", path == managedRoot, ""
	}
	parent := filepath.Dir(path)
	if parent != path {
		if real, _, err := canonical(parent); err == nil {
			result.Parent = &real
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return result, directoryIOError(err)
	}
	defer f.Close()
	names, err := f.Readdirnames(5000)
	if err != nil && err != io.EOF {
		return result, directoryIOError(err)
	}
	if len(names) == 5000 {
		result.Truncated = true
	}
	for _, name := range names {
		if err := r.Context().Err(); err != nil {
			return result, directoryError("directory_unavailable")
		}
		real, _, err := canonical(filepath.Join(path, name))
		if err != nil {
			continue
		}
		if len(result.Entries) == 1000 {
			result.Truncated = true
			break
		}
		result.Entries = append(result.Entries, workspaceDirectoryEntry{Name: name, Path: real})
	}
	slices.SortFunc(result.Entries, func(a, b workspaceDirectoryEntry) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	return limitWorkspaceDirectoryListing(result), nil
}

func limitWorkspaceDirectoryListing(result workspaceDirectoryListing) workspaceDirectoryListing {
	// Count serialized bytes, including escaping, without repeatedly encoding a
	// shrinking large listing. Reserve space for truncated=true and newline.
	entries := result.Entries
	result.Entries = []workspaceDirectoryEntry{}
	base, _ := json.Marshal(result)
	for len(base)+16 > 256<<10 && len(result.Roots) > 0 {
		result.Roots = result.Roots[:len(result.Roots)-1]
		result.Truncated = true
		base, _ = json.Marshal(result)
	}
	size := len(base) + 16
	for _, entry := range entries {
		encoded, _ := json.Marshal(entry)
		if size+len(encoded)+1 > 256<<10 {
			result.Truncated = true
			break
		}
		size += len(encoded) + 1
		result.Entries = append(result.Entries, entry)
	}
	return result
}

func (m *roomManager) serveWorkspaceDirectories(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	id := RoomIdentity{RoomID: q.Get("room_id"), RoomInstance: q.Get("room_instance")}
	// Capture only immutable policy and member evidence. Directory IO runs outside
	// the catalog lock so a slow mount cannot block all Room management.
	m.mu.Lock()
	validate := func() (managedRoomConfig, error) {
		if m.closing {
			return managedRoomConfig{}, ErrClosing
		}
		if q.Get("service_epoch") != m.epoch || !validText(q.Get("caller_id")) {
			return managedRoomConfig{}, ErrIdentity
		}
		entry, err := m.workspaceRoom(id)
		if err != nil || entry.Status != "ready" {
			return managedRoomConfig{}, ErrIdentity
		}
		saved, err := m.readWorkspaceRoomConfig(entry)
		if err != nil {
			return saved, err
		}
		for _, member := range saved.Members {
			if member.AgentID == q.Get("agent_id") && member.MembershipID == q.Get("membership_id") {
				return saved, nil
			}
		}
		return saved, ErrIdentity
	}
	saved, err := validate()
	if err != nil {
		m.mu.Unlock()
		writeAPIError(w, err)
		return
	}
	if err = m.store.check(false); err != nil {
		m.mu.Unlock()
		writeAPIError(w, err)
		return
	}
	policy := &roomManager{config: m.config, store: m.store, teamsDir: m.teamsDir, workspaceProtected: slices.Clone(m.workspaceProtected)}
	if m.workspaceRoots != nil {
		roots := slices.Clone(*m.workspaceRoots)
		policy.workspaceRoots = &roots
	}
	current := ""
	workspace := ""
	for _, member := range saved.Members {
		if member.MembershipID == q.Get("membership_id") {
			workspace = member.Workspace
			current = resolveMemberWorkspace(m.roomDir(id.RoomID), *saved.Workspace, member).Directory
		}
	}
	m.mu.Unlock()
	result, err := policy.browseMemberWorkspaceDirectory(r, q.Get("path"), current, m.roomDir(id.RoomID), workspace)
	m.mu.Lock()
	latest, identityErr := validate()
	if identityErr == nil && latest.WorkspaceVersion != saved.WorkspaceVersion {
		identityErr = ErrConflict
	}
	m.mu.Unlock()
	if identityErr != nil {
		writeAPIError(w, identityErr)
		return
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	_ = json.NewEncoder(w).Encode(result)
}
