//go:build e2e

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/tool"
)

// Offline, explicit opt-in: prepare a persistent Room using an EXISTING platform.
// Stop its normal server first; the Store lock prevents concurrent ownership.
// No login, credential copy, model turn, branch creation or remote write occurs.
func TestScionParallelDevelopmentEnvironment(t *testing.T) {
	if os.Getenv("TEAMX_SCION_PARALLEL_PREPARE") != "1" {
		t.Skip("explicit offline environment preparation opt-in required")
	}
	configPath, cli := os.Getenv("TEAMX_SCION_CONFIG"), os.Getenv("TEAMX_SCION_TEAMX_BIN")
	platformID, repo := os.Getenv("TEAMX_SCION_PLATFORM_ID"), os.Getenv("TEAMX_SCION_REPO")
	createID := os.Getenv("TEAMX_SCION_CREATE_ID")
	if !filepath.IsAbs(configPath) || !filepath.IsAbs(cli) || platformID == "" || !workspaceProjectPath(repo) || !safeMembershipID(createID) {
		t.Fatal("absolute config/CLI, existing platform ID, canonical repo and unique create ID required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "recorded_at": time.Now().UTC(), "account_mode": "shared", "expected_subject": "csgship", "browser": "not exercised", "dsh": "no model turns", "remote_writes": false}
	defer imageRealReport(t, output, report)
	config, err := readStartupConfig(configPath, true, false)
	if err != nil || config.Runtime == nil || config.Server.DataDir == nil || config.Server.TeamsDir == nil || config.Server.SessionMaxTurns == nil {
		t.Fatal("managed startup config required", err)
	}
	models, err := loadModelExecution(config)
	if err != nil {
		t.Fatal(err)
	}
	runtime := *config.Runtime
	runtime.ManagedModels = true
	m, err := newTeamRoomManager(*config.Server.DataDir, *config.Server.TeamsDir, *config.Server.SessionMaxTurns, &runtime)
	if err != nil {
		t.Fatal("stop the configured server before preparation", err)
	}
	defer func() {
		if err := m.close(); err != nil {
			t.Error(err)
		}
	}()
	m.settings = newSystemSettings(config)
	server := httptest.NewUnstartedServer(m)
	endpoint := "http://" + server.Listener.Addr().String()
	access, err := newServerAccess(config, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	server.Config.Handler = access.wrap(m)
	server.Start()
	defer server.Close()
	m.config = serviceConfig{models: models, settings: m.settings, access: access, endpoint: endpoint, cliPath: cli}
	admin, err := readAdminToken(adminTokenPath(config))
	if err != nil {
		t.Fatal(err)
	}
	client := cliClient{endpoint: endpoint, http: server.Client(), token: admin}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	human := Caller{Author: Author{Kind: "human", ID: config.Access.OperatorID}}
	request := func(path string, body, out any) {
		t.Helper()
		if err := client.request(ctx, path, body, out); err != nil {
			t.Fatal(path, err)
		}
	}
	// Verification refreshes the existing record without replacing its profile.
	var catalog struct {
		Platforms []managedPlatform `json:"platforms"`
	}
	request(fmt.Sprintf("/v1/platforms?service_epoch=%s&caller_id=%s", m.epoch, human.ID), nil, &catalog)
	var platform managedPlatform
	for _, candidate := range catalog.Platforms {
		if candidate.ID == platformID {
			platform = candidate
		}
	}
	if platform.ID == "" {
		t.Fatal("existing platform not found")
	}
	request("/v1/platforms/verify", managedPlatformRequest{ServiceEpoch: m.epoch, Caller: human, ID: platformID, Version: platform.Version}, &platform)
	if platform.Provider != "scion" || platform.Validation.Status != "valid" || platform.Validation.Account.ID.Value != "csgship" {
		t.Fatal("existing platform must verify as Scion csgship")
	}
	report["platform_id"], report["instance"], report["repo"] = platform.ID, platform.URL, repo
	var room roomSummary
	request("/v1/rooms/create", roomCreateRequest{ServiceEpoch: m.epoch, Caller: human, roomSelection: roomSelection{TeamID: "scion-parallel-development", CreateID: createID}}, &room)
	report["room"] = room.RoomIdentity
	saved, state := readManagedRoom(t, m, room)
	if saved.Scheduling != schedulingParallel || len(saved.Members) != 4 || state.LatestSeq != 0 {
		t.Fatal("an empty four-member parallel Room is required")
	}
	openRoom := func() roomOpen {
		openID, err := newOpaqueID()
		if err != nil {
			t.Fatal(err)
		}
		var opened roomOpen
		request("/v1/rooms/open", roomOpenRequest{RoomIdentity: room.RoomIdentity, ServiceEpoch: m.epoch, Caller: human, OpenID: openID}, &opened)
		for opened.Status == "running" {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
			request(fmt.Sprintf("/v1/rooms/open?service_epoch=%s&caller_id=%s&open_id=%s", m.epoch, human.ID, openID), nil, &opened)
		}
		if opened.Status != "completed" || opened.Service == nil {
			t.Fatal("Room open failed", opened.Error)
		}
		return opened
	}
	opened := openRoom()
	var bindings toolBindingsFile
	request(fmt.Sprintf("/v1/tools/bindings?service_epoch=%s&caller_id=%s&room_id=%s&room_instance=%s", m.epoch, human.ID, room.RoomID, room.RoomInstance), nil, &bindings)
	for _, member := range saved.Members {
		request("/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: human, Revision: bindings.Revision, Enabled: true, PlatformVersion: platform.Version, Binding: tool.Binding{AgentID: member.AgentID, MembershipID: member.MembershipID, Tool: "scion", PlatformID: platform.ID}}, &bindings)
	}
	var workspace workspaceResponse
	if saved.WorkspaceVersion == 1 {
		operationID, err := newOpaqueID()
		if err != nil {
			t.Fatal(err)
		}
		var operation workspaceOperation
		request("/v1/workspace/apply", workspaceApplyRequest{ServiceEpoch: m.epoch, RoomIdentity: room.RoomIdentity, Caller: human, OperationID: operationID, ExpectedVersion: saved.WorkspaceVersion, PlatformVersion: platform.Version, Target: workspaceConfig{Repository: &workspaceRepository{Kind: "scion", URL: platform.URL + "/" + repo, Branch: "main"}, Binding: &workspaceBinding{Provider: "scion", URL: platform.URL, Project: repo, PlatformID: platform.ID}}}, &operation)
		workspace.Operation = &operation
		for workspace.Operation != nil && !workspace.Operation.finished() {
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(100 * time.Millisecond):
			}
			request(fmt.Sprintf("/v1/workspace?service_epoch=%s&caller_id=%s&room_id=%s&room_instance=%s&operation_id=%s", m.epoch, human.ID, room.RoomID, room.RoomInstance, operationID), nil, &workspace)
		}
		if workspace.Operation == nil || workspace.Operation.Status != "completed" {
			t.Fatal("workspace preparation did not complete", workspace.Operation)
		}
	} else {
		request(fmt.Sprintf("/v1/workspace?service_epoch=%s&caller_id=%s&room_id=%s&room_instance=%s", m.epoch, human.ID, room.RoomID, room.RoomInstance), nil, &workspace)
		if workspace.Operation == nil || workspace.Operation.Status != "completed" {
			t.Fatal("existing Workspace is not complete")
		}
	}
	report["workspace_operation_id"] = workspace.Operation.OperationID
	// A completed receipt precedes runtime resumption; join that lifecycle too.
	m.mu.Lock()
	done := m.runtimes[room.RoomIdentity].operationDone
	m.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	opened = openRoom()
	saved, _ = readManagedRoom(t, m, room)
	seenPaths := map[string]bool{}
	var baseline string
	var evidence []map[string]any
	for _, member := range saved.Members {
		// Issue the same ephemeral membership-scoped credential as the DSH adapter.
		// This lets the normal CLI/HTTP tool chain run without opening a model turn.
		caller := Caller{Author: Author{Kind: "agent", ID: member.AgentID}, MembershipID: member.MembershipID}
		token, revoke, err := access.issueExecution(executionBinding{ServiceInfo: *opened.Service, Caller: caller})
		if err != nil {
			t.Fatal(err)
		}
		defer revoke()
		values := map[string]string{"TEAMX_ENDPOINT": endpoint, "TEAMX_ROOM_ID": room.RoomID, "TEAMX_ROOM_INSTANCE": room.RoomInstance, "TEAMX_SERVICE_EPOCH": opened.Service.ServiceEpoch, "TEAMX_AGENT_ID": member.AgentID, "TEAMX_MEMBERSHIP_ID": member.MembershipID, "TEAMX_EXECUTION_TOKEN": token}
		var env []string
		for _, entry := range os.Environ() {
			if !isRoomToolEnv(entry) {
				env = append(env, entry)
			}
		}
		for key, value := range values {
			env = append(env, key+"="+value)
		}
		run := func(args ...string) json.RawMessage {
			t.Helper()
			cmd := exec.CommandContext(ctx, cli, append([]string{"tools", "run", platform.ToolEntryID}, args...)...)
			cmd.Env = env
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil || !json.Valid(out) {
				t.Fatalf("member %s tool %v failed: %v; %s", member.AgentID, args, err, stderr.String())
			}
			return out
		}
		whoami := run("--json", "whoami", platform.URL)
		var identity struct{ Subject, Endpoint string }
		if json.Unmarshal(whoami, &identity) != nil || identity.Subject != "csgship" || identity.Endpoint != platform.URL {
			t.Fatal("member identity mismatch", member.AgentID)
		}
		repoInfo := run("--json", "repo", "info", repo)
		var repository struct {
			Name     string
			Archived bool
		}
		if json.Unmarshal(repoInfo, &repository) != nil || repository.Name != repo || repository.Archived {
			t.Fatal("canonical repository unavailable", member.AgentID)
		}
		status := run("--json", "status")
		var local struct {
			Ref                                  string `json:"ref"`
			Base                                 string
			Pending                              *string
			Detached                             bool
			Added, Modified, Deleted, Conflicted []string
		}
		if json.Unmarshal(status, &local) != nil || local.Ref != "main" || len(local.Base) != 66 || !strings.HasPrefix(local.Base, "c1") || local.Pending != nil || local.Detached || len(local.Added)+len(local.Modified)+len(local.Deleted)+len(local.Conflicted) != 0 {
			t.Fatal("invalid initial working copy", member.AgentID, string(status))
		}
		if baseline == "" {
			baseline = local.Base
		} else if baseline != local.Base {
			t.Fatal("members cloned different baselines")
		}
		path := filepath.Join(m.roomDir(room.RoomID), member.Workspace)
		var native struct{ Endpoint, Repo, Base string }
		data, err := os.ReadFile(filepath.Join(path, ".scion", "state.json"))
		if err != nil || json.Unmarshal(data, &native) != nil || native.Endpoint != platform.URL || native.Repo != repo || native.Base != baseline || seenPaths[path] || member.Workspace != workspaceMemberPath(*saved.Workspace, member.MembershipID) {
			t.Fatal("workspace ownership or native identity mismatch", member.AgentID)
		}
		seenPaths[path] = true
		item := map[string]any{"agent_id": member.AgentID, "membership_id": member.MembershipID, "platform_id": platform.ID, "subject": identity.Subject, "workspace": path, "base": baseline, "status": status, "whoami": whoami, "repo_info": repoInfo, "author": fmt.Sprintf("TeamX %s <%s@teamx.local>", member.AgentID, member.AgentID), "task_branch_pattern": fmt.Sprintf("teamx/%s/%s/<task>", room.RoomID, member.MembershipID)}
		if member.AgentID == "architect" {
			// Read-only authorization evidence; never probe permission with a write.
			organization := strings.Split(repo, "/")[0]
			members := run("--json", "org", "member", "ls", organization)
			var membership struct {
				Org     string
				Members []struct{ Subject, Role string }
			}
			if json.Unmarshal(members, &membership) != nil || membership.Org != organization {
				t.Fatal("invalid organization evidence")
			}
			var role string
			for _, account := range membership.Members {
				if account.Subject == identity.Subject {
					role = account.Role
				}
			}
			if role != "owner" && role != "developer" && role != "maintainer" {
				t.Fatal("shared subject lacks an organization development role")
			}
			item["organization_role"] = role
			item["branch_rules"] = run("--json", "--repo", repo, "repo", "rules", repo)
		}
		evidence = append(evidence, item)
		report["members"] = evidence
		t.Logf("%s membership=%s subject=%s base=%s workspace=%s", member.AgentID, member.MembershipID, identity.Subject, baseline, path)
	}
	var members serviceMembers
	request("/v1/member/list", apiRequest{ServiceInfo: *opened.Service, Caller: human}, &members)
	for _, member := range members.Members {
		if member.Execution == nil || member.Execution.Active || member.Execution.Agent == nil || member.Execution.Agent.TurnNumber != 0 {
			t.Fatal("preparation opened a member turn", member.AgentID)
		}
	}
	if members.LatestSeq != 0 {
		t.Fatal("preparation unexpectedly added a message")
	}
	report["baseline"], report["status"] = baseline, "passed"
}
