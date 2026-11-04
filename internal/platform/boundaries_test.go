package platform_test

import (
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"testing"
)

// Check production transitive imports, including dependencies added indirectly.
// Agent commands and built-in adapters must remain independently buildable.
func TestCapabilityImportBoundaries(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./internal/platform/...", "./tools/gitlab")
	cmd.Dir = "../.."
	out, err := cmd.Output()
	if err != nil {
		t.Fatal("cannot inspect production package graph", err)
	}
	var packages []struct {
		ImportPath string
		Deps       []string
	}
	d := json.NewDecoder(strings.NewReader(string(out)))
	for {
		var p struct {
			ImportPath string
			Deps       []string
		}
		if err := d.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, p)
	}
	const root = "github.com/go-aie/teamx/"
	checked := 0
	for _, p := range packages {
		if !strings.HasPrefix(p.ImportPath, root) {
			continue
		}
		checked++
		for _, dep := range p.Deps {
			forbidden := strings.HasPrefix(dep, root+"internal/room")
			switch p.ImportPath {
			case root + "internal/platform/tool", root + "internal/platform/connection", root + "internal/platform/notification":
				forbidden = forbidden || strings.HasPrefix(dep, root+"internal/platform/gitlab") || strings.HasPrefix(dep, root+"tools/")
			case root + "internal/platform/gitlab":
				forbidden = forbidden || strings.HasPrefix(dep, root+"internal/platform/tool") || strings.HasPrefix(dep, root+"tools/")
			case root + "tools/gitlab":
				forbidden = forbidden || strings.HasPrefix(dep, root+"internal/")
			}
			if forbidden {
				t.Errorf("capability boundary crossed: %s depends on %s", p.ImportPath, dep)
			}
		}
	}
	if checked < 6 {
		t.Fatal("production graph did not include all capability packages")
	}
}
