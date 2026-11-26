//go:build darwin || linux

package room

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// A reference names a complete, verified program archive, never an individual
// runtime file. Backups own read-only links under programs/, independently of
// installed releases and the disposable build cache.
type serviceProgram struct {
	Release string `json:"release"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

var serviceProgramDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

var serviceProgramRelease = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,240}$`)

func (p serviceProgram) valid() bool {
	return serviceProgramRelease.MatchString(p.Release) && serviceProgramDigest.MatchString(p.SHA256) && p.Size > 0
}

func (p serviceProgram) archive(dir string) string { return filepath.Join(dir, p.SHA256+".tar.gz") }

func privateServiceProgramDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := servicePhysicalDirectory(dir); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("program store must be private: %s", dir)
	}
	return nil
}

func checkServiceProgramFile(name string, p serviceProgram) error {
	if !p.valid() {
		return errors.New("invalid program archive reference")
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || info.Size() != p.Size {
		return fmt.Errorf("program archive size, mode or type mismatch: %s", name)
	}
	return nil
}

// Read and hash bytes only on admission. Later snapshots link the protected
// object; recovery checks its SHA-256 and complete release inventory again.
func copyServiceProgram(ctx context.Context, source, dir string, p serviceProgram) error {
	if err := privateServiceProgramDir(dir); err != nil {
		return err
	}
	dest := p.archive(dir)
	if _, err := os.Lstat(dest); err == nil {
		return checkServiceProgramFile(dest, p)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(dir, ".program-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), serviceBackupReader{ctx, in})
	if err != nil {
		return err
	}
	if n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return errors.New("program archive SHA-256 mismatch")
	}
	if err := errors.Join(out.Chmod(0400), out.Sync()); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// Link rather than overwrite an object another snapshot already owns.
	if err := os.Link(out.Name(), dest); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	if err := checkServiceProgramFile(dest, p); err != nil {
		return err
	}
	return syncDirectory(dir)
}

func registerServiceProgram(ctx context.Context, root, release, archive, digest string) error {
	info, err := os.Stat(archive)
	if err != nil {
		return err
	}
	p := serviceProgram{Release: filepath.Base(release), SHA256: digest, Size: info.Size()}
	store := filepath.Join(root, "programs")
	if err := copyServiceProgram(ctx, archive, store, p); err != nil {
		return err
	}
	if err := privateServiceProgramDir(filepath.Join(store, "releases")); err != nil {
		return err
	}
	return writeServiceJSON(filepath.Join(store, "releases", p.Release+".json"), p)
}

func installedServiceProgram(root, release string) (serviceProgram, error) {
	var p serviceProgram
	store := filepath.Join(root, "programs")
	for _, dir := range []string{store, filepath.Join(store, "releases")} {
		if err := privateServiceProgramDir(dir); err != nil {
			return p, err
		}
	}
	record := filepath.Join(store, "releases", release+".json")
	info, err := os.Lstat(record)
	if err != nil {
		return p, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return p, errors.New("invalid program registration file")
	}
	if err := readSnapshot(record, &p, maxPrivateFileBytes); err != nil {
		return p, err
	}
	if !p.valid() || p.Release != release {
		return p, errors.New("program registration mismatch")
	}
	return p, checkServiceProgramFile(p.archive(store), p)
}

// Historical releases have no archive registration. Validate/repack each once,
// before stopping the service. There is no application-file diff algorithm.
func archiveLegacyServiceProgram(ctx context.Context, r serviceRegistration, release string) error {
	dir := filepath.Join(r.Root, "releases", release)
	if err := servicePhysicalDirectory(dir); err != nil {
		return err
	}
	if _, err := verifyServiceRelease(dir); err != nil {
		return err
	}
	temp, err := os.CreateTemp(r.Root, ".program-*.tar.gz")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	h := sha256.New()
	gz, err := gzip.NewWriterLevel(io.MultiWriter(temp, h), gzip.BestSpeed)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(name)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.Dir(dir), name)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(name)
		if err != nil {
			return err
		}
		_, err = io.Copy(tw, serviceBackupReader{ctx, in})
		return errors.Join(err, in.Close())
	})
	if err = errors.Join(err, tw.Close(), gz.Close(), temp.Sync()); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return registerServiceProgram(ctx, r.Root, dir, temp.Name(), hex.EncodeToString(h.Sum(nil)))
}

