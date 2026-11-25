package room

import (
	"os"
	"path/filepath"
	"testing"
)

func TestServiceReleaseReferencesForSeparateServiceUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root is required to exercise a separate service file owner")
	}
	r := serviceTestRegistration(t)
	r.UID, r.GID = 65534, 65534
	path := filepath.Join(r.Home, "spaces.json")
	if err := writeServiceJSON(path, spaceDirectory{Version: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, r.UID, r.GID); err != nil {
		t.Fatal(err)
	}
	// The ordinary runtime reader still rejects another account's registry.
	var directory spaceDirectory
	if err := readIdentitySnapshot(path, &directory); err == nil {
		t.Fatal("runtime reader accepted another OS account's identity file")
	}
	if _, err := serviceRequiredReleases(r); err != nil {
		t.Fatal("root could not inspect registered service-owned identity", err)
	}
	backup := filepath.Join(filepath.Dir(r.Root), "registered-owner-backup")
	if err := createServiceBackup(r, backup); err != nil {
		t.Fatal("root could not back up registered service-owned identity", err)
	}
	if _, err := verifyServiceBackup(backup, r); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("source permissions changed", err)
	}
	wrong := r
	wrong.UID = 0
	if _, err := serviceRequiredReleases(wrong); err == nil {
		t.Fatal("accepted caller owner instead of registered owner")
	}
}
