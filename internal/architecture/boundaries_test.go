package architecture_test

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// Inspect production dependencies, not test imports: Room tests deliberately
// use the frozen comparison fixtures. Existing platform and workspace tests
// enforce their more detailed capability boundaries separately.
func TestProductionImportBoundaries(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/...", "./internal/...", "./tools/...")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("cannot inspect production package graph:", err)
	}
	const root = "github.com/go-aie/teamx/internal/"
	within := func(path, prefix string) bool { return path == prefix || strings.HasPrefix(path, prefix+"/") }
	decoder := json.NewDecoder(strings.NewReader(string(out)))
	checked := map[string]bool{}
	for {
		var pkg struct {
			ImportPath string
			Deps       []string
		}
		if err := decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(pkg.ImportPath, "github.com/go-aie/teamx/") || within(pkg.ImportPath, root+"inputstreamtest") || within(pkg.ImportPath, root+"testutil") {
			continue
		}
		checked[pkg.ImportPath] = true
		lower := false
		for _, name := range []string{"inputstream", "agent/dsh", "platform", "workspace", "calendar", "conversation", "identity", "persistence"} {
			lower = lower || within(pkg.ImportPath, root+name)
		}
		service := strings.HasPrefix(pkg.ImportPath, root+"app/")
		for _, dep := range pkg.Deps {
			forbidden := within(dep, root+"inputstreamtest") || within(dep, root+"testutil")
			if within(pkg.ImportPath, root+"identity") {
				forbidden = forbidden || strings.HasPrefix(dep, root)
			}
			if lower || service {
				for _, name := range []string{"room", "server", "transport", "cli"} {
					forbidden = forbidden || within(dep, root+name)
				}
				forbidden = forbidden || dep == root+"app"
			}
			if lower {
				forbidden = forbidden || within(dep, root+"app")
			}
			if within(pkg.ImportPath, root+"app/history") {
				forbidden = forbidden || within(dep, root+"app/inputs")
			}
			if within(pkg.ImportPath, root+"app/inbox") {
				forbidden = forbidden || within(dep, root+"app/agents")
			}
			if forbidden {
				t.Errorf("production boundary crossed: %s depends on %s", pkg.ImportPath, dep)
			}
		}
	}
	for _, name := range []string{"cmd/teamx", "internal/room", "internal/identity", "internal/persistence", "internal/app/agents", "internal/app/platforms", "internal/inputstream", "internal/agent/dsh", "internal/platform", "internal/workspace", "internal/calendar"} {
		if !checked["github.com/go-aie/teamx/"+name] {
			t.Errorf("production graph missing %s", name)
		}
	}
}
