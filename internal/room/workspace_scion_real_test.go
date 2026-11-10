//go:build e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/scion"
)

// This opt-in acceptance clones only the already authorized demo repository.
// It exercises production management, credentials and CLI; model collaboration
// and remote business writes are deliberately the subsequent S4 acceptance.
func TestScionRealWorkspace(t *testing.T) {
	if os.Getenv("TEAMX_SCION_REAL") != "1" {
		t.Skip("set TEAMX_SCION_REAL and the S2 isolated credentials/CLI inputs")
	}
	data, err := os.ReadFile(os.Getenv("TEAMX_SCION_TEST_CREDENTIALS"))
	if err != nil {
		t.Fatal("credentials unavailable")
	}
	bin := os.Getenv("TEAMX_SCION_BIN")
	if !filepath.IsAbs(bin) {
		t.Fatal("absolute TEAMX_SCION_BIN required")
	}
	const origin = "https://scion.opencsg.com"
	root := t.TempDir()
	profiles := managerTestProfiles(t)
	setup := func() *roomManager {
		m := testManager(t, root, profiles)
		m.config = serviceConfig{endpoint: "http://127.0.0.1:1", cliPath: os.Args[0]}
		m.factory = controlledActivationFactory(nil, nil)
		return m
	}
	m := setup()
	// Three memberships with software-development role names, without changing
	// production templates (S4 owns the role protocol changes).
	team := m.teams["takeout-dinner"]
	for i, id := range []string{"architect", "developer", "reviewer"} {
		team.Members[i].ID = id
		team.Members[i].Name = id
	}
	m.teams[team.ID] = team
	room := managerCreate(t, m, "scion-s3b-real")
	openRoomOK(t, m, room, "initial")
	profile := &platform.CLIProfile{Version: 1, Brand: "scion", Profile: strings.Repeat("c", 32), Executable: bin, CLIVersion: scion.CLIVersion}
	dir := filepath.Join(m.store.dir, "scion-profiles", profile.Profile)
	if os.MkdirAll(dir, 0700) != nil || os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0600) != nil {
		t.Fatal("cannot prepare profile")
	}
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "Scion Workspace acceptance", Provider: "scion", URL: origin, CLIProfile: profile})
	c = verifyScion(t, m, c)
	req := scionWorkspaceRequest(m, room, c)
	req.Target.Repository.Branch = "definitely-missing-s3b"
	if r := managerHTTP(m, "/v1/workspace/apply", req); r.Code == 200 {
		t.Fatal("missing branch accepted")
	}
	if len(m.index.WorkspaceOperations) != 0 {
		t.Fatal("invalid branch reserved operation")
	}
	req.Target.Repository.Branch = ""
	// Controlled failure of the second clone, before spawning native clone; the
	// first and remaining clones still use the real native CLI and platform.
	clones := 0
	m.workspaceRun = func(ctx context.Context, dir string, env map[string]string, exe string, args ...string) ([]byte, error) {
		if len(args) > 0 && args[0] == "clone" {
			clones++
			if clones == 2 {
				return nil, errors.New("controlled clone failure")
			}
		}
		return runScionPreparation(ctx, dir, env, exe, args...)
	}
	applyWorkspace(t, m, req)
	failed := awaitWorkspace(t, m)
	if failed.Status != "failed" || failed.ConfigCommitted || clones != 2 {
		t.Fatal("expected controlled second clone failure", failed.Status, failed.ErrorCode, clones)
	}
	first := filepath.Join(m.roomDir(room.RoomID), failed.Members[0].Workspace)
	firstState, err := os.ReadFile(filepath.Join(first, ".scion/state.json"))
	if err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(m.roomDir(room.RoomID), failed.Members[1].Workspace)
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("unexpected partial target")
	}
	retryWorkspaceHTTP(t, m, room, req.OperationID)
	blocked := awaitWorkspace(t, m)
	if blocked.Status != "failed" || clones != 2 {
		t.Fatal("unchecked failure replayed")
	}
	if err := os.Remove(second + ".clone-pending"); err != nil {
		t.Fatal(err)
	}
	retryWorkspaceHTTP(t, m, room, req.OperationID)
	done := awaitWorkspace(t, m)
	if done.Status != "completed" || clones != 4 || done.OperationID != failed.OperationID {
		t.Fatal(done.Status, done.Phase, done.ErrorCode, clones)
	}
	saved, _ := readManagedRoom(t, m, room)
	if saved.Workspace.Repository.Branch != "main" || len(saved.Members) != 3 || saved.Workspace.Binding.ProjectID != 0 {
		t.Fatal("invalid effective workspace")
	}
	preserved, _ := os.ReadFile(filepath.Join(first, ".scion/state.json"))
	if !bytes.Equal(firstState, preserved) {
		t.Fatal("first clone replaced")
	}
	svc, _ := m.runtimeServiceLocked(room.RoomIdentity)
	client, endpoint := toolPipeServer(t, m)
	var out, stderr bytes.Buffer
	revision := uint64(0)
	for i, member := range saved.Members {
		// Clone authority does not silently grant a member the native CLI tool.
		runtimeMember := MemberState{AgentID: member.AgentID, MembershipID: member.MembershipID}
		env := cliToolEnv(endpoint, svc, runtimeMember)
		if err := runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "--help"}, bytes.NewReader(nil), &out, &stderr, env, client); err == nil {
			t.Fatal("clone implicitly enabled member tool")
		}
		bindings := bindCLITool(t, m, room.RoomIdentity, runtimeMember, c, "scion", revision)
		revision = bindings.Revision
		env = cliToolEnv(endpoint, svc, runtimeMember)
		out.Reset()
		stderr.Reset()
		if err := runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "--json", "status"}, bytes.NewReader(nil), &out, &stderr, env, client); err != nil {
			t.Fatal("member native status", err, stderr.String())
		}
		var state struct {
			Format               int
			Endpoint, Repo, Base string
		}
		target := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
		raw, err := os.ReadFile(filepath.Join(target, ".scion/state.json"))
		if err != nil || json.Unmarshal(raw, &state) != nil || state.Format != 1 || state.Endpoint != origin || state.Repo != "OpenCSG/teamx-demo" || len(state.Base) != 66 {
			t.Fatal("invalid native clone")
		}
		t.Logf("role=%s independent-clone=%d repo=%s base=%s", member.AgentID, i+1, state.Repo, state.Base)
	}
	// Detach only the reviewer via its actual member tool. Preserve its detached
	// copy and the developer's dirty files across a local revisit and restart.
	reviewer := saved.Members[2]
	var state struct{ Base string }
	raw, _ := os.ReadFile(filepath.Join(m.roomDir(room.RoomID), reviewer.Workspace, ".scion/state.json"))
	json.Unmarshal(raw, &state)
	out.Reset()
	stderr.Reset()
	if err := runToolsWithClient(context.Background(), []string{"run", c.ToolEntryID, "--json", "switch", "--detach", state.Base}, bytes.NewReader(nil), &out, &stderr, cliToolEnv(endpoint, svc, MemberState{AgentID: reviewer.AgentID, MembershipID: reviewer.MembershipID}), client); err != nil {
		t.Fatal("detach", err, stderr.String())
	}
	developer := saved.Members[1]
	dirty := filepath.Join(m.roomDir(room.RoomID), developer.Workspace, "s3b-local-only.txt")
	if os.WriteFile(dirty, []byte("uncommitted isolated workspace evidence"), 0600) != nil {
		t.Fatal("dirty file")
	}
	before := map[string][]byte{}
	for _, member := range saved.Members {
		target := filepath.Join(m.roomDir(room.RoomID), member.Workspace, ".scion/state.json")
		before[target], _ = os.ReadFile(target)
	}
	if err := m.close(); err != nil {
		t.Fatal(err)
	}
	// Dependency disabled on purpose: reopening performs no native CLI call.
	native := os.Getenv("TEAMX_SCN_BIN")
	t.Setenv("TEAMX_SCN_BIN", "/nonexistent-scn-local-revisit")
	m = setup()
	openRoomOK(t, m, room, "restarted-local")
	for target, previous := range before {
		after, err := os.ReadFile(target)
		if err != nil || !bytes.Equal(previous, after) {
			t.Fatal("restart rewrote native state")
		}
	}
	if current, err := os.ReadFile(dirty); err != nil || !strings.Contains(string(current), "uncommitted") {
		t.Fatal("restart removed dirty work")
	}
	status := queryWorkspace(t, m, room, req.OperationID)
	if status.Operation.Status != "completed" || status.Version != 2 || status.CredentialStatus != "configured" {
		t.Fatal("lost operation receipt")
	}
	t.Setenv("TEAMX_SCN_BIN", native)
	t.Log("same-operation controlled failure/retry, three native clones, member status/detach, offline revisit and normal manager restart passed; no remote writes")
}
