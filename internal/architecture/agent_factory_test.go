package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAgentFactoryDependencyBoundary(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"agent", "builtin/agents/dsh", "internal/room"} {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			name := filepath.Join(root, dir, entry.Name())
			file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			componentImport := ""
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if dir == "agent" && (strings.Contains(path, "/builtin/") || strings.Contains(path, "/internal/agent/") || strings.Contains(path, "/internal/room")) {
					t.Errorf("%s: public contract depends on implementation %s", name, path)
				}
				if dir == "builtin/agents/dsh" && strings.Contains(path, "/internal/room") {
					t.Errorf("%s: factory imports Room host", name)
				}
				if path == "github.com/go-aie/teamx/internal/agent/dsh" {
					componentImport = "dsh"
					if imp.Name != nil {
						componentImport = imp.Name.Name
					}
				}
			}
			if dir != "internal/room" || componentImport == "" {
				continue
			}
			ast.Inspect(file, func(node ast.Node) bool {
				sel, ok := node.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if !ok || id.Name != componentImport {
					return true
				}
				switch sel.Sel.Name {
				case "Open", "Resume", "CheckResume", "CheckReset", "CheckResumeConfig", "InspectRuntime":
					t.Errorf("%s: %s bypasses the selected backend factory", name, sel.Sel.Name)
				}
				return true
			})
		}
	}
}
