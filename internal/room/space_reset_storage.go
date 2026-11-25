package room

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// These are Home-owned business stores, including migration preimages. Data
// and Teams are entirely managed business roots, with explicit protected paths.
// Native macOS CLI profiles and Keychain are deliberately outside this list.
var spaceResetHomeEntries = []string{
	"model-credentials", "model-validations", "model-operations", "model-setup.json",
	"model-migration.json", "model-migration-backup", "platform-tmp",
}

type resetFileIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}
type spaceResetCandidate struct {
	Version   int                          `json:"version"`
	RequestID string                       `json:"request_id"`
	Before    string                       `json:"before_sha256"`
	After     string                       `json:"after_sha256"`
	Roots     map[string]resetFileIdentity `json:"roots"`
}
type spaceResetStorage struct {
	record                          SpaceRecord
	configPath, seed, candidatePath string
	protected                       []string
	roots                           map[string]resetFileIdentity
}

func (s *spaceServer) resetStorage(record SpaceRecord) (*spaceResetStorage, error) {
	home := s.assembly.roots.Home
	path := filepath.Join(record.Roots.Home, "config.toml")
	if record.ID == s.access.space.SpaceID {
		path = s.assembly.config.settings.path
	}
	seed := s.assembly.config.resetTeamsSource
	if seed == "" {
		return nil, &requestDiagnostic{cause: ErrRequest, Message: "缺少当前版本的初始模板。源码部署请指定 --reset-teams-source；发行版请核对 share/teamx/teams。"}
	}
	canonical, err := resolveFutureDirectory(seed)
	if err != nil || canonical != filepath.Clean(seed) {
		return nil, resetPathError()
	}
	storage := &spaceResetStorage{record: record, configPath: path, seed: seed,
		candidatePath: filepath.Join(home, "space-reset", record.ID, record.ResetID(), "candidate.json"), roots: map[string]resetFileIdentity{}}
	protected, err := s.resetProtection(record, path)
	if err != nil {
		return nil, err
	}
	storage.protected = protected

	s.mu.Lock()
	for _, other := range s.directory.Spaces {
		if other.ID == record.ID {
			continue
		}
		for _, root := range []string{other.Roots.Home, other.Roots.Data, other.Roots.Teams} {
			// Home naturally contains the additional Spaces. Business roots may not.
			if root == other.Roots.Home && pathWithin(root, record.Roots.Home) {
				continue
			} // registered child Space of the personal deployment Home
			if absoluteDirectoriesOverlap(record.Roots.Data, root) || absoluteDirectoriesOverlap(record.Roots.Teams, root) {
				s.mu.Unlock()
				return nil, resetPathError()
			}
		}
	}
	s.mu.Unlock()

	parent, err := resolveFutureDirectory(filepath.Dir(storage.configPath))
	if err != nil {
		return nil, err
	}
	storage.configPath = filepath.Join(parent, filepath.Base(storage.configPath))
	if err := storage.validate(); err != nil {
		return nil, err
	}
	if err := validateResetSeed(seed); err != nil {
		return nil, resetConfigDiagnostic(err)
	}
	return storage, nil
}

func (s *spaceServer) resetProtection(record SpaceRecord, path string) ([]string, error) {
	home := s.assembly.roots.Home
	s.access.mu.Lock()
	deployment := s.access.config
	s.access.mu.Unlock()
	var protected []string
	// Both possible installation-id locations are protected. Some foreground
	// configurations keep admin_token_file inside a business directory.
	protected = []string{path, adminTokenPath(deployment)}
	for _, root := range s.assembly.config.workspaceProtected {
		if root != "" && !pathWithin(root, record.Roots.Home) {
			protected = append(protected, root)
		}
	}
	if manifest := deployment.Deployment.Execution.Manifest; manifest != "" {
		protected = append(protected, manifest)
	}
	for _, base := range []string{home, record.Roots.Home} {
		for _, name := range []string{"installation-id", "personal-space-registration.json", "multi-user-registration.json", "users.json", "spaces.json", "auth", "first-access.json", "workspace-claims.json", "computer-state", "computer-credentials", "space-reset", "execution", "gitlab-applications", "user-credentials"} {
			protected = append(protected, filepath.Join(base, name))
		}
	}
	protected = append(protected, filepath.Join(s.assembly.roots.Data, "installation-id"))
	for i, p := range protected {
		parent, err := resolveFutureDirectory(filepath.Dir(p))
		if err != nil {
			return nil, resetPathError()
		}
		protected[i] = filepath.Join(parent, filepath.Base(p))
	}
	return protected, nil
}

