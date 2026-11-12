package dsh

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionEnvironmentDoesNotInheritAdminOrSessionCredentials(t *testing.T) {
	for _, key := range []string{"TEAMX_ADMIN_TOKEN", "TEAMX_ADMIN_TOKEN_FILE", "TEAMX_EXECUTION_TOKEN", "TEAMX_EXECUTION_AUTH", "TEAMX_SESSION_COOKIE", "TEAMX_SESSION"} {
		t.Setenv(key, "admin-secret")
		if err := CheckToolEnv(map[string]string{key: "override"}, "MODEL_KEY"); err == nil {
			t.Fatal("persistent tool env accepted", key)
		}
	}
	opts := Options{StateDir: t.TempDir(), ToolEnv: map[string]string{"ORDINARY_SETTING": "retained"}, ExecutionToken: "ephemeral-execution"}
	env := strings.Join(environment(opts), "\n")
	if strings.Contains(env, "admin-secret") || !strings.Contains(env, "TEAMX_EXECUTION_TOKEN=ephemeral-execution") || !strings.Contains(env, "TEAMX_EXECUTION_AUTH=ephemeral-execution") || !strings.Contains(env, "ORDINARY_SETTING=retained") {
		t.Fatal("execution environment isolation failed")
	}
	opts.ExecutionToken = ""
	if strings.Contains(strings.Join(environment(opts), "\n"), "TEAMX_EXECUTION_TOKEN") {
		t.Fatal("execution credential inherited from server")
	}
}
func TestExecutionTokenDoesNotChangeMaterialOrRecoveryFingerprint(t *testing.T) {
	opts := fixtureOptions(t)
	opts.ExecutionToken = "first-execution"
	before := configDigest(opts)
	session := openFixture(t, opts)
	if err := session.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	opts.ExecutionToken = "replacement-execution"
	if before != configDigest(opts) {
		t.Fatal("execution token changed recovery identity")
	}
	if err := CheckResumeConfig(opts); err != nil {
		t.Fatal("token rotation blocked recovery", err)
	}
	err := filepath.WalkDir(opts.StateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if strings.Contains(string(data), "first-execution") || strings.Contains(string(data), "replacement-execution") {
				t.Error("execution token persisted", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
