//go:build darwin || linux

package room

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceProgramBackupsReuseObjectsAndKeepRecoveryIndependent(t *testing.T) {
	r := serviceTestRegistration(t)
	base := filepath.Dir(r.Root)
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	for _, dest := range []string{first, second} {
		if err := createServiceBackup(r, dest); err != nil {
			t.Fatal(err)
		}
	}
	a, err := verifyServiceBackup(first, r)
	if err != nil {
		t.Fatal(err)
	}
	b, err := verifyServiceBackup(second, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Programs) != 1 || len(b.Programs) != 1 || a.Programs[0] != b.Programs[0] {
		t.Fatal(a.Programs, b.Programs)
	}
	p := a.Programs[0]
	af, _ := os.Stat(p.archive(filepath.Join(first, "programs")))
	bf, _ := os.Stat(p.archive(filepath.Join(second, "programs")))
	if !os.SameFile(af, bf) {
		t.Fatal("backups duplicated program bytes")
	}
	// Removing another snapshot, the build/installed archive store and pool must
	// not invalidate this snapshot's program objects.
	for _, dir := range []string{first, filepath.Join(base, ".teamx-programs"), filepath.Join(r.Root, "programs"), filepath.Join(r.Root, "releases")} {
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(r.Root, "releases"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := restoreServiceBackup(context.Background(), r, second); err != nil {
		t.Fatal(err)
	}
	if _, err := installedServiceProgram(r.Root, p.Release); err != nil {
		t.Fatal("restore did not recover program registration", err)
	}
}

func TestServiceProgramBackupAdmitsLegacyReleaseOnce(t *testing.T) {
	r := serviceTestRegistration(t)
	if err := os.RemoveAll(filepath.Join(r.Root, "programs")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(filepath.Dir(r.Root), "backup")
	first, err := prepareServiceBackupPrograms(context.Background(), r, dest)
	if err != nil {
		t.Fatal(err)
	}
	second, err := prepareServiceBackupPrograms(context.Background(), r, dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatal("legacy registration was not reused", first, second)
	}
	if err := createServiceBackup(r, dest); err != nil {
		t.Fatal(err)
	}
	if err := restoreServiceBackup(context.Background(), r, dest); err != nil {
		t.Fatal(err)
	}
}

func TestServiceProgramRecoveryRejectsMissingCorruptOrUnsafeObjects(t *testing.T) {
	for _, scenario := range []string{"missing", "corrupt", "symlink", "writable", "duplicate", "wrong-release"} {
		t.Run(scenario, func(t *testing.T) {
			r := serviceTestRegistration(t)
			dest := filepath.Join(filepath.Dir(r.Root), "backup")
			if err := createServiceBackup(r, dest); err != nil {
				t.Fatal(err)
			}
			b, err := verifyServiceBackup(dest, r)
			if err != nil {
				t.Fatal(err)
			}
			p := b.Programs[0]
			archive := p.archive(filepath.Join(dest, "programs"))
			switch scenario {
			case "missing":
				err = os.Remove(archive)
			case "corrupt":
				if err = os.Chmod(archive, 0600); err != nil {
					t.Fatal(err)
				}
				f, e := os.OpenFile(archive, os.O_WRONLY, 0)
				if e != nil {
					t.Fatal(e)
				}
				_, err = f.WriteAt([]byte("broken"), 10)
				f.Close()
				if err == nil {
					err = os.Chmod(archive, 0400)
				}
			case "symlink":
				err = os.Rename(archive, archive+".saved")
				if err == nil {
					err = os.Symlink(archive+".saved", archive)
				}
			case "writable":
				err = os.Chmod(archive, 0600)
			case "duplicate":
				b.Programs = append(b.Programs, p)
				err = writeServiceJSON(filepath.Join(dest, "backup.json"), b)
			case "wrong-release":
				b.Programs[0].Release = "../escape"
				err = writeServiceJSON(filepath.Join(dest, "backup.json"), b)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(r.Home, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if err := restoreServiceBackup(context.Background(), r, dest); err == nil {
				t.Fatal("accepted damaged program", scenario)
			}
			after, _ := os.ReadFile(filepath.Join(r.Home, "config.toml"))
			if string(before) != string(after) {
				t.Fatal("changed data before validating programs")
			}
		})
	}
}

func TestServiceBackupRestoresPreviousStreamArchive(t *testing.T) {
	r := serviceTestRegistration(t)
	dest := filepath.Join(filepath.Dir(r.Root), "v2")
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	current, err := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	digest, size, err := writeServiceBackupArchive(context.Background(), r, filepath.Join(dest, serviceBackupArchive))
	if err != nil {
		t.Fatal(err)
	}
	b := serviceBackup{Version: 2, Created: time.Now(), Registration: r, Current: filepath.Base(current), ArchiveSHA256: digest, ArchiveSize: size}
	if err := writeServiceJSON(filepath.Join(dest, "backup.json"), b); err != nil {
		t.Fatal(err)
	}
	if err := restoreServiceBackup(context.Background(), r, dest); err != nil {
		t.Fatal(err)
	}
}

func TestServiceProgramAdmissionRejectsMissingRegisteredArchive(t *testing.T) {
	r := serviceTestRegistration(t)
	dest := filepath.Join(filepath.Dir(r.Root), "backup")
	refs, err := prepareServiceBackupPrograms(context.Background(), r, dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(refs[0].archive(filepath.Join(r.Root, "programs"))); err != nil {
		t.Fatal(err)
	}
	if err := createServiceArchiveBackup(context.Background(), r, dest, io.Discard); err == nil {
		t.Fatal("silently replaced missing registered object")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("published incomplete backup", err)
	}
}

func TestServiceProgramCleanupProtectsManualBackupsAndCollectsUnreferencedObjects(t *testing.T) {
	r := serviceTestRegistration(t)
	ctx := context.Background()
	base := filepath.Dir(r.Root)
	old, _ := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	manual := filepath.Join(base, "manual")
	if err := createServiceBackup(r, manual); err != nil {
		t.Fatal(err)
	}
	b, err := verifyServiceBackup(manual, r)
	if err != nil {
		t.Fatal(err)
	}
	p := b.Programs[0]
	poolArchive := p.archive(serviceProgramPool(r, manual))
	for _, version := range []string{"next", "newest"} {
		dest := filepath.Join(base, "before-"+version)
		if err := createServiceBackup(r, dest); err != nil {
			t.Fatal(err)
		}
		release := serviceRetentionRelease(t, r, version)
		if err := selectServiceRelease(r.Root, release); err != nil {
			t.Fatal(err)
		}
		if _, err := pruneServiceDeployment(r, dest, 2, 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("obsolete installed release retained", err)
	}
	if _, err := os.Stat(poolArchive); err != nil {
		t.Fatal("manual backup lost shared program", err)
	}
	if err := restoreServiceBackup(ctx, r, manual); err != nil {
		t.Fatal(err)
	}
	// After every backup link and installed registration for this object is gone,
	// the shared pool may release its final link without looking inside the tar.
	if err := os.RemoveAll(manual); err != nil {
		t.Fatal(err)
	}
	if err := pruneServiceProgramObjects(r, []string{p.Release}, []string{manual}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(poolArchive); !os.IsNotExist(err) {
		t.Fatal("unreferenced program object leaked", err)
	}
}

func TestServiceRetentionChecksProgramContentBeforeDeletingRecoverySources(t *testing.T) {
	r := serviceTestRegistration(t)
	unused := serviceRetentionRelease(t, r, "unused")
	dest := filepath.Join(filepath.Dir(r.Root), "backup")
	if err := createServiceBackup(r, dest); err != nil {
		t.Fatal(err)
	}
	b, err := verifyServiceBackup(dest, r)
	if err != nil {
		t.Fatal(err)
	}
	name := b.Programs[0].archive(filepath.Join(dest, "programs"))
	if err := os.Chmod(name, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("bad"), 10); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(name, 0400); err != nil {
		t.Fatal(err)
	}
	if _, err := pruneServiceDeployment(r, dest, 2, 2); err == nil {
		t.Fatal("deleted sources with a corrupt program backup")
	}
	if _, err := os.Stat(unused); err != nil {
		t.Fatal("deleted before verifying recovery material", err)
	}
}