func serviceProgramPool(r serviceRegistration, dest string) string {
	key := sha256.Sum256([]byte(r.Root + "\x00" + r.InstallationID))
	return filepath.Join(filepath.Dir(dest), ".teamx-programs", hex.EncodeToString(key[:]))
}

func prepareServiceBackupPrograms(ctx context.Context, r serviceRegistration, dest string) ([]serviceProgram, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := serviceRequiredReleases(r)
	if err != nil {
		return nil, err
	}
	pool := serviceProgramPool(r, dest)
	var result []serviceProgram
	for _, name := range names {
		p, err := installedServiceProgram(r.Root, name)
		if errors.Is(err, os.ErrNotExist) {
			// A missing archive for an existing record is corruption, not permission
			// to silently replace the saved program identity.
			record := filepath.Join(r.Root, "programs/releases", name+".json")
			if _, recordErr := os.Lstat(record); !errors.Is(recordErr, os.ErrNotExist) {
				return nil, err
			}
			if err = archiveLegacyServiceProgram(ctx, r, name); err != nil {
				return nil, err
			}
			p, err = installedServiceProgram(r.Root, name)
		}
		if err != nil {
			return nil, err
		}
		if err := privateServiceProgramDir(pool); err != nil {
			return nil, err
		}
		if err := os.Link(p.archive(filepath.Join(r.Root, "programs")), p.archive(pool)); err != nil {
			if errors.Is(err, syscall.EXDEV) {
				if err := copyServiceProgram(ctx, p.archive(filepath.Join(r.Root, "programs")), pool, p); err != nil {
					return nil, err
				}
			} else if !errors.Is(err, os.ErrExist) {
				return nil, err
			}
		}
		if err := checkServiceProgramFile(p.archive(pool), p); err != nil {
			return nil, err
		}
		if err := syncDirectory(pool); err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, nil
}

// Under stopped data locks, resolve references again but never unpack, migrate
// or copy a program. A newly introduced, unprepared dependency fails closed.
func readyServiceBackupPrograms(ctx context.Context, r serviceRegistration, dest string) ([]serviceProgram, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	names, err := serviceRequiredReleases(r)
	if err != nil {
		return nil, err
	}
	var result []serviceProgram
	for _, name := range names {
		p, err := installedServiceProgram(r.Root, name)
		if err != nil {
			return nil, fmt.Errorf("program %s was not prepared before stop: %w", name, err)
		}
		if err := checkServiceProgramFile(p.archive(serviceProgramPool(r, dest)), p); err != nil {
			return nil, fmt.Errorf("backup program %s was not prepared before stop: %w", name, err)
		}
		result = append(result, p)
	}
	return result, nil
}

func linkServiceBackupPrograms(r serviceRegistration, stage, dest string, programs []serviceProgram) error {
	dir := filepath.Join(stage, "programs")
	if err := privateServiceProgramDir(dir); err != nil {
		return err
	}
	pool := serviceProgramPool(r, dest)
	for _, p := range programs {
		if err := checkServiceProgramFile(p.archive(pool), p); err != nil {
			return err
		}
		if err := os.Link(p.archive(pool), p.archive(dir)); err != nil {
			return err
		}
	}
	return syncDirectory(dir)
}

func checkServiceBackupPrograms(ctx context.Context, dir string, b serviceBackup) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(b.Programs) == 0 {
		return errors.New("backup has no program references")
	}
	if err := servicePhysicalDirectory(filepath.Join(dir, "programs")); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range b.Programs {
		if !p.valid() || seen[p.Release] {
			return errors.New("invalid or duplicate program reference")
		}
		seen[p.Release] = true
		if err := checkServiceProgramFile(p.archive(filepath.Join(dir, "programs")), p); err != nil {
			return err
		}
	}
	if !seen[b.Current] {
		return errors.New("backup has no current program reference")
	}
	return nil
}

