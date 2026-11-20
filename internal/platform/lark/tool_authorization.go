package lark

import (
	"slices"
	"strings"
)

// ToolAuthorizationRequired reports user-grant gaps that OAuth can repair for
// this tool intent. Application gaps and unsupported capabilities cannot be
// repaired by granting the user more scopes.
func ToolAuthorizationRequired(check PermissionCheck, operations []string) bool {
	for _, cap := range check.Capabilities {
		if cap.State == "unsupported" {
			continue
		}
		if strings.HasPrefix(cap.Name, "message.") && cap.Name != "message.read" && !slices.Contains(operations, cap.Name) {
			continue
		}
		for _, scope := range cap.MissingUserScopes {
			if scope == "offline_access" || slices.Contains(check.Application.Granted, scope) {
				return true
			}
		}
	}
	return false
}
