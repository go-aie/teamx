//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/inputstream"
	"github.com/go-aie/teamx/internal/platform/attachment"
)

func TestInputSimplePersistentRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_INPUT_SIMPLE_REAL") != "1" {
		t.Skip("opt-in isolated persistent runtime acceptance")
	}
	cfg, entry, credential := inputAcceptanceModel(t)
	output := os.Getenv("TEAMX_INPUT_SIMPLE_OUTPUT")
	if !filepath.IsAbs(output) {
		t.Fatal("fresh absolute evidence directory required")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		t.Fatal(err)
	}
	h, s := unifiedReaderFixtureWithTimeout(t, 1, 3*time.Minute)
	s.service.referenceResolver = referenceTestResolver
	imageFile := os.Getenv("TEAMX_INPUT_SIMPLE_IMAGE_FILE")
	expectedVisual := os.Getenv("TEAMX_INPUT_SIMPLE_IMAGE_EXPECT")
	if imageFile != "" && (!filepath.IsAbs(imageFile) || expectedVisual == "") {
		t.Fatal("saved image acceptance requires an absolute image path and a pixel-only expectation")
	}
	imageMode := imageFile != "" || os.Getenv("TEAMX_INPUT_SIMPLE_IMAGE") == "1"
	var downloads atomic.Int32
	var imageDigest string
	if imageMode {
		data, mime := savedInputImage(t, imageFile)
		imageDigest = inputstream.Digest(data)
		s.service.materialAttachments = &materialAttachments{download: func(context.Context, attachmentRequest, *attachmentReader, attachment.ImageLimits) ([]byte, string, *attachmentAccess, error) {
			downloads.Add(1)
			return data, mime, &attachmentAccess{}, nil
		}}
	}
	// This fixture uses an isolated notification, including when its image was
	// fetched in a separate read-only platform test.
	for _, key := range []string{"one", "two"} {
		item := storedGitLab(s.storage.id, key, "events", "7", "42", "RECEIPT_"+key+" saved evidence; do not comment or merge")
		if imageMode && key == "one" {
			item.Text += " ![saved diagram](/uploads/524bb5cf042cd0bb3ef9a7a117a744b7/diagram.png)"
			item.Source.ProjectID = 7
			item.Source.URL = "https://gitlab.example.com/group/repo/-/merge_requests/42"
		}
		receivePlatform(t, s.storage, item)
	}
	agent, _ := h.store.LoadAgent(s.storage.id)
	agent.TurnNumber = 2
	agent.Turn.Number = 2
	if err := h.store.SaveAgent(agent); err != nil {
		t.Fatal(err)
	}
	s = startPlatformReader(t, h, s.storage, s.service, 2)
	s.images = imageMode
	a, _, _ := accessFixture(t)
	initial, _ := issueInboxCredential(t, a, s.service, s.storage.id, false)
	token, revoke, err := a.issueInputBinding(s)
	if err != nil {
		t.Fatal(err)
	}
	defer revoke()
	transport := os.Getenv("TEAMX_INPUT_SIMPLE_TRANSPORT")
	cliMode := transport == "cli"
	if imageFile != "" && cliMode {
		t.Fatal("saved image recovery acceptance uses HTTP MCP")
	}
	handler := http.NotFoundHandler()
	if cliMode {
		s.service.inboxMode = "cli"
		s.service.access = a
		handler = http.HandlerFunc(s.service.serveInputCLI)
	}
	server := httptest.NewServer(a.wrap(handler))
	defer server.Close()
	a.mu.Lock()
	a.internal = server.URL
	a.mu.Unlock()
	workspace := filepath.Join(output, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(output, "runtime")
	if imageFile != "" {
		stateDir = filepath.Join(t.TempDir(), "runtime")
	}
	instructions := `Use only supplied inbox tools for this isolated read-only task. No shell, file, environment, network, delegation or external actions. First read the Room human message, then list the directory with limit=1, formally read MR !42 one entry at a time until both saved messages have been read. Report both exact RECEIPT markers and preserve the no-comment/no-merge restriction. At most 12 tool calls.` + "\n" + unifiedAgentGuide()
	if imageFile != "" {
		instructions += "\nThe first saved notice includes a screenshot. Transcribe the error text visible in the screenshot in addition to reporting the markers. If no image is supplied, state that it cannot be read."
	}
	opts := dsh.Options{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: cfg.Runtime.DSH.BinaryArgs, Artifact: cfg.Runtime.DSH.Artifact, Workspace: workspace, StateDir: stateDir, Instructions: instructions, Model: entry.materialize(), Credential: credential, InputBridge: true, MCPServers: []dsh.MCPHTTPServer{mcpHTTPConfiguration(server.URL, initial)}, Limits: dsh.Limits{TurnTimeout: 2 * time.Minute}}
	if debug := os.Getenv("TEAMX_INPUT_SIMPLE_DSH_DEBUG"); debug != "" {
		opts.BinaryPath = debug
		opts.BinaryArgs = nil
	}
	if value := os.Getenv("TEAMX_INPUT_SIMPLE_MAX_OUTPUT"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil {
			t.Fatal(err)
		}
		opts.Model.MaxTokens = n
	}
	if cliMode {
		cliPath := filepath.Join(workspace, "teamx")
		command := exec.Command("go", "build", "-o", cliPath, "../../cmd/teamx")
		if b, err := command.CombinedOutput(); err != nil {
			t.Fatal(err, string(b))
		}
		cliToken, revokeCLI, err := a.issueExecution(executionBinding{ServiceInfo: s.service.info, Caller: agentCallerIdentity(s.storage.id)})
		if err != nil {
			t.Fatal(err)
		}
		defer revokeCLI()
		opts.ExecutionToken = cliToken
		opts.MCPServers = nil
		opts.ToolEnv = map[string]string{"TEAMX_CLI": cliPath, "TEAMX_ENDPOINT": server.URL, "TEAMX_ROOM_ID": s.storage.id.RoomID, "TEAMX_ROOM_INSTANCE": s.storage.id.RoomInstance, "TEAMX_SERVICE_EPOCH": s.service.info.ServiceEpoch, "TEAMX_AGENT_ID": s.storage.id.AgentID, "TEAMX_MEMBERSHIP_ID": s.storage.id.MembershipID, "TEAMX_INPUT_CONTRACT": inputstream.Contract}
		opts.Instructions = strings.ReplaceAll(instructions, "Use only supplied inbox tools", "Use only bash to run the configured TeamX Inbox CLI")
		opts.Instructions = strings.ReplaceAll(opts.Instructions, "No shell, file, environment, network, delegation or external actions.", "No other shell commands, files, environment inspection, network, delegation or external actions. Execute only one inbox read per bash invocation, wait for its result, then continue.")
		opts.Instructions += "\nCLI: \"$TEAMX_CLI\" inbox list --limit 1; \"$TEAMX_CLI\" inbox read --stream REF --limit 1. Use the directory stream_ref values; no other flags are needed."
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	session, err := dsh.Open(ctx, opts)
	if session != nil {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := session.Close(closeCtx); err != nil {
				t.Error(err)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	binding, _ := json.Marshal(map[string]string{"token": token, "endpoint": server.URL})
	if err = os.WriteFile(filepath.Join(stateDir, "input-execution.json"), binding, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(stateDir, "input-execution.json"))
	c, err := s.catalog(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	directory, _, err := c.Directory()
	if err != nil {
		t.Fatal(err)
	}
	s.storage.catalogs = append(s.storage.catalogs, c)
	s.rememberDirectory(directory)
	b, _ := json.Marshal(directory)
	start := time.Now()
	result, runErr := session.Run(ctx, "Input directory (JSON):\n"+string(b))
	state, stateErr := s.storage.repository.State(s.execution)
	report := map[string]any{"instructions": opts.Instructions, "transport": transport, "directory": directory, "result_text": result.Text, "tools": result.Tools, "stop_reason": result.StopReason, "elapsed_seconds": time.Since(start).Seconds(), "state": state, "image_fixture": imageMode, "fixture_downloads": downloads.Load(), "material_sha256": imageDigest, "pixel_text_match": expectedVisual != "" && strings.Contains(strings.ToLower(result.Text), strings.ToLower(expectedVisual)), "external_platform_requests": 0, "external_writes": 0, "contract": inputstream.Contract, "bridge": "framework invocation -> immutable execution token + logical ID -> final text/native images -> durable Session flush -> body proof -> native consumption"}
	if imageFile != "" {
		delete(report, "result_text")
	}
	if runErr != nil {
		report["error"] = runErr.Error()
	}
	data, _ := json.MarshalIndent(report, "", "  ")
	if err = os.WriteFile(filepath.Join(output, "evidence.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if runErr != nil || stateErr != nil {
		t.Fatal(runErr, stateErr)
	}
	if state.Used != 2 || h.list(t).Members[0].ReadThrough != 1 || s.storage.repository.Barrier(s.execution) != nil {
		t.Fatal("incomplete durable delivery", state.Used)
	}
	if imageMode && (state.MaterialImages != 1 || downloads.Load() != 1) {
		t.Fatal("image was not durably delivered", state.MaterialImages, downloads.Load())
	}
	if !strings.Contains(result.Text, "RECEIPT_one") || !strings.Contains(result.Text, "RECEIPT_two") {
		t.Fatal("missing saved evidence", result.Text)
	}
	if imageFile != "" && !strings.Contains(strings.ToLower(result.Text), strings.ToLower(expectedVisual)) {
		t.Fatal("answer omitted the pixel-only expectation")
	}
	if imageFile != "" {
		revoke()
		if err := session.Close(ctx); err != nil {
			t.Fatal(err)
		}
		agent.Turn.Status = "completed"
		if err := h.store.SaveAgent(agent); err != nil {
			t.Fatal(err)
		}
		agent.TurnNumber++
		agent.Turn = &TurnRecord{Number: agent.TurnNumber, Status: "running", SnapshotSeq: 1, StartReadThrough: 1}
		if err := h.store.SaveAgent(agent); err != nil {
			t.Fatal(err)
		}
		next := startPlatformReader(t, h, s.storage, s.service, agent.TurnNumber)
		token, revokeNext, err := a.issueInputBinding(next)
		if err != nil {
			t.Fatal(err)
		}
		defer revokeNext()
		if err := os.WriteFile(filepath.Join(stateDir, "input-execution.json"), mustContractJSON(map[string]string{"token": token, "endpoint": server.URL}), 0600); err != nil {
			t.Fatal(err)
		}
		checkResumedImage(t, ctx, opts, output, expectedVisual, &downloads, next)
	}
}

func checkResumedImage(t *testing.T, ctx context.Context, opts dsh.Options, output, expected string, downloads *atomic.Int32, next *inputSession) {
	t.Helper()
	report := map[string]any{"status": "failed", "transport": "HTTP MCP", "external_platform_requests": 0}
	defer imageRealReport(t, output, report)
	resumed, err := dsh.Resume(ctx, opts)
	if resumed != nil {
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if err := resumed.Close(closeCtx); err != nil {
				t.Error(err)
			}
		}()
	}
	if err != nil {
		t.Fatal(err)
	}
	prompt := "Resume the previous read-only task. Transcribe the error from the screenshot already in our conversation. Use the saved context only. Do not call any tools or read materials again."
	result, err := resumed.Run(ctx, prompt)
	match := strings.Contains(strings.ToLower(result.Text), strings.ToLower(expected))
	report["prompt"], report["pixel_text_match"], report["tool_count"], report["downloads"] = prompt, match, len(result.Tools), downloads.Load()
	state, stateErr := next.storage.repository.State(next.execution)
	if err != nil || stateErr != nil || len(result.Tools) != 0 || state.Used != 0 ||
		downloads.Load() != 1 || !match {
		t.Fatal("settled Session did not recover the image evidence without a second read", err, stateErr)
	}
	report["status"] = "passed"
}

func savedInputImage(t *testing.T, path string) ([]byte, string) {
	t.Helper()
	if path != "" {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > attachment.MaxBytes {
			t.Fatal("readable bounded image file required", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data, http.DetectContentType(data)
	}
	picture := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			picture.Set(x, y, color.RGBA{R: 220, A: 255})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, picture); err != nil {
		t.Fatal(err)
	}
	return data.Bytes(), "image/png"
}
