package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// Read-only acceptance through the same member CLI and HTTP process lifecycle.
// The operator supplies an already logged-in isolated native PAT profile.
func TestScionRealMemberRead(t *testing.T) {
	if os.Getenv("TEAMX_SCION_REAL") != "1" {
		t.Skip("set TEAMX_SCION_REAL=1 and TEAMX_SCION_TEST_CREDENTIALS, TEAMX_SCION_BIN, TEAMX_SCN_BIN")
	}
	data, err := os.ReadFile(os.Getenv("TEAMX_SCION_TEST_CREDENTIALS"))
	if err != nil {
		t.Fatal("test credentials unavailable")
	}
	bin := os.Getenv("TEAMX_SCION_BIN")
	if !filepath.IsAbs(bin) {
		t.Fatal("absolute TEAMX_SCION_BIN required")
	}
	const origin = "https://scion.opencsg.com"
	const repo = "OpenCSG/teamx-demo"
	root := t.TempDir()
	profiles := managerTestProfiles(t)
	m := testManager(t, root, profiles)
	room := managerCreate(t, m, "scion-s2-read")
	_, snapshot := readManagedRoom(t, m, room)
	member := snapshot.Members[0]
	profile := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), Executable: bin, CLIVersion: scion.CLIVersion}
	dir := filepath.Join(m.store.dir, "scion-profiles", profile.Profile)
	if os.MkdirAll(dir, 0700) != nil || os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0600) != nil {
		t.Fatal("cannot prepare private profile")
	}
	c := platformTestSave(t, m, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "Scion read acceptance", Provider: "scion", URL: origin, CLIProfile: profile})
	c = verifyScion(t, m, c)
	bindCLITool(t, m, room.RoomIdentity, member, c, "scion", 0)
	t.Logf("verified %s / %s", c.Validation.Account.ID.Namespace, c.Validation.Account.ID.Value)
	for _, restart := range []bool{false, true} {
		if restart {
			if err := m.close(); err != nil {
				t.Fatal(err)
			}
			m = testManager(t, root, profiles)
		}
		svc := eventLiveService(t, &eventHarness{m: m, room: room})
		client, endpoint := toolPipeServer(t, m)
		env := cliToolEnv(endpoint, svc, member)
		for _, check := range []struct {
			args   []string
			schema string
		}{
			{[]string{"run", "scion", "--json", "whoami", origin}, "scion.cli.whoami/v1"},
			{[]string{"run", "scion", "--json", "repo", "info", repo}, "scion.cli.repo/v1"},
			{[]string{"run", "scion", "--json", "--repo", repo, "review", "ls", "--state", "all", "--limit", "100"}, "scion.cli.reviews/v1"},
		} {
			var out, stderr bytes.Buffer
			if err := runToolsWithClient(context.Background(), check.args, bytes.NewReader(nil), &out, &stderr, env, client); err != nil {
				t.Fatalf("read failed (%s, restarted=%v): %v; %s", check.schema, restart, err, stderr.String())
			}
			var result struct {
				Schema, Subject, Name, Repo string
				Truncated                   bool
			}
			if json.Unmarshal(out.Bytes(), &result) != nil || result.Schema != check.schema {
				t.Fatal("invalid native read schema")
			}
			if result.Schema == "scion.cli.whoami/v1" && result.Subject != c.Validation.Account.ID.Value || result.Schema == "scion.cli.repo/v1" && result.Name != repo || result.Schema == "scion.cli.reviews/v1" && (result.Repo != repo || result.Truncated) {
				t.Fatal("native identity or repository mismatch")
			}
			t.Logf("restarted=%v schema=%s subject=%s repo=%s%s truncated=%v", restart, result.Schema, result.Subject, result.Name, result.Repo, result.Truncated)
		}
	}
}

