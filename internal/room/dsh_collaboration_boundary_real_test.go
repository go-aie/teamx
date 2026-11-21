//go:build e2e && (darwin || linux)

package room

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/actorx"
	"github.com/go-aie/teamx/internal/agent/dsh"
)

// Pin a JS installation without editing the operator's config or node_modules.
// Both A3 and the boundary suite use this same launcher/version evidence.
func collaborationRuntime(t *testing.T, binary string, args []string) ([]string, map[string]any) {
	t.Helper()
	args = append([]string(nil), args...)
	if entry := os.Getenv("TEAMX_COLLAB_DSH_ENTRY"); entry != "" {
		if !filepath.IsAbs(entry) {
			t.Fatal("absolute TEAMX_COLLAB_DSH_ENTRY required")
		}
		args = []string{entry}
	}
	evidence := map[string]any{"binary": binary, "args": args}
	version, err := exec.CommandContext(t.Context(), binary, "--version").Output()
	if err != nil {
		t.Fatal("runtime executable version", err)
	}
	evidence["executable_version"] = strings.TrimSpace(string(version))
	if len(args) != 1 || !filepath.IsAbs(args[0]) {
		t.Fatal("collaboration acceptance requires a pinned absolute JS launcher")
	}
	for name, path := range map[string]string{"launcher": args[0], "package": filepath.Join(filepath.Dir(filepath.Dir(args[0])), "package.json")} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		evidence[name+"_sha256"] = hex.EncodeToString(sum[:])
		if name == "package" {
			var pkg struct{ Name, Version string }
			if json.Unmarshal(data, &pkg) != nil || pkg.Name != "@deepseek-ai/dsh" || pkg.Version == "" {
				t.Fatal("not a DSH package", path)
			}
			if want := os.Getenv("TEAMX_COLLAB_DSH_VERSION"); want != "" && want != pkg.Version {
				t.Fatal("DSH version mismatch", pkg.Version, want)
			}
			evidence["package_version"] = pkg.Version
		}
	}
	return args, evidence
}

type collaborationHTTPCall struct {
	Path   string `json:"path"`
	Caller Caller `json:"caller"`
	ID     string `json:"id,omitempty"`
	Status int    `json:"status"`
}

type collaborationStatusWriter struct {
	http.ResponseWriter
	status int
}

func (w *collaborationStatusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *collaborationStatusWriter) Flush() { w.ResponseWriter.(http.Flusher).Flush() }

// This observer keeps auth headers and private execution bindings out of reports.
type collaborationRoom struct {
	t       *testing.T
	out     string
	dir     string
	id      MemberIdentity
	svc     *service
	server  *httptest.Server
	profile dshProfile
	access  *serverAccess
	audit   *policyRealAudit
	mu      sync.Mutex
	calls   []collaborationHTTPCall
	report  map[string]any
}

