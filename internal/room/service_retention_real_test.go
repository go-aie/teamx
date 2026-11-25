//go:build e2e && (darwin || linux)

package room

import (
	"os"
	"testing"
)

// Read-only inspection of the installed service's recovery dependencies. This
// does not stop/start the service, create a backup or prune any files.
func TestInstalledServiceRecoveryDependencies(t *testing.T) {
	root := os.Getenv("TEAMX_INSTALLED_SERVICE_ROOT")
	if root == "" {
		t.Skip("set TEAMX_INSTALLED_SERVICE_ROOT for installed read-only inspection")
	}
	r, err := loadServiceRegistration(root)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := serviceRequiredReleases(r)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependencies) == 0 {
		t.Fatal("installed service has no recovery dependencies")
	}
	t.Logf("caller UID %d; registered service UID %d; recovery releases: %v", os.Geteuid(), r.UID, dependencies)
}
