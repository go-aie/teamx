//go:build e2e

package room

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/agent/srt"
)

// Explicit installed acceptance only. This inspects pinned executables without
// loading a model credential, sending a prompt or changing business state.
func TestInstalledSpaceExecutionRetainsModelRuntime(t *testing.T) {
	home := os.Getenv("TEAMX_INSTALLED_EXECUTION_HOME")
	if home == "" {
		t.Skip("set TEAMX_INSTALLED_EXECUTION_HOME for installed acceptance")
	}
	deployment, err := readStartupConfig(filepath.Join(home, "config.toml"), true, false)
	if err != nil {
		t.Fatal(err)
	}
	var directory spaceDirectory
	if err := readSnapshot(filepath.Join(home, "spaces.json"), &directory, maxPrivateFileBytes); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, record := range directory.Spaces {
		space, err := readStartupConfig(filepath.Join(record.Roots.Home, "config.toml"), true, false)
		if err != nil {
			t.Fatal(err)
		}
		retained, err := retainedSpaceExecutionConfig(deployment, space)
		if err != nil {
			t.Fatal(err)
		}
		if retained == nil {
			continue
		}
		factory := srt.New(*retained)
		if err := factory.Check(t.Context()); err != nil {
			t.Fatal("retained execution prerequisites", err)
		}
		paths, err := filepath.Glob(filepath.Join(record.Roots.Data, "rooms/*/agents/*/session.json*.model.json"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			var frozen modelSessionSnapshot
			if err := readSnapshot(path, &frozen, maxPrivateFileBytes); err != nil {
				t.Fatal(err)
			}
			if frozen.Legacy {
				continue
			}
			ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
			actual, err := dsh.InspectRuntime(ctx, dsh.Options{BinaryPath: frozen.Profile.BinaryPath, BinaryArgs: frozen.Profile.BinaryArgs, Execution: factory.ForSpace(record.OwnerUserID, record.ID), ExecutionBinding: agent.ExecutionBinding{UserID: record.OwnerUserID, SpaceID: record.ID}})
			cancel()
			if err != nil {
				t.Fatal("retained runtime could not start", err)
			}
			expected := frozen.Runtime
			expected.Agent = dsh.Artifact{}
			if !reflect.DeepEqual(actual, expected) {
				t.Fatal("host upgrade changed existing model runtime identity")
			}
			cli, err := spaceCLIPath(filepath.Join(filepath.Dir(frozen.Profile.BinaryPath), "teamx"))
			if err != nil || !pathWithin(filepath.Dir(frozen.Profile.BinaryPath), cli) {
				t.Fatal("retained tool CLI unavailable", err)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no retained installed model runtime was checked")
	}
	t.Logf("verified %d existing model runtime identities and their pinned tool CLI after host upgrade", checked)
}
