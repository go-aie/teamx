package room

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReferenceActionRequestSizeLimits(t *testing.T) {
	base := referenceActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: "ref_" + strings.Repeat("a", 64), ID: "size-check", Operation: "object.comment"}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"empty", "", false}, {"whitespace", " \n", false},
		{"4095 bytes", strings.Repeat("x", 4095), true},
		{"4096 bytes", strings.Repeat("x", 4096), true},
		{"4097 bytes", strings.Repeat("x", 4097), false},
		{"multibyte below", strings.Repeat("中", 1365), true},
		{"multibyte above", strings.Repeat("中", 1366), false},

		{"large", strings.Repeat("x", 65<<10), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := base
			q.Input.Body = tc.body
			if err := referenceActionRequestError(q); (err == nil) != tc.valid {
				t.Fatal(err)
			}
		})
	}
	// Adapter input has its own serialized budget, independent of plain body text.
	for _, size := range []int{(16 << 10) - 1, 16 << 10, (16 << 10) + 1} {
		t.Run(fmt.Sprintf("serialized-%d", size), func(t *testing.T) {
			q := base
			q.Input.raw = `{"adapter":"` + strings.Repeat("x", size-len(`{"adapter":""}`)) + `"}`
			if len(q.Input.raw) != size {
				t.Fatal("invalid boundary fixture")
			}
			if err := referenceActionRequestError(q); (err == nil) != (size <= 16<<10) {
				t.Fatal(size, err)
			}
		})
	}
}

func TestAgentActionEnvelopeSizeLimits(t *testing.T) {
	for _, size := range []int{(64 << 10) - 1, 64 << 10, (64 << 10) + 1, 65 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			// Legal envelope padded to the exact wire limit; no manager or member setup
			// is necessary to check the production HTTP decoder.
			payload := `{"action_request":{"input":{"body":"ok"}}}`
			payload += strings.Repeat(" ", size-len(payload))
			req := httptest.NewRequest("POST", "/v1/actions/execute", strings.NewReader(payload))
			response := httptest.NewRecorder()
			var out apiRequest
			accepted := decodeToolRequest(response, req, &out)
			if accepted != (size <= 64<<10) {
				t.Fatal(size, accepted, response.Code, response.Body.String())
			}
			if accepted {
				if out.ReferenceAction == nil || out.ReferenceAction.Input.Body != "ok" {
					t.Fatal("decoded request lost")
				}
			} else {
				var failure map[string]any
				if response.Code != 400 || json.Unmarshal(response.Body.Bytes(), &failure) != nil || failure["code"] != "request" {
					t.Fatal(response.Code, response.Body.String())
				}
			}
		})
	}
}
