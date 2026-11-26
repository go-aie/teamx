//go:build darwin || linux

package room

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type serviceBackup struct {
	Version       int                 `json:"version"`
	Created       time.Time           `json:"created"`
	Registration  serviceRegistration `json:"registration"`
	Current       string              `json:"current"`
	HomeAssets    []serviceAsset      `json:"home_assets"`
	ReleaseAssets []serviceAsset      `json:"release_assets"`
	ArchiveSHA256 string              `json:"archive_sha256,omitempty"`
	Programs      []serviceProgram    `json:"programs,omitempty"`
	ArchiveSize   int64               `json:"archive_size,omitempty"`
}

// Several standard releases already exceed 64 MiB of inventory metadata.
// Keep a finite file bound while allowing normal multi-version backups.
const maxServiceBackupManifestBytes = 512 << 20

// Private backups cover managed state at its original absolute paths. External
// runtime/data/template/credential roots require a separate deployment recipe.
func serviceDataRoot(r serviceRegistration) (string, error) {
	b, err := os.ReadFile(filepath.Join(r.Home, "config.toml"))
	if err != nil {
		return "", err
	}
	var c startupConfig
	if err := toml.Unmarshal(b, &c); err != nil {
		return "", err
	}
	data := filepath.Join(r.Home, "data")
	if c.Server.DataDir != nil {
		data = configPath(r.Home, *c.Server.DataDir)
	}
	paths := []string{data}
	if c.Server.TeamsDir != nil {
		paths = append(paths, configPath(r.Home, *c.Server.TeamsDir))
	}
	if c.Access.AdminTokenFile != "" {
		paths = append(paths, filepath.Dir(configPath(r.Home, c.Access.AdminTokenFile)))
	}
	for _, name := range paths {
		real, err := resolveFutureDirectory(name)
		if err != nil {
			return "", err
		}
		if !pathWithin(r.Home, real) || real == r.Home {
			return "", fmt.Errorf("managed backup requires data, templates and credentials inside home: %s", name)
		}
	}
	if c.Runtime != nil && c.Runtime.DSH.BinaryPath != "" {
		binary, err := filepath.EvalSymlinks(configPath(r.Home, c.Runtime.DSH.BinaryPath))
		if err != nil {
			return "", err
		}
		if !pathWithin(filepath.Join(r.Root, "releases"), binary) {
			return "", errors.New("external runtime requires a separate consistent backup; managed backup refused")
		}
	}
	return data, nil
}

func acquireStoppedHome(ctx context.Context, r serviceRegistration) (func(), error) {
	deadline, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for {
		lock, err := lockBundleHome(r.Home)
		if err == nil {
			if err := rejectPendingSpaceReset(r.Home, uint32(r.UID)); err != nil {
				_ = unlockDirectory(lock)
				return nil, err
			}
			return func() { _ = unlockDirectory(lock) }, nil
		}
		if !errors.Is(err, ErrLocked) {
			return nil, err
		}
		select {
		case <-deadline.Done():
			return nil, fmt.Errorf("service still owns home after stop: %w", err)
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func acquireStoppedService(ctx context.Context, r serviceRegistration) (func(), error) {
	releaseHome, err := acquireStoppedHome(ctx, r)
	if err != nil {
		return nil, err
	}
	data, err := serviceDataRoot(r)
	if err != nil {
		releaseHome()
		return nil, err
	}
	if _, err := os.Stat(data); errors.Is(err, os.ErrNotExist) {
		return releaseHome, nil
	} else if err != nil {
		releaseHome()
		return nil, err
	}
	dataLock, err := lockDirectory(data)
	if err != nil {
		releaseHome()
		return nil, err
	}
	return func() { _ = unlockDirectory(dataLock); releaseHome() }, nil
}

func copyServiceTree(source, dest string) error {
	type copyJob struct {
		source, dest string
		mode         fs.FileMode
	}
	jobs := make(chan copyJob, 64)
	var workers sync.WaitGroup
	var mu sync.Mutex
	var copyErr error
	failed := func() error { mu.Lock(); defer mu.Unlock(); return copyErr }
	// Keep fsync for each copied file, while allowing the filesystem to batch
	// concurrent flushes. Bound descriptors and memory even for large releases.
	for range 32 {
		workers.Go(func() {
			for job := range jobs {
				if failed() != nil {
					continue
				}
				if err := copyServiceFile(job.source, job.dest, job.mode); err != nil {
					mu.Lock()
					if copyErr == nil {
						copyErr = err
					}
					mu.Unlock()
				}
			}
		})
	}
	walkErr := filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := failed(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm()|0700)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(name)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported backup file: %s", name)
		}
		jobs <- copyJob{name, target, info.Mode().Perm()}
		return nil
	})
	close(jobs)
	workers.Wait()
	return errors.Join(walkErr, failed())
}

