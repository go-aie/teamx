package room

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

// This is the existing packaging inventory wire format, including link hashes.
type serviceAsset struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode,omitempty"`
}

func serviceHash(r io.Reader) (string, error) {
	h := sha256.New()
	_, err := io.Copy(h, r)
	return hex.EncodeToString(h.Sum(nil)), err
}

func serviceInventory(root string) ([]serviceAsset, error) {
	var err error
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	items := []serviceAsset{}
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		i := serviceAsset{Path: filepath.ToSlash(rel)}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode().IsRegular():
			i.Kind, i.Mode = "file", uint32(info.Mode().Perm())
			f, err := os.Open(name)
			if err != nil {
				return err
			}
			i.SHA256, err = serviceHash(f)
			f.Close()
			if err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			i.Kind = "symlink"
			i.Target, err = os.Readlink(name)
			if err != nil {
				return err
			}
			resolved, err := filepath.EvalSymlinks(name)
			if err != nil || filepath.IsAbs(i.Target) || !pathWithin(root, resolved) {
				return fmt.Errorf("unsupported or escaping symlink: %s", rel)
			}
			i.SHA256, _ = serviceHash(strings.NewReader(i.Target))
		default:
			return fmt.Errorf("unsupported file: %s", rel)
		}
		items = append(items, i)
		return nil
	})
	sort.Slice(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return items, err
}