// Exercise PAT stdin login, automatic discovery, connection verification and a
// normal member read with both accepted executable types. No repository writes.
func TestScionRealLoginDiscovery(t *testing.T) {
	if os.Getenv("TEAMX_SCION_AUTH_REAL") != "1" {
		t.Skip("explicit real PAT login/discovery opt-in required")
	}
	const origin = "https://scion.opencsg.com"
	source, err := os.ReadFile(os.Getenv("TEAMX_SCION_TEST_CREDENTIALS"))
	var credentials map[string]string
	if err != nil || json.Unmarshal(source, &credentials) != nil || !platform.ValidCredential(credentials[origin]) {
		t.Fatal("existing private native PAT source required")
	}
	for name, exe := range map[string]string{"native": os.Getenv("TEAMX_SCN_BIN"), "wrapper": os.Getenv("TEAMX_SCION_BIN")} {
		t.Run(name, func(t *testing.T) {
			if !filepath.IsAbs(exe) {
				t.Fatal("explicit absolute CLI required")
			}
			m := testManager(t, t.TempDir(), managerTestProfiles(t))
			room := managerCreate(t, m, "auth-real")
			_, snapshot := readManagedRoom(t, m, room)
			member := snapshot.Members[0]
			id := strings.Repeat("a", 32)
			r := managerHTTP(m, "/v1/platform/connections/scion/login", scionAuthBody(m, map[string]any{"session_id": id, "url": origin, "executable": exe, "pat": credentials[origin]}))
			if r.Code != 200 {
				t.Fatal("native login start rejected", r.Code)
			}
			var view scionAuthView
			deadline := time.Now().Add(65 * time.Second)
			for {
				r = managerHTTP(m, "/v1/platform/connections/scion/status", scionAuthBody(m, map[string]any{"session_id": id}))
				if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &view) != nil {
					t.Fatal("native login status unavailable")
				}
				if view.Status != "starting" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("native login did not settle")
				}
				time.Sleep(50 * time.Millisecond)
			}
			if view.Status != "succeeded" || view.Profile == nil || view.Username != "csgship" {
				t.Fatal("native PAT login did not verify the expected subject", view.Status, view.ErrorCode)
			}
			r = managerHTTP(m, "/v1/platform/connections/scion/discover", scionAuthBody(m, map[string]any{"url": origin, "executable": exe}))
			var discovery scion.Discovery
			if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &discovery) != nil || len(discovery.Profiles) != 1 || discovery.Profiles[0].Profile != view.Profile.Profile || discovery.Profiles[0].Username != view.Username {
				t.Fatal("native account discovery failed")
			}
			c := platformTestSave(t, m, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("b", 32), Name: "Scion native login acceptance", Provider: "scion", URL: origin, CLIProfile: view.Profile})
			c = verifyScion(t, m, c)
			bindCLITool(t, m, room.RoomIdentity, member, c, "scion", 0)
			svc := eventLiveService(t, &eventHarness{m: m, room: room})
			client, endpoint := toolPipeServer(t, m)
			var out, stderr bytes.Buffer
			if err := runToolsWithClient(context.Background(), []string{"run", "scion", "--json", "repo", "info", "OpenCSG/teamx-demo"}, bytes.NewReader(nil), &out, &stderr, cliToolEnv(endpoint, svc, member), client); err != nil {
				t.Fatal("member repository read failed", err)
			}
			var repo struct{ Schema, Name string }
			if json.Unmarshal(out.Bytes(), &repo) != nil || repo.Schema != "scion.cli.repo/v1" || repo.Name != "OpenCSG/teamx-demo" {
				t.Fatal("unexpected native repository")
			}
			t.Logf("%s: PAT stdin login -> verified subject %s -> automatic profile selection -> saved connection -> member repo read passed", name, view.Username)
		})
	}
	after, err := os.ReadFile(os.Getenv("TEAMX_SCION_TEST_CREDENTIALS"))
	if err != nil || !bytes.Equal(source, after) {
		t.Fatal("source native credentials changed")
	}
}
