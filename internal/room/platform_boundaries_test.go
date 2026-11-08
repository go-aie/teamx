package room

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

// Import checks alone miss semantic coupling such as provider-name and native
// operation switches. Adapter assembly and legacy codecs have separate owners.
func TestInboxAndActionCoreHasNoNativeProviderRules(t *testing.T) {
	for _, name := range []string{
		"service.go", "member.go", "inbox_admission.go", "inbox_consumption.go", "inbox_member_service.go", "inbox_protocol.go",
		"notification_views.go", "inbox_message_origin.go", "followups_service.go", "followups_actions.go", "followups_actions_service.go",
		"actions_store.go", "actions_target.go", "agent_contract.go", "agent_read_views.go", "tools_capability_evidence.go",
	} {
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			for _, native := range []string{"lark", "gitlab", "scion"} {
				if strings.HasPrefix(path, "github.com/go-aie/teamx/internal/platform/"+native) {
					t.Errorf("%s imports native adapter %s", name, path)
				}
			}
		}
		ast.Inspect(f, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok && id.Name == "OperationMessageSendSelf" {
				t.Errorf("%s selects authorization by a concrete operation", name)
			}
			if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
				value, _ := strconv.Unquote(literal.Value)
				if value == "lark" || value == "gitlab" || value == "scion" || strings.HasPrefix(value, "lark:") {
					t.Errorf("%s contains native provider rule %q", name, value)
				}
			}
			return true
		})
	}
}
