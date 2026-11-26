package opencsg

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/authn"
)

func TestOAuthGatewayBindsPersonalQuotaToVerifiedSubject(t *testing.T) {
	mode := ""
	var hits int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "OAuth private-iam-token" || r.Method != "GET" || r.URL.Path != "/api/v1/namespaces/owner-uuid/apikeys/builtin" || r.URL.RawQuery != "" {
			t.Error("key request did not bind the token and personal namespace")
		}
		w.Header().Set("Content-Type", "application/json")
		data := map[string]any{"token": "gk_owners-private-quota", "ns_uuid": "owner-uuid", "application": "aigateway", "is_active": true}
		switch mode {
		case "namespace":
			data["ns_uuid"] = "another-namespace"
		case "organization":
			data["organization_key"] = true
		case "inactive":
			data["is_active"] = false
		case "application":
			data["application"] = "git"
		case "invalid-key":
			data["token"] = "other-kind-of-key"
		case "oversized":
			data["token"] = "gk_" + strings.Repeat("x", 70<<10)
		case "redirect":
			http.Redirect(w, r, "https://foreign.invalid/key", 307)
			return
		case "unauthorized":
			w.WriteHeader(401)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer server.Close()
	gateway, err := NewOAuthGateway(server.URL, server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	id := authn.Identity{Provider: "opencsg", Authority: "https://iam.example", Subject: "owner-uuid", DisplayName: "display name is not a routing hint"}
	key, err := gateway.GatewayAPIKey(t.Context(), id, "private-iam-token")
	if err != nil || key != "gk_owners-private-quota" {
		t.Fatal("personal Key not returned", err)
	}
	for _, variant := range []string{"namespace", "organization", "inactive", "application", "invalid-key", "oversized", "redirect", "unauthorized"} {
		mode = variant
		key, err := gateway.GatewayAPIKey(t.Context(), id, "private-iam-token")
		if !errors.Is(err, ErrGatewayCredentials) || key != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("unverified key or credential escaped", variant, err)
		}
	}
	before := hits
	for _, subject := range []string{"", "../foreign", "owner?namespace=other", "owner\n"} {
		id.Subject = subject
		if _, err := gateway.GatewayAPIKey(t.Context(), id, "private-iam-token"); err == nil {
			t.Fatal("untrusted namespace accepted")
		}
	}
	if hits != before {
		t.Fatal("invalid namespace reached the network")
	}
}
