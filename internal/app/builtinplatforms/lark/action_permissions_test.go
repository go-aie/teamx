package lark

import (
	"strings"
	"testing"
)

func TestImagePermissionRepairGuidance(t *testing.T) {
	for _, tc := range []struct{ code, action string }{
		{"image_upload_application_scopes_missing", "configure_application"},
		{"image_upload_user_scopes_missing", "authorize_user"},
		{"image_upload_query_failed", "retry_check"},
	} {
		hint := larkImagePermissionHint(tc.code)
		if !strings.HasPrefix(hint, tc.action+":") || !strings.Contains(hint, "Text sending remains independent") || !strings.Contains(hint, "do not fall back to text or a subset of images") {
			t.Fatal(tc.code, hint)
		}
	}
}
