//go:build e2e && darwin

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
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/notification"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type larkSelfDSHInput struct {
	Profile        string `json:"profile"`
	ExpectedUserID string `json:"expected_user_id"`
	ChatID         string `json:"chat_id"`
	Body           string `json:"body"`
	DispatchRecord string `json:"dispatch_record"`
}

// One real POST, exact target/body/uuid and an exclusive durable dispatch guard.
// The wrapper forwards every native response unchanged, including read errors.
func TestRealLarkSelfDSHSend(t *testing.T) {
	if os.Getenv("TEAMX_LARK_SELF_DSH_REAL") != "1" {
		t.Skip("explicit one-send authorization required")
	}
	var q larkSelfDSHInput
	data, err := os.ReadFile(os.Getenv("TEAMX_LARK_SELF_INPUT"))
	if err != nil || strictToolJSON(data, &q) != nil || q.Profile == "" || !strings.HasPrefix(q.ExpectedUserID, "ou_") || !strings.HasPrefix(q.ChatID, "oc_") || !handlingText(q.Body) || !filepath.IsAbs(q.DispatchRecord) {
		t.Fatal("exact authorized real input required")
	}
	if _, err := os.Stat(q.DispatchRecord); !os.IsNotExist(err) {
		t.Fatal("dispatch record exists or unreadable; no resend")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "maximum_native_dispatches": 1, "scope": "real Agent, production self capability, native self membership and independent receipt", "recorded_at": time.Now().UTC()}
	defer imageRealReport(t, output, report)
	cfg, err := loadStartupConfig(os.Getenv("TEAMX_ACTIONS_CONFIG"), true)
	if err != nil || cfg.Runtime == nil {
		t.Fatal("current runtime required", err)
	}
	runtime := cfg.Runtime
	if key := runtime.Model.APIKeyEnv; os.Getenv(key) == "" && cfg.Env[key] != "" {
		t.Setenv(key, cfg.Env[key])
	}
	imageRealCredential(t, runtime.Model.APIKeyEnv)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	workspace := filepath.Join(output, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(output, "teamx")
	if err = exec.Command("go", "build", "-o", binary, "../../cmd/teamx").Run(); err != nil {
		t.Fatal(err)
	}
	profile := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are an isolated real TeamX self-delivery acceptance assistant. Use only configured TeamX CLI. No file/env/credential inspection, direct network/CLI, tools run, debugging, delegation, Inbox creation or followup creation. From the human Room task resolve provider:lark, locator:{self:true}, turn_number:current_turn. Verify the returned actual user/self chat and executable message.send_self schema. Execute exactly ONCE using the supplied stable action ID, target_ref, turn_number, operation:message.send_self, input:{body:<exact human text>}. Omit policy_token and authority; the verified enabled self capability is the authority. No context needed. Then read the durable receipt with actions read --id. On any unknown/error stop, never change ID/operation/account or resend. Do not post extra Room messages; finish privately.`}
	report["model"] = runtime.Model.ModelID
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	room := managerCreate(t, m, "s2-self-real")
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	svc, err := newService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 2, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": profile}})
	if err != nil {
		t.Fatal(err)
	}
	defer closeService(t, svc)
	m.bindPlatformReferences(svc)
	observePolicyReal(svc, audit)
	m.mu.Lock()
	m.runtimes[room.RoomIdentity] = &roomRuntime{service: svc, status: "ready"}
	m.mu.Unlock()
	member, err := svc.add(apiRequest{AgentID: "assistant", Runner: RunnerConfig{Kind: "dsh", Profile: "runtime"}})
	if err != nil {
		t.Fatal(err)
	}
	id := MemberIdentity{RoomIdentity: room.RoomIdentity, AgentID: member.AgentID, MembershipID: member.MembershipID}
	const actionID = "s2-self-20261108-1"
	key := referenceDigest("", id, actionID)[:32]
	wrapperCfg := map[string]string{"profile": q.Profile, "group_id": q.ChatID, "user_id": q.ExpectedUserID, "body": q.Body, "key": key, "dispatch_record": q.DispatchRecord}
	raw, _ := json.Marshal(wrapperCfg)
	quoted, _ := json.Marshal(string(raw))
	script := strings.ReplaceAll(larkGroupRealCLI, "{'receive_id_type':'chat_id'}", "{'receive_id_type':'open_id'}")
	script = strings.ReplaceAll(script, "'receive_id':cfg['group_id']", "'receive_id':cfg['user_id']")
	script = strings.ReplaceAll(script, "payload['receive_id']==cfg['group_id']", "payload['receive_id']==cfg['user_id']")
	script = strings.Replace(script, "b=a[2:];is_post=False", "b=a[2:];is_post=False;is_search=False", 1)
	script = strings.Replace(script, "elif b[:2]==['api','GET']:", `elif b[:3]==['api','POST','/open-apis/im/v1/chat_p2p/batch_query']:
 assert b[-4:]==['--as','user','--format','json']
 assert json.loads(b[b.index('--params')+1])=={'chatter_id_type':'open_id'}
 assert json.loads(b[b.index('--data')+1])=={'chatter_ids':[cfg['user_id']]}
elif b[:3]==['api','POST','/open-apis/im/v1/messages/search']:
 assert b[-4:]==['--as','user','--format','json']
 payload=json.loads(b[b.index('--data')+1])
 assert payload['query']=='' and payload['filter']['chat_ids']==[cfg['group_id']] and payload['filter']['chat_type']=='p2p'
 is_search=True
elif b[:3]==['api','GET','/open-apis/im/v1/messages/mget']:
 assert b[-4:]==['--as','user','--format','json']
 ids=json.loads(b[b.index('--params')+1])['message_ids']
 known=json.load(open(cfg['dispatch_record']+'.search-ids.json'))
 assert ids and set(ids).issubset(set(known)), 'detail outside self-search results'
elif b[:2]==['api','GET']:`, 1)
	script = strings.Replace(script, "if is_post:\n summary=", `if is_search and r.returncode==0:
 response=json.loads(r.stdout)
 assert response.get('ok') is True and response.get('identity')=='user'
 ids=[item['id'] for item in response['data']['items']]
 cache=cfg['dispatch_record']+'.search-ids.json'
 if os.path.exists(cache):ids+=json.load(open(cache))
 with open(cache,'w') as f:json.dump(sorted(set(ids)),f)
if is_post:
 summary=`, 1)
	wrapper := filepath.Join(output, "bounded-lark-cli")
	if err = os.WriteFile(wrapper, []byte(fmt.Sprintf(script, string(quoted))), 0700); err != nil {
		t.Fatal(err)
	}
	p := platform.CLIProfile{Version: 1, Executable: wrapper, Profile: q.Profile, Brand: "feishu", CLIVersion: lark.CLIVersion}
	c := platformTestSave(t, m, managedPlatformRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "S2 isolated self user", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &p})
	c, err = m.verifyPlatform(ctx, managedPlatformRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" || c.Validation.Account.ID.Value != q.ExpectedUserID {
		t.Fatal("expected actual account verification failed", err)
	}
	pin, err := lark.Pin(ctx, p, c.Validation.Account)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	permission, err := pin.SendPermission(ctx)
	if err != nil || permission.UserScopes != "granted" || permission.ApplicationScopes != "granted" {
		t.Fatal("send permission not verified", err)
	}
	// No readiness override: the normal production registry and human settings path.
	r := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Enabled: true, PlatformVersion: c.Version, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", PlatformID: c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.send_self", TargetKind: "object", ObjectType: "user"}}}}})
	if r.Code != 200 {
		t.Fatal("self enable failed", r.Code, r.Body.String())
	}
	clock := &eventTestClock{now: time.Now().UTC()}
	m.notifications.Clock = clock.get
	state, err := m.configureLarkSubscription(ctx, notification.Key{Owner: room.RoomIdentity, Source: lark.MessagesSourceID}, 0, lark.Config{Enabled: true, PlatformID: c.ID, Private: lark.PrivateConfig{Enabled: true, Scope: "specified", ChatIDs: []string{q.ChatID}}, Target: notification.Target{Mode: "inbox", AgentID: id.AgentID, MembershipID: id.MembershipID}})
	if err != nil {
		t.Fatal(err)
	}
	checkNoInbox := func() {
		t.Helper()
		self, e := pin.SendReader().(interface {
			SelfChat(context.Context, platform.AccountIdentity) (lark.Chat, error)
		}).SelfChat(ctx, c.Validation.Account)
		if e != nil || self.ID != q.ChatID || !self.Relation.ValidFor(c.Validation.Account, q.ChatID) || self.Relation.Kind != "self" {
			t.Fatal("native self relationship not proven", e)
		}
		clock.add(2 * time.Minute)
		m.mu.Lock()
		runner := m.notifications.NewRunnerLocked(state.Metadata().Key)
		m.mu.Unlock()
		for range 3 {
			runner.Step(ctx)
		}
		m.mu.Lock()
		current, e := m.currentLarkLocked(room.RoomIdentity)
		m.mu.Unlock()
		if e != nil || current == nil || current.ErrorCode != "" {
			t.Fatal("native self notification scan failed", e)
		}
		if current.Search != nil {
			report["self_notification_search_hits"] = current.Search.Metrics.Hits
			report["self_notification_excluded"] = current.Search.Metrics.Excluded
		}
		if e = m.notifications.Deliver(ctx, current.Metadata().Key, current.SubscriptionVersion); e != nil {
			t.Fatal(e)
		}
		if page := inboxRoutingPreview(t, svc, id); page.SnapshotSeq != 0 {
			t.Fatal("self backflow", page)
		}
	}
	checkNoInbox()
	report["phase"] = "real-agent-send"
	_, err = svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "authorized-self-save", Text: fmt.Sprintf("把下面 JSON 的 body 原样发到我的飞书，正文只包含 body 的值。已确认当前绑定账号属于我且已启用本人投递。仅发送一次，稳定动作 ID 为 %s。通过 locator:{self:true} 解析本人目标，执行 message.send_self；不要提交 policy_token 或 authority。核验回执后停止，不补发。\n准确 input JSON：%s", actionID, func() string { b, _ := json.Marshal(lark.SendInput{Body: q.Body}); return string(b) }())})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	status := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= 1 || s.Paused) })
	if status.Error != "" || status.Agent.Turn == nil || status.Agent.Turn.Status != "completed" {
		report["turn_error"] = status.Error
		t.Fatal("real Agent turn failed", status.Error)
	}
	records, err := svc.store.queryActions(id, actionID, "", "")
	if err != nil || len(records) != 1 {
		t.Fatal("no durable self receipt", err)
	}
	record := records[0]
	report["action"] = record
	if record.Status != "succeeded" || record.Attempts != 1 || record.CapabilityBasis == nil || record.Rule != nil || record.Basis != nil || record.Request.PolicyToken != "" || record.Request.UnitRef != "" || record.Request.Input.Body != q.Body || record.Target.Object.ID != c.Validation.Account.ID || record.Target.Containers[0].ID.Value != q.ChatID {
		t.Fatal("real self delivery not verified", record.Status)
	}
	message, err := pin.MessageReader().Message(ctx, c.Validation.Account, record.Result.ResultID.Value)
	var body struct {
		Text string `json:"text"`
	}
	if err != nil || message.ID != record.Result.ResultID.Value || message.ChatID != q.ChatID || message.Sender.ID != q.ExpectedUserID || message.Sender.Type != "user" || message.Sender.IDType != "open_id" || message.Sender.TenantKey != c.Validation.Account.Tenant.Value || message.Type != "text" || message.Deleted || message.ParentID != "" || message.RootID != "" || message.ThreadID != "" || json.Unmarshal([]byte(message.Body.Content), &body) != nil || body.Text != q.Body {
		t.Fatal("independent self receipt mismatch", err)
	}
	checkNoInbox()
	// A peer cannot acquire a self action; ordinary user sends remain absent.
	candidate := record.Target
	candidate.Object.ID.Value = "ou_unapproved_peer"
	candidate.Containers = nil
	cap := lark.SelfSendAction(pin.SendReader(), nil)
	if _, err := cap.Inspect(ctx, platform.TargetActionRequest{Target: candidate, Operation: platform.OperationMessageSendSelf, Input: json.RawMessage(`{"body":"reject before write"}`)}, record.Access); err == nil {
		t.Fatal("peer accepted as self")
	}
	// Same-ID receipt recovery performs no native operation.
	again := agentActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: record.Request.TargetRef, ID: actionID, Operation: platform.OperationMessageSendSelf, Input: record.Request.Input}
	b, _ := json.Marshal(again)
	var receipt referenceActionReceipt
	if err = contractCLI(t, server.URL, svc, id)([]string{"actions", "execute", "--request", string(b)}, &receipt); err != nil || receipt.Status != "succeeded" || receipt.Result.ResultID != record.Result.ResultID {
		t.Fatal("same ID receipt recovery failed", err)
	}
	audit.mu.Lock()
	calls := audit.Calls["/v1/actions/read"]
	audit.mu.Unlock()
	if calls == 0 {
		t.Fatal("Agent did not read durable receipt")
	}
	report["status"] = "passed"
	report["message_id"] = message.ID
	report["verified_message"] = message
	var dispatch struct {
		Attempts int `json:"attempts"`
	}
	if b, e := os.ReadFile(q.DispatchRecord); e != nil || json.Unmarshal(b, &dispatch) != nil || dispatch.Attempts != 1 {
		t.Fatal("single native dispatch proof missing", e)
	}
	report["native_cli_post_invocations"] = dispatch.Attempts
	report["inbox_entries"] = 0
	report["self_subscription_verified"] = true
	report["account"] = c.Validation.Account
	report["dispatch_record"] = q.DispatchRecord
}
