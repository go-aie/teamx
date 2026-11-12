package dsh

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundCredentialOverridesAmbientAndRetainsRecoveryVersion(t *testing.T) {
	for _, ambient := range []string{"", "wrong-host-key"} {
		t.Run(ambient, func(t *testing.T) {
			first := fixtureOptions(t)
			second := first
			second.StateDir = filepath.Join(filepath.Dir(first.StateDir), "second")
			t.Setenv(first.Model.APIKeyEnv, ambient)
			first.Credential = &Credential{Ref: "version-A", Value: "private-bound-key-A"}
			second.Credential = &Credential{Ref: "version-B", Value: "private-bound-key-B"}
			for _, opts := range []Options{first, second} {
				session, err := Open(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				result, err := session.Run(context.Background(), opts.Credential.Value)
				if err != nil || strings.Contains(result.Text, opts.Credential.Value) || !strings.Contains(result.Text, "[REDACTED]") {
					t.Fatal("bound key not redacted", err)
				}
				if err := session.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				wrong := opts
				value := *opts.Credential
				value.Ref += "changed"
				wrong.Credential = &value
				if err := CheckResumeConfig(wrong); err == nil {
					t.Fatal("changed credential version restored")
				}
				restored, err := Resume(context.Background(), opts)
				if err != nil {
					t.Fatal(err)
				}
				if err := restored.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(opts)
				if strings.Contains(string(body), "private-bound-key") {
					t.Fatal("credential serialized")
				}
				for _, name := range []string{"backend.json", "home/cordis.patch.yml"} {
					data, err := os.ReadFile(filepath.Join(opts.StateDir, name))
					if err != nil || strings.Contains(string(data), "private-bound-key") {
						t.Fatal("credential materialized", err)
					}
				}
			}
		})
	}
	opts := fixtureOptions(t)
	opts.Credential = &Credential{}
	if _, err := Open(context.Background(), opts); err == nil {
		t.Fatal("empty binding fell back to process environment")
	}
}

func TestProbeEnvironmentExcludesBusinessAuthority(t *testing.T) {
	t.Setenv("PLATFORM_SECRET", "do-not-copy")
	t.Setenv("OPENAI_API_KEY", "ambient-key")
	t.Setenv("TEAMX_EXECUTION_TOKEN", "do-not-copy")
	opts := Options{StateDir: t.TempDir(), Workspace: t.TempDir(), Model: ModelConfig{APIKeyEnv: "OPENAI_API_KEY"}, Credential: &Credential{Value: "bound-key"}, probe: &probeOptions{}}
	env := strings.Join(environment(opts), "\n")
	if strings.Contains(env, "do-not-copy") || strings.Contains(env, "ambient-key") || !strings.Contains(env, "OPENAI_API_KEY=bound-key") {
		t.Fatal("probe inherited authority")
	}
}

func TestProbeDiagnosticsAreClassifiedAndRedacted(t *testing.T) {
	for _, test := range []struct{ message, code string }{
		{"request failed ENOTFOUND secret-key", "dns"},
		{"ECONNREFUSED secret-key", "connection"},
		{"UNABLE_TO_VERIFY_LEAF_SIGNATURE secret-key", "tls"},
		{"ETIMEDOUT secret-key", "timeout"},
		{"HTTP 401 secret-key", "authentication"},
		{"HTTP 404 secret-key", "route_model_access"},
		{"HTTP 429 secret-key", "rate_quota"},
		{"HTTP 500 secret-key", "upstream_5xx"},
		{"HTTP 400 secret-key", "parameters_roles"},
		{"unstructured secret-key", "tool_acp"},
	} {
		err := probeFailure("text", &rpcError{Code: -32603, Message: test.message})
		diagnostic := err.(*ProbeError)
		if diagnostic.Code != test.code || strings.Contains(err.Error(), "secret-key") {
			t.Fatal(test.code, err)
		}
	}
}