func copyServiceFile(source, dest string, mode fs.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	return errors.Join(copyErr, out.Chmod(mode), out.Sync(), out.Close())
}

// Run this before stopping the service, then repeat it while publishing the
// backup. A typo or unwritable parent must not take a working service down.
func validateServiceBackupDestination(r serviceRegistration, dest string) error {
	dest, err := resolveFutureDirectory(dest)
	if err != nil {
		return err
	}
	if pathWithin(r.Root, dest) || pathWithin(r.Home, dest) || pathWithin(dest, r.Root) || pathWithin(dest, r.Home) {
		return errors.New("backup must be outside installation and home")
	}
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return errors.New("backup destination must be new")
	}
	probe, err := os.MkdirTemp(filepath.Dir(dest), ".teamx-backup-check-*")
	if err != nil {
		return fmt.Errorf("备份父目录必须已存在且可写: %w", err)
	}
	return os.Remove(probe)
}

func defaultServiceUpgradeBackup(r serviceRegistration) (string, error) {
	parent := r.Home + "-backups"
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	return filepath.Join(parent, "before-upgrade-"+time.Now().UTC().Format("20060102T150405.000000000Z")), nil
}

func createServiceBackup(r serviceRegistration, dest string) error {
	return createServiceBackupWithProgress(r, dest, io.Discard)
}

// Progress is written to stderr by the CLI; JSON results remain on stdout.
func serviceOperationStep(output io.Writer, label string, operation func() error) error {
	fmt.Fprintf(output, "[service] %s：开始\n", label)
	started := time.Now()
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				fmt.Fprintf(output, "[service] %s：执行中（%.0fs）\n", label, time.Since(started).Seconds())
			}
		}
	}()
	err := operation()
	close(stop)
	<-done
	status := "完成"
	if err != nil {
		status = "失败"
	}
	fmt.Fprintf(output, "[service] %s：%s（%.2fs）\n", label, status, time.Since(started).Seconds())
	return err
}

func createServiceBackupWithProgress(r serviceRegistration, dest string, output io.Writer) error {
	ctx := context.Background()
	var err error
	dest, err = resolveFutureDirectory(dest)
	if err != nil {
		return err
	}
	if err := validateServiceBackupDestination(r, dest); err != nil {
		return err
	}
	if err := rejectPendingSpaceReset(r.Home, uint32(r.UID)); err != nil {
		return err
	}
	if _, err := prepareServiceBackupPrograms(ctx, r, dest); err != nil {
		return err
	}
	return createServiceArchiveBackup(ctx, r, dest, output)
}

func createServiceArchiveBackup(ctx context.Context, r serviceRegistration, dest string, output io.Writer) error {
	if err := rejectPendingSpaceReset(r.Home, uint32(r.UID)); err != nil {
		return err
	}
	dest, err := resolveFutureDirectory(dest)
	if err != nil {
		return err
	}
	if err := validateServiceBackupDestination(r, dest); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(dest), ".teamx-backup-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	current, err := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	if err != nil {
		return err
	}
	if filepath.Dir(current) != filepath.Join(r.Root, "releases") {
		return errors.New("invalid current release")
	}
	// Repeat reference resolution under the stopped data locks. Normal upgrades
	// have already admitted these archives before stopping.
	programs, err := readyServiceBackupPrograms(ctx, r, dest)
	if err != nil {
		return err
	}
	if err := linkServiceBackupPrograms(r, stage, dest, programs); err != nil {
		return err
	}
	backup := serviceBackup{Version: 3, Created: time.Now().UTC(), Registration: r, Current: filepath.Base(current), Programs: programs}
	if err := serviceOperationStep(output, "备份：流式归档数据与登记信息并同步落盘", func() error {
		backup.ArchiveSHA256, backup.ArchiveSize, err = writeServiceDataArchive(ctx, r, filepath.Join(stage, serviceBackupArchive))
		return err
	}); err != nil {
		return err
	}
	if err := writeServiceJSON(filepath.Join(stage, "backup.json"), backup); err != nil {
		return err
	}
	if err := serviceOperationStep(output, "备份：顺序回读并校验归档", func() error { _, err := verifyServiceBackupContext(ctx, stage, r); return err }); err != nil {
		return err
	}
	if err := syncDirectory(stage); err != nil {
		return err
	}
	if err := os.Rename(stage, dest); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(dest))
}

func verifyServiceBackup(dir string, r serviceRegistration) (serviceBackup, error) {
	return verifyServiceBackupContext(context.Background(), dir, r)
}

