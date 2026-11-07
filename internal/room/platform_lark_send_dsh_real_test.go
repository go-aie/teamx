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
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

type larkGroupDSHInput struct {
	Profile        string `json:"profile"`
	GroupID        string `json:"group_id"`
	GroupName      string `json:"group_name"`
	ExpectedUserID string `json:"expected_user_id"`
	Body           string `json:"body"`
	DispatchRecord string `json:"dispatch_record"`
}

// This transparent executable forwards to the real CLI, with the actual pinned
// config and keychain. It rejects all writes except one exact authorized POST.
// The exclusive dispatch file is outside the fresh Room directory, so another
// test run/session cannot replay a possibly dispatched request.
const larkGroupRealCLI = `#!/usr/bin/env python3
import json,sys,os,subprocess,datetime
cfg=json.loads(%s)
a=sys.argv[1:]
assert a[:2]==['--profile',cfg['profile']]
b=a[2:];is_post=False
if b[:3]==['api','POST','/open-apis/im/v1/messages']:
 assert b[-4:]==['--as','user','--format','json']
 params=json.loads(b[b.index('--params')+1]);payload=json.loads(b[b.index('--data')+1])
 assert params=={'receive_id_type':'chat_id'}
 assert payload=={'receive_id':cfg['group_id'],'msg_type':'text','content':json.dumps({'text':cfg['body']},ensure_ascii=False,separators=(',',':')),'uuid':cfg['key']} or (payload['receive_id']==cfg['group_id'] and payload['msg_type']=='text' and json.loads(payload['content'])=={'text':cfg['body']} and payload['uuid']==cfg['key'] and set(payload)=={'receive_id','msg_type','content','uuid'})
 assert os.environ.get('LARKSUITE_CLI_CONFIG_DIR'), 'fixed account config required'
 fd=os.open(cfg['dispatch_record'],os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f:
  json.dump({'status':'unknown','attempts':1,'group_id':cfg['group_id'],'body':cfg['body'],'uuid':cfg['key'],'time':datetime.datetime.now(datetime.timezone.utc).isoformat()},f,ensure_ascii=False);f.flush();os.fsync(f.fileno())
 is_post=True
elif b[:2]==['api','GET']:
 path=b[2]
 allowed=['/open-apis/authen/v1/user_info','/open-apis/im/v1/chats/'+cfg['group_id'],'/open-apis/im/v1/chats/'+cfg['group_id']+'/members']
 response_path=cfg['dispatch_record']+'.response.json'
 if os.path.exists(response_path):
  response=json.load(open(response_path));mid=response.get('message_id')
  if mid:allowed.append('/open-apis/im/v1/messages/'+mid)
 assert path in allowed, 'read outside authorized acceptance target'
 assert b[-4:]==['--as','user','--format','json']
elif b in [['--version'],['config','show'],['whoami','--as','user'],['auth','scopes','--json']]:pass
elif b[:2]==['auth','check']:pass
else:raise Exception('operation outside bounded acceptance')
r=subprocess.run(['/opt/homebrew/bin/lark-cli']+a,capture_output=True)
if is_post:
 summary={'exit_code':r.returncode}
 try:
  result=json.loads(r.stdout if r.returncode==0 else r.stderr)
  summary['ok']=result.get('ok');summary['identity']=result.get('identity')
  summary['message_id']=result.get('data',{}).get('message_id');summary['chat_id']=result.get('data',{}).get('chat_id')
  summary['error_code']=result.get('error',{}).get('code')
 except Exception:summary['response']='unparseable'
 with open(cfg['dispatch_record']+'.response.json','w') as f:json.dump(summary,f)
 os.chmod(cfg['dispatch_record']+'.response.json',0o600)
sys.stdout.buffer.write(r.stdout);sys.stderr.buffer.write(r.stderr)
sys.exit(r.returncode)
`