func newCollaborationRoom(t *testing.T, out, task string) *collaborationRoom {
	t.Helper()
	cfg, err := loadStartupConfig(os.Getenv("TEAMX_INPUT_COMPARISON_CONFIG"), true)
	if err != nil || cfg.Runtime == nil || cfg.Models == nil {
		t.Fatal("configured runtime and default model required", err)
	}
	args, runtime := collaborationRuntime(t, cfg.Runtime.DSH.BinaryPath, cfg.Runtime.DSH.BinaryArgs)
	entry, err := cfg.Models.resolve(modelSelection{Mode: "default"})
	if err != nil {
		t.Fatal(err)
	}
	credentials := &modelCredentials{dir: filepath.Join(filepath.Dir(cfg.path), "model-credentials")}
	credential, err := credentials.get(entry.CredentialRef)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(entry.materialize().APIKeyEnv, credential.Value)
	if err := os.Mkdir(out, 0700); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	state := historyRoom(1, task)
	state.InputLog = &roomLogRoot{Version: 1}
	state.Members = append(state.Members, MemberState{AgentID: "reviewer", MembershipID: "generation-reviewer", Runner: RunnerConfig{Kind: "dsh", Profile: "reviewer"}})
	h := &collaborationRoom{t: t, out: out, dir: filepath.Join(out, "room"),
		id:    MemberIdentity{RoomIdentity: state.RoomIdentity, AgentID: "A", MembershipID: "generation-A"},
		audit: &policyRealAudit{Calls: map[string]int{}}, report: map[string]any{"status": "failed", "runtime": runtime, "model": entry.ModelID, "external_platform_writes": 0}}
	h.profile = dshProfile{BinaryPath: cfg.Runtime.DSH.BinaryPath, BinaryArgs: args, Artifact: cfg.Runtime.DSH.Artifact,
		Workspace: workspace, Model: entry.materialize(), Instructions: assistantInstructions}
	store, err := OpenStore(h.dir, state.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRoom(state); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	h.access, _, _ = accessFixture(t)
	h.access.disabled, h.access.operator = true, humanCaller.ID
	h.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req apiRequest
		if r.Body != nil {
			data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
			if err != nil {
				http.Error(w, "read request", 400)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(data))
			_ = json.Unmarshal(data, &req)
		}
		observed := &collaborationStatusWriter{ResponseWriter: w, status: 200}
		h.access.wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.svc.ServeHTTP(w, r) })).ServeHTTP(observed, r)
		h.mu.Lock()
		h.calls = append(h.calls, collaborationHTTPCall{r.URL.Path, req.Caller, req.ClientMessageID, observed.status})
		h.mu.Unlock()
	}))
	endpoint := "http://" + h.server.Listener.Addr().String()
	h.access.internal, h.access.public = endpoint, endpoint
	h.server.Start()
	t.Cleanup(func() {
		defer imageRealReport(t, out, h.report)
		if h.svc != nil {
			h.close()
		}
		h.server.Close()
		h.report["http"] = h.calls
		h.report["audit"] = h.audit
		if _, exists := h.report["native_sessions"]; !exists {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			sessions, err := readCollaborationNativeEvidence(ctx, h.dir)
			h.report["native_sessions"] = sessions
			if err != nil {
				h.report["native_evidence_error"] = err.Error()
			}
		}
	})
	h.start()
	return h
}

func (h *collaborationRoom) start() {
	h.t.Helper()
	var err error
	h.svc, err = startService(h.dir, h.id.RoomID, serviceConfig{unifiedInputs: true, persistentScheduling: true, access: h.access,
		endpoint: h.server.URL, cliPath: productionFixtureCLI(h.t), maxTurns: 12, dshTurnTimeout: 4 * time.Minute,
		dshProfiles: map[string]dshProfile{"fixture-ignore": h.profile, "reviewer": {Instructions: "Independent Room reviewer; never enabled in this fixture."}}}, func(s *service, state RoomSnapshot) error {
		if err := s.loadMembers(state); err != nil {
			return err
		}
		s.memberFactory = func(id MemberIdentity, runner RunnerConfig, restored *AgentSnapshot) actorx.Actor {
			m := s.newMember(id, runner, restored).(*MemberAgent)
			m.Backend = policyRealBackend{m.Backend.(dshBackend), h.audit}
			return m
		}
		return s.attachMembers(state)
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *collaborationRoom) close() error {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	err := h.svc.close(ctx)
	if _, exists := h.report["closed_cleanly"]; !exists || err != nil {
		h.report["closed_cleanly"] = err == nil
	}
	h.svc = nil
	if err != nil {
		h.report["close_error"] = err.Error()
		h.t.Error("Room close reported a lifecycle failure", err)
	}
	return err
}

func (h *collaborationRoom) run(ctx context.Context) {
	h.t.Helper()
	if _, err := h.svc.control(ctx, apiRequest{AgentID: h.id.AgentID, MembershipID: h.id.MembershipID}, "run"); err != nil {
		h.t.Fatal(err)
	}
}

func (h *collaborationRoom) wait(ctx context.Context, turn uint64) MemberStatus {
	h.t.Helper()
	status := waitMember(h.t, ctx, h.svc.target(h.id.MembershipID), h.id, func(s MemberStatus) bool {
		return !s.Active && (s.Agent.TurnNumber >= turn || s.Paused)
	})
	if status.Error != "" || status.Agent.TurnNumber != turn || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
		h.report["failed_execution"] = status
		h.t.Fatal("incomplete real collaboration turn", turn, status.Error)
	}
	return status
}

func (h *collaborationRoom) post(ctx context.Context, key, text string) uint64 {
	h.t.Helper()
	receipt, err := h.svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: h.id.RoomIdentity, Caller: humanCaller, ClientMessageID: key, Text: text})
	if err != nil {
		h.t.Fatal(err)
	}
	return receipt.Seq
}

