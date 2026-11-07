//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
)

// Actual installed DSH/model and production CLI/HTTP/authority/ledger. Only the
// remote platform boundary is a local sink. This cannot send a Lark message or
// establish that a production bot binding/adapter exists.
func TestActionsStandaloneRealDSHAcceptance(t *testing.T) {
	if os.Getenv("TEAMX_ACTIONS_DSH_REAL") != "1" {
		t.Skip("explicit opt-in, current config and fresh output required")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "real DSH standalone actions and service reload; local remote sink", "remote_writes": 0, "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, output, report)
	config, err := loadStartupConfig(os.Getenv("TEAMX_ACTIONS_CONFIG"), true)
	if err != nil || config.Runtime == nil {
		t.Fatal("current runtime config required", err)
	}
	runtime := config.Runtime
	if key := runtime.Model.APIKeyEnv; os.Getenv(key) == "" && config.Env[key] != "" {
		t.Setenv(key, config.Env[key])
	}
	imageRealCredential(t, runtime.Model.APIKeyEnv)
	workspace := filepath.Join(output, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(output, "teamx")
	if err = exec.Command("go", "build", "-o", binary, "../../cmd/teamx").Run(); err != nil {
		t.Fatal("build acceptance CLI", err)
	}
	profile := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are a bounded acceptance assistant using the real TeamX CLI and production action protocol. Only the remote forge-test provider is a LOCAL TEST SINK. It sends no real messages. Use the configured TeamX CLI in JSON, current_turn and policy_token. No file, credential or environment inspection, network requests, delegation, tools run, or implementation debugging. Do not create Inbox entries or followup units. All instructions arrive as direct human Room messages. For an explicit task, call actions resolve with provider forge-test, locator.url exactly as supplied, and current turn_number. Use only the returned target_ref and applicable schema. Execute once with the supplied stable ID, message.send, input.body exactly as instructed, typed authority {type:"human_reply",room_seq:<actual human message.seq>,reason:<why this direct command permits this target and input>}, and current policy_token. No context is needed. Then read the saved action receipt with actions read --id. A succeeded result is final. An unknown result may have been applied: read/query only, never change ID, operation, target or platform to resend. If a later human message asks only for receipt review, use actions read/list without resolve/execute. Do not ask for duplicate approval of an explicit direct instruction. Stop on execution errors; do not retry an action to debug. Finish privately after each task without posting Room messages.`}
	report["configured_model"] = runtime.Model.ModelID
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	room := managerCreate(t, m, "l4c-actions-acceptance")
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	var svc *service
	start := func() {
		t.Helper()
		var e error
		svc, e = newService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 6, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": profile}})
		if e != nil {
			t.Fatal(e)
		}
		m.bindPlatformReferences(svc)
		observePolicyReal(svc, audit)
		m.mu.Lock()
		m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
		m.mu.Unlock()
	}
	start()
	defer func() {
		if svc != nil {
			closeService(t, svc)
		}
	}()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	scope := platform.IdentityScope{Provider: "forge-test", Instance: "https://forge.invalid"}
	account := platform.AccountIdentity{IdentityScope: scope, ID: platform.ExternalID{Namespace: "forge:user", Value: "accepted-writer"}}
	access := platform.AccessContext{RoomIdentity: id.RoomIdentity, AgentID: id.AgentID, MembershipID: id.MembershipID, Account: account, ConnectionID: strings.Repeat("a", 32), Generation: strings.Repeat("b", 32), ConnectionVersion: 1, CredentialVersion: 1}
	targets := map[string]platform.TargetIdentity{
		"https://forge.invalid/users/one": {IdentityScope: scope, Object: platform.NativeIdentity{Type: "user", ID: platform.ExternalID{Namespace: "forge:user", Value: "one"}}},
		"https://forge.invalid/chats/one": {IdentityScope: scope, Object: platform.NativeIdentity{Type: "chat", ID: platform.ExternalID{Namespace: "forge:chat", Value: "one"}}},
	}
	var mu sync.Mutex
	applied := []platform.TargetActionRequest{}
	caps := platform.ReferenceCapabilities{Resolve: func(ctx context.Context, l platform.TargetLocator, a platform.AccessContext) (platform.TargetResolution, error) {
		target, ok := targets[l.URL]
		if !ok || l.Source != nil || l.Candidate != nil || l.Query != nil {
			return platform.TargetResolution{Status: platform.ResolutionUnsupported, Account: a.Account, CheckedAt: time.Now().UTC()}, nil
		}
		return platform.TargetResolution{Status: platform.ResolutionResolved, Target: &target, Account: a.Account, AccountLabel: "local acceptance writer", TargetLabel: target.Object.Type + " one", Complete: true, ObjectVerified: true, CheckedAt: time.Now().UTC()}, nil
	}, Actions: map[platform.Operation]platform.ActionCapability{platform.OperationMessageSend: {Spec: platform.ActionSpec{Operation: platform.OperationMessageSend, TargetKind: "object", ObjectTypes: []string{"user", "chat"}, DirectRoomInstructionOnly: true, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false,"required":["body"],"properties":{"body":{"type":"string"}}}`), Retry: platform.RetryPolicy{MaxAttempts: 1}}, Inspect: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionInspection, error) {
		found := false
		for _, target := range targets {
			if sameNativeTarget(q.Target, target) {
				found = true
			}
		}
		var input struct {
			Body string `json:"body"`
		}
		if !found || json.Unmarshal(q.Input, &input) != nil || (q.Target.Object.Type == "user" && input.Body != "L4C-USER-RECEIPT") || (q.Target.Object.Type == "chat" && input.Body != "L4C-GROUP-UNKNOWN") {
			return platform.TargetActionInspection{}, ErrIdentity
		}
		return platform.TargetActionInspection{Target: q.Target, Account: a.Account}, nil
	}, Execute: func(ctx context.Context, q platform.TargetActionRequest, a platform.AccessContext) (platform.TargetActionResult, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(applied) >= 2 {
			return platform.TargetActionResult{}, ErrCapacity
		}
		applied = append(applied, q)
		if err := os.WriteFile(filepath.Join(output, "sink-attempts.json"), mustContractJSON(applied), 0600); err != nil {
			return platform.TargetActionResult{}, err
		}
		status := "succeeded"
		if q.Target.Object.Type == "chat" {
			status = "unknown"
		}
		return platform.TargetActionResult{Target: q.Target, Account: a.Account, Status: status, ResultID: platform.ExternalID{Namespace: "forge:receipt", Value: q.IdempotencyKey}, RetryClass: "never"}, nil
	}}}}
	prepare := func(ctx context.Context, member MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		if member != id || old.Account.Valid() && old.Account != account {
			return referenceActionSession{}, ErrIdentity
		}
		return referenceActionSession{Access: access, Capabilities: caps, Check: func(ctx context.Context) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.checkToolMemberLocked(ctx, toolsRequest{ServiceInfo: svc.info, Caller: agentCallerIdentity(id)})
		}, Close: func() {}}, nil
	}
	m.referenceExecutors["forge-test"] = prepare
	m.actionResolvers["forge-test"] = func(ctx context.Context, member MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
		s, e := prepare(ctx, member, platform.AccessContext{})
		if e != nil {
			return platform.TargetResolution{}, s, e
		}
		r, e := s.Capabilities.Resolve(ctx, l, s.Access)
		return r, s, e
	}
	defer func() {
		mu.Lock()
		report["sink_attempts"] = applied
		mu.Unlock()
		audit.mu.Lock()
		report["audit"] = audit
		audit.mu.Unlock()
	}()
	post := func(key, text string) PostReceipt {
		t.Helper()
		p, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: key, Text: text})
		if e != nil {
			t.Fatal(e)
		}
		return p
	}
	wait := func(n uint64) {
		t.Helper()
		s := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= n || s.Paused) })
		if s.Error != "" || s.Agent.TurnNumber != n || s.Agent.Turn == nil || s.Agent.Turn.Status != "completed" {
			report["turn_error"] = s.Error
			t.Fatalf("real turn %d failed: %s", n, s.Error)
		}
	}
	report["phase"] = "direct-user-action"
	first := post("send-user", "请对本地验收平台 forge-test 的 https://forge.invalid/users/one 执行一次 message.send，准确正文 L4C-USER-RECEIPT，动作 ID 固定为 standalone-user。本条指令即为本次准确目标和正文的直接授权。不要创建 Inbox 或跟进。")
	if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(1)
	rows, err := svc.store.queryActions(id, "standalone-user", "", "")
	if err != nil || len(rows) != 1 || rows[0].Status != "succeeded" || rows[0].Basis == nil || rows[0].Basis.Source.Seq != first.Seq || rows[0].Request.UnitRef != "" {
		t.Fatal("standalone human action missing", err)
	}
	report["phase"] = "direct-group-unknown"
	second := post("send-group", "请对本地验收平台 forge-test 的 https://forge.invalid/chats/one 执行一次 message.send，准确正文 L4C-GROUP-UNKNOWN，动作 ID 固定为 standalone-group。本条指令即为本次准确目标和正文的直接授权。如果回执是 unknown，可能已应用，只查询核实，不补发。")
	wait(2)
	rows, err = svc.store.queryActions(id, "standalone-group", "", "")
	if err != nil || len(rows) != 1 || rows[0].Status != "unknown" || rows[0].Attempts != 1 || rows[0].Basis == nil || rows[0].Basis.Source.Seq != second.Seq {
		t.Fatal("unknown action missing", err)
	}
	svc.store.mu.Lock()
	units, err := svc.store.followupUnitsLocked(id, "")
	svc.store.mu.Unlock()
	if err != nil || len(units) != 0 {
		t.Fatal("standalone created followup units", err)
	}
	before, err := svc.store.LoadAgent(id)
	if err != nil {
		t.Fatal(err)
	}
	report["before_restart_turn"] = before.TurnNumber
	report["phase"] = "service-reload"
	closeService(t, svc)
	svc = nil
	start()
	recovered, err := svc.store.queryActions(id, "", "", "")
	if err != nil || len(recovered) != 2 {
		t.Fatal("actions lost across service reload", err)
	}
	report["phase"] = "real-agent-receipt-review"
	post("review-receipts", "只查询本成员原有动作 standalone-user 和 standalone-group（actions read/list），说明成功与 unknown 的证据。不得执行任何动作、换 ID 或补发，也不创建跟进。")
	if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(3)
	mu.Lock()
	count := len(applied)
	mu.Unlock()
	if count != 2 {
		t.Fatal("service reload or receipt review resent", count)
	}
	records, err := svc.store.queryActions(id, "", "", "")
	if err != nil || len(records) != 2 {
		t.Fatal(err)
	}
	audit.mu.Lock()
	executions, reads, lists := audit.Calls["/v1/actions/execute"], audit.Calls["/v1/actions/read"], audit.Calls["/v1/actions/list"]
	audit.mu.Unlock()
	if executions != 2 || reads < 2 || lists+reads < 3 {
		t.Fatalf("unexpected model calls execute=%d read=%d list=%d", executions, reads, lists)
	}
	report["records"] = records
	report["standalone_units"] = len(units)
	report["sink_writes"] = count
	report["status"] = "passed"
	fmt.Println("real DSH direct instruction -> standalone actions -> ledger -> service reload/query passed; no Lark writes")
}
