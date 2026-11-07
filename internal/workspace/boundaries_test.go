package workspace_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/workspace"
)

func TestRepositoryRegistryPreservesImplicitGitAndRejectsUnknown(t *testing.T) {
	calls := 0
	source := workspace.Source{URL: "https://example.test/group/repo", Branch: "main"}
	registry := workspace.Registry{"git": func(got workspace.Source) workspace.Repository {
		calls++
		if got != source {
			t.Fatal("changed repository identity")
		}
		return nil
	}}
	for _, kind := range []string{"", "git"} {
		if _, err := registry.Open(kind, source); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := registry.Open("scion", source); err == nil || calls != 2 {
		t.Fatal("unregistered kind fell back to Git")
	}
}

func TestRepositoryImportBoundaries(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./...")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	const root = "github.com/go-aie/teamx/internal/"
	d := json.NewDecoder(strings.NewReader(string(out)))
	checked := 0
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
		if !strings.HasPrefix(p.ImportPath, root+"workspace") {
			continue
		}
		checked++
		for _, dep := range p.Deps {
			if strings.HasPrefix(dep, root+"room") || strings.HasPrefix(dep, root+"platform") ||
				p.ImportPath == root+"workspace" && strings.HasPrefix(dep, root+"workspace/") {
				t.Errorf("repository boundary crossed: %s depends on %s", p.ImportPath, dep)
			}
		}
	}
	if checked < 2 {
		t.Fatal("did not inspect contract and Git implementation")
	}
}

func TestCommonPreparationDoesNotSelectVersionControlCommandsOrMetadata(t *testing.T) {
	for _, name := range []string{"workspace.go", "workspace_config.go", "workspace_management.go", "workspace_records.go"} {
		f, err := parser.ParseFile(token.NewFileSet(), "../room/"+name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "ProjectID" {
				t.Errorf("%s directly interprets a GitLab numeric identity", name)
			}
			literal, ok := n.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, _ := strconv.Unquote(literal.Value)
			if value == "git" || value == "scn" || strings.HasPrefix(value, ".git") || strings.HasPrefix(value, ".scion") ||
				strings.Contains(value, "/internal/workspace/") || strings.Contains(value, "/internal/platform/gitlab") || strings.Contains(value, "/internal/platform/scion") {
				t.Errorf("%s contains a backend-specific dependency: %q", name, value)
			}
			return true
		})
	}
}