func (h *collaborationRoom) reference() (sessionReference, string) {
	h.t.Helper()
	dir := filepath.Join(h.dir, "agents", h.id.MembershipID)
	var ref sessionReference
	if err := jsonFile(filepath.Join(dir, "session.json"), &ref); err != nil {
		h.t.Fatal(err)
	}
	return ref, filepath.Join(dir, ref.Directory)
}

func (h *collaborationRoom) backendID(dir string) string {
	h.t.Helper()
	var ref struct {
		SessionID string          `json:"sessionId"`
		AgentInfo json.RawMessage `json:"agentInfo"`
	}
	if err := jsonFile(filepath.Join(dir, "backend.json"), &ref); err != nil || ref.SessionID == "" {
		h.t.Fatal("missing backend identity", err)
	}
	h.report["acp_agent"] = ref.AgentInfo
	return ref.SessionID
}

func (h *collaborationRoom) inputProof(ctx context.Context, through uint64) {
	h.t.Helper()
	p, err := h.svc.inputStorage(h.id)
	if err != nil {
		h.t.Fatal(err)
	}
	state, ok, err := p.repository.CurrentState()
	if err != nil || !ok {
		h.t.Fatal("missing input coordinator evidence", err)
	}
	proved := false
	for _, a := range state.Attempts {
		if a.Kind == "room" && a.Through >= through && a.Phase == "settled" && a.ReceiptRequired && a.Received && a.ResultDigest != "" {
			proved = true
		}
	}
	h.report["input_proof"] = state
	if !proved {
		h.t.Fatal("formal input lacks final-body and durable-receipt proof")
	}
}

func (h *collaborationRoom) publications() []RoomMessage {
	h.t.Helper()
	room, err := h.svc.store.LoadRoom()
	if err != nil {
		h.t.Fatal(err)
	}
	var messages []RoomMessage
	for _, msg := range room.Messages {
		if msg.Author.Kind == "agent" {
			if msg.Author.ID != "A" || msg.AuthorMembershipID != h.id.MembershipID {
				h.t.Fatal("publication escaped owner membership", msg.Author)
			}
			messages = append(messages, msg)
		}
	}
	for _, member := range room.Members {
		if member.AgentID == "reviewer" && member.ReadThrough != 0 {
			h.t.Fatal("other member input consumed")
		}
	}
	return messages
}

// Native logs identify which Agent actually issued a TeamX command. The HTTP
// observer alone cannot distinguish a parent from its correctly bound child.
func collaborationNativeEvidence(t *testing.T, dir string) []map[string]any {
	t.Helper()
	evidence, err := readCollaborationNativeEvidence(t.Context(), dir)
	if err != nil {
		t.Fatal("native Session evidence", err)
	}
	return evidence
}

