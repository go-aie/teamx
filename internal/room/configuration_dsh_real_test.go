//go:build e2e && (darwin || linux)

package room

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
)

// Real DSH/model and production prompts, CLI, HTTP and stores. All platform
// accounts belong to local read-only fixtures; no collector or action is run.
func TestConfigurationIntentRealDSH(t *testing.T) {
	if os.Getenv("TEAMX_CONFIGURATION_DSH") != "fixture" {
		t.Skip("explicit fixture model opt-in, runtime config and fresh output required")
	}
	out := imageRealOutput(t)
	report := map[string]any{"status": "incomplete", "provider": "local fixtures", "real_platform": "not run: separately authorized isolated targets required", "remote_business_writes": 0, "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, out, report)
	cfg, err := loadStartupConfig(os.Getenv("TEAMX_CONFIGURATION_CONFIG"), true)
	if err != nil || cfg.Runtime == nil {
		t.Fatal("valid runtime configuration required", err)
	}
	runtime := *cfg.Runtime
	if cfg.Models != nil {
		entry, err := cfg.Models.resolve(modelSelection{Mode: "default"})
		if err != nil {
			t.Fatal(err)
		}
		// Read an existing immutable credential without initializing or changing
		// the operator's model store. Keep the key in the child environment only.
		credentials := &modelCredentials{dir: filepath.Join(filepath.Dir(cfg.path), "model-credentials")}
		credential, err := credentials.get(entry.CredentialRef)
		if err != nil {
			t.Fatal(err)
		}
		runtime.Model = entry.materialize()
		runtime.Model.APIKeyEnv = "TEAMX_CONFIGURATION_MODEL_KEY"
		t.Setenv(runtime.Model.APIKeyEnv, credential.Value)
	} else {
		if key := runtime.Model.APIKeyEnv; os.Getenv(key) == "" && cfg.Env[key] != "" {
			t.Setenv(key, cfg.Env[key])
		}
		imageRealCredential(t, runtime.Model.APIKeyEnv)
	}
	report["model"] = runtime.Model.ModelID
	workspace := filepath.Join(out, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	h := larkToolConfigurationHarness(t)
	h.m.notifications.Clock = time.Now
	g := configurationGitLab(t, h)
	r := managerCreate(t, h.m, "configuration-combination")
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(h.m))
	defer server.Close()
	h.m.config.endpoint = server.URL
	p := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are a bounded TeamX acceptance assistant. Read the platforms skill and CLI help, then execute actual human Room requests with the production configuration protocol. Use only the configured TeamX CLI. Reply through Room send; ordinary output is private. Tool enablement is an absolute intent: use a distinct stable operation ID per platform and actual human source_seq, without the retired global tool revision. Read both rules before updating either; policy update uses each record version, not the file revision. Notification edits require the exact member get notification_version. Inspect original receipts and authoritative state before reporting success. On conflict preserve the original observation and explain it; on unknown stop and inspect status, never change ID or replay. Do not access files, environment, credentials, native tools/network, tools run, external actions, account creation, members, delegation or collectors. Do not infer authorization from a saved binding or a version. Finish the current request without polling for another human message.`}
	svc, err := newService(h.m.roomDir(r.RoomID), r.RoomID, serviceConfig{endpoint: server.URL, cliPath: productionFixtureCLI(t), maxTurns: 8, persistentScheduling: true, dshTurnTimeout: 4 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": p}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	h.m.bindPlatformReferences(svc)
	observePolicyReal(svc, audit)
	h.m.mu.Lock()
	h.m.runtimes[r.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	h.m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: r.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	report["member"] = id
	for _, rule := range []PolicyChange{
		{ID: "style", Status: "active", Text: "简短中文", SourceText: "以后简短中文"},
		{ID: "order", Status: "active", Text: "先给结论", SourceText: "以后先给结论"},
	} {
		if _, err := svc.store.changePolicy(id, humanCaller, rule); err != nil {
			t.Fatal(err)
		}
	}
	initialCfg := h.m.defaultLarkConfig(h.c.ID)
	initialCfg.Enabled, initialCfg.Private.Enabled = true, false
	initialCfg.Target = platformMemberTarget(id)
	initialCfg.Groups = lark.GroupConfig{Enabled: true, Scope: "specified", ChatIDs: []string{"oc_group_one"}, MessageMode: "mentions", AtMe: true}
	initial, err := h.m.configureLarkSubscription(t.Context(), notification.Key{Owner: r.RoomIdentity, Source: lark.MessagesSourceID}, 0, initialCfg)
	if err != nil {
		t.Fatal(err)
	}
	var toolSaves, policySaves atomic.Int32
	var loseNotificationReceipt atomic.Bool
	h.m.mu.Lock()
	rename := h.m.store.ops.rename
	h.m.store.ops.rename = func(from, to string) error {
		if to == h.m.toolBindingsPath(id.RoomIdentity) {
			toolSaves.Add(1)
		}
		if loseNotificationReceipt.Load() && filepath.Dir(to) == filepath.Dir(h.m.platformMemberPath("receipt-probe")) {
			var op platformMemberOperation
			if jsonFile(from, &op) == nil && op.Kind == "notifications" && op.Status == "completed" {
				loseNotificationReceipt.Store(false)
				return os.ErrPermission
			}
		}
		return rename(from, to)
	}
	h.m.mu.Unlock()
	svc.store.mu.Lock()
	policyRename := svc.store.ops.rename
	svc.store.ops.rename = func(from, to string) error {
		if filepath.Base(to) == "policy.json" {
			policySaves.Add(1)
		}
		return policyRename(from, to)
	}
	svc.store.mu.Unlock()
	var cases []map[string]any
	defer func() {
		policy, _ := svc.store.readPolicy(id)
		state, _ := svc.store.LoadRoom()
		h.m.mu.Lock()
		bindings, _ := h.m.readToolBindingsLocked(id.RoomIdentity)
		notifications, _ := h.m.currentLarkForTargetLocked(id.RoomIdentity, h.c.ID, initialCfg.Target)
		ops, _ := h.m.platformMemberOperationsLocked()
		h.m.mu.Unlock()
		report["policy"], report["bindings"], report["notification"], report["operations"], report["messages"] = policy, bindings, notifications, ops, state.Messages
		report["cases"], report["audit"], report["tool_saves"], report["policy_saves"] = cases, audit, toolSaves.Load(), policySaves.Load()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 13*time.Minute)
	defer cancel()
	turn := uint64(0)
	post := func(name, text string) uint64 {
		t.Helper()
		report["phase"] = name
		t.Log("real model case:", name)
		source, err := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: name, Text: text})
		if err != nil {
			t.Fatal(err)
		}
		if turn == 0 {
			if _, err := svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
				t.Fatal(err)
			}
		}
		turn++
		status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= turn || s.Paused) })
		if status.Error != "" || status.Agent.TurnNumber != turn || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
			t.Fatalf("%s: real model turn failed: %s", name, status.Error)
		}
		cases = append(cases, map[string]any{"case": name, "source_seq": source.Seq, "turn": turn})
		return source.Seq
	}
	seq := post("dual-tools", fmt.Sprintf("请为当前助理同时开启飞书（平台 ID %s）和 GitLab（平台 ID %s）的工具，分别保存。它们是已有且已核验的连接，直接复用；不改通知、不执行外部动作。", h.c.ID, g.ID))
	h.m.mu.Lock()
	bindings, err := h.m.readToolBindingsLocked(id.RoomIdentity)
	ops, oe := h.m.platformMemberStatusLocked(platformMemberRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID), SourceSeq: seq})
	h.m.mu.Unlock()
	if err != nil || oe != nil || len(bindings.Bindings) != 2 || toolSaves.Load() != 2 || len(ops) != 2 {
		t.Fatal("dual tool state/receipt/save mismatch", bindings, ops, err, oe)
	}
	for _, op := range ops {
		if op.Kind != "tool" || op.Status != "completed" || op.Source.Seq != seq || op.Input.ToolRevision != 0 {
			t.Fatal("tool intent was not completed under new contract", op)
		}
	}
	seq = post("two-rule-edits", "请先读取当前两条长期规则，再分别修改：style 改为准确正文“中文简明，并保留必要限制”；order 改为准确正文“先给结论，再给理由”。两条规则都直接更新为有效规则，保留各自 ID，不新建相似规则。")
	policy, err := svc.store.readPolicy(id)
	if err != nil || policy.Revision != 4 || policySaves.Load() != 2 || len(policy.Current) != 2 {
		t.Fatal("two independent rule edits not saved exactly once", policy, err)
	}
	for _, rule := range policy.Current {
		want := map[string]string{"style": "中文简明，并保留必要限制", "order": "先给结论，再给理由"}[rule.ID]
		if rule.Version != 2 || rule.Text != want || rule.Source.Seq != seq || rule.Conversation == nil || rule.Conversation.DigestVersion != 2 {
			t.Fatal("wrong rule or missing genuine human evidence", rule)
		}
	}
	seq = post("notification-observation", fmt.Sprintf("飞书平台 %s：只把当前助理的群接收条件改为同时接收 @我 和 @所有人，其他范围、私聊、起点和间隔保留。先读取准确成员通知设置，用该观察前提直接保存一次。", h.c.ID))
	h.m.mu.Lock()
	next, err := h.m.currentLarkForTargetLocked(id.RoomIdentity, h.c.ID, initialCfg.Target)
	ops, oe = h.m.platformMemberStatusLocked(platformMemberRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID), SourceSeq: seq})
	h.m.mu.Unlock()
	if err != nil || next == nil || next.SubscriptionVersion != initial.SubscriptionVersion+1 || !next.Config.Groups.AtMe || !next.Config.Groups.AtAll || !reflect.DeepEqual(next.Config.Groups.ChatIDs, initial.Config.Groups.ChatIDs) || !reflect.DeepEqual(next.Config.Private, initial.Config.Private) || next.Baseline != initial.Baseline || next.Config.BackfillStart != initial.Config.BackfillStart {
		t.Fatal("notification observation/state mismatch", next, err)
	}
	if oe != nil || len(ops) != 1 || ops[0].Kind != "notifications" || ops[0].Status != "completed" || ops[0].Input.NotificationVersion != initial.SubscriptionVersion || ops[0].Result.Version != next.SubscriptionVersion {
		t.Fatal("notification receipt lost the original observation", ops, oe)
	}
	// Give the real model an uncertain result after a proven target save. It
	// must inspect original status and stop, without replay or a replacement ID.
	loseNotificationReceipt.Store(true)
	seq = post("unknown-notification-result", fmt.Sprintf("飞书平台 %s：关闭当前助理群接收中的 @所有人，保留 @我 和其他设置；先读当前设置并保存一次。如果结果未知，只查询原操作回执并说明待核实，不再次提交，也不换 ID。", h.c.ID))
	h.m.mu.Lock()
	unknown, ue := h.m.currentLarkForTargetLocked(id.RoomIdentity, h.c.ID, initialCfg.Target)
	ops, oe = h.m.platformMemberStatusLocked(platformMemberRequest{ServiceInfo: svc.info, Caller: agentCaller(id.AgentID, id.MembershipID), SourceSeq: seq})
	h.m.mu.Unlock()
	if loseNotificationReceipt.Load() || ue != nil || oe != nil || unknown == nil || unknown.SubscriptionVersion != next.SubscriptionVersion+1 || unknown.Config.Groups.AtAll || !unknown.Config.Groups.AtMe || unknown.Baseline != initial.Baseline || len(ops) != 1 || ops[0].Status != "unknown" || ops[0].Input.NotificationVersion != next.SubscriptionVersion {
		t.Fatal("unknown result was replayed, lost or promoted", unknown, ops, ue, oe)
	}
	audit.mu.Lock()
	defer audit.mu.Unlock()
	if len(audit.Turns) != 4 || audit.Calls["/v1/platforms/member/tool"] != 2 || audit.Calls["/v1/platforms/member/notifications"] != 2 || audit.Calls["/v1/policy/update"] != 2 || audit.Calls["/v1/actions/execute"] != 0 || audit.Calls["/v1/tools/run"] != 0 {
		t.Fatal("unexpected configuration retries or business action attempts", audit.Calls)
	}
	if toolSaves.Load() != 2 || policySaves.Load() != 2 {
		t.Fatal("later request changed unrelated domain")
	}
	report["status"] = "passed"
}
