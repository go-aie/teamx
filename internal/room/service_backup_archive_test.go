//go:build darwin || linux

package room

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Produce the previous on-disk format independently of the new archive writer.
func createLegacyServiceTestBackup(t *testing.T, r serviceRegistration, dest string) {
	t.Helper()
	if err := os.MkdirAll(dest, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tree := range []struct{ source, name string }{{r.Home, "home"}, {filepath.Join(r.Root, "releases"), "releases"}} {
		if err := copyServiceTree(tree.source, filepath.Join(dest, tree.name)); err != nil {
			t.Fatal(err)
		}
	}
	current, err := filepath.EvalSymlinks(filepath.Join(r.Root, "current"))
	if err != nil {
		t.Fatal(err)
	}
	home, err := serviceInventory(filepath.Join(dest, "home"))
	if err != nil {
		t.Fatal(err)
	}
	releases, err := serviceInventory(filepath.Join(dest, "releases"))
	if err != nil {
		t.Fatal(err)
	}
	if err := writeServiceJSON(filepath.Join(dest, "backup.json"), serviceBackup{Version: 1, Created: time.Now(), Registration: r, Current: filepath.Base(current), HomeAssets: home, ReleaseAssets: releases}); err != nil {
		t.Fatal(err)
	}
}

func TestServiceArchiveBackupCorruptionCancellationAndPermissions(t *testing.T) {
	r := serviceTestRegistration(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := filepath.Join(filepath.Dir(r.Root), "canceled")
	if err := createServiceArchiveBackup(ctx, r, canceled, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(canceled); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("published canceled snapshot", err)
	}
	backup := filepath.Join(filepath.Dir(r.Root), "complete")
	if err := createServiceBackup(r, backup); err != nil {
		t.Fatal(err)
	}
	b, err := verifyServiceBackup(backup, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pruneServiceDeploymentContext(ctx, r, backup, 2, 2); !errors.Is(err, context.Canceled) {
		t.Fatal("backup verification ignored canceled maintenance", err)
	}
	if b.Version != 3 || len(b.HomeAssets) != 0 || len(b.ReleaseAssets) != 0 {
		t.Fatal("new backup contains per-file inventories")
	}
	archive := filepath.Join(backup, serviceBackupArchive)
	info, err := os.Stat(archive)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	f, err := os.OpenFile(archive, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteAt([]byte("tampered"), 1024); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = verifyServiceBackup(backup, r); err == nil {
		t.Fatal("accepted corrupted archive")
	}
}

func TestServiceBackupArchiveRejectsUnsafeExtraction(t *testing.T) {
	for _, header := range []*tar.Header{
		{Name: "../escaped", Typeflag: tar.TypeReg, Mode: 0600},
		{Name: "home/escape", Typeflag: tar.TypeSymlink, Linkname: "../../escaped", Mode: 0777},
		{Name: "home/hardlink", Typeflag: tar.TypeLink, Linkname: "home/config.toml", Mode: 0600},
		{Name: "home/setuid", Typeflag: tar.TypeReg, Mode: 04755},
	} {
		t.Run(header.Name, func(t *testing.T) {
			var payload bytes.Buffer
			tw := tar.NewWriter(&payload)
			if err := tw.WriteHeader(header); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			data := payload.Bytes()
			if err := os.WriteFile(filepath.Join(dir, serviceBackupArchive), data, 0600); err != nil {
				t.Fatal(err)
			}
			digest, _ := serviceHash(bytes.NewReader(data))
			b := serviceBackup{Current: "current", ArchiveSize: int64(len(data)), ArchiveSHA256: digest}
			if err := readServiceBackupArchive(context.Background(), dir, b, t.TempDir()); err == nil {
				t.Fatal("accepted unsafe tar header")
			}
		})
	}
}
