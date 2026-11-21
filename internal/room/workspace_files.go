package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	workspaceFileTextBytes       = 1 << 20
	workspaceFileImageBytes      = 16 << 20
	workspaceFileImagePixels     = 25_000_000
	workspaceFileDownloadBytes   = 64 << 20
	workspaceFileAutoImages      = 20
	workspaceFileConcurrency     = 4
	workspaceFileDownloadTimeout = 30 * time.Second
	workspaceFilePathBytes       = 4096
	workspaceFileScanLimit       = 5000
	workspaceFileEntryLimit      = 1000
	workspaceFileJSONBytes       = 256 << 10
)

// A stable description of an authorized directory, never an access credential.
// Keep this pure: the execution binding and the file browser use the same input.
func workspaceRootID(id RoomIdentity, membership, directory string) string {
	b, _ := json.Marshal([]string{"wr1", id.RoomID, id.RoomInstance, membership, directory})
	sum := sha256.Sum256(b)
	return "wr1_" + hex.EncodeToString(sum[:])
}

type workspaceFileError string

func (e workspaceFileError) Error() string { return string(e) }
func fileError(code string) error          { return workspaceFileError(code) }
func fileIOError(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return fileError("file_not_found")
	}
	return fileError("file_not_allowed")
}

type workspaceFileSource struct {
	MembershipID    string `json:"membership_id"`
	Label           string `json:"label"`
	DirectorySource string `json:"directory_source"`
	RootID          string `json:"root_id,omitempty"`
	// SharedKey groups identical canonical directories without disclosing paths.
	SharedKey         string `json:"shared_key,omitempty"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type workspaceFileEntry struct {
	Name  string    `json:"name"`
	Path  string    `json:"path"`
	Kind  string    `json:"kind"`
	Size  int64     `json:"size"`
	Mtime time.Time `json:"mtime"`
}
type workspaceFileListing struct {
	Path      string               `json:"path"`
	Entries   []workspaceFileEntry `json:"entries"`
	Truncated bool                 `json:"truncated"`
}
type workspaceFilePreview struct {
	Path              string    `json:"path"`
	Name              string    `json:"name"`
	Size              int64     `json:"size"`
	Mtime             time.Time `json:"mtime"`
	MIME              string    `json:"mime"`
	PreviewKind       string    `json:"preview_kind"`
	Text              string    `json:"text,omitempty"`
	Reason            string    `json:"reason,omitempty"`
	CurrentContent    bool      `json:"current_content"`
	DownloadAvailable bool      `json:"download_available"`
	ContentURL        string    `json:"content_url,omitempty"`
	Width             int       `json:"width,omitempty"`
	Height            int       `json:"height,omitempty"`
}

type workspaceFileRoot struct {
	root      *os.Root
	info      os.FileInfo
	directory string
	protected []string
}

func (f *workspaceFileRoot) Close() { _ = f.root.Close() }

// API paths have already undergone exactly one standard query decoding. They
// are filesystem paths, not URLs or shell expressions; never decode them again.
func workspaceFilePathCharacters(p string) bool {
	return len(p) <= workspaceFilePathBytes && utf8.ValidString(p) && !strings.ContainsAny(p, "\\:") && !strings.HasPrefix(p, "/") && strings.IndexFunc(p, unicode.IsControl) < 0
}
func validWorkspaceFilePath(p string) bool {
	if !workspaceFilePathCharacters(p) {
		return false
	}
	if p == "" || p == "." {
		return true
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}
func (f *workspaceFileRoot) allowed(p string) bool {
	if !validWorkspaceFilePath(p) {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if strings.EqualFold(part, ".git") || strings.EqualFold(part, ".teamx") {
			return false
		}
	}
	absolute := filepath.Join(f.directory, filepath.FromSlash(p))
	for _, protected := range f.protected {
		if pathWithin(protected, absolute) {
			return false
		}
	}
	return true
}

// Walk through pinned directory handles. A component may only be followed if
// its opened inode matches the preceding non-symlink lstat. Symlinks are read
// explicitly and traversal restarts from the root, applying protection again.
// This also prevents an intermediate symlink swap into a protected subtree.
func (f *workspaceFileRoot) open(p string) (*os.File, error) {
	if !f.allowed(p) {
		return nil, fileError("file_not_allowed")
	}
	p = path.Clean(p)
	for links := 0; links < 40; links++ {
		current := f.root
		var owned *os.Root
		closeOwned := func() {
			if owned != nil {
				_ = owned.Close()
				owned = nil
			}
		}
		parts := slices.DeleteFunc(strings.Split(p, "/"), func(part string) bool { return part == "" || part == "." })
		if len(parts) == 0 {
			parts = []string{"."}
		}
		restart := false
		for i, part := range parts {
			if part == ".." {
				// Earlier components were already resolved through pinned
				// handles. Resolve a parent only now, after any prior links.
				base := path.Clean(strings.Join(parts[:i], "/"))
				closeOwned()
				if base == "." || base == "" {
					return nil, fileError("file_not_allowed")
				}
				p = path.Dir(base) + "/" + strings.Join(parts[i+1:], "/")
				restart = true
				break
			}
			if !f.allowed(strings.Join(parts[:i+1], "/")) {
				closeOwned()
				return nil, fileError("file_not_allowed")
			}
			info, err := current.Lstat(part)
			if err != nil {
				closeOwned()
				return nil, fileIOError(err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				target, err := current.Readlink(part)
				closeOwned()
				if err != nil {
					return nil, fileIOError(err)
				}
				if strings.Contains(target, "\\") {
					return nil, fileError("file_not_allowed")
				}
				base := strings.Join(parts[:i], "/")
				if filepath.IsAbs(target) {
					prefix := strings.TrimSuffix(f.directory, string(filepath.Separator)) + string(filepath.Separator)
					if target == f.directory {
						target = "."
					} else if strings.HasPrefix(target, prefix) {
						target = strings.TrimPrefix(target, prefix)
					} else {
						return nil, fileError("file_not_allowed")
					}
					base = ""
				}
				// Preserve unresolved parents: a/../x must resolve a before
				// its parent, since a may itself be a symbolic link.
				p = strings.TrimPrefix(base+"/"+filepath.ToSlash(target)+"/"+strings.Join(parts[i+1:], "/"), "/")
				if !workspaceFilePathCharacters(p) {
					return nil, fileError("file_not_allowed")
				}
				restart = true
				break
			}
			if i < len(parts)-1 {
				if !info.IsDir() {
					closeOwned()
					return nil, fileError("file_not_allowed")
				}
				next, err := current.OpenRoot(part)
				if err != nil {
					closeOwned()
					return nil, fileIOError(err)
				}
				opened, err := next.Stat(".")
				if err != nil || !os.SameFile(info, opened) {
					_ = next.Close()
					closeOwned()
					return nil, fileError("file_changed")
				}
				closeOwned()
				owned = next
				current = next
				continue
			}
			if !info.IsDir() && !info.Mode().IsRegular() {
				closeOwned()
				return nil, fileError("file_not_allowed")
			}
			opened, err := openWorkspaceFile(current, part)
			closeOwned()
			if err != nil {
				return nil, fileIOError(err)
			}
			actual, err := opened.Stat()
			if err != nil || !os.SameFile(info, actual) || (!actual.IsDir() && !actual.Mode().IsRegular()) {
				_ = opened.Close()
				return nil, fileError("file_changed")
			}
			return opened, nil
		}
		closeOwned()
		if !restart {
			break
		}
	}
	return nil, fileError("file_not_allowed")
}

func (f *workspaceFileRoot) unchanged(p string, opened os.FileInfo) error {
	// Check the root pathname still identifies the pinned root, then check the
	// complete traversal again before admitting any bytes to the HTTP response.
	real, err := filepath.EvalSymlinks(f.directory)
	if err != nil || real != f.directory {
		return fileError("workspace_source_changed")
	}
	rootInfo, err := os.Stat(real)
	if err != nil || !os.SameFile(rootInfo, f.info) {
		return fileError("workspace_source_changed")
	}
	again, err := f.open(p)
	if err != nil {
		return fileError("file_changed")
	}
	defer again.Close()
	now, err := again.Stat()
	if err != nil || !os.SameFile(opened, now) || opened.Size() != now.Size() || !opened.ModTime().Equal(now.ModTime()) {
		return fileError("file_changed")
	}
	return nil
}

func (f *workspaceFileRoot) list(ctx context.Context, p string, hidden bool) (workspaceFileListing, error) {
	result := workspaceFileListing{Path: p, Entries: []workspaceFileEntry{}}
	dir, err := f.open(p)
	if err != nil {
		return result, err
	}
	defer dir.Close()
	before, err := dir.Stat()
	if err != nil || !before.IsDir() {
		return result, fileError("file_not_allowed")
	}
	names, err := dir.Readdirnames(workspaceFileScanLimit + 1)
	if err != nil && err != io.EOF {
		return result, fileIOError(err)
	}
	if len(names) > workspaceFileScanLimit {
		names = names[:workspaceFileScanLimit]
		result.Truncated = true
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		rel := path.Join(p, name)
		entry, err := f.open(rel)
		if err != nil {
			continue
		}
		info, err := entry.Stat()
		_ = entry.Close()
		if err != nil {
			continue
		}
		kind := "file"
		if info.IsDir() {
			kind = "directory"
		}
		result.Entries = append(result.Entries, workspaceFileEntry{Name: name, Path: rel, Kind: kind, Size: info.Size(), Mtime: info.ModTime()})
	}
	slices.SortFunc(result.Entries, func(a, b workspaceFileEntry) int {
		if a.Kind != b.Kind {
			if a.Kind == "directory" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Name, b.Name)
	})
	if len(result.Entries) > workspaceFileEntryLimit {
		result.Entries = result.Entries[:workspaceFileEntryLimit]
		result.Truncated = true
	}
	// Bound escaped JSON, including long Unicode names and metadata.
	entries := result.Entries
	result.Entries = []workspaceFileEntry{}
	size := len(p)*6 + (8 << 10)
	for _, entry := range entries {
		b, _ := json.Marshal(entry)
		if size+len(b)+1 > workspaceFileJSONBytes {
			result.Truncated = true
			break
		}
		result.Entries = append(result.Entries, entry)
		size += len(b) + 1
	}
	return result, f.unchanged(p, before)
}

type contextFileReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextFileReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}