func restoreServicePrograms(ctx context.Context, dir string, b serviceBackup, stage string) error {
	if err := checkServiceBackupPrograms(ctx, dir, b); err != nil {
		return err
	}
	for _, p := range b.Programs {
		if err := ctx.Err(); err != nil {
			return err
		}
		temp, err := os.MkdirTemp(stage, ".program-*")
		if err != nil {
			return err
		}
		archive := p.archive(filepath.Join(dir, "programs"))
		release, err := unpackServiceArchive(archive, temp, p.SHA256)
		if err != nil {
			return err
		}
		id, err := verifyServiceRelease(release)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(p.Release, id+"-") {
			return errors.New("restored program release identity mismatch")
		}
		dest := filepath.Join(stage, "server/releases", p.Release)
		if err := os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
			return err
		}
		if err := os.Rename(release, dest); err != nil {
			return err
		}
		os.RemoveAll(temp)
	}
	return nil
}

// Run under the installation maintenance lock, after backup/release validation.
// Backup links pin objects even for manual and failed-deployment snapshots;
// their lifetime does not depend on enrollment or the installed release tree.
func pruneServiceProgramObjects(r serviceRegistration, deleted []string, backups []string) error {
	store := filepath.Join(r.Root, "programs")
	records := filepath.Join(store, "releases")
	for _, name := range deleted {
		if !serviceProgramRelease.MatchString(name) {
			return errors.New("invalid deleted program name")
		}
		if err := os.Remove(filepath.Join(records, name+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	entries, err := os.ReadDir(records)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	live := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return errors.New("unexpected program registration entry")
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		if !serviceProgramRelease.MatchString(name) {
			return errors.New("invalid program registration name")
		}
		if _, err := os.Lstat(filepath.Join(r.Root, "releases", name)); errors.Is(err, os.ErrNotExist) {
			if err := os.Remove(filepath.Join(records, entry.Name())); err != nil {
				return err
			}
			continue
		} else if err != nil {
			return err
		}
		p, err := installedServiceProgram(r.Root, name)
		if err != nil {
			return err
		}
		live[p.SHA256] = true
	}
	objects, err := os.ReadDir(store)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, entry := range objects {
		digest := strings.TrimSuffix(entry.Name(), ".tar.gz")
		if !strings.HasSuffix(entry.Name(), ".tar.gz") || !serviceProgramDigest.MatchString(digest) || live[digest] {
			continue
		}
		name := filepath.Join(store, entry.Name())
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0400 {
			return errors.New("unsafe program object during cleanup")
		}
		if err := os.Remove(name); err != nil {
			return err
		}
	}
	if len(objects) > 0 {
		if err := syncDirectory(store); err != nil {
			return err
		}
	}
	pools := map[string]bool{}
	for _, backup := range backups {
		pools[serviceProgramPool(r, backup)] = true
	}
	for pool := range pools {
		if _, err := os.Lstat(pool); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := privateServiceProgramDir(pool); err != nil {
			return err
		}
		entries, err := os.ReadDir(pool)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".tar.gz") || !serviceProgramDigest.MatchString(strings.TrimSuffix(entry.Name(), ".tar.gz")) {
				continue
			}
			name := filepath.Join(pool, entry.Name())
			info, err := os.Lstat(name)
			if err != nil {
				return err
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || int(stat.Uid) != os.Geteuid() {
				return errors.New("unsafe shared program object during cleanup")
			}
			if stat.Nlink == 1 {
				if err := os.Remove(name); err != nil {
					return err
				}
			}
		}
		if err := syncDirectory(pool); err != nil {
			return err
		}
	}
	return nil
}

// Background maintenance checks whole program objects before deleting any
// recovery source. Shared inodes are read only once per maintenance pass.
func verifyServiceBackupProgramContent(ctx context.Context, dir string, b serviceBackup, verified map[string][]os.FileInfo) error {
	for _, p := range b.Programs {
		name := p.archive(filepath.Join(dir, "programs"))
		if err := checkServiceProgramFile(name, p); err != nil {
			return err
		}
		info, err := os.Lstat(name)
		if err != nil {
			return err
		}
		cached := false
		for _, prior := range verified[p.SHA256] {
			if os.SameFile(prior, info) {
				cached = true
				break
			}
		}
		if cached {
			continue
		}
		in, err := os.Open(name)
		if err != nil {
			return err
		}
		digest, readErr := serviceHash(serviceBackupReader{ctx, in})
		if err := errors.Join(readErr, in.Close()); err != nil {
			return err
		}
		if digest != p.SHA256 {
			return errors.New("backup program archive SHA-256 mismatch")
		}
		verified[p.SHA256] = append(verified[p.SHA256], info)
	}
	return nil
}
