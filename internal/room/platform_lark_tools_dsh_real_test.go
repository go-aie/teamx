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
	"reflect"
	"strings"
	"testing"
	"time"
)

// Real model/DSH/CLI/HTTP and durable member receipts, synthetic native CLI.
// The opt-in is deliberately separate from all real-message acceptance gates.
func TestRealLarkToolEnablementDSH(t *testing.T) {
	if os.Getenv("TEAMX_LARK_TOOLS_DSH_REAL") != "1" {
		t.Skip("explicit model acceptance opt-in and fresh output required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "failed", "native_writes": 0, "platform": "synthetic CLI", "scope": "real DSH member-tool enablement and historical receipt replay", "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, out, report)
	cfg, err := loadStartupConfig(os.Getenv("TEAMX_ACTIONS_CONFIG"), true)
	if err != nil || cfg.Runtime == nil {
		t.Fatal("current runtime required", err)
	}
	runtime := cfg.Runtime
	if key := runtime.Model.APIKeyEnv; os.Getenv(key) == "" && cfg.Env[key] != "" {
		t.Setenv(key, cfg.Env[key])
	}
	imageRealCredential(t, runtime.Model.APIKeyEnv)
	report["model"] = runtime.Model.ModelID
	workspace := filepath.Join(out, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(out, "teamx")
	if err = exec.Command("go", "build", "-o", binary, "../../cmd/teamx").Run(); err != nil {
		t.Fatal(err)
	}
	p := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are a bounded TeamX member-tool acceptance assistant. Use only the configured TeamX CLI, first read platforms skill and platforms member help. Read the actual human Room message to obtain source_seq. The human requests enabling the exact current member's Lark tool with the supplied stable operation ID. Use platforms list/member get to read the actual platform version/tool revision, then platforms member tool --enabled true without --action (default capabilities), with that exact operation ID and human source_seq. Read member status to confirm the durable result. Never invoke actions, tools run, native CLI, network, credentials/files, OAuth, notifications, policies, other members, delegation or Room messages. Stop on an uncertain write; do not invent a new operation ID. Finish privately.`}
	m := testManager(t, filepath.Join(out, "rooms-root"), managerTestProfiles(t))
	r := managerCreate(t, m, "lark-tool-enable")
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	svc, err := newService(m.roomDir(r.RoomID), r.RoomID, serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 2, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	m.bindPlatformReferences(svc)
	observePolicyReal(svc, audit)
	m.mu.Lock()
	m.runtimes[r.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	profile, _ := roomAuthProfile(t)
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("d", 32), Name: "验收飞书", Provider: "lark", URL: "https://open.feishu.cn", CLIProfile: &profile})
	c, err = m.verifyPlatform(t.Context(), managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" {
		t.Fatal("fixture verification", err)
	}
	// Reproduce the historical enabled/read-only state before the real request.
	before := bindCLITool(t, m, r.RoomIdentity, MemberState{AgentID: member.AgentID, MembershipID: member.MembershipID}, c, "lark", 0)
	if before.Bindings[0].ActionAuthorization != nil {
		t.Fatal("fixture already had actions")
	}
	opID, _ := newOpaqueID()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	source, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: r.RoomIdentity, Caller: humanCaller, ClientMessageID: "enable-lark", Text: fmt.Sprintf("为当前助理启用飞书工具（平台 %s），补齐默认动作并核验。操作 ID 固定为 %s。不要关闭再打开，不要发送消息，不修改通知。", c.ID, opID)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= 1 || s.Paused) })
	if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
		report["turn_error"] = status.Error
		t.Fatal("real DSH turn failed", status.Error)
	}
	m.mu.Lock()
	op, err := m.readPlatformMemberLocked(opID)
	after, bindingsErr := m.readToolBindingsLocked(r.RoomIdentity)
	m.mu.Unlock()
	if err != nil || bindingsErr != nil || op.Status != "completed" || op.Result.Status != "applied" || op.Source.Seq != source.Seq || op.MemberIdentity != id || len(op.RequestedActions) != 3 || len(after.Bindings) != 1 || after.Bindings[0].ActionAuthorization == nil || len(after.Bindings[0].ActionAuthorization.Grants) != 3 {
		t.Fatal("real model did not upgrade the exact read-only binding", err, bindingsErr)
	}
	audit.mu.Lock()
	memberWrites, actionWrites := audit.Calls["/v1/platforms/member/tool"], audit.Calls["/v1/actions/execute"]
	audit.mu.Unlock()
	if memberWrites != 1 || actionWrites != 0 {
		t.Fatal("unexpected model mutations", memberWrites, actionWrites)
	}
	req := platformMemberRequest{ServiceInfo: svc.info, Caller: agentCaller(member.AgentID, member.MembershipID), OperationID: opID, SourceSeq: source.Seq, Input: &op.Input}
	replay, err := m.configurePlatformMember(ctx, "tool", req)
	if err != nil || !reflect.DeepEqual(op, replay) {
		t.Fatal("historical receipt replay changed", err)
	}
	data, _ := json.Marshal(op)
	if err = os.WriteFile(filepath.Join(out, "member-receipt.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	report["status"], report["member_writes"], report["action_writes"], report["grants"], report["receipt_replay_equal"] = "passed", memberWrites, actionWrites, 3, true
}
