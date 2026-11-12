//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

func TestScionMemberToolsIdentityAndHelp(t *testing.T) {
	t.Setenv("SCION_TOKEN", "operator-token-must-not-leak")
	t.Setenv("MODEL_API_KEY", "model-secret-must-not-leak")
	m, room, svc, c, members, dir := scionRoomFixture(t)
	bind := toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Enabled: true, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "scion", PlatformID: c.ID}, PlatformVersion: c.Version}
	if r := managerHTTP(m, "/v1/tools/bind", bind); r.Code == 200 {
		t.Fatal("unverified connection bound")
	}
	c = verifyScion(t, m, c)
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "scion", 0)
	// Selected executable metadata is authoritative for business and help;
	// neither may fall back to an unrelated catalog executable.
	m.toolCatalog[len(m.toolCatalog)-1].Executable = filepath.Join(dir, "missing-catalog-wrapper")
	client, endpoint := toolPipeServer(t, m)
	env := cliToolEnv(endpoint, svc, members[0])
	var out, stderr bytes.Buffer
	run := func(args ...string) error {
		out.Reset()
		stderr.Reset()
		return runToolsWithClient(context.Background(), args, bytes.NewReader(nil), &out, &stderr, env, client)
	}
	if err := run("list"); err != nil || !strings.Contains(out.String(), "scion") || strings.Contains(out.String(), "credentials") || strings.Contains(out.String(), "action_disabled_reason") {
		t.Fatal(err, out.String())
	}
	if err := run("skill", c.ToolEntryID); err != nil || !strings.Contains(out.String(), "scn land publishes") || !strings.Contains(out.String(), "scn review land N merges") {
		t.Fatal(err, out.String())
	}
	err := run("run", c.ToolEntryID, "--json", "--repo", "Owner/Repo", "review", "ls")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 23 || out.String() != "native-result\n" {
		t.Fatal(err, out.String(), stderr.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "executed"))
	if err != nil {
		t.Fatal(err)
	}
	var executed struct{ Credentials string }
	if json.Unmarshal(bytes.TrimSpace(data), &executed) != nil {
		t.Fatal(string(data))
	}
	if _, err = os.Stat(executed.Credentials); !os.IsNotExist(err) {
		t.Fatal("private credentials retained", err)
	}
	// No other membership inherits this tool, including help.
	other := cliToolEnv(endpoint, svc, members[1])
	if err = runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "--help"}, bytes.NewReader(nil), &out, &stderr, other, client); err == nil {
		t.Fatal("unbound member executed")
	}
	for _, args := range [][]string{{"--endpoint", "https://evil.invalid", "review", "ls"}, {"self-update", "--version", "v0.1.13"}} {
		if err = run(append([]string{"run", c.ToolEntryID}, args...)...); err == nil {
			t.Fatal("unsafe arguments admitted")
		}
	}
	scionWriteCredential(t, m, c.CLIProfile.Profile, "bob")
	if err = run("run", c.ToolEntryID, "review", "ls"); !errors.As(err, &exit) || exit.Code != 5 || !strings.Contains(stderr.String(), "identity_mismatch") {
		t.Fatal(err, stderr.String())
	}
	if after, _ := os.ReadFile(filepath.Join(dir, "executed")); !bytes.Equal(after, data) {
		t.Fatal("business executed as changed account")
	}
	if err = run("run", c.ToolEntryID, "review", "ls", "--help"); err != nil || out.String() != "native help\n" {
		t.Fatal(err, out.String(), stderr.String())
	}
	if err = os.Remove(filepath.Join(m.store.dir, "scion-profiles", c.CLIProfile.Profile, "credentials.json")); err != nil {
		t.Fatal(err)
	}
	if err = run("run", c.ToolEntryID, "review", "ls"); !errors.As(err, &exit) || !strings.Contains(stderr.String(), "credential_unavailable") {
		t.Fatal(err, stderr.String())
	}
	if err = run("run", c.ToolEntryID, "--help"); err != nil {
		t.Fatal(err)
	}
}

func TestScionCallLifecycle(t *testing.T) {
	parallelFixtureTest(t)
	for _, phase := range []string{"preflight", "block"} {
		for _, action := range []string{"cancel", "disable", "rotation", "logout", "deletion", "shutdown"} {
			t.Run(phase+"/"+action, func(t *testing.T) {
				m, room, svc, c, members, dir := scionRoomFixture(t)
				c = verifyScion(t, m, c)
				binding := bindCLITool(t, m, room.RoomIdentity, members[0], c, "scion", 0)
				if err := os.WriteFile(filepath.Join(dir, "mode"), []byte(phase), 0600); err != nil {
					t.Fatal(err)
				}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				m.mu.Lock()
				p, call, ctx, err := m.prepareToolLocked(parent, toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: "scion", Args: []string{"review", "ls"}})
				m.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				p.Output, p.Error = io.Discard, io.Discard
				result := make(chan int, 1)
				go func() { code := p.Run(ctx, []string{"review", "ls"}); m.finishPlatformCall(call); result <- code }()
				deadline := time.Now().Add(5 * time.Second)
				var credentials []byte
				for time.Now().Before(deadline) {
					credentials, err = os.ReadFile(filepath.Join(dir, "started"))
					if err == nil && len(credentials) > 0 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if err != nil || len(credentials) == 0 {
					t.Fatal("process not started", err)
				}
				switch action {
				case "cancel":
					cancel()
				case "disable":
					r := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: humanCaller, Revision: binding.Revision, Binding: tool.Binding{AgentID: members[0].AgentID, MembershipID: members[0].MembershipID, Tool: "scion", PlatformID: c.ID}})
					if r.Code != 200 {
						t.Fatal(r.Code, r.Body.String())
					}
				case "rotation":
					profile := *c.CLIProfile
					profile.Profile = strings.Repeat("e", 32)
					scionWriteCredential(t, m, profile.Profile, "alice")
					rotated, err := m.updatePlatform(managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: c.ID, Version: c.Version, Name: c.Name, Provider: c.Provider, URL: c.URL, CLIProfile: &profile, ReferencedRooms: []RoomIdentity{room.RoomIdentity}}, false)
					if err != nil || rotated.CredentialVersion <= c.CredentialVersion || rotated.Validation.Status != "unverified" {
						t.Fatal(rotated, err)
					}
				case "logout":
					r := managerHTTP(m, "/v1/platforms/scion/logout", scionAuthBody(m, map[string]any{"url": c.URL, "cli_profile": c.CLIProfile, "affected": []scionAuthPlatform{{ID: c.ID, Version: c.Version, ReferencedRooms: []RoomIdentity{room.RoomIdentity}}}}))
					if r.Code != 200 {
						t.Fatal(r.Code, r.Body.String())
					}
					if _, err := os.Stat(filepath.Join(m.store.dir, "scion-profiles", c.CLIProfile.Profile)); !os.IsNotExist(err) {
						t.Fatal("managed login survived logout", err)
					}
				case "deletion":
					m.mu.Lock()
					_, err := m.startDeletion(room.RoomIdentity)
					done := m.runtimes[room.RoomIdentity].operationDone
					m.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Fatal("deletion did not settle")
					}
				case "shutdown":
					if err := m.close(); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case code := <-result:
					if code != 130 {
						t.Fatal(code)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("call did not settle")
				}
				if _, err := os.Stat(string(credentials)); !os.IsNotExist(err) {
					t.Fatal("private credentials retained", err)
				}
				m.mu.Lock()
				pending := len(m.platformCalls)
				m.mu.Unlock()
				if pending != 0 {
					t.Fatal(pending)
				}
			})
		}
	}
}
