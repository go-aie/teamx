//go:build darwin || linux

package room

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceStoppedHomeChecksResetUsingRegisteredOwner(t *testing.T) {
	home := t.TempDir()
	mustWriteResetFile(t, filepath.Join(home, "spaces.json"), `{"version":1,"spaces":[]}`)
	r := serviceRegistration{Home: home, UID: os.Geteuid()}
	unlock, err := acquireStoppedHome(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	r.UID++ // A file belonging to another account must still be refused.
	if unlock, err := acquireStoppedHome(context.Background(), r); err == nil {
		unlock()
		t.Fatal("reset check ignored the registered service owner")
	}
	r.UID = os.Geteuid()
	mustWriteResetFile(t, filepath.Join(home, "spaces.json"), `{"version":1,"spaces":[{"reset":{"format_version":1,"request_id":"`+strings.Repeat("e", 32)+`","expected_version":1,"phase":"cleaning"}}]}`)
	if unlock, err := acquireStoppedHome(context.Background(), r); err == nil {
		unlock()
		t.Fatal("pending reset allowed deployment ownership to proceed")
	}
}
