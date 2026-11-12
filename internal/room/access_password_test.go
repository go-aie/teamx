package room

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminPasswordCreationPolicy(t *testing.T) {
	data, err := os.ReadFile("testdata/access_password_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Value, Issue string }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for i, input := range cases {
		if got := newAdminPasswordIssue(input.Value); got != input.Issue {
			t.Errorf("case %d: got %q, want %q", i, got, input.Issue)
		}
	}
	if got := newAdminPasswordIssue(strings.Repeat("orchid trail ", 30)); got != "length" {
		t.Fatal("oversized password accepted", got)
	}
}

func TestAccessSettingsRejectWeakPasswordsBeforeActivatingCredential(t *testing.T) {
	a, h := anonymousAccessFixture(t)
	original, err := os.ReadFile(a.config.path)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"Password2026!", "abcdefghijkl", "abcdabcdabcd", "short-value"} {
		w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, candidate, "", ""), nil)
		if w.Code != 400 || !strings.Contains(w.Body.String(), `"field":"token"`) {
			t.Fatal("weak candidate accepted", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), candidate) {
			t.Fatal("candidate leaked in rejection")
		}
	}
	unchanged, err := os.ReadFile(a.config.path)
	if err != nil || string(original) != string(unchanged) || !a.disabled {
		t.Fatal("rejected candidate changed access state", err)
	}
	if files, _ := filepath.Glob(filepath.Join(filepath.Dir(a.config.path), "auth", "admin-token*")); len(files) != 0 {
		t.Fatal("rejection installed a credential file")
	}
	const password = "orchidtrail7" // Exactly 12 characters; no composition rule.
	w := accessRequest(h, "POST", "/v1/access/settings", "", a.internal, settingsBody(a, true, false, password, "", ""), nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w = accessRequest(h, "GET", "/v1/ui", password, "", "", nil); w.Code != 200 {
		t.Fatal("12-character Bearer rejected", w.Code)
	}
	accessLogin(t, h, password)
	c := freshAccessConfig(t, filepath.Dir(a.config.path))
	if _, err := newServerAccess(c, a.internal); err != nil {
		t.Fatal("12-character password lost after restart", err)
	}
}

func TestLegacyWeakAdminPasswordStillAuthenticatesAndCanBeRetained(t *testing.T) {
	c, _ := testAccessConfig(t, t.TempDir())
	const existing = "passwordpassword"
	if err := os.WriteFile(adminTokenPath(c), []byte(existing+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := newServerAccess(c, "http://127.0.0.1:8088")
	if err != nil {
		t.Fatal("creation policy locked out existing installation", err)
	}
	a.document = newSystemSettings(c).configDocument
	h := a.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	accessLogin(t, h, existing)
	w := accessRequest(h, "POST", "/v1/access/settings", existing, "", settingsBody(a, true, false, "", existing, ""), nil)
	if w.Code != 200 {
		t.Fatal("mode-only save rejected existing password", w.Code, w.Body.String())
	}
	if w = accessRequest(h, "POST", "/v1/access/settings", existing, "", settingsBody(a, true, false, existing, existing, ""), nil); w.Code != 400 {
		t.Fatal("explicitly setting weak password bypassed creation policy", w.Code)
	}
}
