//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-aie/teamx/internal/platform"
)

func TestLarkActionAgentContract(t *testing.T) {
	h, id, server, cli, refs := imageSendHarness(t)
	env := cliToolEnv(server.URL, h.svc, h.members[0])
	entry := h.c.ToolEntryID
	// Both entry points expose the same contracts, including stdin JSON, without
	// native processes, image preparation or permission repair on help requests.
	for _, op := range []string{"resolve", "execute"} {
		var root, managed bytes.Buffer
		if err := runCLI(t.Context(), []string{"actions", op, "--help"}, &root, &bytes.Buffer{}, env); err != nil {
			t.Fatal(err)
		}
		if err := runCLI(t.Context(), []string{"tools", "run", entry, "actions", op, "--help"}, &managed, &bytes.Buffer{}, env); err != nil {
			t.Fatal(err)
		}
		if managed.String() != builtinActionHelpText(entry, op) || !strings.Contains(managed.String(), referenceCommandHelps["actions/"+op].parameters) || strings.Contains(managed.String(), "--tool ENTRY") {
			t.Fatal("entry help differs", op)
		}
	}
	for _, payload := range []string{
		`{"locator":{"candidate":{"target":{"secret":"do-not-echo"}}},"turn_number":1}`,
		`{"locator":{"candidate":{"namespace":"do-not-echo"}},"turn_number":1}`,
		`{"locator":{"query":{"text":"group"}},"turn_number":1}`,
		`{"locator":{"query":{"object_type":"group","text":"group"}},"turn_number":1}`,
		`{"locator":{"self":true}}`,
	} {
		var diagnostics []string
		for _, args := range [][]string{{"actions", "resolve", "--tool", entry, "--request", payload}, {"tools", "run", entry, "actions", "resolve", "--request", payload}} {
			err := runCLI(t.Context(), args, &bytes.Buffer{}, &bytes.Buffer{}, env)
			if err == nil || strings.Contains(err.Error(), "do-not-echo") {
				t.Fatal("unsafe/missing diagnostic", err)
			}
			diagnostics = append(diagnostics, err.Error())
		}
		if diagnostics[0] != diagnostics[1] {
			t.Fatal("different field diagnostics", diagnostics)
		}
	}
	var listed any
	if err := cli([]string{"tools", "list", "--json"}, &listed); err != nil {
		t.Fatal("entry discovery JSON flag", err)
	}
	entries := listed.([]any)
	if len(entries) != 1 {
		t.Fatal(listed)
	}
	discovered := entries[0].(map[string]any)
	if discovered["name"] != entry || discovered["skill"] != `"$TEAMX_CLI" tools skill `+entry || discovered["account"] == nil {
		t.Fatal("missing exact entry/Skill/account", listed)
	}
	h.m.mu.Lock()
	bindings, err := h.m.readToolBindingsLocked(id.RoomIdentity)
	if err != nil {
		h.m.mu.Unlock()
		t.Fatal(err)
	}
	original, err := h.m.selectionsLocked(bindings, agentCallerIdentity(id))
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(mustContractJSON(projectToolSelections(original))) >= len(mustContractJSON(original)) {
		t.Fatal("discovery did not shrink")
	}
	t.Logf("tools list response bytes: before=%d after=%d", len(mustContractJSON(original)), len(mustContractJSON(projectToolSelections(original))))
	var stdin bytes.Buffer
	err = runToolsCLI(t.Context(), []string{"run", entry, "actions", "resolve", "--request-file", "-"}, strings.NewReader(`{"locator":{"self":true},"turn_number":1}`), &stdin, &bytes.Buffer{}, env)
	if err != nil {
		t.Fatal(err)
	}
	var resolved map[string]any
	if json.Unmarshal(stdin.Bytes(), &resolved) != nil {
		t.Fatal(stdin.String())
	}
	action := resolved["actions"].([]any)[0].(map[string]any)
	if !reflect.DeepEqual(action["required"], []any{"target_ref", "turn_number", "id", "operation", "input"}) {
		t.Fatal(action)
	}

	// The native query provides duplicate names and an incomplete marker; neither
	// creates an executable target until a locator is selected and reverified.
	path := filepath.Join(h.dir, "lark-cli")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	search := `if a[:2]==['im','+chat-search']:
 print(json.dumps({'ok':True,'identity':'user','data':{'chats':[{'chat_id':'oc_group','chat_mode':'group','name':'准确测试群'},{'chat_id':'oc_other','chat_mode':'group','name':'准确测试群'}],'has_more':True}}));sys.exit()
`
	if err = os.WriteFile(path, []byte(strings.Replace(string(script), "path=a[2]", search+"path=a[2]", 1)), 0700); err != nil {
		t.Fatal(err)
	}
	var query struct {
		TargetRef  string `json:"target_ref"`
		Resolution struct {
			Candidates []agentTargetCandidate `json:"candidates"`
			Incomplete bool                   `json:"candidates_incomplete"`
		} `json:"resolution"`
	}
	if err = cli([]string{"tools", "run", entry, "actions", "resolve", "--request", `{"locator":{"query":{"object_type":"chat","text":"准确测试群"}},"turn_number":1}`}, &query); err != nil {
		t.Fatal(err)
	}
	if query.TargetRef != "" || len(query.Resolution.Candidates) != 2 || !query.Resolution.Incomplete || query.Resolution.Candidates[0].Label != query.Resolution.Candidates[1].Label {
		t.Fatal(query)
	}
	post, err := h.svc.room.Call[PostReceipt](t.Context(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "selected-target", Text: "选 oc_group，把第一张图和说明一起发到此群"})
	if err != nil {
		t.Fatal(err)
	}
	if err = h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 2, LastOfferedUnreadSeq: post.Seq, Turn: &TurnRecord{Number: 2, Status: "running", SnapshotSeq: post.Seq, OfferedUnreadSeq: post.Seq}}); err != nil {
		t.Fatal(err)
	}
	selected := actionResolveRequest{Locator: query.Resolution.Candidates[0].Locator, TurnNumber: 2}
	var target struct {
		TargetRef string                `json:"target_ref"`
		Actions   []agentSendCapability `json:"actions"`
	}
	if err = cli([]string{"tools", "run", entry, "actions", "resolve", "--request", string(mustContractJSON(selected))}, &target); err != nil || target.TargetRef == "" || len(target.Actions) != 1 || !target.Actions[0].Executable {
		t.Fatal(target, err)
	}
	if !reflect.DeepEqual(target.Actions[0].Required, []string{"target_ref", "turn_number", "id", "operation", "input", "policy_token", "authority"}) {
		t.Fatal(target)
	}
	// A copied locator is still untrusted; altered tenant or unavailable group is rejected.
	for _, change := range []func(*platform.TargetIdentity){func(x *platform.TargetIdentity) { x.Tenant.Value = "another-tenant" }, func(x *platform.TargetIdentity) { x.Object.ID.Value = "oc_other" }} {
		altered := *selected.Locator.Candidate
		change(&altered)
		var denied actionTargetView
		q := actionResolveRequest{Locator: platform.TargetLocator{Candidate: &altered}, TurnNumber: 2}
		err = cli([]string{"tools", "run", entry, "actions", "resolve", "--request", string(mustContractJSON(q))}, &denied)
		if err == nil && denied.TargetRef != "" {
			t.Fatal("altered candidate executed", denied)
		}
	}
	policy, err := h.svc.store.readAgentPolicy(id)
	if err != nil {
		t.Fatal(err)
	}
	var input ActionInput
	if err = json.Unmarshal(mustContractJSON(map[string]any{"body": "完整说明", "images": refs[:2]}), &input); err != nil {
		t.Fatal(err)
	}
	q := agentActionRequest{followupCommand: followupCommand{TurnNumber: 2}, TargetRef: target.TargetRef, ID: "selected-group-images", PolicyToken: policy.PolicyToken, Operation: platform.OperationMessageSend, Authority: agentActionAuthority{Type: "human_reply", RoomSeq: post.Seq, Reason: "按用户选择发送完整图文"}, Input: input}
	for _, raw := range []string{`{"images":[{"message_seq":1,"attachment_id":"img_0123456789abcdef0123456789abcdef","path":"private-value"}]}`, `{"images":[{"message_seq":"private-value","attachment_id":"img_0123456789abcdef0123456789abcdef"}]}`} {
		malformed := q
		if err = json.Unmarshal([]byte(raw), &malformed.Input); err != nil {
			t.Fatal(err)
		}
		args := []string{"tools", "run", entry, "actions", "execute", "--request", string(mustContractJSON(malformed))}
		var out any
		err = cli(args, &out)
		if err == nil || !strings.Contains(err.Error(), "input.images[0].") || strings.Contains(err.Error(), "private-value") {
			t.Fatal("image field diagnostic", err)
		}
	}
	var receipt struct {
		ID, Status string
		Input      ActionInput
		Result     agentSendResult
		Authority  agentActionAuthority
	}
	if err = cli([]string{"tools", "run", entry, "actions", "execute", "--request", string(mustContractJSON(q))}, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "succeeded" || !receipt.Result.Verified || receipt.Result.Account.ID.Value != "ou_reader" || receipt.Result.Target.Object.ID.Value != "oc_group" || receipt.Result.ResultID.Value != "om_receipt" || !reflect.DeepEqual(actionInputJSON(receipt.Input), actionInputJSON(q.Input)) || receipt.Authority.RoomSeq != post.Seq {
		t.Fatal(receipt)
	}
	if len(selfSendPosts(t, h.dir)) != 1 {
		t.Fatal("split message")
	}
	view, err := h.m.resolveActionTarget(t.Context(), apiRequest{ServiceInfo: h.svc.info, Caller: agentCallerIdentity(id), Tool: entry}, selected)
	if err != nil {
		t.Fatal(err)
	}
	internal, err := h.svc.store.agentAction(id, q)
	if err != nil {
		t.Fatal(err)
	}
	oldReceipt, err := h.svc.store.existingReferenceAction(id, internal)
	if err != nil || oldReceipt == nil {
		t.Fatal(err)
	}
	before := len(mustContractJSON(view)) + len(mustContractJSON(oldReceipt))
	after := len(mustContractJSON(projectActionTarget(view))) + len(mustContractJSON(projectActionReceipt(*oldReceipt, internal)))
	t.Logf("group resolve + execute normal response bytes: before=%d after=%d", before, after)
	if after >= before {
		t.Fatal("normal response did not shrink")
	}
	// The compact response does not alter storage, and the original read path
	// restores complete frozen identities, evidence and image order after reopen.
	if err = h.svc.store.Close(); err != nil {
		t.Fatal(err)
	}
	h.svc.store, err = OpenStore(h.svc.store.dir, id.RoomID)
	if err != nil {
		t.Fatal(err)
	}
	var old actionQueryRecord
	if err = cli([]string{"actions", "read", "--id", q.ID}, &old); err != nil || old.Detail == nil || !old.Detail.Target.Valid() || !reflect.DeepEqual(actionInputJSON(old.Detail.Request.Input), actionInputJSON(q.Input)) || old.Detail.Basis.Source.Seq != post.Seq {
		t.Fatal(old, err)
	}
	if len(selfSendPosts(t, h.dir)) != 1 {
		t.Fatal("receipt lookup resent")
	}
}
