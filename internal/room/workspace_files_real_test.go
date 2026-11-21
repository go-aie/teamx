//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// Real serve process, DSH/model, CLI delivery and Chromium. The test never
// writes the delivered documents or image, and copies no platform connections.
func TestWorkspaceFilesRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_WORKSPACE_FILES_REAL") != "1" {
		t.Skip("set TEAMX_WORKSPACE_FILES_REAL=1, TEAMX_WORKSPACE_FILES_CONFIG and fresh absolute TEAMX_DSH_OUTPUT; Chromium required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "recorded_at": time.Now().UTC(), "apiKeyEnv": "TEAMX_FILES_UNUSED_KEY", "external_platform_writes": 0, "daily_deployment_changed": false}
	defer imageRealReport(t, out, report)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	configPath, err := filepath.Abs(os.Getenv("TEAMX_WORKSPACE_FILES_CONFIG"))
	must(err)
	cfg, err := loadStartupConfig(configPath, true)
	must(err)
	if cfg.Runtime == nil || cfg.Models == nil {
		t.Fatal("configured DSH and managed model catalog required")
	}
	entry, err := cfg.Models.resolve(modelSelection{Mode: "default"})
	must(err)
	t.Setenv("TEAMX_DSH_BINARY_PATH", cfg.Runtime.DSH.BinaryPath)
	args, err := json.Marshal(cfg.Runtime.DSH.BinaryArgs)
	must(err)
	t.Setenv("TEAMX_DSH_BINARY_ARGS", string(args))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	installation, err := testutil.InstalledDSH(ctx)
	must(err)
	report["installation"], report["model"] = installation, entry.ModelID
	// Hash JS launcher bytes as well as the Node executable used to run them.
	if len(installation.BinaryArgs) > 0 {
		data, err := os.ReadFile(installation.BinaryArgs[0])
		must(err)
		sum := sha256.Sum256(data)
		report["launcher_sha256"] = hex.EncodeToString(sum[:])
	}
	runtime := *cfg.Runtime
	runtime.DSH.BinaryPath, runtime.DSH.BinaryArgs = installation.BinaryPath, installation.BinaryArgs
	home, projects := filepath.Join(out, "home"), filepath.Join(out, "projects")
	other := filepath.Join(projects, "other")
	must(os.MkdirAll(home, 0700))
	must(os.MkdirAll(other, 0700))
	private, err := newModelCredentials(home)
	must(err)
	secret, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), "model-credentials", entry.CredentialRef))
	must(err)
	must(os.WriteFile(filepath.Join(private.dir, entry.CredentialRef), secret, 0600))
	credential, err := private.get(entry.CredentialRef)
	must(err)
	probe, err := dsh.Probe(ctx, dsh.Options{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Model: entry.materialize(), Credential: credential})
	must(err)
	must(saveModelValidation(private.dir, modelValidation{EntryID: entry.ID, Revision: entry.Revision, ExecutionDigest: entry.executionDigest(), CredentialRef: entry.CredentialRef, Result: probe}))
	report["model_probe"] = probe
	owner, err := newOpaqueID()
	must(err)
	caller := Caller{Author: Author{Kind: "human", ID: "owner_" + owner}}
	t.Setenv("USER", caller.ID)
	startup := startupConfig{Runtime: &runtime, Models: &modelCatalog{SchemaVersion: 1, Version: 1, Default: entry.ID, Entries: []modelEntry{entry}}}
	login := false
	startup.Access = accessConfig{OperatorID: caller.ID, LoginEnabled: &login}
	dataDir, teams := filepath.Join(home, "data"), filepath.Join(home, "teams")
	startup.Server.DataDir, startup.Server.TeamsDir, startup.Server.WorkspaceRoots = &dataDir, &teams, &[]string{projects}
	data, err := toml.Marshal(startup)
	must(err)
	isolatedConfig := filepath.Join(home, "config.toml")
	must(os.WriteFile(isolatedConfig, data, 0600))
	writeTestTeam(t, teams, teamDefinition{ID: "file-preview", Name: "文件闭环验收", Description: "Isolated real workspace preview acceptance", OpeningMessage: "文件预览验收草稿", Scheduling: schedulingSerial, Members: []teamMember{{ID: "writer", Name: "文档助理", Instructions: `You are a bounded local file acceptance assistant. Formally read the new human Room input with the supplied Inbox tool or TeamX CLI. Create or modify only the files explicitly requested, relative to your current working directory. You may use bash and Python standard library for these local files. Deliver exactly ONE concise Room message per human request via the configured TeamX CLI send command, using an explicit relative Markdown file link. Ordinary final output is private. Do not inspect credentials, environment, other directories or application source; do not use network/platform tools, delegate, change directory, create policies or schedule tasks. Stop after completing the current request.`}}})
	d := &realRoomDriver{cliDriver: cliDriver{ctx: ctx, dir: dataDir, binary: filepath.Join(out, "teamx")}, output: out, report: report, managementCaller: &caller}
	defer func() { must(d.stopReal()) }()
	build := exec.CommandContext(ctx, "go", "build", "-o", d.binary, "../../cmd/teamx")
	if result, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, result)
	}
	binary, err := os.ReadFile(d.binary)
	must(err)
	binarySum := sha256.Sum256(binary)
	report["backend_sha256"] = hex.EncodeToString(binarySum[:])
	epoch, err := d.startManagedConfig(1, isolatedConfig)
	must(err)
	var room roomSummary
	must(d.managementRequest("/v1/rooms/create", roomCreateRequest{ServiceEpoch: epoch, Caller: caller, roomSelection: roomSelection{CreateID: "files-real", TeamID: "file-preview", Name: "文件闭环验收"}}, &room))
	must(d.selectManaged(epoch, room, "files-initial"))
	roomDir := filepath.Join(dataDir, "rooms", room.RoomID)
	var saved managedRoomConfig
	must(jsonFile(filepath.Join(roomDir, "room-config.json"), &saved))
	member := saved.Members[0]
	workspace, err := filepath.EvalSymlinks(filepath.Join(roomDir, member.Workspace))
	must(err)
	report["room"], report["membership_id"] = room.RoomIdentity, member.MembershipID
	report["workspace"] = workspace
	task := "请在当前工作目录生成一个可阅读的中文示例：docs/preview.md，标题为‘真实文件预览’，正文包含标记 WORKSPACE_PREVIEW_V1、一个表格和一段代码；包含相对图片 ![示例图片](../images/preview.png) 和相对文档链接 [使用指南](guide.md#使用)。另生成 downloads/preview.bin，内容为恰好 32 MiB 的 ASCII F 字节，用于有界下载验收。同时生成 docs/guide.md（含‘使用’二级标题）和一张有效的 32×32 PNG images/preview.png，图片可用 Python 标准库生成。验证本地文件后，仅发一条简短 Room 消息，含 [预览示例](docs/preview.md) 链接。不要发送其他 Room 消息。"
	must(d.command(nil, "send", "--id", "files-create", "--text", task))
	_, err = d.waitAgent("writer", 1, "completed")
	must(err)
	state, err := readPublishedRoom(roomDir, room.RoomIdentity)
	must(err)
	var delivery RoomMessage
	for _, msg := range state.Messages {
		if msg.Author.Kind == "agent" {
			if delivery.Seq != 0 {
				t.Fatal("extra creation delivery")
			}
			delivery = msg
		}
	}
	rootID := workspaceRootID(room.RoomIdentity, member.MembershipID, workspace)
	if delivery.Seq == 0 || delivery.WorkspaceRootID != rootID || !strings.Contains(delivery.Text, "docs/preview.md") {
		t.Fatal("real CLI delivery lacks trusted frozen source", delivery)
	}
	report["initial_delivery"] = delivery
	hashes := func() map[string]string {
		t.Helper()
		result := map[string]string{}
		for _, path := range []string{"docs/preview.md", "docs/guide.md", "images/preview.png", "downloads/preview.bin"} {
			data, err := os.ReadFile(filepath.Join(workspace, path))
			must(err)
			sum := sha256.Sum256(data)
			result[path] = hex.EncodeToString(sum[:])
		}
		return result
	}
	initialHashes := hashes()
	report["initial_files_sha256"] = initialHashes
	var runs []json.RawMessage
	report["browser"] = &runs
	browser := func(phase string) {
		t.Helper()
		fixture, err := json.Marshal(map[string]any{"phase": phase, "room": room.RoomIdentity, "seq": delivery.Seq, "member": member.MembershipID, "other": other, "output": out, "download_sha256": initialHashes["downloads/preview.bin"]})
		must(err)
		cmd := exec.CommandContext(ctx, "node", "testdata/workspace_files_real_browser.cjs", d.endpoint, string(fixture))
		result, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("real browser %s: %v\n%s", phase, err, result)
		}
		if !json.Valid(result) {
			t.Fatalf("invalid browser evidence: %s", result)
		}
		runs = append(runs, json.RawMessage(result))
		t.Log("real browser verified:", phase)
	}
	browser("modify")
	_, err = d.waitAgent("writer", 2, "completed")
	must(err)
	modifiedHashes := hashes()
	report["modified_files_sha256"] = modifiedHashes
	if modifiedHashes["docs/preview.md"] == initialHashes["docs/preview.md"] || modifiedHashes["images/preview.png"] != initialHashes["images/preview.png"] || modifiedHashes["docs/guide.md"] != initialHashes["docs/guide.md"] {
		t.Fatal("model modified incorrect files")
	}
	state, err = readPublishedRoom(roomDir, room.RoomIdentity)
	must(err)
	agents, humans := 0, 0
	for _, msg := range state.Messages {
		if msg.Author.Kind == "agent" {
			agents++
			if msg.WorkspaceRootID != rootID {
				t.Fatal("modified delivery lost source")
			}
		}
		if msg.Author.Kind == "human" {
			humans++
		}
	}
	if agents != 2 || humans != 2 {
		t.Fatal("unexpected additional Room messages", agents, humans)
	}
	report["model_turns"], report["agent_deliveries"], report["human_requests"] = 2, agents, humans
	backend := func() map[string]any {
		t.Helper()
		agentDir := filepath.Join(roomDir, "agents", member.MembershipID)
		var ref sessionReference
		must(jsonFile(filepath.Join(agentDir, "session.json"), &ref))
		var info map[string]any
		must(jsonFile(filepath.Join(agentDir, ref.Directory, "backend.json"), &info))
		if info["sessionId"] == "" || info["sessionId"] == nil {
			t.Fatal("missing real DSH SessionID")
		}
		return map[string]any{"session_id": info["sessionId"], "workspace": info["workspace"]}
	}
	before := backend()
	report["before_restart"] = before
	must(d.stopReal())
	nextEpoch, err := d.startManagedConfig(2, isolatedConfig)
	must(err)
	if nextEpoch == epoch {
		t.Fatal("restart retained epoch")
	}
	must(d.selectManaged(nextEpoch, room, "files-restart"))
	browser("restart")
	after := backend()
	report["after_restart"] = after
	if !reflect.DeepEqual(before, after) {
		t.Fatal("read-only restart changed SessionID or cwd", before, after)
	}
	stateAfter, err := readPublishedRoom(roomDir, room.RoomIdentity)
	must(err)
	if !reflect.DeepEqual(state.Members, stateAfter.Members) || state.LatestSeq != stateAfter.LatestSeq {
		t.Fatal("read-only preview changed Agent cursors or messages")
	}
	browser("invalidate")
	if !reflect.DeepEqual(modifiedHashes, hashes()) {
		t.Fatal("directory switch or clear deleted/modified generated files")
	}
	report["restart_new_epoch"], report["read_only_agent_state_preserved"], report["generated_files_preserved"] = true, true, true
	report["status"] = "passed"
}
