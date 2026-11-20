package room

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"unicode"
)

// Resolution is purely static. Filesystem availability never governs decoding
// a snapshot: unavailable paths must remain visible and repairable.
type memberWorkspace struct {
	Directory        string
	DefaultDirectory string
	Source           string
}

func resolveMemberWorkspace(roomDir string, w workspaceConfig, member managedMember) memberWorkspace {
	def := filepath.Join(roomDir, filepath.FromSlash(member.Workspace))
	if dir := w.MemberDirectories[member.MembershipID]; dir != "" {
		return memberWorkspace{dir, def, "external"}
	}
	return memberWorkspace{def, def, "managed"}
}

type workspaceDirectoryError string

func (e workspaceDirectoryError) Error() string { return string(e) }
func (e workspaceDirectoryError) Unwrap() error { return ErrRequest }
func directoryError(code string) error          { return workspaceDirectoryError(code) }

func validDirectoryPath(dir string) bool {
	return filepath.IsAbs(dir) && len(dir) <= 4096 && strings.IndexFunc(dir, unicode.IsControl) < 0
}

func validateWorkspaceRoots(roots *[]string) error {
	if roots == nil {
		return nil
	}
	if len(*roots) > 16 {
		return fmt.Errorf("%w: server.workspace_roots accepts at most 16 roots", ErrRequest)
	}
	for _, root := range *roots {
		if !validDirectoryPath(root) {
			return fmt.Errorf("%w: workspace_roots requires absolute paths without control characters", ErrRequest)
		}
	}
	return nil
}

func validateMemberDirectories(w workspaceConfig, members []managedMember) error {
	for id := range w.MemberDirectories {
		found := false
		for _, member := range members {
			found = found || member.MembershipID == id
		}
		if !found {
			return ErrIdentity
		}
	}
	return nil
}

func sameManagedWorkspace(a, b workspaceConfig) bool {
	a.MemberDirectories, b.MemberDirectories = nil, nil
	return equalWorkspace(a, b)
}

func directoryOnlyOperation(op workspaceOperation) bool {
	return op.PreviousConfig != nil && op.PreviousConfig.Workspace != nil && sameManagedWorkspace(*op.PreviousConfig.Workspace, op.Target)
}

