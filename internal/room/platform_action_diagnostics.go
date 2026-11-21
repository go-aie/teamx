package room

import (
	"errors"

	"github.com/go-aie/teamx/internal/platform/lark"
)

// Platform action errors are projected at the Room adapter boundary, separately
// from the provider-independent Agent request contract.
func agentActionDiagnostic(err error) error {
	err = toolRuntimeError(err)
	var unavailable *actionAuthorizationUnavailable
	if errors.As(err, &unavailable) {
		return &requestDiagnostic{cause: err, Field: "operation", Message: unavailable.Message, Hint: unavailable.NextAction + ": read platforms skill and inspect this exact member/account; never substitute a recipient or account."}
	}
	var native *lark.Error
	if errors.As(err, &native) {
		switch native.Code {
		case "identity_mismatch", "account_pinning_unavailable":
			return &requestDiagnostic{cause: errors.Join(ErrIdentity, err), Field: "target_ref", Message: native.Code, Hint: "revalidate_account: use platforms skill to verify the same bound account; never switch accounts or guess a replacement target."}
		case "permission_denied":
			return &requestDiagnostic{cause: errors.Join(ErrConflict, err), Field: "operation", Message: "The current account cannot verify or send to this target.", Hint: "Inspect this account's capability reason/next_action with platforms skill; repair only the requested member/account."}
		}
	}
	var d *requestDiagnostic
	if !errors.As(err, &d) {
		return err
	}
	copy := *d
	switch d.Field {
	case "authority.rule_version", "authority.instruction_version":
		copy.Field = "authority.version"
	}
	return &copy
}
