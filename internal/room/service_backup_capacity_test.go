//go:build darwin || linux

package room

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceBackupManifestAboveOldCapacityStillVerifiesInventory(t *testing.T) {
	r := serviceTestRegistration(t)
	dir := filepath.Join(filepath.Dir(r.Root), "large-manifest-backup")
	if err := createServiceBackup(r, dir); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "backup.json")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	padding := bytes.Repeat([]byte(" "), 1<<20)
	for range 65 {
		if _, err := f.Write(padding); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyServiceBackup(dir, r); err != nil {
		t.Fatal("valid inventory above 64 MiB rejected", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "home/config.toml"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyServiceBackup(dir, r); err == nil {
		t.Fatal("larger capacity bypassed inventory verification")
	}
	if err := os.Truncate(p, maxServiceBackupManifestBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyServiceBackup(dir, r); err == nil {
		t.Fatal("unbounded manifest accepted")
	}
}
