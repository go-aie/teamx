package room

import (
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestSetupProbeDiagnosticDistinguishesLocalRuntimeFromModelSupport(t *testing.T) {
	for _, stage := range []string{"local_identity", "local"} {
		field, message := setupProbeDiagnostic(&dsh.ProbeError{Stage: stage, Code: "tool_acp"})
		if field != "" || !strings.Contains(message, "部署管理员") || strings.Contains(message, "接口协议") || strings.Contains(message, "工具调用") {
			t.Fatal("local deployment failure was attributed to model support", field, message)
		}
	}
	field, message := setupProbeDiagnostic(&dsh.ProbeError{Stage: "text", Code: "authentication"})
	if field != "api_key" || !strings.Contains(message, "API Key") {
		t.Fatal("upstream authentication guidance lost", field, message)
	}
}