func resetPathError() error {
	return &requestDiagnostic{cause: ErrRequest, Message: "空间路径、保护文件或模板位置无法安全清理，请先核对部署目录。"}
}
func (r SpaceRecord) ResetID() string {
	if r.Reset != nil {
		return r.Reset.RequestID
	}
	return ""
}
func (r SpaceRecord) resetting() bool {
	return r.Reset != nil && (!r.Reset.valid() || r.Reset.Phase != "completed")
}

func (x *spaceResetStorage) validate() error {
	r := x.record.Roots
	if r.Home == "" || r.Data == "" || r.Teams == "" || pathWithin(r.Data, r.Home) || pathWithin(r.Teams, r.Home) || absoluteDirectoriesOverlap(r.Data, r.Teams) {
		return resetPathError()
	}
	for _, root := range []string{r.Home, r.Data, r.Teams} {
		real, err := resolveFutureDirectory(root)
		if err != nil || real != root || !filepath.IsAbs(root) || root == string(filepath.Separator) {
			return resetPathError()
		}
		if absoluteDirectoriesOverlap(root, x.seed) {
			return resetPathError()
		}
		info, err := os.Lstat(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			return resetPathError()
		}
		identity, err := resetIdentity(info)
		if err != nil {
			return err
		}
		if before, ok := x.roots[root]; ok && before != identity {
			return resetPathError()
		}
		x.roots[root] = identity
	}
	for _, p := range append(append([]string{}, x.protected...), filepath.Join(r.Home, ".bundle.lock"), filepath.Join(r.Data, ".lock"), filepath.Join(r.Teams, ".lock")) {
		real, err := resolveFutureDirectory(filepath.Dir(p))
		if err != nil || real != filepath.Dir(p) {
			return resetPathError()
		}
		if info, err := os.Lstat(p); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return resetPathError()
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, target := range x.targets() {
		if err := x.inspect(target); err != nil {
			return err
		}
	}
	return nil
}

func (x *spaceResetStorage) targets() []string {
	r := x.record.Roots
	result := []string{r.Data, r.Teams}
	for _, name := range spaceResetHomeEntries {
		result = append(result, filepath.Join(r.Home, name))
	}
	// Only our private CLI storage is eligible; native macOS profiles live elsewhere.
	result = append(result, filepath.Join(r.Home, "platform-cli", "lark"))
	return result
}
func (x *spaceResetStorage) protectedPath(path string) bool {
	if path == filepath.Join(x.record.Roots.Data, ".lock") || path == filepath.Join(x.record.Roots.Teams, ".lock") {
		return true
	}
	for _, keep := range x.protected {
		if pathWithin(keep, path) {
			return true
		}
	}
	return false
}
func (x *spaceResetStorage) containsProtection(path string) bool {
	for _, root := range []string{x.record.Roots.Home, x.record.Roots.Data, x.record.Roots.Teams} {
		if path == root {
			return true
		}
	}
	for _, keep := range x.protected {
		if pathWithin(path, keep) {
			return true
		}
	}
	return false
}

// WalkDir never follows nested links. Crossing a mount within a deletion target
// is rejected before any deletion, including when the mount contains secrets.
func (x *spaceResetStorage) inspect(path string) error {
	real, err := resolveFutureDirectory(filepath.Dir(path))
	if err != nil || real != filepath.Dir(path) {
		return resetPathError()
	}
	parent, err := os.Stat(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	device, err := resetIdentity(parent)
	if err != nil {
		return err
	}
	if registered, ok := x.roots[path]; ok {
		device = registered
	}
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) && p == path {
			return nil
		}
		if err != nil {
			return err
		}
		if x.protectedPath(p) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		id, err := resetIdentity(info)
		if err != nil {
			return err
		}
		if id.Device != device.Device {
			return resetPathError()
		}
		if p == path && d.Type()&os.ModeSymlink != 0 {
			return resetPathError()
		}
		return nil
	})
}

