//go:build integration || e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform/tool"
)

// The controlled native program owns protocol assertions; this layer proves
// the production assembly and every consumer retain the selected program pair.
func scionProgramRelease(t *testing.T, native string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"teamx-scion": "#!/bin/sh\nexec \"$TEAMX_SCN_BIN\" \"$@\"\n",
		"scn":         native,
		"mode":        "ok",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestScionProgramUpgradeKeepsConnectionAndAdmittedCall(t *testing.T) {
	parallelFixtureTest(t)
	m, room, svc, c, members, _ := scionRoomFixture(t)
	c = verifyScion(t, m, c)
	bindCLITool(t, m, room.RoomIdentity, members[0], c, "scion", 0)
	before, err := m.platforms.Load()
	if err != nil {
		t.Fatal(err)
	}
	binding, err := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil {
		t.Fatal(err)
	}
	a, b := scionProgramRelease(t, scionToolFixture), scionProgramRelease(t, scionToolFixture)
	m.config.platformBinDir = a
	m.toolCatalog = nil
	req := toolsRequest{ServiceInfo: svc.info, Caller: agentCaller(members[0].AgentID, members[0].MembershipID), Tool: c.ToolEntryID, Args: []string{"review", "ls"}}
	admit := func() (tool.Process, *platformCall) {
		t.Helper()
		m.mu.Lock()
		p, call, _, err := m.prepareToolLocked(t.Context(), req)
		m.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		return p, call
	}
	pa, ca := admit()
	defer m.finishPlatformCall(ca)
	// A newly assembled service uses B, while the admitted A call retains A.
	m.config.platformBinDir = b
	if err := os.Remove(c.CLIProfile.Executable); err != nil {
		t.Fatal(err)
	}
	if validation := m.verifyScionProfile(t.Context(), c.URL, *c.CLIProfile); validation.Status != "valid" {
		t.Fatal(validation)
	}
	pb, cb := admit()
	defer m.finishPlatformCall(cb)
	for _, tc := range []struct {
		p   tool.Process
		dir string
	}{{pa, a}, {pb, b}} {
		if tc.p.Definition.Executable != filepath.Join(tc.dir, "teamx-scion") || environmentValue(tc.p.Definition.ProcessEnvironment, "TEAMX_SCN_BIN") != filepath.Join(tc.dir, "scn") {
			t.Fatal("wrong bound pair", tc.p.Definition)
		}
		var out, stderr bytes.Buffer
		tc.p.Output, tc.p.Error = &out, &stderr
		if code := tc.p.Run(t.Context(), req.Args); code != 23 || out.String() != "native-result\n" {
			t.Fatal(code, out.String(), stderr.String())
		}
		if _, err := os.Stat(filepath.Join(tc.dir, "executed")); err != nil {
			t.Fatal("wrong native program executed", err)
		}
	}
	// Help still works after credential loss, using B rather than the old path.
	credential := filepath.Join(m.store.dir, "scion-profiles", c.CLIProfile.Profile, "credentials.json")
	if err := os.Remove(credential); err != nil {
		t.Fatal(err)
	}
	req.Args = []string{"--help"}
	help, call := admit()
	defer m.finishPlatformCall(call)
	var out, stderr bytes.Buffer
	help.Output, help.Error = &out, &stderr
	if code := help.Run(t.Context(), req.Args); code != 0 || out.String() != "native help\n" {
		t.Fatal(code, out.String(), stderr.String())
	}
	// List and execution agree on a damaged dependency, even with a good PATH.
	m.config.environment = append(slices.Clone(m.config.environment), "TEAMX_SCN_BIN="+filepath.Join(b, "missing"))
	d, err := m.toolDefinition("scion")
	if err != nil {
		t.Fatal(err)
	}
	selection := toolSelection{}
	m.toolAvailability(before, before.Platforms[0], tool.Binding{}, d, &selection)
	if slices.Contains(selection.Capabilities, "run") || selection.UnavailableReason != "executable_unavailable" {
		t.Fatal(selection)
	}
	help, call = admit()
	defer m.finishPlatformCall(call)
	out.Reset()
	stderr.Reset()
	help.Output, help.Error = &out, &stderr
	if code := help.Run(t.Context(), req.Args); code != 127 || out.Len() != 0 {
		t.Fatal(code, out.String(), stderr.String())
	}
	after, err := m.platforms.Load()
	afterBinding, bindErr := os.ReadFile(m.toolBindingsPath(room.RoomIdentity))
	if err != nil || bindErr != nil || !reflect.DeepEqual(before, after) || !bytes.Equal(binding, afterBinding) {
		t.Fatal("program update changed persisted identity or grants", err, bindErr)
	}
}

func TestScionProgramWorkspaceUsesBoundPairAndSnapshot(t *testing.T) {
	parallelFixtureTest(t)
	m, room, c, _ := scionWorkspaceManager(t)
	release := scionProgramRelease(t, scionWorkspaceFixture)
	m.config.platformBinDir = release
	old := c.CLIProfile.Executable
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	target := scionWorkspaceRequest(m, room, c).Target
	var commands [][]string
	var credentialPath string
	run := func(ctx context.Context, dir string, env map[string]string, exe string, args ...string) ([]byte, error) {
		if exe != filepath.Join(release, "teamx-scion") || env["TEAMX_SCN_BIN"] != filepath.Join(release, "scn") {
			t.Fatal("Workspace bypassed binding", exe, env["TEAMX_SCN_BIN"])
		}
		if credentialPath == "" {
			credentialPath = env["SCION_CREDENTIALS_FILE"]
		} else if credentialPath != env["SCION_CREDENTIALS_FILE"] {
			t.Fatal("Workspace replaced snapshot within invocation")
		}
		// Replacing the source after version probing cannot change whoami/repo's user.
		if slices.Equal(args, []string{"--version"}) {
			scionWriteCredential(t, m, c.CLIProfile.Profile, "bob")
		}
		commands = append(commands, slices.Clone(args))
		return runScionPreparation(ctx, dir, env, exe, args...)
	}
	execute := m.scionWorkspaceExecute(room.RoomIdentity, target, run)
	if _, err := execute(t.Context(), t.TempDir(), nil, "ignored-old-program", "--json", "repo", "info", "OpenCSG/teamx-demo"); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 || !slices.Equal(commands[0], []string{"--version"}) || !slices.Equal(commands[1], []string{"--json", "whoami", c.URL}) {
		t.Fatal(commands)
	}
	if _, err := os.Stat(credentialPath); !os.IsNotExist(err) {
		t.Fatal("Workspace retained credential snapshot", err)
	}
	// Recheck with original credentials through production repo/branch/clone.
	scionWriteCredential(t, m, c.CLIProfile.Profile, "alice")
	applyWorkspace(t, m, scionWorkspaceRequest(m, room, c))
	if op := awaitWorkspace(t, m); op.Status != "completed" {
		t.Fatal(op)
	}
	if count, err := os.ReadFile(filepath.Join(release, "clones")); err != nil || string(count) != "3" {
		t.Fatal(string(count), err)
	}
}

func TestScionProgramManagedDiscoveryAndLogin(t *testing.T) {
	parallelFixtureTest(t)
	m := activationTestManager(t)
	m.config.environment = os.Environ()
	release := scionProgramRelease(t, scionAuthCLI)
	m.config.platformBinDir = release
	req := scionAuthBody(m, map[string]any{"url": "https://scion.invalid", "executable": "/removed-release/scn"})
	if r := managerHTTP(m, "/v1/platforms/scion/discover", req); r.Code != 400 {
		t.Fatal("expected rejected caller-selected program", r.Code, r.Body.String())
	}
	delete(req, "executable")
	r := managerHTTP(m, "/v1/platforms/scion/discover", req)
	var found struct {
		Executable     string
		ManagedProgram bool `json:"managed_program"`
	}
	if r.Code != 200 || json.Unmarshal(r.Body.Bytes(), &found) != nil || !found.ManagedProgram || found.Executable != filepath.Join(release, "teamx-scion") {
		t.Fatal(r.Code, r.Body.String())
	}
	id := strings.Repeat("a", 32)
	startScionAuth(t, m, "", id, "alice-pat")
	view := awaitScionAuth(t, m, id)
	if view.Status != "succeeded" || view.Profile.Executable != found.Executable {
		t.Fatal(view)
	}
	// Verification continues using the injected native override, never process env.
	wrong := scionProgramRelease(t, "#!/bin/sh\nprintf 'scn 0.2.0\\n'\n")
	m.config.environment = append(slices.Clone(m.config.environment), "TEAMX_SCN_BIN="+filepath.Join(wrong, "scn"))
	if v := m.verifyScionProfile(t.Context(), "https://scion.invalid", *view.Profile); v.Status == "valid" {
		t.Fatal("wrong dependency version accepted", v)
	}
	m.config.hostTools.restricted = true
	if _, err := m.scionProgram(view.Profile); err == nil {
		t.Fatal("multi-user Scion admitted")
	}
}
