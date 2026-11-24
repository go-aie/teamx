//go:build e2e && linux

package room

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/go-aie/teamx/agent"
	"github.com/go-aie/teamx/internal/agent/dsh"
	"github.com/go-aie/teamx/internal/agent/srt"
	"github.com/pelletier/go-toml/v2"
)

type recordingProcessFactory struct {
	agent.ExecutionFactory
	record func(any)
}

func (f recordingProcessFactory) RouteEndpoint(raw string) (string, error) {
	return f.ExecutionFactory.(agent.EndpointRouter).RouteEndpoint(raw)
}
func (f recordingProcessFactory) Start(ctx context.Context, r agent.ExecutionRequest, failed func(error)) (agent.Execution, error) {
	p, err := f.ExecutionFactory.Start(ctx, r, failed)
	if p == nil {
		return nil, err
	}
	return recordingProcessHandle{p, f.record}, err
}

type recordingProcessHandle struct {
	agent.Execution
	record func(any)
}

func (p recordingProcessHandle) Stop(grace time.Duration) {
	p.Execution.Stop(grace)
	d, _ := p.Execution.Diagnostic()
	if d != "" {
		p.record(map[string]any{"stderr": d})
	}
}

type recordingExecutionFactory struct {
	agent.Factory
	record func(any)
}

func (f recordingExecutionFactory) OpenSession(ctx context.Context, host agent.Host, resume bool) (*agent.Session, error) {
	return agent.OpenSession(ctx, f, host, resume)
}
func (f recordingExecutionFactory) Open(ctx context.Context, launch agent.Launch, resume bool) (agent.Component, error) {
	record := func(v any) {
		raw, _ := json.Marshal(v)
		secrets := []string{launch.ExecutionToken}
		if launch.Credential != nil {
			secrets = append(secrets, launch.Credential.Value)
		}
		for _, server := range launch.MCPServers {
			for _, h := range server.Headers {
				secrets = append(secrets, h.Value)
			}
		}
		for _, secret := range secrets {
			if secret != "" {
				raw = []byte(strings.ReplaceAll(string(raw), secret, "[redacted]"))
			}
		}
		var value any
		_ = json.Unmarshal(raw, &value)
		f.record(value)
	}
	launch.Execution = recordingProcessFactory{launch.Execution, record}
	c, err := f.Factory.Open(ctx, launch, resume)
	if err != nil {
		var details func(error) any
		details = func(e error) any {
			if e == nil {
				return nil
			}
			raw, _ := json.Marshal(e)
			for _, server := range launch.MCPServers {
				for _, h := range server.Headers {
					if h.Value != "" {
						raw = []byte(strings.ReplaceAll(string(raw), h.Value, "[redacted]"))
					}
				}
			}
			out := map[string]any{"error": e.Error(), "details": string(raw)}
			if many, ok := e.(interface{ Unwrap() []error }); ok {
				var children []any
				for _, v := range many.Unwrap() {
					children = append(children, details(v))
				}
				out["children"] = children
			} else if one, ok := e.(interface{ Unwrap() error }); ok {
				out["cause"] = details(one.Unwrap())
			}
			return out
		}
		record(details(err))
	}
	return c, err
}

func appendRecord(previous, v any) []any { out, _ := previous.([]any); return append(out, v) }