func readCollaborationNativeEvidence(ctx context.Context, dir string) ([]map[string]any, error) {
	var evidence []map[string]any
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() != "session.v4.jsonl.zstd" {
			return err
		}
		data, err := exec.CommandContext(ctx, "zstd", "-dc", path).Output()
		if err != nil {
			return err
		}
		row := map[string]any{"path": strings.TrimPrefix(path, dir+"/")}
		var calls []map[string]any
		var results []map[string]any
		var endings []json.RawMessage
		var schemas []string
		requested, shared, backend, identity := false, false, false, false
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var event struct {
				Type, ID, ParentSession string
				Data                    json.RawMessage
			}
			if err := json.Unmarshal(line, &event); err != nil {
				return err
			}
			switch event.Type {
			case "session":
				row["id"], row["parent"] = event.ID, event.ParentSession
			case "request/header":
				requested = true
				var header struct {
					Header struct {
						Tools json.RawMessage
					}
				}
				if err := json.Unmarshal(event.Data, &header); err != nil {
					return err
				}
				if schemas == nil {
					var tools []struct{ Name string }
					if err := json.Unmarshal(header.Header.Tools, &tools); err != nil {
						return err
					}
					for _, tool := range tools {
						schemas = append(schemas, tool.Name)
					}
					sum := sha256.Sum256(event.Data)
					row["first_request_header_sha256"] = hex.EncodeToString(sum[:])
					sum = sha256.Sum256(header.Header.Tools)
					row["tool_schemas_sha256"] = hex.EncodeToString(sum[:])
				}
			case "system/message":
				sum := sha256.Sum256(event.Data)
				row["system_message_sha256"] = hex.EncodeToString(sum[:])
			case "user/message":
				if !requested {
					var message struct{ Content []struct{ Text string } }
					if err := json.Unmarshal(event.Data, &message); err != nil {
						return err
					}
					for _, block := range message.Content {
						shared = shared || strings.Contains(block.Text, strings.TrimSpace(unifiedAgentGuide()))
						backend = backend || strings.Contains(block.Text, strings.TrimSpace(dshBackendGuide))
						identity = identity || strings.Contains(block.Text, `You are member "A" in Room "design-review" (instance "room-1", membership "generation-A")`)
						if strings.Contains(block.Text, strings.TrimSpace(dshBackendGuide)) &&
							strings.Contains(block.Text, `You are member "A" in Room "design-review" (instance "room-1", membership "generation-A")`) {
							sum := sha256.Sum256([]byte(block.Text))
							row["instructions_sha256"] = hex.EncodeToString(sum[:])
						}
					}
				}
			case "tool/call":
				var call map[string]any
				if err := json.Unmarshal(event.Data, &call); err != nil {
					return err
				}
				calls = append(calls, call)
			case "tool/result":
				var result map[string]any
				if err := json.Unmarshal(event.Data, &result); err != nil {
					return err
				}
				results = append(results, result)
			case "turn/end":
				endings = append(endings, event.Data)
			}
		}
		row["shared_guide"], row["backend_guide"], row["member_identity"] = shared, backend, identity
		if requested && (len(schemas) == 0 || !shared || !backend || !identity) {
			return fmt.Errorf("requested native Session %v lacks tools or effective member instructions", row["id"])
		}
		row["tools"], row["calls"], row["results"] = schemas, calls, results
		row["turn_endings"] = endings
		evidence = append(evidence, row)
		return nil
	})
	if err != nil {
		return evidence, err
	}
	if len(evidence) == 0 {
		return nil, fmt.Errorf("no native Session logs")
	}
	return evidence, nil
}