func verifyServiceBackupContext(ctx context.Context, dir string, r serviceRegistration) (serviceBackup, error) {
	var b serviceBackup
	if err := readSnapshot(filepath.Join(dir, "backup.json"), &b, maxServiceBackupManifestBytes); err != nil {
		return b, err
	}
	if (b.Version != 1 && b.Version != 2 && b.Version != 3) || b.Registration.Root != r.Root || b.Registration.Home != r.Home || b.Registration.InstallationID != r.InstallationID || b.Registration.UID != r.UID || !serviceProgramRelease.MatchString(b.Current) {
		return b, errors.New("backup does not belong to this registered instance and original paths")
	}
	if b.Version == 3 {
		if err := checkServiceBackupPrograms(ctx, dir, b); err != nil {
			return b, err
		}
	}
	if b.Version >= 2 {
		return b, readServiceBackupArchive(ctx, dir, b, "")
	}
	home, err := serviceInventory(filepath.Join(dir, "home"))
	if err != nil {
		return b, err
	}
	releases, err := serviceInventory(filepath.Join(dir, "releases"))
	if err != nil {
		return b, err
	}
	if !reflect.DeepEqual(home, b.HomeAssets) || !reflect.DeepEqual(releases, b.ReleaseAssets) {
		return b, errors.New("backup checksum, mode or link mismatch")
	}
	if _, err := os.Stat(filepath.Join(dir, "releases", b.Current, "manifest.json")); err != nil {
		return b, err
	}
	return b, nil
}

// Restore only the same installation at the same absolute paths. Keep the
// replaced home, every existing release and a pre-restore backup for recovery.
func restoreServiceBackup(ctx context.Context, r serviceRegistration, source string) error {
	b, err := verifyServiceBackupContext(ctx, source, r)
	if err != nil {
		return err
	}
	backupSource := source
	releases := filepath.Join(source, "releases")
	if b.Version >= 2 {
		stage, err := os.MkdirTemp(filepath.Dir(r.Home), ".teamx-backup-restore-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(stage)
		if err := readServiceBackupArchive(ctx, source, b, stage); err != nil {
			return err
		}
		if b.Version == 3 {
			if err := restoreServicePrograms(ctx, source, b, stage); err != nil {
				return err
			}
		}
		source, releases = stage, filepath.Join(stage, "server", "releases")
	}
	entries, err := os.ReadDir(releases)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return errors.New("backup releases must be directories")
		}
		src, dest := filepath.Join(releases, entry.Name()), filepath.Join(r.Root, "releases", entry.Name())
		if _, err := os.Lstat(dest); err == nil {
			a, err := serviceInventory(src)
			if err != nil {
				return err
			}
			b, err := serviceInventory(dest)
			if err != nil || !reflect.DeepEqual(a, b) {
				return fmt.Errorf("retained release differs: %s", entry.Name())
			}
		} else if errors.Is(err, os.ErrNotExist) {
			stage, err := os.MkdirTemp(filepath.Join(r.Root, "releases"), ".restore-*")
			if err != nil {
				return err
			}
			if err := copyServiceTree(src, filepath.Join(stage, "release")); err != nil {
				os.RemoveAll(stage)
				return err
			}
			err = os.Rename(filepath.Join(stage, "release"), dest)
			os.RemoveAll(stage)
			if err != nil {
				return err
			}
		} else {
			return err
		}
		if b.Version == 3 {
			for _, p := range b.Programs {
				if p.Release == entry.Name() {
					if err := registerServiceProgram(ctx, r.Root, dest, p.archive(filepath.Join(backupSource, "programs")), p.SHA256); err != nil {
						return err
					}
				}
			}
		}
	}
	stage, err := os.MkdirTemp(filepath.Dir(r.Home), ".teamx-home-restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	newHome := filepath.Join(stage, "home")
	if err := copyServiceTree(filepath.Join(source, "home"), newHome); err != nil {
		return err
	}
	if err := filepath.WalkDir(newHome, func(name string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(name, r.UID, r.GID)
	}); err != nil {
		return err
	}
	// Hold the replacement inode's home lock across publication, in addition
	// to the old home/data locks owned by the caller.
	newLock, err := lockBundleHome(newHome)
	if err != nil {
		return err
	}
	defer unlockDirectory(newLock)
	oldHome := r.Home + ".before-restore-" + time.Now().UTC().Format("20060102T150405.000000000")
	if err := os.Rename(r.Home, oldHome); err != nil {
		return err
	}
	if err := os.Rename(newHome, r.Home); err != nil {
		return errors.Join(err, os.Rename(oldHome, r.Home))
	}
	return selectServiceRelease(r.Root, filepath.Join(r.Root, "releases", b.Current))
}