func (x *spaceResetStorage) clean() error {
	if err := x.validate(); err != nil {
		return err
	}
	for _, path := range x.targets() {
		if err := x.remove(path); err != nil {
			return err
		}
	}
	// Crashed atomic writers in Home can contain an old full configuration.
	entries, err := os.ReadDir(x.record.Roots.Home)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".config-") || strings.HasPrefix(e.Name(), ".snapshot-") || strings.HasPrefix(e.Name(), ".init-") {
			if err := x.remove(filepath.Join(x.record.Roots.Home, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// Anchor deletion to the verified root descriptor. Each directory is opened
// and compared with its Lstat identity before descent; links are only unlinked.
func (x *spaceResetStorage) remove(path string) error {
	rootPath := x.record.Roots.Home
	for _, candidate := range []string{x.record.Roots.Data, x.record.Roots.Teams} {
		if pathWithin(candidate, path) {
			rootPath = candidate
			break
		}
	}
	root, err := os.OpenRoot(rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	id, err := resetIdentity(info)
	if err != nil {
		return err
	}
	if expected, ok := x.roots[rootPath]; !ok || expected != id {
		return resetPathError()
	}
	rel, err := filepath.Rel(rootPath, path)
	if err != nil || !filepath.IsLocal(rel) {
		return resetPathError()
	}
	if rel == "." {
		return x.removeChildren(root, rootPath, id.Device)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		info, err := root.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return resetPathError()
		}
		child, err := root.OpenRoot(part)
		if err != nil {
			return err
		}
		defer child.Close()
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return resetPathError()
		}
		root = child
	}
	return x.removeEntry(root, parts[len(parts)-1], path, id.Device)
}
func (x *spaceResetStorage) removeChildren(root *os.Root, path string, device uint64) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := x.removeEntry(root, e.Name(), filepath.Join(path, e.Name()), device); err != nil {
			return err
		}
	}
	return syncResetRoot(root)
}
func syncResetRoot(root *os.Root) error {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}
func (x *spaceResetStorage) removeEntry(parent *os.Root, name, path string, device uint64) error {
	if x.protectedPath(path) {
		return nil
	}
	info, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	id, err := resetIdentity(info)
	if err != nil {
		return err
	}
	if id.Device != device {
		return resetPathError()
	}
	if info.IsDir() {
		child, err := parent.OpenRoot(name)
		if err != nil {
			return err
		}
		defer child.Close()
		opened, err := child.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			return resetPathError()
		}
		if err := x.removeChildren(child, path, device); err != nil {
			return err
		}
		if x.containsProtection(path) {
			return nil
		}
	}
	if err := parent.Remove(name); err != nil {
		return err
	}
	return syncResetRoot(parent)
}

func validateResetSeed(source string) error {
	count := 0
	err := filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return ErrInvalid
		}
		if d.Name() == "team.json" {
			count++
		}
		return nil
	})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrInvalid
	}
	defaults := &teamRuntime{ManagedModels: true}
	defaults.DSH.BinaryPath = "/reset-template-check"
	teams, err := loadTeams(source, defaults)
	if err != nil {
		return err
	}
	if len(teams) == 0 {
		return ErrInvalid
	}
	return nil
}
func copyResetTemplates(source, destination string, expected resetFileIdentity) error {
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	id, err := resetIdentity(info)
	if err != nil || id != expected {
		return resetPathError()
	}
	// Only this initializer creates these top-level temporary template files.
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, err := f.ReadDir(-1)
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".init-") {
			if entry.IsDir() {
				return ErrInvalid
			}
			if err := root.Remove(entry.Name()); err != nil {
				return err
			}
		}
	}
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
		if d.IsDir() {
			return root.MkdirAll(rel, 0700)
		}
		if !d.Type().IsRegular() {
			return ErrInvalid
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if info, err := root.Lstat(rel); err == nil {
			if !info.Mode().IsRegular() {
				return ErrInvalid
			}
			f, err := root.Open(rel)
			if err != nil {
				return err
			}
			opened, err := f.Stat()
			if err != nil || !os.SameFile(info, opened) {
				f.Close()
				return ErrConflict
			}
			actual, err := io.ReadAll(io.LimitReader(f, int64(len(data))+1))
			if err == nil {
				err = f.Sync()
			}
			err = errors.Join(err, f.Close())
			if err != nil {
				return err
			}
			if !bytes.Equal(actual, data) {
				return fmt.Errorf("reset template differs: %w", ErrConflict)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			id, err := newOpaqueID()
			if err != nil {
				return err
			}
			tmp := ".init-" + id
			f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if err != nil {
				return err
			}
			defer root.Remove(tmp)
			_, writeErr := f.Write(data)
			if writeErr == nil {
				writeErr = f.Sync()
			}
			writeErr = errors.Join(writeErr, f.Close())
			if writeErr != nil {
				return writeErr
			}
			if err := root.Link(tmp, rel); err != nil {
				return err
			}
			if err := root.Remove(tmp); err != nil {
				return err
			}
		} else {
			return err
		}
		parent, err := root.Open(filepath.Dir(rel))
		if err != nil {
			return err
		}
		if err := errors.Join(parent.Sync(), parent.Close()); err != nil {
			return err
		}
		return syncResetRoot(root)
	})
}

