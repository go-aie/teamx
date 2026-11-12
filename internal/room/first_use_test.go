package room

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/pelletier/go-toml/v2"
)

func freshAccessConfig(t *testing.T, home string) startupConfig {
	t.Helper()
	c, err := readStartupConfig(filepath.Join(home, "config.toml"), false, false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestFirstAccessResumesAndNeverReissuesLostToken(t *testing.T) {
	home := t.TempDir()
	c := freshAccessConfig(t, home)
	enabled := true
	c.Access.LoginEnabled = &enabled
	if err := prepareFirstAccess(c); err != nil {
		t.Fatal(err)
	}
	c = freshAccessConfig(t, home)
	token, err := readAdminToken(adminTokenPath(c))
	if err != nil {
		t.Fatal(err)
	}
	identity := c.Access.OperatorID
	if err := prepareFirstAccess(c); err != nil {
		t.Fatal(err)
	}
	next, _ := readAdminToken(adminTokenPath(c))
	if token != next {
		t.Fatal("token changed")
	}
	// A crash after token publication, before config publication, resumes the
	// reserved operator and token rather than allocating another identity.
	record := firstAccessRecord{Version: 1, Operator: identity, TokenPath: adminTokenPath(c)}
	if err := (&Store{ops: defaultSnapshotOps()}).save(filepath.Join(home, "first-access.json"), record, 16<<10); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.path, []byte("# untouched\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareFirstAccess(freshAccessConfig(t, home)); err != nil {
		t.Fatal(err)
	}
	c = freshAccessConfig(t, home)
	next, _ = readAdminToken(adminTokenPath(c))
	if c.Access.OperatorID != identity || next != token {
		t.Fatal("interrupted initialization replaced access")
	}
	if err := os.Remove(adminTokenPath(c)); err != nil {
		t.Fatal(err)
	}
	if err := prepareFirstAccess(c); err == nil {
		t.Fatal("lost token reissued")
	}
	if _, err := os.Stat(adminTokenPath(c)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("token reappeared")
	}
}
func TestFirstAccessRejectsHistoricalIdentity(t *testing.T) {
	for _, name := range []string{"rooms.json", "installation-id"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			c := freshAccessConfig(t, home)
			if err := os.MkdirAll(*c.Server.DataDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(*c.Server.DataDir, name), []byte("history"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := prepareFirstAccess(c); err == nil {
				t.Fatal("historical instance treated as new")
			}
		})
	}
}
func TestFirstModelValidationIsDurableAsyncAndPrivate(t *testing.T) {
	home := t.TempDir()
	runtime := testTeamDefinition(t).Runtime
	runtime.Model.ContextWindow = 32768
	v := false
	runtime.Model.Compat.SupportsDeveloperRole = &v
	const key = "private-prefill-key"
	t.Setenv(runtime.Model.APIKeyEnv, key)
	source, err := toml.Marshal(map[string]any{"runtime": runtime, "server": map[string]any{"time_zone": "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.toml"), append([]byte("# keep comment\n"), source...), 0600); err != nil {
		t.Fatal(err)
	}
	c := freshAccessConfig(t, home)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f, err := newFirstModelSetup(context.Background(), c, newSystemSettings(c), func(ctx context.Context, options dsh.Options) (dsh.ProbeResult, error) {
		if options.Credential.Value != key || options.Model.Compat.SupportsDeveloperRole == nil || *options.Model.Compat.SupportsDeveloperRole {
			t.Error("probe lost bound credential or compat")
		}
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return dsh.ProbeResult{}, &dsh.ProbeError{Stage: "text", Code: "authentication"}
		}
		return successfulModelProbe(ctx, options)
	})
	if err != nil {
		t.Fatal(err)
	}
	input := setupModelInput{RequestID: "request-one", ModelParameters: parametersFromLegacyModel(runtime.Model)}
	first, err := f.submit(input)
	if err != nil || first.Status != "validating" {
		t.Fatal(first, err)
	}
	<-entered
	same, err := f.submit(input)
	if err != nil || same.ID != first.ID {
		t.Fatal(same, err)
	}
	changed := input
	changed.ModelID = "other"
	if _, err := f.submit(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("request reuse accepted", err)
	}
	input.RequestID = "request-two"
	if _, err := f.submit(input); !errors.Is(err, ErrBusy) {
		t.Fatal("overlapping probe admitted", err)
	}
	close(release)
	f.workers.Wait()
	if f.operation.Operation.Status != "failed" {
		t.Fatal(f.operation)
	}
	untouched, _ := os.ReadFile(c.path)
	if string(untouched) != string(append([]byte("# keep comment\n"), source...)) {
		t.Fatal("failed validation published")
	}
	if _, err := f.submit(input); err != nil {
		t.Fatal(err)
	}
	f.workers.Wait()
	if calls.Load() != 2 || f.operation.Operation.Status != "saved" {
		t.Fatal(calls.Load(), f.operation)
	}
	loaded, err := readStartupConfig(c.path, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Models == nil || loaded.Runtime.Model.ModelID != "" || !strings.Contains(string(loaded.source), "# keep comment") || strings.Contains(string(loaded.source), key) {
		t.Fatal("invalid managed publication")
	}
	execution, err := loadModelExecution(loaded)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := execution.Credentials.get(loaded.Models.Entries[0].CredentialRef)
	if err != nil || credential.Value != key {
		t.Fatal("credential not durable", err)
	}
	encoded, _ := json.Marshal(f.state())
	if strings.Contains(string(encoded), key) || strings.Contains(string(encoded), "api_key_env") {
		t.Fatal("private details in setup response")
	}
	// A validation that was still pending at shutdown is never replayed on boot.
	op := *f.operation
	op.Operation.Status = "validating"
	if err := f.persist(&op); err != nil {
		t.Fatal(err)
	}
	restarted, err := newFirstModelSetup(context.Background(), c, newSystemSettings(c), successfulModelProbe)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.operation.Operation.Code != "interrupted" {
		t.Fatal("validation auto-replayed")
	}
}
func TestFirstModelCannotWriteExistingData(t *testing.T) {
	home := t.TempDir()
	c := freshAccessConfig(t, home)
	if err := os.MkdirAll(*c.Server.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(*c.Server.DataDir, "rooms.json"), []byte("history"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := newFirstModelSetup(context.Background(), c, newSystemSettings(c), successfulModelProbe)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.submit(setupModelInput{RequestID: "blocked"}); err == nil {
		t.Fatal("existing data writable")
	}
}
func TestConfigSubtreeRemovalPreservesInlineSibling(t *testing.T) {
	source := []byte("# original\nruntime = {dsh={binary_path='/path'}, model={model_id='prefill'}} # comment\nenv = {OLD_KEY='secret', OTHER='keep'}\n")
	next, err := removeConfigSubtrees(source, []string{"runtime", "model"}, []string{"env", "OLD_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if toml.Unmarshal(next, &value) != nil {
		t.Fatal(string(next))
	}
	runtime := value["runtime"].(map[string]any)
	env := value["env"].(map[string]any)
	if runtime["model"] != nil || runtime["dsh"] == nil || env["OLD_KEY"] != nil || env["OTHER"] != "keep" || !strings.Contains(string(next), "# comment") {
		t.Fatal(string(next))
	}
}

func TestFirstUseStartupCreatesLocalAccessWithoutLogin(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan any, 1)
	done := make(chan error, 1)
	go func() {
		done <- runServer(ctx, []string{"--home", home, "--listen", "127.0.0.1:0"}, os.Stderr, os.Stderr, serverOptions{ready: func(info any) error { ready <- info; return nil }})
	}()
	var endpoint string
	select {
	case value := <-ready:
		data, _ := json.Marshal(value)
		var info serverTestReady
		if err := json.Unmarshal(data, &info); err != nil {
			t.Fatal(err)
		}
		endpoint = info.Endpoint
	case err := <-done:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("startup timed out")
	}
	c := freshAccessConfig(t, home)
	if !validOperatorID(c.Access.OperatorID) {
		t.Fatal("access not initialized")
	}
	if c.Access.LoginEnabled == nil || *c.Access.LoginEnabled {
		t.Fatal("first use requires login")
	}
	response, err := http.Get(endpoint + "/v1/access/session")
	if err != nil {
		t.Fatal(err)
	}
	var session struct {
		Operator     string `json:"operator"`
		LoginEnabled bool   `json:"login_enabled"`
	}
	err = json.NewDecoder(response.Body).Decode(&session)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || session.LoginEnabled || session.Operator != c.Access.OperatorID {
		t.Fatal("first-use session unavailable", session, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestFirstModelReplacesIncompletePrefill(t *testing.T) {
	for _, inline := range []bool{false, true} {
		t.Run(map[bool]string{false: "table", true: "inline"}[inline], func(t *testing.T) {
			home := t.TempDir()
			source := []byte("[runtime.model]\nprotocol='openai-completions'\nbase_url='https://example.invalid/v1'\nmodel_id='prefill'\n")
			if inline {
				source = []byte("runtime={model={protocol='openai-completions',base_url='https://example.invalid/v1',model_id='prefill'}}\n")
			}
			if err := os.WriteFile(filepath.Join(home, "config.toml"), source, 0600); err != nil {
				t.Fatal(err)
			}
			c := freshAccessConfig(t, home)
			f, err := newFirstModelSetup(context.Background(), c, newSystemSettings(c), successfulModelProbe)
			if err != nil {
				t.Fatal(err)
			}
			f.service.launcher.BinaryPath = "/fixture/launcher"
			input := setupModelInput{RequestID: "complete-prefill", ModelParameters: f.prefill, APIKey: "new-key"}
			if _, err := f.submit(input); err != nil {
				t.Fatal(err)
			}
			f.workers.Wait()
			if f.operation.Operation.Status != "saved" {
				t.Fatal(f.operation.Operation)
			}
			loaded, err := readStartupConfig(c.path, true, true)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Models == nil || loaded.Runtime.Model.ModelID != "" || loaded.Runtime.DSH.BinaryPath != "/fixture/launcher" {
				t.Fatal("incomplete prefill retained as execution source")
			}
		})
	}
}
func TestFirstModelDoesNotImportReservedCredential(t *testing.T) {
	for _, name := range []string{"TEAMX_ADMIN_TOKEN", "HOME", "DSH_HOME"} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			runtime := testTeamDefinition(t).Runtime
			runtime.Model.APIKeyEnv = name
			t.Setenv(name, "not-a-model-key")
			data, err := toml.Marshal(map[string]any{"runtime": runtime})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(home, "config.toml"), data, 0600); err != nil {
				t.Fatal(err)
			}
			c := freshAccessConfig(t, home)
			f, err := newFirstModelSetup(context.Background(), c, newSystemSettings(c), successfulModelProbe)
			if err != nil {
				t.Fatal(err)
			}
			if f.prefillKey != "" {
				t.Fatal("reserved credential imported")
			}
		})
	}
}
