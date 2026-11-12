package lark

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func permissionAccount() platform.AccountIdentity {
	return platform.AccountIdentity{IdentityScope: platform.IdentityScope{Provider: "lark", Instance: Origin("feishu"), Tenant: platform.ExternalID{Namespace: "lark:tenant_key", Value: "tenant"}}, ID: platform.ExternalID{Namespace: "lark:open_id:cli_fixture", Value: "ou_reader"}}
}

func findCapability(t *testing.T, all []Capability, name string) Capability {
	t.Helper()
	for _, c := range all {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing capability %s", name)
	return Capability{}
}

func TestPermissionDependenciesAndRequestScopeBoundary(t *testing.T) {
	all := DefaultScopes()
	if len(all) != 10 || !slices.IsSorted(all) {
		t.Fatal(all)
	}
	used := map[string]bool{}
	for _, d := range CapabilityDependencies() {
		for _, s := range d.Scopes {
			used[s] = true
		}
	}
	if len(used) != len(all) {
		t.Fatal("dependency and request sets diverged")
	}
	for _, s := range all {
		if !used[s] {
			t.Fatal("unused requested permission", s)
		}
	}
	all[0] = "mutated"
	if DefaultScopes()[0] == "mutated" {
		t.Fatal("mutable default set")
	}
	p := PermissionCheck{Application: ScopeCheck{State: "missing", Granted: []string{"im:chat:read", "future:scope"}}}
	scopes, err := p.RequestScopes()
	if err != nil || !reflect.DeepEqual(scopes, []string{"im:chat:read", "offline_access"}) {
		t.Fatal(scopes, err)
	}
	p.Application.State = "unchecked"
	if _, err := p.RequestScopes(); err == nil {
		t.Fatal("unknown application scope set was used")
	}
}

func TestScopeReadersRejectAmbiguousOrWrongIdentityOutput(t *testing.T) {
	required := []string{"im:message", "im:chat.members:read"}
	for _, tc := range []struct {
		raw  string
		code string
	}{
		{`{"ok":true,"granted":["im:message","im:message"],"missing":[]}`, "invalid_response"},
		{`{"ok":true,"granted":["im:message"],"missing":["im:chat.members:read"]}`, "invalid_response"},
		{`{"ok":false,"granted":["im:message"],"missing":["future:scope"]}`, "invalid_response"},
		{`{"granted":["im:message","im:chat.members:read"]}`, "invalid_response"},
		{`{"ok":false,"error":"no_token","missing":["im:message","im:chat.members:read"]}`, "credential_unavailable"},
	} {
		if _, err := parseUserScopes([]byte(tc.raw), required); ErrorCode(err) != tc.code {
			t.Fatal(tc.raw, err)
		}
	}
	user, err := parseUserScopes([]byte(`{"ok":false,"granted":["im:message"],"missing":["im:chat.members:read"]}`), required)
	if err != nil || user.State != "missing" {
		t.Fatal(user, err)
	}
	for _, raw := range []string{
		`{"appId":"cli_fixture","scopes":[]}`,
		`{"appId":"cli_fixture","tokenType":"user","userScopes":null}`,
	} {
		app, err := parseApplicationScopes([]byte(raw), permissionAccount(), append(required, "offline_access"))
		if err != nil || len(app.Missing) != 2 || slices.Contains(app.Missing, "offline_access") {
			t.Fatal(app, err)
		}
	}
	for _, tc := range []struct{ raw, code string }{
		{`{"appId":"cli_other","scopes":[]}`, "identity_mismatch"},
		{`{"appId":"cli_fixture"}`, "invalid_response"},
		{`{"appId":"cli_fixture","tokenType":"tenant","scopes":["im:message"]}`, "invalid_response"},
		{`{"appId":"cli_fixture","tokenType":"user","scopes":["im:message"]}`, "invalid_response"},
	} {
		if _, err := parseApplicationScopes([]byte(tc.raw), permissionAccount(), required); ErrorCode(err) != tc.code {
			t.Fatal(tc, err)
		}
	}
}

func TestPermissionCapabilitiesDegradeOnlyAffectedBranches(t *testing.T) {
	ready := func() PermissionCheck {
		return PermissionCheck{Application: ScopeCheck{State: "granted"}, User: ScopeCheck{State: "granted"}, Renewal: ScopeCheck{State: "granted"}, AccountFixed: true}
	}
	for _, tc := range []struct{ name, scope, branch, state, next string }{
		{"search", PeopleSearchScopes, "people.search", "blocked", "authorize_user"},
		{"target", SendTargetScopes, "message.send", "blocked", "authorize_user"},
		{"send", "im:message.send_as_user", "message.reply", "blocked", "authorize_user"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := ready()
			p.User.State, p.User.Missing = "missing", []string{tc.scope}
			all := permissionCapabilities(p, "darwin", "")
			v := findCapability(t, all, tc.branch)
			if v.State != tc.state || v.NextAction != tc.next {
				t.Fatal(v)
			}
			if findCapability(t, all, "message.read").State != "ready" {
				t.Fatal("optional failure blocked message reads")
			}
			if tc.name != "send" && findCapability(t, all, "message.reply").State != "ready" {
				t.Fatal("optional failure blocked reply")
			}
		})
	}
	p := ready()
	p.Application = ScopeCheck{State: "unchecked", ReasonCode: "network_error"}
	all := permissionCapabilities(p, "darwin", "")
	if v := findCapability(t, all, "message.read"); v.State != "unchecked" || v.NextAction != "retry_check" || len(v.MissingApplicationScopes) != 0 {
		t.Fatal(v)
	}
	if findCapability(t, all, "token.renew").State != "ready" {
		t.Fatal("OAuth control incorrectly depends on app scope listing")
	}
	p = ready()
	p.Renewal = ScopeCheck{State: "missing", ReasonCode: "refresh_token_missing"}
	all = permissionCapabilities(p, "darwin", "")
	if findCapability(t, all, "token.renew").State != "blocked" || findCapability(t, all, "message.reply").State != "ready" {
		t.Fatal(all)
	}
	for _, goos := range []string{"linux", "windows"} {
		all = permissionCapabilities(ready(), goos, "")
		for _, v := range all {
			if strings.HasPrefix(v.Name, "message.") && v.Name != "message.read" {
				if v.State != "unsupported" || v.NextAction != "" {
					t.Fatal(v)
				}
			} else if v.State != "ready" {
				t.Fatal(v)
			}
			if v.Configured != nil {
				t.Fatal("platform check invents member configuration")
			}
		}
	}
	// Decode the public contract too; readiness must not leak native credentials.
	b, _ := json.Marshal(PermissionCheck{Capabilities: all})
	if strings.Contains(string(b), "access_token") || strings.Contains(string(b), "device_code") {
		t.Fatal("credential in check contract")
	}
}
