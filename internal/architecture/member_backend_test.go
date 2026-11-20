package architecture_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"testing"
)

// The member lifecycle uses backend capabilities. Provider-specific files and
// the service configuration remain explicit adapters in the same package.
func TestMemberLifecycleBackendBoundary(t *testing.T) {
	for _, name := range []string{
		"session.go", "member.go", "rotation.go", "rotation_state.go",
		"recovery.go", "automatic_recovery.go", "automatic_rotation.go",
		"scheduling_state.go", "runtime_recovery.go", "model_apply.go",
		"execution_notice.go", "ui_http.go", "turn_execution.go", "default_assistant.go",
	} {
		path := filepath.Join("..", "room", name)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if value == "github.com/go-aie/teamx/internal/agent/dsh" {
				t.Errorf("%s: backend component import belongs in the adapter", name)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && (id.Name == "dshBackend" || id.Name == "dshSession" || id.Name == "dshStateDir") {
				t.Errorf("%s: concrete backend dependency %s", fset.Position(id.Pos()), id.Name)
			}
			return true
		})
	}
}
