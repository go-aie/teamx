//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/testutil"
	"github.com/pelletier/go-toml/v2"
)

// A real serve process, Chromium, installed DSH and configured model. Writes
// are confined to a fresh evidence directory; no platform bindings are copied.
func TestWorkspaceDirectoriesRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_WORKSPACE_DIRECTORIES_REAL") != "1" {
		t.Skip("set TEAMX_WORKSPACE_DIRECTORIES_REAL=1, TEAMX_WORKSPACE_DIRECTORIES_CONFIG and fresh TEAMX_DSH_OUTPUT; Chromium required")
	}
	output := imageRealOutput(t)
	owner, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	caller := Caller{Author: Author{Kind: "human", ID: "owner_" + owner}}
	t.Setenv("USER", caller.ID)
	report := map[string]any{"status": "failed", "external_business_writes": 0, "apiKeyEnv": "TEAMX_WORKSPACE_UNUSED_KEY", "recorded_at": time.Now().UTC(), "device": "desktop Chromium; physical phone not exercised", "docker": "separate deployment gate"}
	defer imageRealReport(t, output, report)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	configPath := os.Getenv("TEAMX_WORKSPACE_DIRECTORIES_CONFIG")
	cfg, err := loadStartupConfig(configPath, true)
	must(err)
	if cfg.Runtime == nil || cfg.Models == nil {
		t.Fatal("a DSH launcher and managed model catalog are required")
	}
	entry, err := cfg.Models.resolve(modelSelection{Mode: "default"})
	must(err)
	// An explicit packaged launcher overrides a developer source checkout.
	if os.Getenv("TEAMX_DSH_BINARY_PATH") == "" {
		t.Setenv("TEAMX_DSH_BINARY_PATH", cfg.Runtime.DSH.BinaryPath)
		args, _ := json.Marshal(cfg.Runtime.DSH.BinaryArgs)
		t.Setenv("TEAMX_DSH_BINARY_ARGS", string(args))
	}
	ctx, cancel := context.WithTimeout(t.Context(), 18*time.Minute)
	defer cancel()
	installation, err := testutil.InstalledDSH(ctx)
	must(err)
	report["installation"], report["model"] = installation, entry.ModelID
	runtime := *cfg.Runtime
	runtime.DSH.BinaryPath, runtime.DSH.BinaryArgs = installation.BinaryPath, installation.BinaryArgs
	if installation.BinaryPath != cfg.Runtime.DSH.BinaryPath || !reflect.DeepEqual(installation.BinaryArgs, cfg.Runtime.DSH.BinaryArgs) {
		// Do not attribute a source launcher's provenance to an installed binary.
		runtime.DSH.Artifact.Source = ""
		runtime.DSH.Artifact.Version = ""
	}
	home, projects := filepath.Join(output, "home"), filepath.Join(output, "projects")
	shared, other := filepath.Join(projects, "共享 项目"), filepath.Join(projects, "另一个 项目")
	for _, dir := range []string{home, shared, other} {
		must(os.MkdirAll(dir, 0700))
	}
	// Copy only the chosen immutable credential into the isolated private home.
	credential, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), "model-credentials", entry.CredentialRef))
	must(err)
	private, err := newModelCredentials(home)
	must(err)
	must(os.WriteFile(filepath.Join(private.dir, entry.CredentialRef), credential, 0600))
	key, err := private.get(entry.CredentialRef)
	must(err)
	probe, err := dsh.Probe(ctx, dsh.Options{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Model: entry.materialize(), Credential: key})
	must(err)
	validation := modelValidation{EntryID: entry.ID, Revision: entry.Revision, ExecutionDigest: entry.executionDigest(), CredentialRef: entry.CredentialRef, Result: probe}
	must(saveModelValidation(private.dir, validation))
	report["model_probe"] = probe
	startup := startupConfig{Runtime: &runtime, Models: &modelCatalog{SchemaVersion: 1, Version: 1, Default: entry.ID, Entries: []modelEntry{entry}}}
	login := false // Dedicated loopback acceptance only.
	startup.Access = accessConfig{OperatorID: caller.ID, LoginEnabled: &login}
	dataDir, teams := filepath.Join(home, "data"), filepath.Join(home, "teams")
	startup.Server.DataDir, startup.Server.TeamsDir, startup.Server.WorkspaceRoots = &dataDir, &teams, &[]string{projects}
	data, err := toml.Marshal(startup)
	must(err)
	isolatedConfig := filepath.Join(home, "config.toml")
	must(os.WriteFile(isolatedConfig, data, 0600))
	role := `You are a bounded local Workspace acceptance member. Read the new Room input formally using the supplied inbox tool or TeamX CLI, then execute only its exact Python file proof with bash. Use only your current working directory and the named TEAMX_AGENT_ID environment variable. Never change directory, inspect credentials, use network/platform tools, delegate, send Room messages, or do unrelated work. Respond privately with DIRECTORY_PROOF_OK after success.`
	writeTestTeam(t, teams, teamDefinition{ID: "directories", Name: "目录验收", Description: "Isolated real cwd acceptance", OpeningMessage: "仅草稿", Scheduling: schedulingSerial, Members: []teamMember{{ID: "developer", Name: "Developer", Instructions: role}, {ID: "reviewer", Name: "Reviewer", Instructions: role}}})
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: dataDir, binary: filepath.Join(output, "teamx")}, output: output, report: report, managementCaller: &caller}
	defer func() { must(d.stopReal()) }()
	build := exec.CommandContext(ctx, "go", "build", "-o", d.binary, "../../cmd/teamx")
	if result, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, result)
	}
	epoch, err := d.startManagedConfig(1, isolatedConfig)
	must(err)
	var room roomSummary
	must(d.managementRequest("/v1/rooms/create", roomCreateRequest{ServiceEpoch: epoch, Caller: caller, roomSelection: roomSelection{CreateID: "directories", TeamID: "directories", Name: "Workspace directories 验收"}}, &room))
	must(d.selectManaged(epoch, room, "initial"))
	roomDir := filepath.Join(dataDir, "rooms", room.RoomID)
	var initial managedRoomConfig
	must(jsonFile(filepath.Join(roomDir, "room-config.json"), &initial))
	report["room"] = room.RoomIdentity
	report["paths"] = map[string]string{"shared": shared, "other": other, "room": roomDir}

	var browserRuns []json.RawMessage
	report["browser"] = &browserRuns
	choose := func(index int, path, mode string) {
		t.Helper()
		fixture, err := json.Marshal(map[string]any{"index": index, "path": path, "mode": mode, "root": projects, "room": room.RoomIdentity, "epoch": epoch, "caller": caller.ID, "screenshots": output})
		must(err)
		cmd := exec.CommandContext(ctx, "node", "testdata/workspace_directories_runtime.cjs", d.endpoint, string(fixture))
		result, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("real browser: %v\n%s", err, result)
		}
		if !json.Valid(result) {
			t.Fatalf("browser did not return evidence: %s", result)
		}
		browserRuns = append(browserRuns, json.RawMessage(result))
		var receipt struct {
			OperationID string `json:"operation_id"`
		}
		must(json.Unmarshal(result, &receipt))
		must(d.until(func() (bool, error) {
			status, err := d.realWorkspaceStatus(epoch, room)
			if err != nil {
				return false, err
			}
			if status.Operation == nil || status.Operation.OperationID != receipt.OperationID {
				return false, nil
			}
			if status.Operation != nil && status.Operation.Status == "failed" {
				return false, fmt.Errorf("directory operation failed at %s: %s", status.Operation.Phase, status.Operation.ErrorCode)
			}
			if status.Operation != nil && status.Operation.Status == "completed" && status.Active != nil {
				d.info = *status.Active
				return true, nil
			}
			return false, nil
		}))
	}
	type backendProof struct {
		SessionID string `json:"sessionId"`
		Workspace string `json:"workspace"`
	}
	backends := func() map[string]backendProof {
		t.Helper()
		result := map[string]backendProof{}
		for _, member := range initial.Members {
			agentDir := filepath.Join(roomDir, "agents", member.MembershipID)
			var ref sessionReference
			must(jsonFile(filepath.Join(agentDir, "session.json"), &ref))
			var proof backendProof
			must(jsonFile(filepath.Join(agentDir, ref.Directory, "backend.json"), &proof))
			if proof.SessionID == "" {
				t.Fatal("empty real SessionID")
			}
			result[member.AgentID] = proof
		}
		return result
	}
	turn := uint64(0)
	run := func(phase string, paths map[string]string, extra string) map[string]backendProof {
		t.Helper()
		turn++
		// The model, not the test owner, creates these cwd/visibility proofs.
		code := "import os,json; from pathlib import Path; agent=os.environ['TEAMX_AGENT_ID']; " + extra + "; Path('proof-'+agent+'-" + phase + ".json').write_text(json.dumps({'cwd':os.getcwd(),'agent':agent,'phase':'" + phase + "'}))"
		must(d.until(func() (bool, error) {
			err := d.command(nil, "send", "--id", phase, "--text", "Read this new Room message formally. Run this exact Python code in your current directory using bash and python3 (quote safely):\n"+code+"\nReturn DIRECTORY_PROOF_OK privately; do not send a Room message.")
			if err != nil && strings.Contains(err.Error(), "busy:") {
				return false, nil // Retry only the same stable ID and exact body.
			}
			return err == nil, err
		}))
		for _, member := range initial.Members {
			_, err := d.waitAgent(member.AgentID, turn, "completed")
			must(err)
			var proof struct{ CWD, Agent, Phase string }
			must(jsonFile(filepath.Join(paths[member.AgentID], "proof-"+member.AgentID+"-"+phase+".json"), &proof))
			if proof.CWD != paths[member.AgentID] || proof.Agent != member.AgentID || proof.Phase != phase {
				t.Fatal("model file proof disagrees with expected cwd", proof)
			}
		}
		result := backends()
		for agent, proof := range result {
			if proof.Workspace != paths[agent] {
				t.Fatal("Backend cwd disagrees with actual file", agent, proof)
			}
		}
		report[phase] = result
		t.Log("verified real DSH phase:", phase)
		return result
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = shared
		result, err := cmd.CombinedOutput()
		must(err)
		return strings.TrimSpace(string(result))
	}
	git("init", "-q")
	must(os.WriteFile(filepath.Join(shared, "tracked.txt"), []byte("baseline\n"), 0600))
	git("add", "tracked.txt")
	git("-c", "user.name=Workspace Acceptance", "-c", "user.email=workspace@example.invalid", "commit", "-qm", "local baseline")
	git("remote", "add", "origin", "https://example.invalid/unchanged.git")
	must(os.WriteFile(filepath.Join(shared, "tracked.txt"), []byte("keep dirty content\n"), 0600))
	head, dirty, origin := git("rev-parse", "HEAD"), git("diff", "--", "tracked.txt"), git("config", "--local", "--list")
	choose(0, shared, "choose")
	choose(1, shared, "choose")
	sharedPaths := map[string]string{"developer": shared, "reviewer": shared}
	first := run("seed", sharedPaths, "Path('shared-uncommitted.txt').write_text('SHARED_FROM_DEVELOPER') if agent=='developer' else None")
	if first["developer"].SessionID == first["reviewer"].SessionID {
		t.Fatal("shared cwd must retain distinct private Sessions")
	}
	run("read-shared", sharedPaths, "assert Path('shared-uncommitted.txt').read_text()=='SHARED_FROM_DEVELOPER'")
	choose(0, other, "choose")
	split := map[string]string{"developer": other, "reviewer": shared}
	second := run("switch", split, "pass")
	if second["developer"].SessionID == first["developer"].SessionID || second["reviewer"] != first["reviewer"] {
		t.Fatal("directory switch rotated incorrect Sessions")
	}
	must(d.stopReal())
	epoch, err = d.startManagedConfig(2, isolatedConfig)
	must(err)
	must(d.selectManaged(epoch, room, "restart"))
	restarted := run("restart", split, "pass")
	if !reflect.DeepEqual(second, restarted) {
		t.Fatal("normal process restart changed SessionIDs or cwd")
	}
	choose(0, "", "default")
	defaults := map[string]string{"developer": filepath.Join(roomDir, initial.Members[0].Workspace), "reviewer": shared}
	third := run("default", defaults, "pass")
	if third["developer"].SessionID == second["developer"].SessionID || third["reviewer"] != second["reviewer"] {
		t.Fatal("restore default affected wrong Session")
	}
	// A failed startup retains its path and exposes the management repair UI.
	must(d.stopReal())
	must(os.Rename(shared, shared+"-offline"))
	epoch, err = d.startManagedConfig(3, isolatedConfig)
	must(err)
	must(d.until(func() (bool, error) {
		catalog, err := d.managedCatalog()
		for _, item := range catalog.Rooms {
			if item.RoomIdentity == room.RoomIdentity && item.RuntimeStatus == "failed" {
				return true, err
			}
		}
		return false, err
	}))
	status, err := d.realWorkspaceStatus(epoch, room)
	must(err)
	if status.DirectoryError != "directory_not_found" || status.Workspace.MemberDirectories[initial.Members[1].MembershipID] != shared {
		t.Fatal("missing external directory silently fell back", status.DirectoryError)
	}
	report["unavailable_directory"] = status.DirectoryError
	choose(1, "", "repair")
	defaults["reviewer"] = filepath.Join(roomDir, initial.Members[1].Workspace)
	repaired := run("repair", defaults, "pass")
	if repaired["developer"] != third["developer"] || repaired["reviewer"].SessionID == third["reviewer"].SessionID {
		t.Fatal("unavailable-directory repair affected wrong Session")
	}
	must(os.Rename(shared+"-offline", shared))
	if git("rev-parse", "HEAD") != head || git("diff", "--", "tracked.txt") != dirty || git("config", "--local", "--list") != origin {
		t.Fatal("external repository was modified by directory management")
	}
	content, err := os.ReadFile(filepath.Join(shared, "shared-uncommitted.txt"))
	must(err)
	if string(content) != "SHARED_FROM_DEVELOPER" {
		t.Fatal("external shared file lost")
	}
	for _, phase := range []string{"switch", "restart"} {
		if _, err := os.Stat(filepath.Join(other, "proof-developer-"+phase+".json")); err != nil {
			t.Fatal("restoring default removed the previous external files", err)
		}
	}
	report["external_files_and_git_preserved"], report["status"] = true, "passed"
}
