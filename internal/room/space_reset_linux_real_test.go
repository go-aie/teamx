//go:build e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/authn"
	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
)

type resetGatewaySource struct {
	hostedProviderFixture
	key string
}

func (p resetGatewaySource) GatewayAPIKey(context.Context, authn.Identity, url.Values) (string, error) {
	return p.key, nil
}

// Explicit Linux acceptance using a read-only source and new disposable Spaces.
// Real encrypted OAuth/refresh/DPoP material is copied, never logged, refreshed,
// revoked or written back to its source. Auth status only reads local evidence.
// SSO identity is a fixture; model validation uses the real DSH/default gateway.
func TestSpaceResetLinuxCredentialMaterials(t *testing.T) {
	source, output := os.Getenv("TEAMX_RESET_LINUX_SOURCE_SPACE"), os.Getenv("TEAMX_RESET_LINUX_OUTPUT")
	if source == "" {
		t.Skip("explicit Linux source Space and fresh private output required")
	}
	if runtime.GOOS != "linux" || os.Geteuid() == 0 || !filepath.IsAbs(source) || !filepath.IsAbs(output) {
		t.Fatal("non-root Linux and absolute paths required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"status": "failed", "real_cli": true, "real_encrypted_credentials": true, "real_default_model_probe": true, "source_modified": false, "refresh_executed": false, "sso": "fixture", "users": 2, "spaces": 4}
	defer func() {
		if t.Failed() {
			report["status"] = "failed"
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(filepath.Join(output, "report.json"), data, 0600); err != nil {
			t.Error(err)
		}
	}()
	deployment := filepath.Dir(filepath.Dir(source))
	var directory spaceDirectory
	if err := readIdentitySnapshot(filepath.Join(deployment, "spaces.json"), &directory); err != nil {
		t.Fatal(err)
	}
	var owner string
	for _, r := range directory.Spaces {
		if r.Roots.Home == source {
			owner = r.OwnerUserID
		}
	}
	if owner == "" {
		t.Fatal("source is not registered")
	}
	if selected := os.Getenv("TEAMX_RESET_LINUX_GATEWAY_OWNER"); selected != "" {
		if !validOperatorID(selected) {
			t.Fatal("invalid gateway owner")
		}
		owner = selected
	}
	var quota hostedGatewayCredential
	if err := readIdentitySnapshot(filepath.Join(deployment, "user-credentials", owner, "ai-gateway.json"), &quota); err != nil {
		t.Fatal("source quota unavailable")
	}
	original, err := readStartupConfig(filepath.Join(source, "config.toml"), true, false)
	if err != nil || original.Runtime == nil {
		t.Fatal("source launcher unavailable", err)
	}
	var profile platform.CLIProfile
	cliRoot := filepath.Join(source, "platform-cli", "lark")
	entries, err := os.ReadDir(cliRoot)
	if err != nil {
		t.Fatal(err)
	}
	var sourceProfile string
	for _, entry := range entries {
		p := filepath.Join(cliRoot, entry.Name())
		if err := readIdentitySnapshot(filepath.Join(p, "profile.json"), &profile); err == nil && lark.ValidProfile(profile) {
			if _, err := os.Stat(filepath.Join(p, "config", "config.json")); err == nil {
				sourceProfile = p
				break
			}
		}
	}
	if sourceProfile == "" {
		t.Fatal("no configured source CLI profile")
	}
	// Inventory real encrypted classes without decrypting or retaining secrets.
	classes := map[string]int{}
	if err := filepath.WalkDir(sourceProfile, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		switch {
		case d.Name() == "master.key":
			classes["master_key"]++
		case strings.HasPrefix(d.Name(), "dpop_"):
			classes["dpop"]++
		case strings.HasPrefix(d.Name(), "appsecret_"):
			classes["app_secret"]++
		case strings.HasSuffix(d.Name(), ".enc"):
			classes["oauth_credentials"]++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"master_key", "dpop", "app_secret", "oauth_credentials"} {
		if classes[class] == 0 {
			t.Fatal("real material missing", class)
		}
	}
	report["material_classes"] = classes
	// Production uses an executable beside TeamX. The test binary runs from its
	// evidence directory and resolves this exact installed dependency through PATH.
	t.Setenv("PATH", filepath.Dir(profile.Executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := filepath.Join(output, "home")
	s := hostedFixtureWithProvider(t, home, externalAuthConfig{Provider: "fixture", AllowedSubjects: []string{"alice", "bob"}}, resetGatewaySource{key: quota.APIKey})
	defer func() {
		if err := s.closeWithTimeout(); err != nil {
			t.Error("isolated Space ownership did not settle", err)
			return
		}
		// Successful and failed runs both discard copied/new private credentials.
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	}()
	s.assembly.factories.hostedResetAccepted = true
	s.assembly.config.teamsSource = resetSeedForTest(t)
	s.assembly.config.hostTools = hostToolPolicy{restricted: true}
	s.assembly.config.modelProbe = nil
	s.assembly.defaults = original.Runtime
	var records []SpaceRecord
	var modelResults []string
	var targetCookie *http.Cookie
	var targetRuntime *SpaceRuntime
	for _, account := range []string{"alice", "bob"} {
		cookie, _, id := hostedLogin(t, s, account)
		for i := 0; i < 2; i++ {
			if i == 1 {
				id, _ = newOpaqueID()
				w := hostedRequest(s, "/v1/spaces", spaceChangeInput{RequestID: id, Name: "验收空间"}, cookie, "")
				if w.Code != 202 {
					t.Fatal(w.Code)
				}
			}
			r, modelResult := waitResetLinuxModel(t, s, id)
			modelResults = append(modelResults, modelResult)
			record := spaceRecordForTest(t, s, id)
			records = append(records, record)
			if err := os.CopyFS(filepath.Join(record.Roots.Home, "platform-cli", "lark"), os.DirFS(cliRoot)); err != nil {
				t.Fatal(err)
			}
			if err := filepath.WalkDir(filepath.Join(record.Roots.Home, "platform-cli", "lark"), func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				mode := fs.FileMode(0600)
				if d.IsDir() {
					mode = 0700
				}
				return os.Chmod(p, mode)
			}); err != nil {
				t.Fatal(err)
			}
			if err := makeDurableDir(filepath.Join(record.Roots.Home, "platform-tmp")); err != nil {
				t.Fatal(err)
			}
			// auth status is local-only. Never force refresh of a copied grant:
			// that could rotate the live deployment's original refresh token.
			cmd := exec.CommandContext(t.Context(), profile.Executable, "--profile", profile.Profile, "auth", "status", "--json")
			copyProfile := filepath.Join(record.Roots.Home, "platform-cli", "lark", filepath.Base(sourceProfile))
			cmd.Env = append(os.Environ(), "LARKSUITE_CLI_CONFIG_DIR="+filepath.Join(copyProfile, "config"), "LARKSUITE_CLI_DATA_DIR="+filepath.Join(copyProfile, "data"), "TMPDIR="+filepath.Join(record.Roots.Home, "platform-tmp"), "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1")
			status, err := cmd.Output()
			if err != nil || !bytes.Contains(status, []byte(`"refreshExpiresAt"`)) {
				t.Fatal("real CLI refresh metadata unavailable")
			}
			mustWriteResetFile(t, filepath.Join(record.Roots.Home, "platform-tmp", "crashed", "material"), "acceptance temporary material")
			if len(records) == 1 {
				targetCookie = cookie
				targetRuntime = r
			}
		}
	}
	record := records[0]
	id := record.ID
	input := resetInputForTest(record)
	if w := hostedRequest(s, "/v1/spaces/"+id+"/reset", input, targetCookie, ""); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	waitResetForTest(t, s, id)
	next, modelResult := waitResetLinuxModel(t, s, id)
	modelResults = append(modelResults, modelResult)
	if next.RuntimeEpoch == targetRuntime.RuntimeEpoch {
		t.Fatal("runtime reused")
	}
	copyProfile := filepath.Join(record.Roots.Home, "platform-cli", "lark", filepath.Base(sourceProfile))
	for _, path := range []string{copyProfile, filepath.Join(record.Roots.Home, "platform-tmp", "crashed")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("credential material survived", err)
		}
	}

	for _, record := range records[1:] {
		if _, err := os.Stat(filepath.Join(record.Roots.Home, "platform-cli", "lark", filepath.Base(sourceProfile), "data", "lark-cli", "master.key")); err != nil {
			t.Fatal("other Space credentials changed", err)
		}
	}
	if t.Failed() {
		return
	}
	report["model_results"] = modelResults
	report["status"] = "passed"
}

func waitResetLinuxModel(t *testing.T, s *spaceServer, id string) (*SpaceRuntime, string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		entry := s.entries[id]
		s.mu.Unlock()
		if entry != nil {
			entry.mu.RLock()
			r, err := entry.runtime, entry.err
			entry.mu.RUnlock()
			if err != nil {
				t.Fatal("Space assembly failed", err)
			}
			if r != nil && r.State() == "ready" {
				return r, "validated"
			}
			if r != nil && r.State() == "setup_required" {
				var op savedSetupOperation
				if err := readIdentitySnapshot(filepath.Join(r.roots.Home, "model-setup.json"), &op); err == nil && op.Operation.Status == "failed" {
					return r, op.Operation.Code
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Space model initialization did not settle")
	return nil, ""
}