func TestLinuxSpaceSRTCallbacksAndRecovery(t *testing.T) {
	base := os.Getenv("TEAMX_EXECUTION_RUNTIME_ROOT")
	if base == "" {
		t.Skip("explicit disposable runtime root required")
	}
	if os.Geteuid() == 0 || !strings.HasPrefix(base, "/root/luop/teamx/agent-runtime-") {
		t.Fatal("approved non-root Linux fixture required")
	}
	root, err := os.MkdirTemp("", "room-srt-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !t.Failed() {
			_ = os.RemoveAll(root)
		}
	}()
	report := map[string]any{"status": "failed", "real_dsh": true, "real_model": false, "uid": os.Geteuid(), "root": root}
	defer func() {
		if t.Failed() {
			report["status"] = "failed"
		}
		data, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(os.Getenv("TEAMX_EXECUTION_REPORT"), data, 0600); err != nil {
			t.Error(err)
		}
	}()
	var mu sync.Mutex
	originalFactory := defaultBackendFactory
	defaultBackendFactory = recordingExecutionFactory{Factory: originalFactory, record: func(v any) {
		mu.Lock()
		defer mu.Unlock()
		report["open_error"] = appendRecord(report["open_error"], v)
	}}
	defer func() { defaultBackendFactory = originalFactory }()
	stage, step := 0, 0
	marker := ""
	var calls []string
	var outputs []string
	refPattern := regexp.MustCompile(`"stream_ref"\s*:\s*"([^"]+)"`)
	var flatten func(any) string
	flatten = func(v any) string {
		switch v := v.(type) {
		case string:
			var nested any
			if json.Unmarshal([]byte(v), &nested) == nil {
				switch nested.(type) {
				case map[string]any, []any:
					return v + "\n" + flatten(nested)
				}
			}
			return v
		case []any:
			var out string
			for _, x := range v {
				out += "\n" + flatten(x)
			}
			return out
		case map[string]any:
			var out string
			for _, x := range v {
				out += "\n" + flatten(x)
			}
			return out
		}
		return ""
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-room-model-key" {
			w.WriteHeader(401)
			return
		}
		var in struct {
			Messages []map[string]any
			Tools    []struct{ Function struct{ Name string } }
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, msg := range in.Messages {
			if msg["role"] == "tool" {
				outputs = append(outputs, flatten(msg["content"]))
			}
		}
		var name string
		args := map[string]any{}
		if stage == -1 {
			name = "bash"
			args = map[string]any{"command": "printf armed > room-cancel.ready; sleep 4; printf late > room-cancel.late", "description": "Exercise the formal cancellation entry"}
		} else if stage < 0 {
		} else if stage < 2 {
			suffix := []string{"inbox_list", "inbox_read"}[stage]
			for _, tool := range in.Tools {
				if strings.HasSuffix(tool.Function.Name, suffix) {
					name = tool.Function.Name
					break
				}
			}
			if stage == 1 {
				var all string
				for _, msg := range in.Messages {
					if msg["role"] == "tool" {
						all += "\n" + flatten(msg["content"])
					}
				}
				matches := refPattern.FindAllStringSubmatch(all, -1)
				outputs = append(outputs, all)
				if len(matches) > 0 {
					args["stream_ref"] = matches[len(matches)-1][1]
				} else {
					name = ""
				}
			}
		} else if stage == 2 {
			name = "bash"
			args = map[string]any{"command": `printf 'PROXY_PRESENT=%s;%s\n' "${DSH_TEAMX_EXECUTION_PROXY:+yes}" "${HTTP_PROXY:+yes}"; "$TEAMX_CLI" send --text ` + marker + ` --id ` + marker + ` --json`, "description": "Publish the local acceptance receipt"}
		}
		delta := map[string]any{"role": "assistant", "content": "DONE"}
		finish := "stop"
		if name != "" {
			step++
			calls = append(calls, name)
			a, _ := json.Marshal(args)
			delta = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("room-%d", step), "type": "function", "function": map[string]any{"name": name, "arguments": string(a)}}}}
			if stage == -1 {
				stage = -2
			} else {
				stage++
			}
			finish = "tool_calls"
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, choice := range []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}, map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}} {
			chunk, _ := json.Marshal(map[string]any{"id": "srt-room", "object": "chat.completion.chunk", "model": "fixture", "choices": []any{choice}})
			fmt.Fprintf(w, "data: %s\n\n", chunk)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	endpoint := "http://" + address
	pin := func(path string) srt.Artifact {
		hash, err := srt.DigestPath(path)
		if err != nil {
			t.Fatal(err)
		}
		return srt.Artifact{Path: path, SHA256: hash}
	}
	manifest := srt.Config{Node: pin(base + "/node/bin/node"), Bwrap: pin(base + "/tools/usr/bin/bwrap"), Socat: pin(base + "/tools/usr/bin/socat"), Ripgrep: pin(base + "/tools/usr/bin/rg"), Runtime: pin(base + "/srt"), ReadOnly: []srt.Artifact{pin(base + "/dsh"), pin(base + "/teamx")}, Endpoints: []string{model.URL, endpoint}, PrivateRoots: []string{root}}
	manifestPath := root + "/srt.json"
	data, _ := json.Marshal(manifest)
	if err := os.WriteFile(manifestPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	profile := dshProfile{BinaryPath: manifest.Node.Path, BinaryArgs: []string{base + "/dsh/node_modules/@deepseek-ai/dsh/lib/bin.js"}, Artifact: dsh.Artifact{Source: "npm:@deepseek-ai/dsh", Version: "0.2.2-rc.1"}, Workspace: root + "/workspace", Instructions: "Use only the synthetic acceptance tools.", Model: dsh.ModelConfig{Protocol: "openai-completions", BaseURL: model.URL + "/v1", ModelID: "fixture", APIKeyEnv: "TEAMX_FIXTURE_MODEL_KEY"}}
	if err := os.Mkdir(profile.Workspace, 0700); err != nil {
		t.Fatal(err)
	}
	profilesPath := root + "/profiles.json"
	data, _ = json.Marshal(map[string]dshProfile{"execution": profile})
	if err := os.WriteFile(profilesPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	configPath := root + "/config.toml"
	data, err = toml.Marshal(map[string]any{"server": map[string]any{"listen": address, "data_dir": root + "/data", "teams_dir": root + "/teams"}, "deployment": map[string]any{"execution": map[string]any{"kind": "srt", "manifest": manifestPath}}, "env": map[string]string{"TEAMX_FIXTURE_MODEL_KEY": "synthetic-room-model-key"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := runAccessInit([]string{"--config", configPath}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	var member MemberInfo
	var sessionID string
	for iteration := 0; iteration < 3; iteration++ {
		if iteration > 0 {
			listener, err = net.Listen("tcp4", address)
			if err != nil {
				t.Fatal(err)
			}
		}
		selected := personalDeployment(nil)
		selected.factories.service = func(dir, id string, c serviceConfig) (*service, error) {
			c.cliPath = base + "/teamx"
			return newConfiguredService(dir, id, c)
		}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		ready := make(chan any, 1)
		go func() {
			done <- runConfiguredServer(ctx, []string{"--config", configPath, "--room", "execution-room", "--state-dir", root + "/data", "--dsh-config", profilesPath}, io.Discard, io.Discard, serverOptions{deployment: &selected, listen: func(string, string) (net.Listener, error) { return listener, nil }, ready: func(v any) error { ready <- v; return nil }})
		}()
		stop := sync.OnceFunc(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					if iteration == 2 && backendCleanupSettled(err) && selected.runtime.State() == "stopped" {
						report["formal_fault_close_diagnostic"] = err.Error()
					} else {
						t.Error("server close", err)
					}
				}
			case <-time.After(35 * time.Second):
				t.Fatal("server close did not settle")
			}
		})
		t.Cleanup(stop)
		select {
		case <-ready:
		case err := <-done:
			cancel()
			t.Fatal("server startup", err)
		case <-time.After(60 * time.Second):
			cancel()
			t.Fatal("server startup deadline")
		}
		svc := selected.runtime.single
		client := serverTestCLI(t, root, endpoint)
		caller := Caller{Author: Author{Kind: "human", ID: selected.runtime.OwnerUserID}}
		if iteration == 0 {
			err = serverTestRequest(t.Context(), client, "/v1/member/add", apiRequest{ServiceInfo: svc.info, Caller: caller, AgentID: "A", Runner: RunnerConfig{Kind: "dsh", Profile: "execution"}}, &member)
			if err != nil {
				stop()
				t.Fatal(err)
			}
		}
		previous, err := svc.target(member.MembershipID).Call[MemberStatus](t.Context(), InspectMember{MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}})
		if err != nil {
			stop()
			t.Fatal(err)
		}
		var previousTurn uint64
		if previous.Agent != nil && previous.Agent.Turn != nil {
			previousTurn = previous.Agent.Turn.Number
		}
		mu.Lock()
		stage = 0
		marker = fmt.Sprintf("SRT_ROOM_RECEIPT_%d", iteration)
		currentMarker := marker
		mu.Unlock()
		var receipt PostReceipt
		if err := serverTestRequest(t.Context(), client, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: caller, Text: "Run the synthetic acceptance.", ClientMessageID: fmt.Sprintf("human-%d", iteration)}, &receipt); err != nil {
			stop()
			t.Fatal(err)
		}
		var status MemberStatus
		if err := serverTestRequest(t.Context(), client, "/v1/member/run", apiRequest{ServiceInfo: svc.info, Caller: caller, AgentID: member.AgentID, MembershipID: member.MembershipID}, &status); err != nil {
			stop()
			t.Fatal(err)
		}
		deadline := time.Now().Add(100 * time.Second)
		for {
			state, err := svc.store.LoadRoom()
			if err != nil {
				stop()
				t.Fatal(err)
			}
			found := false
			for _, message := range state.Messages {
				found = found || message.Author.Kind == "agent" && message.Text == currentMarker
			}
			current, inspectErr := svc.target(member.MembershipID).Call[MemberStatus](t.Context(), InspectMember{MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}})
			if found && current.Agent != nil && current.Agent.Turn != nil && current.Agent.Turn.Status == "completed" {
				break
			}
			if inspectErr == nil && current.Agent != nil && current.Agent.Turn != nil && current.Agent.Turn.Number > previousTurn && (current.Agent.Turn.Status == "failed" || current.Agent.Turn.Status == "completed" && !found) {
				deadline = time.Now()
			}
			if time.Now().After(deadline) {
				mu.Lock()
				report["calls"], report["list_outputs"] = calls, outputs
				mu.Unlock()
				status, e := svc.target(member.MembershipID).Call[MemberStatus](t.Context(), InspectMember{MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}})
				report["member_status"], report["inspect_error"] = status, fmt.Sprint(e)
				stop()
				t.Fatal("authenticated MCP/CLI receipt missing")
			}
			time.Sleep(100 * time.Millisecond)
		}
		path := filepath.Join(svc.store.dir, "agents", member.MembershipID, "session.json")
		ref, _, err := readSessionReference(path, MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID})
		if err != nil {
			t.Fatal(err)
		}
		if iteration == 1 {
			var rotation MemberStatus
			if err := serverTestRequest(t.Context(), client, "/v1/member/session/rotate", apiRequest{ServiceInfo: svc.info, Caller: caller, AgentID: member.AgentID, MembershipID: member.MembershipID, OperationID: "11111111111111111111111111111111", FromGeneration: ref.Directory}, &rotation); err != nil {
				stop()
				t.Fatal("real rotation", err)
			}
			if rotation.Rotation == nil || rotation.Rotation.Status != "completed" || rotation.Rotation.FromGeneration != ref.Directory || rotation.Rotation.ToGeneration == ref.Directory {
				stop()
				t.Fatal("rotation did not settle", rotation)
			}
			report["rotation"] = rotation.Rotation
		}
		if iteration == 2 {
			mu.Lock()
			stage = -1
			mu.Unlock()
			if err := serverTestRequest(t.Context(), client, "/v1/send", apiRequest{ServiceInfo: svc.info, Caller: caller, Text: "Run the cancellation probe.", ClientMessageID: "cancel-human"}, &receipt); err != nil {
				stop()
				t.Fatal(err)
			}
			deadline := time.Now().Add(30 * time.Second)
			for {
				if _, err := os.Stat(profile.Workspace + "/room-cancel.ready"); err == nil {
					break
				}
				if time.Now().After(deadline) {
					stop()
					t.Fatal("formal Shell did not arm")
				}
				time.Sleep(50 * time.Millisecond)
			}
			if err := serverTestRequest(t.Context(), client, "/v1/member/pause", apiRequest{ServiceInfo: svc.info, Caller: caller, AgentID: member.AgentID, MembershipID: member.MembershipID, Cancel: true}, &status); err != nil {
				stop()
				t.Fatal(err)
			}
			for {
				status, err = svc.target(member.MembershipID).Call[MemberStatus](t.Context(), InspectMember{MemberIdentity{RoomIdentity: svc.info.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}})
				if err != nil {
					stop()
					t.Fatal(err)
				}
				if !status.Active && !status.Stopping {
					break
				}
				if time.Now().After(deadline) {
					stop()
					t.Fatal("formal cancellation did not settle")
				}
				time.Sleep(50 * time.Millisecond)
			}
			time.Sleep(4500 * time.Millisecond)
			if _, err := os.Stat(profile.Workspace + "/room-cancel.late"); !os.IsNotExist(err) {
				stop()
				t.Fatal("formal cancellation left a late write", err)
			}
			report["formal_cancel_no_late_write"] = true
			// Inject only into the exact private DSH process created above.
			entries, err := os.ReadDir("/proc")
			if err != nil {
				stop()
				t.Fatal(err)
			}
			killed := false
			home := filepath.Join(filepath.Dir(path), ref.Directory, "home")
			for _, entry := range entries {
				pid, e := strconv.Atoi(entry.Name())
				if e != nil {
					continue
				}
				process, e := os.FindProcess(pid)
				if e != nil {
					continue
				}
				prefix := "/proc/" + entry.Name()
				exe, e := os.Readlink(prefix + "/exe")
				if e != nil || exe != base+"/node/bin/node" {
					process.Release()
					continue
				}
				env, _ := os.ReadFile(prefix + "/environ")
				argv, _ := os.ReadFile(prefix + "/cmdline")
				if bytes.Contains(env, []byte("DSH_HOME="+home+"\x00")) && bytes.Contains(argv, []byte("/dsh/lib/bin.js\x00")) {
					e = process.Signal(syscall.SIGKILL)
					process.Release()
					if e != nil {
						stop()
						t.Fatal(e)
					}
					killed = true
					break
				}
				process.Release()
			}
			if !killed {
				stop()
				t.Fatal("formal DSH fault target unavailable")
			}
			time.Sleep(300 * time.Millisecond)
		}
		stop()
		var backend struct {
			SessionID string `json:"sessionId"`
			Stopped   bool   `json:"stopped"`
		}
		backendData, err := os.ReadFile(filepath.Join(filepath.Dir(path), ref.Directory, "backend.json"))
		if err == nil {
			err = json.Unmarshal(backendData, &backend)
		}
		if err != nil || (iteration != 2 && !backend.Stopped) || (iteration == 2 && backend.Stopped) {
			t.Fatal("original execution did not settle", err)
		}
		if iteration == 2 {
			if err := defaultBackendFactory.CheckResume(filepath.Join(filepath.Dir(path), ref.Directory)); err == nil {
				t.Fatal("formal fault allowed recovery")
			}
			report["formal_dsh_fault_settled_resume_denied"] = true
		}
		if iteration == 0 {
			sessionID = backend.SessionID
		} else if iteration == 1 && backend.SessionID != sessionID {
			t.Fatal("server restart replaced original Session")
		} else if iteration == 2 && backend.SessionID == sessionID {
			t.Fatal("rotation reused the old Session")
		}
	}
	report["session_id"] = sessionID
	report["mcp_cli_and_restart"] = true
	report["rotation_new_session"] = true
	report["status"] = "passed"
}