func TestDSHCollaborationBoundariesReal(t *testing.T) {
	if os.Getenv("TEAMX_COLLAB_BOUNDARIES_REAL") != "1" {
		t.Skip("set TEAMX_COLLAB_BOUNDARIES_REAL=1, TEAMX_INPUT_COMPARISON_CONFIG and fresh TEAMX_DSH_OUTPUT")
	}
	if _, err := exec.LookPath("zstd"); err != nil {
		t.Fatal(err)
	}
	out := imageRealOutput(t)
	t.Run("child-teamx", func(t *testing.T) {
		h := newCollaborationRoom(t, filepath.Join(out, "child-teamx"), `这是隔离测试 Room，明确授权你让一个运行时内部助手直接操作当前成员 A 的 TeamX 工具。把身份、权限和全部相关约束交给助手：先用 "$TEAMX_CLI" policy read --json 只读核对当前成员的规则；如能发现 history_read，也读取当前 Room 流，若子 Agent 没有这个工具则如实报告，不绕路。再执行 "$TEAMX_CLI" send --id child-boundary-receipt --text 'CHILD_BOUNDARY_OK' --json 发布一次。当前测试还明确授权一次拒绝性探测：仅对这一条 send 命令临时设置 TEAMX_AGENT_ID=reviewer TEAMX_MEMBERSHIP_ID=generation-reviewer，使用 ID child-cross-member 和正文 CROSS_MEMBER_FORBIDDEN；必须被服务端拒绝，不能绕过、重试或读取凭据。随后保持原绑定继续。将真实回执和拒绝错误内部回传，你核对后私下总结，不要再发布。独立 reviewer 不运行。`)
		ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
		defer cancel()
		h.run(ctx)
		h.wait(ctx, 1)
		messages := h.publications()
		h.report["messages"] = messages
		if len(messages) != 1 || messages[0].Text != "CHILD_BOUNDARY_OK" || messages[0].ClientMessageID != "child-boundary-receipt" {
			t.Fatal("child publication receipt missing or duplicated")
		}
		h.mu.Lock()
		denied := false
		for _, c := range h.calls {
			denied = denied || c.ID == "child-cross-member" && c.Status == http.StatusForbidden
		}
		h.mu.Unlock()
		if !denied {
			t.Fatal("cross-member rejection was not exercised")
		}
		h.inputProof(ctx, 1)
		old, dir := h.reference()
		oldID := h.backendID(dir)
		if err := h.close(); err != nil {
			t.Fatal("completed child task did not close cleanly", err)
		}
		if err := dsh.CheckResume(dir); err != nil {
			t.Fatal("completed child task cannot resume", err)
		}
		h.start()
		seq := h.post(ctx, "normal-resume", `子任务已完成。正常重启后只通过 history_read 核对 child-boundary-receipt 的原 Room 消息 seq、正文和当前人类请求；读取这条正式 Inbox 指令。私下汇报，不再委派、不再执行拒绝探测、不发送任何消息。`)
		h.run(ctx)
		h.wait(ctx, 2)
		resumed, resumedDir := h.reference()
		if resumed.Directory != old.Directory || h.backendID(resumedDir) != oldID {
			t.Fatal("normal resume did not retain original backend Session")
		}
		if got := h.publications(); len(got) != 1 || got[0].Seq != messages[0].Seq {
			t.Fatal("normal resume replayed child publication")
		}
		h.inputProof(ctx, seq)
		h.report["normal_resume"] = "passed"
		h.close()
		sessions := collaborationNativeEvidence(t, h.dir)
		h.report["native_sessions"] = sessions
		childPublished, childRead, childCross, childHistory := false, false, false, false
		for _, session := range sessions {
			if session["parent"] == "" {
				continue
			}
			if session["shared_guide"] != true || session["backend_guide"] != true || session["member_identity"] != true {
				t.Fatal("operating child lacks actual member guidance")
			}
			for _, call := range session["calls"].([]map[string]any) {
				childHistory = childHistory || call["name"] == "mcp__teamx__history_read"
				args, _ := call["arguments"].(string)
				childRead = childRead || call["name"] == "bash" && strings.Contains(args, "policy read") && strings.Contains(args, "TEAMX_CLI")
				childPublished = childPublished || call["name"] == "bash" && strings.Contains(args, "child-boundary-receipt") && strings.Contains(args, "TEAMX_CLI")
				childCross = childCross || call["name"] == "bash" && strings.Contains(args, "child-cross-member") && strings.Contains(args, "TEAMX_AGENT_ID") && strings.Contains(args, "TEAMX_MEMBERSHIP_ID")
			}
		}
		h.report["child_history_read_executed"] = childHistory
		h.report["child_policy_read_executed"] = childRead
		h.report["child_publication_executed"] = childPublished
		h.report["child_cross_member_probe_executed"] = childCross
		if !childRead || !childPublished || !childCross {
			t.Fatal("parent evidence cannot substitute for direct child operations")
		}
		h.report["status"] = "passed"
	})
	for _, mode := range []string{"cancel", "close"} {
		t.Run(mode+"-inflight", func(t *testing.T) { testCollaborationInflight(t, filepath.Join(out, mode), mode) })
	}
}