// Open and enumerate using the service identity, without creating probe files.
func readableDirectory(dir string) error {
	// Keep the final /. component: opening a directory itself may require read
	// permission without search permission. filepath.Join would remove this
	// component and silently weaken the admission check.
	f, err := os.Open(filepath.Clean(dir) + string(filepath.Separator) + ".")
	if err != nil {
		return directoryIOError(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return directoryIOError(err)
	}
	if !info.IsDir() {
		return directoryError("directory_not_directory")
	}
	_, err = f.Readdirnames(1)
	if err != nil && !errors.Is(err, io.EOF) {
		return directoryIOError(err)
	}
	return nil
}

func directoryIOError(err error) error {
	switch {
	case errors.Is(err, syscall.ENOTDIR):
		return directoryError("directory_not_directory")
	case errors.Is(err, os.ErrNotExist):
		return directoryError("directory_not_found")
	case errors.Is(err, os.ErrPermission):
		return directoryError("directory_permission_denied")
	case errors.Is(err, syscall.ENOTDIR):
		return directoryError("directory_not_directory")
	default:
		return directoryError("directory_unavailable")
	}
}

// A missing configured root stays configured. Every admission retries it; no
// unavailable root broadens policy or disables unrelated Rooms.
func (m *roomManager) canonicalWorkspaceDirectory(dir string, frozen bool) (string, error) {
	if !validDirectoryPath(dir) {
		return "", directoryError("directory_not_allowed")
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", directoryIOError(err)
	}
	real = filepath.Clean(real)
	if !validDirectoryPath(real) {
		return "", directoryError("directory_not_allowed")
	}
	if frozen && real != dir {
		return "", directoryError("directory_path_changed")
	}
	roots := m.workspaceRoots
	if roots == nil {
		home, err := os.UserHomeDir()
		if err != nil || !validDirectoryPath(home) || filepath.Dir(home) == home {
			return "", directoryError("directory_unavailable")
		}
		roots = &[]string{home}
	}
	allowed := false
	for _, root := range *roots {
		resolved, err := filepath.EvalSymlinks(root)
		if err == nil && pathWithin(resolved, real) && readableDirectory(resolved) == nil {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", directoryError("directory_not_allowed")
	}
	protected := append([]string{m.store.dir, m.teamsDir}, m.workspaceProtected...)
	for _, root := range protected {
		if root == "" {
			continue
		}
		resolved, err := resolveFutureDirectory(root)
		if err != nil {
			return "", directoryError("directory_unavailable")
		}
		if pathWithin(resolved, real) {
			return "", directoryError("directory_not_allowed")
		}
	}
	if err := readableDirectory(real); err != nil {
		return "", err
	}
	return real, nil
}

func (m *roomManager) normalizeWorkspaceDirectories(saved managedRoomConfig, target workspaceConfig, frozen bool) (workspaceConfig, error) {
	if err := validateMemberDirectories(target, saved.Members); err != nil {
		return workspaceConfig{}, err
	}
	// normalizeWorkspace owns the map before canonicalization.
	w, err := normalizeWorkspace(target, false)
	if err != nil {
		return workspaceConfig{}, err
	}
	for id, dir := range w.MemberDirectories {
		real, err := m.canonicalWorkspaceDirectory(dir, frozen)
		if err != nil {
			return workspaceConfig{}, err
		}
		w.MemberDirectories[id] = real
	}
	if err := m.checkWorkspaceDirectoryClaimsLocked(saved.RoomIdentity, saved.Scheduling, w); err != nil {
		return workspaceConfig{}, err
	}
	return w, nil
}

func absoluteDirectoriesOverlap(a, b string) bool { return pathWithin(a, b) || pathWithin(b, a) }

// The catalog and unsettled operations are the reservation index. Rebuilding
// from these authoritative records also catches conflicts after restart.
func (m *roomManager) checkWorkspaceDirectoryClaimsLocked(owner RoomIdentity, mode schedulingMode, target workspaceConfig) error {
	effective, err := mode.effective()
	if err != nil {
		return err
	}
	if len(target.MemberDirectories) == 0 {
		return nil
	}
	paths := []string{}
	for _, dir := range target.MemberDirectories {
		for _, previous := range paths {
			if absoluteDirectoriesOverlap(previous, dir) && (previous != dir || effective != schedulingSerial) {
				return directoryError("directory_conflict")
			}
		}
		paths = append(paths, dir)
	}
	conflicts := func(w *workspaceConfig) bool {
		if w == nil {
			return false
		}
		for _, other := range w.MemberDirectories {
			for _, dir := range paths {
				if absoluteDirectoriesOverlap(other, dir) {
					return true
				}
			}
		}
		return false
	}
	for _, entry := range m.index.Rooms {
		if entry.RoomIdentity == owner || entry.Status != "ready" && entry.Status != "deleting" {
			continue
		}
		// A deleting Room keeps its last references until removal is committed.
		c, err := m.readWorkspaceRoomConfig(entry)
		if errors.Is(err, os.ErrNotExist) && entry.Status == "deleting" {
			continue
		}
		if err != nil {
			return err
		}
		if conflicts(c.Workspace) {
			return directoryError("directory_conflict")
		}
	}
	for _, op := range m.index.WorkspaceOperations {
		if op.RoomIdentity == owner || op.finished() {
			continue
		}
		if conflicts(&op.Target) || op.PreviousConfig != nil && conflicts(op.PreviousConfig.Workspace) {
			return directoryError("directory_conflict")
		}
	}
	return nil
}

func (m *roomManager) validateEffectiveWorkspace(saved managedRoomConfig) error {
	if saved.Workspace == nil {
		return ErrInvalid
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, err := m.normalizeWorkspaceDirectories(saved, *saved.Workspace, true)
	return err
}

// Retain roles/environment in the snapshot. Only managed repository guidance
// is omitted from the effective external profile. Explicit platform tools keep
// their own connection and authorization.
func memberWorkspaceInstructions(w workspaceConfig, member managedMember) string {
	if w.MemberDirectories[member.MembershipID] == "" {
		return member.Instructions
	}
	paragraphs := strings.Split(member.Instructions, "\n\n")
	kept := paragraphs[:0]
	for _, p := range paragraphs {
		if strings.HasPrefix(p, "Workspace 提供本地 Git 与") || strings.HasPrefix(p, "当前 Workspace 是原生 Scion 工作副本。") || strings.HasPrefix(p, "OAuth 仓库网络操作使用") {
			continue
		}
		kept = append(kept, p)
	}
	return strings.Join(kept, "\n\n")
}

func memberWorkspaceExecutionChanged(dir string, before, after managedRoomConfig, index int) bool {
	a, b := before.Members[index], after.Members[index]
	return resolveMemberWorkspace(dir, *before.Workspace, a).Directory != resolveMemberWorkspace(dir, *after.Workspace, b).Directory ||
		memberWorkspaceInstructions(*before.Workspace, a) != memberWorkspaceInstructions(*after.Workspace, b) || !reflect.DeepEqual(a.ToolEnv, b.ToolEnv)
}

func memberWorkspaceToolEnv(w workspaceConfig, member managedMember) map[string]string {
	env := cloneToolEnv(member.ToolEnv)
	if w.MemberDirectories[member.MembershipID] != "" && w.Binding != nil && w.Binding.Provider == "gitlab" {
		delete(env, "GIT_ASKPASS")
		delete(env, "TEAMX_GITLAB_TOOLS")
		delete(env, "GIT_TERMINAL_PROMPT")
	}
	return env
}

type workspaceMemberDirectoryStatus struct {
	workspaceMemberPathRecord
	DefaultDirectory   string `json:"default_directory"`
	EffectiveDirectory string `json:"effective_directory"`
	DirectorySource    string `json:"directory_source"`
}

func verifyUnchangedWorkspaceSession(store *Store, id MemberIdentity, op workspaceOperation) error {
	ref, _, err := storedSessionBackend(store).reference(SessionOptions{MemberIdentity: id, StatePath: filepath.Join(store.dir, "agents", id.MembershipID, "session.json")})
	if err != nil {
		return err
	}
	if ref != op.RetainedSessions[id.MembershipID] {
		return ErrConflict
	}
	return nil
}
