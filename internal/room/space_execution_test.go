package room

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/go-aie/teamx/internal/agent/srt"
)

func retainedExecutionFixture(t *testing.T) (startupConfig, startupConfig, srt.Config, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	old := filepath.Join(root, "server/releases/original")
	current := filepath.Join(root, "server/releases/upgrade")
	for _, release := range []string{old, current} {
		if err := os.MkdirAll(filepath.Join(release, "bin"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(release, "bin/dsh"), []byte("pinned fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, "execution"), 0700); err != nil {
		t.Fatal(err)
	}
	deployment := startupConfig{path: filepath.Join(home, "config.toml")}
	deployment.Deployment.Execution.Kind = "srt"
	deployment.Deployment.Execution.Manifest = filepath.Join(home, "execution/upgrade.json")
	deployment.Server.DataDir = new(filepath.Join(home, "data"))
	config := srt.Config{Node: srt.Artifact{Path: filepath.Join(current, "runtime/node/bin/node")}, Runtime: srt.Artifact{Path: filepath.Join(current, "runtime/srt")}, Endpoints: []string{"https://model.example"}, PrivateRoots: []string{home}}
	writeExecutionFixture(t, deployment.Deployment.Execution.Manifest, config)
	config.Node.Path = filepath.Join(old, "runtime/node/bin/node")
	config.Runtime.Path = filepath.Join(old, "runtime/srt")
	previous := filepath.Join(home, "execution/original.json")
	writeExecutionFixture(t, previous, config)
	space := startupConfig{Runtime: &teamRuntime{}}
	space.Runtime.DSH.BinaryPath = filepath.Join(old, "bin/dsh")
	return deployment, space, config, previous
}

func writeExecutionFixture(t *testing.T, path string, c srt.Config) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceExecutionRetainsVerifiedPhysicalReleaseAfterHostUpgrade(t *testing.T) {
	deployment, space, expected, _ := retainedExecutionFixture(t)
	retained, err := retainedSpaceExecutionConfig(deployment, space)
	if err != nil || retained == nil {
		t.Fatal("old launcher lost deployment descriptor", err)
	}
	if retained.Node != expected.Node || retained.Runtime != expected.Runtime || !slices.Equal(retained.Endpoints, expected.Endpoints) || !slices.Contains(retained.PrivateRoots, *deployment.Server.DataDir) {
		t.Fatal("old execution or current private roots changed")
	}
	current := filepath.Dir(filepath.Dir(filepath.Dir(expected.Runtime.Path)))
	space.Runtime.DSH.BinaryPath = filepath.Join(current, "upgrade/bin/dsh")
	if retained, err := retainedSpaceExecutionConfig(deployment, space); err != nil || retained != nil {
		t.Fatal("new Space did not use current execution", err)
	}
}

func TestSpaceExecutionLeavesStandaloneSRTCompositionUnchanged(t *testing.T) {
	deployment, space, c, _ := retainedExecutionFixture(t)
	c.Runtime.Path = filepath.Join(t.TempDir(), "srt")
	writeExecutionFixture(t, deployment.Deployment.Execution.Manifest, c)
	if retained, err := retainedSpaceExecutionConfig(deployment, space); err != nil || retained != nil {
		t.Fatal("standalone SRT selection changed", err)
	}
}

func TestSpaceExecutionRejectsMissingOrExpandedRetainedAuthority(t *testing.T) {
	for _, kind := range []string{"missing", "link", "node", "runtime", "endpoints", "roots", "outside"} {
		t.Run(kind, func(t *testing.T) {
			deployment, space, c, path := retainedExecutionFixture(t)
			switch kind {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "link":
				other := path + ".other"
				if err := os.Rename(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, path); err != nil {
					t.Fatal(err)
				}
			case "node":
				c.Node.Path = "/arbitrary/node"
				writeExecutionFixture(t, path, c)
			case "runtime":
				c.Runtime.Path = "/arbitrary/srt"
				writeExecutionFixture(t, path, c)
			case "endpoints":
				c.Endpoints = append(c.Endpoints, "https://other.example")
				writeExecutionFixture(t, path, c)
			case "roots":
				c.PrivateRoots = nil
				writeExecutionFixture(t, path, c)
			case "outside":
				space.Runtime.DSH.BinaryPath = os.Args[0]
			}
			if retained, err := retainedSpaceExecutionConfig(deployment, space); err == nil || retained != nil {
				t.Fatal("unapproved retained execution admitted", kind, err)
			}
		})
	}
}
