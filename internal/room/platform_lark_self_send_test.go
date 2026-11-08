package room

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

// Native CLI fixture; the manager, pinned account, settings, CLI protocol,
// Inspect/Execute and authoritative ledger all use the production implementation.
func selfSendHarness(t *testing.T) *larkHarness {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("fixed-account user dispatch requires macOS")
	}
	h := newLarkHarness(t)
	script := strings.Replace(l2Fixture, "path=a[2]", `if a==['config','show']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','dpopMode':'required'}));sys.exit()
if a==['auth','scopes','--json']:
 print(json.dumps({'appId':'cli_fixture','userScopes':['im:message.send_as_user','im:message'],'tokenType':'user'}));sys.exit()
if a[:2]==['auth','check']:
 print(json.dumps({'ok':True,'granted':['im:message.send_as_user','im:message'],'missing':[]}));sys.exit()
if a[:3]==['api','POST','/open-apis/im/v1/messages']:
 assert os.environ.get('LARKSUITE_CLI_CONFIG_DIR')
 assert json.loads(a[4])=={'receive_id_type':'open_id'}
 payload=json.loads(a[6]);assert payload['receive_id']=='ou_reader'
 with open(base+'/posts','a') as f:f.write(payload['uuid']+'\n')
 if os.path.exists(base+'/unknown'):sys.exit(1)
 m={'message_id':'om_receipt','chat_id':'oc_chat','msg_type':'text','create_time':'100000','body':{'content':payload['content']},'sender':{'id':'ou_reader','id_type':'open_id','sender_type':'user','tenant_key':'account_tenant'}}
 json.dump([m],open(base+'/messages.json','w'))
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
path=a[2]`, 1)
	script = strings.Replace(script, "{'member_id':'ou_reader','member_id_type':'open_id','tenant_key':'account_tenant'},{'member_id':'ou_sender','member_id_type':'open_id','tenant_key':'external_tenant'}", "{'member_id':'ou_reader','member_id_type':'open_id','tenant_key':'account_tenant'}", 1)
	script = strings.ReplaceAll(script, "'tenant_key':'external_tenant'", "'tenant_key':'account_tenant'")
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return h
}
func TestLarkSelfSendManagedCapability(t *testing.T) {
	h := selfSendHarness(t)
	id := MemberIdentity{RoomIdentity: h.room.RoomIdentity, AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID}
	post, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "task", Text: "把指定内容发到我的飞书"})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: post.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: post.Seq, OfferedUnreadSeq: post.Seq}}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(h.m)
	defer server.Close()
	cli := contractCLI(t, server.URL, h.svc, id)
	resolve := func() actionTargetView {
		t.Helper()
		var v actionTargetView
		if err := cli([]string{"actions", "resolve", "--request", `{"provider":"lark","locator":{"self":true},"turn_number":1}`}, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	bindings := bindCLITool(t, h.m, id.RoomIdentity, h.members[0], h.c, "lark", 0)
	v := resolve()
	if len(v.Actions) != 1 || v.Actions[0].Executable {
		t.Fatal("read binding acquired self send", v)
	}
	grant := toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, Revision: bindings.Revision, Enabled: true, ConnectionVersion: h.c.Version, Binding: tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", ConnectionID: h.c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.send_self", TargetKind: "object", ObjectType: "user"}}}}}
	r := managerHTTP(h.m, "/v1/tools/bind", grant)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if err := json.Unmarshal(r.Body.Bytes(), &bindings); err != nil {
		t.Fatal(err)
	}
	if bindings.Bindings[0].Evidence("self_delivery") == nil {
		t.Fatal("missing exact self evidence")
	}
	v = resolve()
	if !v.Resolution.AccountOwnerConfirmed || len(v.Actions) != 1 || !v.Actions[0].Executable {
		t.Fatal(v)
	}
	q := agentActionRequest{followupCommand: followupCommand{TurnNumber: 1}, TargetRef: v.TargetRef, ID: "self-task-1", Operation: platform.OperationMessageSendSelf, Input: ActionInput{Body: "保存准确内容"}}
	run := func(q agentActionRequest) (referenceActionReceipt, error) {
		b, _ := json.Marshal(q)
		var r referenceActionReceipt
		err := cli([]string{"actions", "execute", "--request", string(b)}, &r)
		return r, err
	}
	// No policy/authority choice is necessary; supplying one is rejected.
	bad := q
	bad.Authority = agentActionAuthority{Type: "human_reply", RoomSeq: 1, Reason: "invented"}
	if _, err := run(bad); err == nil {
		t.Fatal("self accepted invented authority")
	}
	receipt, err := run(q)
	if err != nil || receipt.Status != "succeeded" || receipt.Attempts != 1 {
		t.Fatal(receipt, err)
	}
	if again, err := run(q); err != nil || again.Status != "succeeded" || again.Result.ResultID != receipt.Result.ResultID {
		t.Fatal(again, err)
	}
	records, err := h.svc.store.queryActions(id, q.ID, "", "")
	if err != nil || len(records) != 1 || records[0].CapabilityBasis == nil || records[0].Rule != nil || records[0].Basis != nil || records[0].Access.ActionAuthorizationVersion == 0 {
		t.Fatal(records, err)
	}
	// Preserve old persisted receipts without retaining an execution special case.
	encoded, _ := json.Marshal(records[0])
	var legacy map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &legacy)
	legacy["self_delivery"] = legacy["capability_basis"]
	delete(legacy, "capability_basis")
	var oldSpec map[string]json.RawMessage
	_ = json.Unmarshal(legacy["spec"], &oldSpec)
	delete(oldSpec, "binding_evidence")
	legacy["spec"], _ = json.Marshal(oldSpec)
	encoded, _ = json.Marshal(legacy)
	var restored referenceAction
	if err := json.Unmarshal(encoded, &restored); err != nil || !validActionBasis(restored) || restored.IdempotencyKey != records[0].IdempotencyKey || restored.Result.ResultID != records[0].Result.ResultID {
		t.Fatal("historical action basis/receipt was changed", restored, err)
	}
	bad = q
	bad.Input.Body = "changed"
	if _, err := run(bad); err == nil {
		t.Fatal("same intent accepted changed body")
	}
	bad = q
	bad.ID = "ordinary-send"
	bad.Operation = platform.OperationMessageSend
	if _, err := run(bad); err == nil {
		t.Fatal("ordinary user send gate bypassed")
	}
	// A lost POST response stays unknown; changing ID cannot resend that content.
	if err := os.WriteFile(filepath.Join(h.dir, "unknown"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	unknown := q
	unknown.ID = "self-unknown"
	unknown.Input.Body = "unknown body"
	if r, err := run(unknown); err != nil || r.Status != "unknown" {
		t.Fatal(r, err)
	}
	unknown.ID = "self-unknown-new-id"
	if _, err := run(unknown); err == nil {
		t.Fatal("unknown replayed by new ID")
	}
	// Switching operation/target/authority cannot bypass an unknown self write.
	policy, e := h.svc.store.readAgentPolicy(id)
	if e != nil {
		t.Fatal(e)
	}
	group := platform.TargetIdentity{IdentityScope: records[0].Access.Account.IdentityScope, Object: platform.NativeIdentity{Type: "chat", ID: platform.ExternalID{Namespace: "lark:chat_id:cli_fixture", Value: "oc_group"}}}
	alternate := referenceActionRequest{followupCommand: followupCommand{TurnNumber: 1}, ID: "unknown-alternate-operation", TargetRef: targetReference(id, group), Operation: platform.OperationMessageSend, Input: unknown.Input, PolicyToken: policy.PolicyToken, Authority: referenceActionAuthority{RoomSeq: post.Seq, Reason: "another operation"}}
	spec := lark.SendActions(nil, nil, "")[platform.OperationMessageSend].Spec
	_, started, e := h.svc.store.beginReferenceAction(id, alternate, platform.TargetActionRequest{Target: group}, records[0].Access, platform.TargetActionInspection{Target: group, Account: records[0].Access.Account}, spec)
	if e == nil || started {
		t.Fatal("unknown self content escaped through another operation", e)
	}
	// Disabling the capability stops new sends; original receipts remain readable.
	grant.Revision = bindings.Revision
	grant.Binding.ActionAuthorization = nil
	r = managerHTTP(h.m, "/v1/tools/bind", grant)
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	bad = q
	bad.ID = "after-disable"
	bad.Input.Body = "new task"
	if _, err := run(bad); err == nil {
		t.Fatal("disabled capability sent")
	}
	if r, err := run(q); err != nil || r.Status != "succeeded" {
		t.Fatal("receipt unavailable after disable", r, err)
	}
	h.start()
	if err := h.deliver(); err != nil {
		t.Fatal(err)
	}
	if page := inboxRoutingPreview(t, h.svc, id); page.SnapshotSeq != 0 {
		t.Fatal("self receipt reentered Inbox", page)
	}
	posts, err := os.ReadFile(filepath.Join(h.dir, "posts"))
	if err != nil || len(strings.Fields(string(posts))) != 2 {
		t.Fatal("unexpected native dispatches", string(posts), err)
	}
}