func testCollaborationInflight(t *testing.T, out, mode string) {
	t.Helper()
	entered, gone := make(chan struct{}), make(chan struct{})
	var once sync.Once
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-r.Context().Done()
		close(gone)
	}))
	t.Cleanup(gate.Close)
	task := fmt.Sprintf(`这是隔离的生命周期验收。必须让同一个运行时内部助手在它自己的 bash 工具中完成以下两步，不能只委派第一步：1. 用 "$TEAMX_CLI" send --id inflight-receipt --text 'INFLIGHT_BEFORE_STOP' --json 发布一次，内部回传准确回执；2. 仍由该助手用 bash 前台执行 curl --fail --max-time 180 %s （timeoutMs 180000），在 curl 成功时才允许该助手发布 ID inflight-late 正文 LATE_FORBIDDEN。你本人只等待助手，不得自己运行 curl 或代替助手执行第二步，不要提前结束。此本地 gate 会等待操作者取消／关闭；取消或失败必须停止，不重试，不用后台 shell。助手使用当前成员 A 绑定，并携带身份与约束，不能代表 reviewer。你不要另发消息。`, gate.URL)
	h := newCollaborationRoom(t, out, task)
	ctx, cancel := context.WithTimeout(t.Context(), 9*time.Minute)
	defer cancel()
	h.run(ctx)
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("native child never reached the in-flight gate")
	}
	before := h.publications()
	if len(before) != 1 || before[0].ClientMessageID != "inflight-receipt" || before[0].Text != "INFLIGHT_BEFORE_STOP" {
		t.Fatal("missing pre-stop original receipt")
	}
	old, dir := h.reference()
	oldID := h.backendID(dir)
	h.report["original_backend_session"] = oldID
	if mode == "cancel" {
		if _, err := h.svc.control(ctx, apiRequest{AgentID: h.id.AgentID, MembershipID: h.id.MembershipID, Cancel: true}, "pause"); err != nil {
			t.Fatal(err)
		}
		settled := waitMember(t, ctx, h.svc.target(h.id.MembershipID), h.id, func(s MemberStatus) bool { return !s.Active && s.Paused })
		h.report["cancelled_execution"] = settled
	}
	closeErr := h.close()
	resumeErr := dsh.CheckResume(dir)
	canResume := closeErr == nil && resumeErr == nil
	h.report["normal_resume_eligible"] = canResume
	if resumeErr != nil {
		h.report["resume_error"] = resumeErr.Error()
		t.Error("old Session is not eligible for normal resume", resumeErr)
	}
	if err := dsh.CheckReset(dir); err != nil {
		t.Fatal("old execution lacks settlement proof; no replacement allowed", err)
	}
	h.report["old_execution_settled"] = true
	select {
	case <-gone:
		h.report["child_gate_cancelled"] = true
	case <-time.After(10 * time.Second):
		t.Fatal("child work survived Room closure")
	}
	h.start()
	h.post(ctx, "recovery-command", `原阻塞任务已取消。只恢复核对原 ID inflight-receipt 的 Room 发布，使用只读 history_read 查回原消息和最新撤销，不再委派，不执行旧 curl，不重放或补发旧消息。读取正式 Inbox 中这条最新指令；请私下报告原 seq 和正文。`)
	nextTurn, requiredHistoryReads := uint64(2), 1
	if canResume {
		h.run(ctx)
		h.wait(ctx, 2)
		resumed, resumedDir := h.reference()
		if resumed.Directory != old.Directory || h.backendID(resumedDir) != oldID {
			t.Fatal("normal recovery replaced the original generation")
		}
		if got := h.publications(); len(got) != 1 || got[0].Seq != before[0].Seq || got[0].ClientMessageID != before[0].ClientMessageID {
			t.Fatal("recovery replayed publication or lost receipt")
		}
		h.report["normal_resume"] = "passed"
		nextTurn, requiredHistoryReads = 3, 2
	}
	rotationID, err := newOpaqueID()
	if err != nil {
		t.Fatal(err)
	}
	rotation, err := h.svc.rotateSession(ctx, apiRequest{AgentID: h.id.AgentID, MembershipID: h.id.MembershipID, OperationID: rotationID, FromGeneration: old.Directory})
	if err != nil {
		t.Fatal("rotation", err)
	}
	if rotation.Rotation == nil || rotation.Rotation.Status != "completed" {
		rotation = waitMember(t, ctx, h.svc.target(h.id.MembershipID), h.id, func(s MemberStatus) bool {
			return s.Rotation != nil && (s.Rotation.Status == "completed" || s.Rotation.Status == "failed")
		})
	}
	if rotation.Rotation.Status != "completed" {
		t.Fatal("rotation failed")
	}
	h.report["rotation"] = rotation.Rotation
	seq := h.post(ctx, "rotation-command", `轮换后仍只核实 inflight-receipt 原回执及最新取消指令。history_read 查证；消费当前正式 Inbox 指令。不得委派、运行 curl、补发或重放任何消息。私下报告即可。`)
	h.run(ctx)
	h.wait(ctx, nextTurn)
	fresh, freshDir := h.reference()
	if fresh.Directory == old.Directory || h.backendID(freshDir) == oldID {
		t.Fatal("rotation did not establish a fresh generation")
	}
	if got := h.publications(); len(got) != 1 || got[0].Seq != before[0].Seq {
		t.Fatal("rotation replayed old publication")
	}
	room, err := h.svc.store.LoadRoom()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range room.Members {
		if m.AgentID == "A" && m.ReadThrough < seq {
			t.Fatal("latest formal input lacks durable delivery proof")
		}
	}
	h.report["messages"], h.report["input_delivered_through"] = before, seq
	h.inputProof(ctx, seq)
	h.close()
	sessions := collaborationNativeEvidence(t, h.dir)
	h.report["native_sessions"] = sessions
	children, historyReads := 0, 0
	childGate := false
	for _, session := range sessions {
		if session["parent"] != "" {
			children++
		}
		for _, call := range session["calls"].([]map[string]any) {
			if session["parent"] == "" && call["name"] == "mcp__teamx__history_read" {
				historyReads++
			}
			args, _ := call["arguments"].(string)
			childGate = childGate || session["parent"] != "" && call["name"] == "bash" && strings.Contains(args, gate.URL)
		}
	}
	if children != 1 || !childGate || historyReads < requiredHistoryReads {
		t.Fatal("missing native in-flight/recovery evidence or delegation replay", children, childGate, historyReads)
	}
	for _, call := range h.calls {
		if call.Path == "/v1/send" && call.ID != "inflight-receipt" {
			t.Fatal("unexpected publication attempt after cancellation", call.ID)
		}
	}
	sends := 0
	for _, call := range h.calls {
		if call.Path == "/v1/send" {
			sends++
		}
	}
	if sends != 1 {
		t.Fatal("original publication was retried", sends)
	}
	h.report["rotation_without_replay"] = "passed"
	if !t.Failed() {
		h.report["status"] = "passed"
	}
}