// Real Agent -> production managed Lark action path -> actual group POST ->
// independent read -> service reload -> receipt query and identical-ID recovery.
// Only readiness is injected on this isolated manager; production stays closed.
func TestRealLarkGroupDSHSendAndRecovery(t *testing.T) {
	if os.Getenv("TEAMX_LARK_GROUP_DSH_REAL") != "1" {
		t.Skip("explicit real send opt-in and exact authorized input required")
	}
	var q larkGroupDSHInput
	b, err := os.ReadFile(os.Getenv("TEAMX_LARK_GROUP_INPUT"))
	if err != nil || strictToolJSON(b, &q) != nil || q.Profile == "" || !strings.HasPrefix(q.GroupID, "oc_") || !strings.HasPrefix(q.ExpectedUserID, "ou_") || q.GroupName == "" || !handlingText(q.Body) || !filepath.IsAbs(q.DispatchRecord) {
		t.Fatal("exact bounded real input required")
	}
	if _, err = os.Stat(q.DispatchRecord); !os.IsNotExist(err) {
		t.Fatal("dispatch record exists or cannot be checked; no second send permitted")
	}
	output := imageRealOutput(t)
	report := map[string]any{"status": "failed", "scope": "real user identity, one group, no Inbox/unit, real DSH and production managed actions", "recorded_at": time.Now().UTC(), "maximum_native_dispatches": 1}
	defer imageRealReport(t, output, report)
	config, err := loadStartupConfig(os.Getenv("TEAMX_ACTIONS_CONFIG"), true)
	if err != nil || config.Runtime == nil {
		t.Fatal("current runtime required", err)
	}
	runtime := config.Runtime
	if key := runtime.Model.APIKeyEnv; os.Getenv(key) == "" && config.Env[key] != "" {
		t.Setenv(key, config.Env[key])
	}
	imageRealCredential(t, runtime.Model.APIKeyEnv)
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	workspace := filepath.Join(output, "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(output, "teamx")
	if err = exec.Command("go", "build", "-o", binary, "../../cmd/teamx").Run(); err != nil {
		t.Fatal(err)
	}
	profile := dshProfile{BinaryPath: runtime.DSH.BinaryPath, BinaryArgs: runtime.DSH.BinaryArgs, Artifact: runtime.DSH.Artifact, Workspace: workspace, Model: runtime.Model.Clone(), Instructions: `You are an isolated TeamX real Lark acceptance assistant. This task performs ONE REAL user-identity group message under the exact direct human Room instruction. Use only configured TeamX CLI in JSON with current_turn/policy_token. No file/environment/credential inspection, direct network/CLI access, tools run, delegation or debugging. Never create Inbox entries or followup units. First actions resolve with provider lark and the exact locator.candidate supplied by the human; it is input to native verification, not an execution shortcut. Confirm the resolved account/group and applicable message.send schema, then actions execute ONCE with the exact supplied stable ID, input.body, target_ref, current turn/policy token, authority {type:"human_reply",room_seq:<actual human message.seq>,reason:<this instruction authorizes the exact group and body>}. No context. Read the saved receipt with actions read --id. Stop on not_applied, unknown or any action error; never change ID/target/operation/platform or retry to debug. If only asked to review receipts after restart, use actions read/list only, never execute. End privately without sending extra Room questions or messages.`}
	report["model"] = runtime.Model.ModelID
	m := testManager(t, filepath.Join(output, "rooms-root"), managerTestProfiles(t))
	room := managerCreate(t, m, "l4c-real-group")
	audit := &policyRealAudit{Calls: map[string]int{}}
	server := httptest.NewServer(audit.serve(m))
	defer server.Close()
	var svc *service
	start := func() {
		t.Helper()
		var e error
		svc, e = newService(m.roomDir(room.RoomID), room.RoomID, serviceConfig{endpoint: server.URL, cliPath: binary, maxTurns: 4, persistentScheduling: true, dshTurnTimeout: 3 * time.Minute, dshProfiles: map[string]dshProfile{"runtime": profile}})
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
	const actionID = "l4c-real-group-1"
	key := referenceDigest("", id, actionID)[:32]
	wrapperConfig := map[string]string{"profile": q.Profile, "group_id": q.GroupID, "body": q.Body, "key": key, "dispatch_record": q.DispatchRecord}
	raw, _ := json.Marshal(wrapperConfig)
	quoted, _ := json.Marshal(string(raw))
	wrapper := filepath.Join(output, "bounded-lark-cli")
	if err = os.WriteFile(wrapper, []byte(fmt.Sprintf(larkGroupRealCLI, string(quoted))), 0700); err != nil {
		t.Fatal(err)
	}
	p := platform.CLIProfile{Version: 1, Executable: wrapper, Profile: q.Profile, Brand: "feishu", CLIVersion: lark.CLIVersion}
	c := platformTestSave(t, m, platformConnectionRequest{ServiceEpoch: m.epoch, Caller: humanCaller, ID: strings.Repeat("e", 32), Name: "L4-C isolated real user send", Provider: "lark", URL: lark.Origin("feishu"), CLIProfile: &p})
	c, err = m.verifyPlatformConnection(ctx, platformConnectionRequest{ID: c.ID, Version: c.Version})
	if err != nil || c.Validation.Status != "valid" || c.Validation.Account.ID.Value != q.ExpectedUserID {
		t.Fatal("expected real account verification failed", err)
	}
	account := c.Validation.Account
	target := platform.TargetIdentity{IdentityScope: account.IdentityScope, Object: platform.NativeIdentity{Type: "chat", ID: platform.ExternalID{Namespace: "lark:chat_id:" + strings.TrimPrefix(account.ID.Namespace, "lark:open_id:"), Value: q.GroupID}}}
	pin, err := lark.Pin(ctx, p, account)
	if err != nil {
		t.Fatal(lark.ErrorCode(err))
	}
	defer pin.Close()
	native, err := pin.SendReader().SendGroup(ctx, account, q.GroupID)
	if err != nil || native.Name != q.GroupName {
		t.Fatal("exact joined group preflight failed", lark.ErrorCode(err))
	}
	permission, err := pin.SendPermission(ctx)
	if err != nil || permission.UserScopes != "granted" || permission.ApplicationScopes != "granted" {
		t.Fatal("send scopes missing", lark.ErrorCode(err))
	}
	// Static descriptions and the managed executor are enabled only in this test
	// manager, then the normal operator-owned per-member grant is saved via HTTP.
	m.referenceAdapters["lark"] = func() platform.ReferenceCapabilities {
		caps := lark.ActionCapabilities(nil, nil, nil, lark.ReplyDisabledReason, "")
		cap := caps.Actions[platform.OperationMessageSend]
		cap.Spec.ObjectTypes = []string{"chat"}
		caps.Actions[platform.OperationMessageSend] = cap
		return caps
	}
	var adapterCalls atomic.Int32
	prepare := func(ctx context.Context, member MemberIdentity, old platform.AccessContext) (referenceActionSession, error) {
		if member != id {
			return referenceActionSession{}, ErrIdentity
		}
		session, e := m.prepareLarkReferenceActionWithReadiness(ctx, member, old, lark.ReplyDisabledReason, "")
		if e != nil {
			return session, e
		}
		cap := session.Capabilities.Actions[platform.OperationMessageSend]
		cap.Spec.ObjectTypes = []string{"chat"}
		execute := cap.Execute
		cap.Execute = func(ctx context.Context, request platform.TargetActionRequest, access platform.AccessContext) (platform.TargetActionResult, error) {
			var input lark.SendInput
			if !sameNativeTarget(request.Target, target) || access.Account != account || request.IdempotencyKey != key || request.Operation != platform.OperationMessageSend || strictToolJSON(request.Input, &input) != nil || input.Body != q.Body || adapterCalls.Add(1) != 1 {
				return platform.TargetActionResult{Target: request.Target, Account: access.Account, Status: "not_applied", ReasonCode: "outside_single_authorized_dispatch", RetryClass: "never"}, nil
			}
			return execute(ctx, request, access)
		}
		session.Capabilities.Actions[platform.OperationMessageSend] = cap
		return session, nil
	}
	m.referenceExecutors["lark"] = prepare
	m.actionResolvers["lark"] = func(ctx context.Context, member MemberIdentity, l platform.TargetLocator) (platform.TargetResolution, referenceActionSession, error) {
		session, e := prepare(ctx, member, platform.AccessContext{})
		if e != nil {
			return platform.TargetResolution{}, session, e
		}
		r, e := session.Capabilities.Resolve(ctx, l, session.Access)
		return r, session, e
	}
	r := managerHTTP(m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Enabled: true, ConnectionVersion: c.Version, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", ConnectionID: c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.send", TargetKind: "object", ObjectType: "chat"}}}}})
	if r.Code != 200 {
		t.Fatal("explicit member group-send grant failed", r.Code, r.Body.String())
	}
	// Dry-run the exact native request before scheduling the real Agent.
	dry, err := exec.CommandContext(ctx, "/opt/homebrew/bin/lark-cli", "--profile", q.Profile, "im", "+messages-send", "--chat-id", q.GroupID, "--text", q.Body, "--idempotency-key", key, "--as", "user", "--dry-run", "--format", "json").Output()
	var preview struct {
		OK       bool   `json:"ok"`
		Identity string `json:"identity"`
		Dry      bool   `json:"dry_run"`
		Data     struct {
			Context struct {
				App  string `json:"app_id"`
				User string `json:"user_open_id"`
			} `json:"context"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(dry, &preview) != nil || !preview.OK || !preview.Dry || preview.Identity != "user" || "lark:open_id:"+preview.Data.Context.App != account.ID.Namespace || preview.Data.Context.User != q.ExpectedUserID {
		t.Fatal("dry-run identity failed", err)
	}
	report["dry_run_verified"] = true
	report["member"] = id
	report["account"] = account
	defer func() {
		report["adapter_execute_calls"] = adapterCalls.Load()
		report["dispatch_record"] = q.DispatchRecord
		audit.mu.Lock()
		report["audit"] = audit
		audit.mu.Unlock()
	}()
	post := func(key, text string) PostReceipt {
		t.Helper()
		r, e := svc.room.Call[PostReceipt](ctx, PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: key, Text: text})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	wait := func(n uint64) {
		t.Helper()
		s := waitMember(t, ctx, svc.target(id.MembershipID), id, func(s MemberStatus) bool { return !s.Active && (s.Agent.TurnNumber >= n || s.Paused) })
		if s.Error != "" || s.Agent.TurnNumber != n || s.Agent.Turn == nil || s.Agent.Turn.Status != "completed" {
			report["turn_error"] = s.Error
			t.Fatalf("real Agent turn %d failed: %s", n, s.Error)
		}
	}
	candidate, _ := json.Marshal(target)
	report["phase"] = "real-agent-send"
	first := post("authorized-group-send", fmt.Sprintf("本轮用户已明确授权以当前绑定的罗彭用户身份，向指定单群 %s 发送一次测试消息。准确正文：%s。动作 ID 固定为 %s。使用 actions resolve，provider=lark，locator.candidate=%s。核验准确账号、群和 schema 后执行 message.send，仅一次；本条真实 Room 消息为直接授权依据。没有 Inbox 或跟进，也不要创建。结果不确定就停止，不换 ID 补发。", q.GroupName, q.Body, actionID, candidate))
	if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(1)
	records, err := svc.store.queryActions(id, actionID, "", "")
	if err != nil || len(records) != 1 {
		t.Fatal("real action missing", err)
	}
	record := records[0]
	report["action"] = record
	if record.Status != "succeeded" || record.Attempts != 1 || record.Result == nil || record.Basis == nil || record.Basis.Source.Seq != first.Seq || record.Access.Account != account || record.Request.UnitRef != "" || record.Request.Input.Body != q.Body || !sameNativeTarget(record.Target, target) {
		t.Fatal("real send not verified; no automatic replay", record.Status)
	}
	messageID := record.Result.ResultID.Value
	verify := func() {
		t.Helper()
		m, e := pin.MessageReader().Message(ctx, account, messageID)
		var text struct {
			Text string `json:"text"`
		}
		if e != nil || m.ID != messageID || m.ChatID != q.GroupID || m.Type != "text" || m.Deleted || m.Sender.Type != "user" || m.Sender.IDType != "open_id" || m.Sender.ID != q.ExpectedUserID || m.Sender.TenantKey != account.Tenant.Value || m.ParentID != "" || m.RootID != "" || m.ThreadID != "" || json.Unmarshal([]byte(m.Body.Content), &text) != nil || text.Text != q.Body {
			t.Fatal("independent native receipt verification failed", lark.ErrorCode(e))
		}
		report["verified_message"] = m
	}
	verify()
	svc.store.mu.Lock()
	units, err := svc.store.followupUnitsLocked(id, "")
	svc.store.mu.Unlock()
	if err != nil || len(units) != 0 {
		t.Fatal("unexpected followup unit", err)
	}
	report["phase"] = "service-reload"
	previousEpoch := svc.info.ServiceEpoch
	closeService(t, svc)
	svc = nil
	start()
	if svc.info.ServiceEpoch == previousEpoch {
		t.Fatal("runtime epoch did not change")
	}
	records, err = svc.store.queryActions(id, actionID, "", "")
	if err != nil || len(records) != 1 || records[0].Status != "succeeded" || records[0].Result.ResultID.Value != messageID {
		t.Fatal("receipt lost across service reload", err)
	}
	report["phase"] = "real-agent-query-after-reload"
	post("query-only", "只查询原有动作 l4c-real-group-1，使用 actions read/list 核对原发送账号、目标、正文、回执。不得再 execute、重新解析发送、换 ID 或补发。")
	if _, err = svc.control(ctx, apiRequest{AgentID: id.AgentID, MembershipID: id.MembershipID}, "run"); err != nil {
		t.Fatal(err)
	}
	wait(2)
	// Identical business ID recovery returns the original receipt without another
	// adapter call. This is a recovery query, not a second native write attempt.
	request := record.Request
	again := agentActionRequest{followupCommand: followupCommand{TurnNumber: 2}, TargetRef: request.TargetRef, ID: request.ID, PolicyToken: request.PolicyToken, Authority: agentActionAuthority{Type: "human_reply", RoomSeq: request.Authority.RoomSeq, Reason: request.Authority.Reason}, Operation: request.Operation, Input: request.Input}
	body, _ := json.Marshal(again)
	var receipt referenceActionReceipt
	if err = contractCLI(t, server.URL, svc, id)([]string{"actions", "execute", "--request", string(body)}, &receipt); err != nil || receipt.Status != "succeeded" || receipt.Result == nil || receipt.Result.ResultID.Value != messageID {
		t.Fatal("same ID recovery failed", err)
	}
	verify()
	if adapterCalls.Load() != 1 {
		t.Fatal("adapter called again after reload", adapterCalls.Load())
	}
	var dispatch struct {
		Attempts int `json:"attempts"`
	}
	if b, e := os.ReadFile(q.DispatchRecord); e != nil || json.Unmarshal(b, &dispatch) != nil || dispatch.Attempts != 1 {
		t.Fatal("single native invocation evidence missing", e)
	}
	audit.mu.Lock()
	reads, lists := audit.Calls["/v1/actions/read"], audit.Calls["/v1/actions/list"]
	audit.mu.Unlock()
	if reads+lists < 2 {
		t.Fatal("Agent failed to query durable receipt")
	}
	report["status"] = "passed"
	report["message_id"] = messageID
	report["native_cli_post_invocations"] = 1
	report["standalone_units"] = 0
	report["service_reload"] = true
	report["same_id_recovery"] = true
	fmt.Println("real user Agent -> managed actions -> exact group send -> independent receipt -> service reload/no resend passed")
}
