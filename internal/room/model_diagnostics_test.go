package room

import (
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestSetupProbeDiagnosticDistinguishesLocalRuntimeFromModelSupport(t *testing.T) {
	for _, stage := range []string{"local_identity", "local", "handshake"} {
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

func TestModelValidationGuidanceDistinguishesFinalFailures(t *testing.T) {
	for _, test := range []struct{ stage, code, want, unwanted string }{
		{"tool", "rate_limited", "暂时限流", "额度"},
		{"tool", "quota_exceeded", "额度不足", "暂时限流"},
		{"tool", "authentication", "API Key", "工具调用"},
		{"tool", "upstream_5xx", "模型服务暂时异常", "工具调用"},
		{"handshake", "timeout", "运行环境启动超时", "网络连接"},
		{"text", "timeout", "验证超时", "启动超时"},
		{"tool", "tool_acp", "工具调用验证未完成", "支持工具调用"},
		{"tool", "round_trip_missing", "工具调用验证未完成", "支持工具调用"},
	} {
		t.Run(test.stage+"/"+test.code, func(t *testing.T) {
			probe := &dsh.ProbeError{Stage: test.stage, Code: test.code}
			_, message := setupProbeDiagnostic(probe)
			if !strings.Contains(message, test.want) || strings.Contains(message, test.unwanted) {
				t.Fatal(message)
			}
			if modelChangeError(probe) != message {
				t.Fatal("online model settings lost the same validation guidance")
			}
		})
	}
}