func verifyServiceRelease(dir string) (string, error) {
	var manifest struct {
		Schema  int    `json:"schema_version"`
		Version string `json:"version"`
		Target  string `json:"target"`
		Profile string `json:"profile"`
	}
	// Release manifests also contain provenance, dependency and acceptance
	// fields owned by packaging. They are extensible, unlike private snapshots.
	f, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return "", err
	}
	dec := json.NewDecoder(io.LimitReader(f, 8<<20))
	decodeErr := dec.Decode(&manifest)
	var trailing any
	if decodeErr == nil && dec.Decode(&trailing) != io.EOF {
		decodeErr = errors.New("invalid manifest trailing data")
	}
	if err := errors.Join(decodeErr, f.Close()); err != nil {
		return "", err
	}
	if manifest.Schema != 1 || manifest.Profile != "standard" || manifest.Target != runtime.GOOS+"-"+runtime.GOARCH || !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,150}$`).MatchString(manifest.Version) {
		return "", errors.New("service requires a standard archive for this native platform with a valid version")
	}
	var expected []serviceAsset
	if err := readSnapshot(filepath.Join(dir, "checksums.txt"), &expected, 32<<20); err != nil {
		return "", err
	}
	// The Node packager emits directory traversal order. Compare by path so
	// files such as foo.js and foo/child do not depend on global sort order.
	sort.Slice(expected, func(i, j int) bool { return expected[i].Path < expected[j].Path })
	actual, err := serviceInventory(dir)
	if err != nil {
		return "", err
	}
	filtered := []serviceAsset{}
	for _, item := range actual {
		if item.Path == "checksums.txt" {
			continue
		}
		for _, component := range strings.Split(item.Path, "/") {
			if component == ".teamx" || component == ".git" || component == ".local" || component == ".env" || strings.HasPrefix(component, ".env.") {
				return "", fmt.Errorf("private state in archive: %s", item.Path)
			}
		}
		for _, dir := range []string{"model-credentials/", "model-operations/", "model-migration-backup/", "auth/"} {
			if strings.HasPrefix(item.Path, dir) {
				return "", fmt.Errorf("private state in archive: %s", item.Path)
			}
		}
		filtered = append(filtered, item)
	}
	if !reflect.DeepEqual(expected, filtered) {
		return "", errors.New("archive inventory, checksum, mode or link mismatch")
	}
	for _, name := range []string{"start", "bin/teamx", "bin/dsh", "bin/glab", "bin/lark-cli", "runtime/node/bin/node"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
			return "", fmt.Errorf("missing executable: %s", name)
		}
	}
	for _, name := range []string{"LICENSE", "THIRD_PARTY_NOTICES", "share/teamx/teams"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			return "", fmt.Errorf("missing release material %s: %w", name, err)
		}
	}
	return manifest.Version + "-" + manifest.Target + "-standard", nil
}

// Decode into a fresh private stage. Links are created only after all regular
// files, so no archive entry can traverse a link created by an earlier entry.
func unpackServiceArchive(archive, stage, digest string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	actual, err := serviceHash(f)
	if err != nil {
		return "", err
	}
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(digest) || actual != digest {
		return "", errors.New("archive SHA-256 mismatch")
	}
	if _, err := f.Seek(0, 0); err != nil {
		return "", err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := map[string]bool{}
	var top string
	type link struct{ name, target string }
	var links []link
	var total int64
	for count := 0; ; count++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || strings.ContainsAny(name, "\\\r\n\x00") || strings.HasPrefix(name, "../") || name == ".." || count > 500000 || h.Size < 0 || h.Mode&07000 != 0 {
			return "", fmt.Errorf("invalid archive entry: %q", h.Name)
		}
		root := strings.Split(name, "/")[0]
		if top == "" {
			top = root
		}
		if root != top || seen[name] {
			return "", errors.New("archive must have one root and unique entries")
		}
		seen[name] = true
		total += h.Size
		if total > 20<<30 {
			return "", errors.New("archive exceeds 20 GiB extraction limit")
		}
		dest := filepath.Join(stage, filepath.FromSlash(name))
		if !pathWithin(stage, dest) {
			return "", errors.New("archive path escapes stage")
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
				return "", err
			}
			out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return "", err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if err := errors.Join(copyErr, closeErr, os.Chmod(dest, fs.FileMode(h.Mode)&0777)); err != nil {
				return "", err
			}
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) || strings.ContainsAny(h.Linkname, "\\\r\n\x00") || !pathWithin(filepath.Join(stage, top), filepath.Join(filepath.Dir(dest), h.Linkname)) {
				return "", errors.New("archive link escapes release")
			}
			links = append(links, link{dest, h.Linkname})
		default:
			return "", fmt.Errorf("unsupported archive entry type: %s", name)
		}
	}
	// Consume the gzip trailer, including its CRC, before publication.
	if _, err := io.Copy(io.Discard, gz); err != nil {
		return "", err
	}
	for _, l := range links {
		if err := os.MkdirAll(filepath.Dir(l.name), 0755); err != nil {
			return "", err
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(l.name))
		stageRoot, rootErr := filepath.EvalSymlinks(filepath.Join(stage, top))
		if err != nil || rootErr != nil || !pathWithin(stageRoot, parent) {
			return "", errors.New("archive link parent escapes release")
		}
		if err := os.Symlink(l.target, l.name); err != nil {
			return "", err
		}
	}
	if top == "" {
		return "", errors.New("empty archive")
	}
	return filepath.Join(stage, top), nil
}

func publishServiceArchive(root, archive, digest string) (string, error) {
	stage, err := os.MkdirTemp(root, ".unpack-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	bundle, err := unpackServiceArchive(archive, stage, digest)
	if err != nil {
		return "", err
	}
	id, err := verifyServiceRelease(bundle)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(root, "releases"), 0755); err != nil {
		return "", err
	}
	dest := filepath.Join(root, "releases", id+"-"+digest[:12])
	if _, err := os.Lstat(dest); err == nil {
		if _, err := verifyServiceRelease(dest); err != nil {
			return "", err
		}
		a, err := serviceInventory(dest)
		if err != nil {
			return "", err
		}
		b, err := serviceInventory(bundle)
		if err != nil || !reflect.DeepEqual(a, b) {
			return "", errors.New("build ID already contains different content")
		}
		return dest, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(bundle, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func selectServiceRelease(root, release string) error {
	if filepath.Dir(release) != filepath.Join(root, "releases") {
		return errors.New("invalid release selection")
	}
	tmp, err := os.MkdirTemp(root, ".select-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	link := filepath.Join(tmp, "current")
	if err := os.Symlink(filepath.Join("releases", filepath.Base(release)), link); err != nil {
		return err
	}
	return os.Rename(link, filepath.Join(root, "current"))
}

func writeServiceJSON(name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return writeServiceFile(name, append(b, '\n'), 0600)
}

func writeServiceFile(name string, data []byte, mode fs.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".service-write-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(data)
	err = errors.Join(writeErr, f.Chmod(mode), f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}