func (x *spaceResetStorage) saveCandidate(doc *configDocument) error {
	doc.mu.Lock()
	defer doc.mu.Unlock()
	if doc.pending != nil {
		if err := doc.reconcile(); err != nil {
			return err
		}
	}
	if err := doc.unchanged(); err != nil {
		return err
	}
	next, err := spaceResetConfig(doc.source)
	if err != nil {
		return err
	}
	var parsed startupConfig
	if err := toml.Unmarshal(next, &parsed); err != nil {
		return err
	}
	if real, err := resolveFutureDirectory(filepath.Dir(x.candidatePath)); err != nil || real != filepath.Dir(x.candidatePath) {
		return resetPathError()
	}
	if err := makeDurableDir(filepath.Dir(x.candidatePath)); err != nil {
		return err
	}
	candidate := spaceResetCandidate{Version: 1, RequestID: x.record.ResetID(), Before: modelMigrationDigest(doc.source), After: modelMigrationDigest(next), Roots: x.roots}
	return (&snapshotWriter{ops: defaultSnapshotOps()}).save(x.candidatePath, candidate, 4<<20)
}
func (x *spaceResetStorage) loadCandidate() (spaceResetCandidate, error) {
	var candidate spaceResetCandidate
	if real, err := resolveFutureDirectory(filepath.Dir(x.candidatePath)); err != nil || real != filepath.Dir(x.candidatePath) {
		return candidate, resetPathError()
	}
	err := readIdentitySnapshot(x.candidatePath, &candidate)
	if err != nil {
		return candidate, err
	}
	if candidate.Version != 1 || candidate.RequestID != x.record.ResetID() || candidate.Before == "" || candidate.After == "" {
		return candidate, ErrInvalid
	}
	for root, id := range candidate.Roots {
		if current, ok := x.roots[root]; !ok || current != id {
			return candidate, resetPathError()
		}
	}
	return candidate, nil
}
func (x *spaceResetStorage) initialize(candidate spaceResetCandidate) error {
	if err := x.validate(); err != nil {
		return err
	}
	if err := makeDurableDir(x.record.Roots.Teams); err != nil {
		return err
	}
	if err := x.validate(); err != nil {
		return err
	}
	data, file, err := readSettingsFile(x.configPath)
	if err != nil {
		return err
	}
	if modelMigrationDigest(data) != candidate.After && modelMigrationDigest(data) != candidate.Before {
		return fmt.Errorf("config changed before reset initialization: %w", ErrConflict)
	}
	next, err := spaceResetConfig(data)
	if err != nil {
		return err
	}
	if modelMigrationDigest(next) != candidate.After {
		return fmt.Errorf("reset candidate fingerprint differs: %w", ErrConflict)
	}
	doc := &configDocument{path: x.configPath, source: data, file: file, private: true, ops: defaultSnapshotOps()}
	doc.mu.Lock()
	err = doc.commitLocked(func([]byte) ([]byte, func(), error) { return next, func() {}, nil })
	doc.mu.Unlock()
	if err != nil {
		return err
	}
	if err := copyResetTemplates(x.seed, x.record.Roots.Teams, x.roots[x.record.Roots.Teams]); err != nil {
		return err
	}
	// Initialization never cleans again. Only verify that business stores remain
	// empty (retained lock files and protected siblings are expected).
	for _, path := range x.targets() {
		if path == x.record.Roots.Teams {
			continue
		}
		if err := filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) && p == path {
				return nil
			}
			if err != nil {
				return err
			}
			if x.protectedPath(p) {
				if d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				return fmt.Errorf("business material remains: %w", ErrConflict)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
