package room

import (
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/agent/dsh"
)

func TestExecutionNoticeSeparatesRecoveryAndOperatorDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		status MemberStatus
		code   string
	}{
		{MemberStatus{Status: "recovering", Error: "internal failure"}, "session_recovering"},
		{MemberStatus{Status: "backend_blocked", SessionResetRequired: true, Error: "internal failure"}, "session_unavailable"},
		{MemberStatus{Status: "checkpoint_failed", Error: "internal failure"}, "state_unavailable"},
		{MemberStatus{Error: "automatic Session recovery blocked: 1 action outcomes require inspection"}, "action_outcome_unknown"},
		{MemberStatus{Error: dsh.ErrAuthentication.Error()}, "model_authentication"},
		{MemberStatus{Error: dsh.ErrRateLimited.Error()}, "model_rate_limited"},
		{MemberStatus{Error: dsh.ErrModelNotFound.Error()}, "model_not_found"},
		{MemberStatus{Error: "internal failure"}, "execution_failed"},
	} {
		notice := executionNotice(tc.status)
		if notice == nil || notice.Code != tc.code || strings.Contains(notice.Message, "internal failure") || strings.Contains(notice.Message, "DSH") {
			t.Fatal("unsafe or missing presentation", tc.status, notice)
		}
	}
	if executionNotice(MemberStatus{Status: "idle"}) != nil {
		t.Fatal("healthy execution retained a failure notice")
	}
}
