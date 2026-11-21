//go:build integration || e2e

package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform"
	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

const imageSendCLI = `
if a[:3]==['im','images','create']:
 import glob,hashlib,time
 assert os.environ.get('LARKSUITE_CLI_CONFIG_DIR')
 assert a[3:5]==['--data','{"image_type":"message"}'] and a[7:]==['--as','user','--format','json']
 rel=a[6];assert a[5]=='--file' and rel.startswith('image=./image-') and rel.endswith('.png')
 name=rel[len('image=./'):];assert '/' not in name
 data=open(name,'rb').read()
 records=[json.load(open(p))['action'] for p in glob.glob(open(base+'/ledger').read()+'/records/*.json')]
 active=[r for r in records if r['status']=='unknown' and r['attempts']==1]
 assert len(active)==1 and len(active[0]['inspection']['content_digest'])==64
 assert 'images' in active[0]['request']['input']
 index=int(name.split('-')[1].split('.')[0])
 ref=active[0]['request']['input']['images'][index-1]
 source_index=int(ref['attachment_id'][4:],16)
 assert data==open(base+'/image-'+str(source_index)+'.png','rb').read()
 with open(base+'/uploads','a') as f:f.write(json.dumps({'cwd':os.getcwd(),'file':name,'hash':hashlib.sha256(data).hexdigest()})+'\n')
 index=int(name.split('-')[1].split('.')[0])
 if os.path.exists(base+'/pause-'+str(index)):
  open(base+'/upload-ready','w').write('yes')
  while not os.path.exists(base+'/release'):time.sleep(.01)
 if os.path.exists(base+'/fail-'+str(index)):sys.exit(1)
 print(json.dumps({'ok':True,'identity':'user','data':{'image_key':'img_'+hashlib.sha256(data).hexdigest()}}));sys.exit()
if a[:3]==['api','POST','/open-apis/im/v1/messages']:
 assert a[7:]==['--as','user','--format','json']
 payload=json.loads(a[6]);params=json.loads(a[4]);assert len(payload['uuid'])==32
 assert set(payload)=={'receive_id','msg_type','content','uuid'}
 assert params=={'receive_id_type':'open_id' if payload['receive_id']=='ou_reader' else 'chat_id'}
 assert payload['receive_id'] in ['ou_reader','oc_group']
 assert payload['msg_type'] in ['text','image','post']
 with open(base+'/posts','a') as f:f.write(payload['uuid']+'\n')
 with open(base+'/payloads','a') as f:f.write(json.dumps(payload)+'\n')
 if os.path.exists(base+'/unknown'):sys.exit(1)
 native_content=json.loads(payload['content'])
 if payload['msg_type']=='post':
  native_content=native_content['zh_cn']
  for row in native_content['content']:
   for node in row:
    if node['tag']=='img':node.update({'width':256,'height':256})
    elif node['tag']=='text':node['style']=[]
  native_content['content_v2']=json.loads(json.dumps(native_content['content']))
 m={'message_id':'om_receipt','chat_id':'oc_chat' if payload['receive_id']=='ou_reader' else 'oc_group','msg_type':payload['msg_type'],'create_time':'100000','body':{'content':json.dumps(native_content)},'sender':{'id':'ou_reader','id_type':'open_id','sender_type':'user','tenant_key':'account_tenant'}}
 if os.path.exists(base+'/bad-read'):m['body']['content']='{}'
 json.dump([m],open(base+'/messages.json','w'))
 print(json.dumps({'ok':True,'identity':'user','data':{'message_id':'om_receipt'}}));sys.exit()
`

func imageSendHarness(t *testing.T, inputLog ...bool) (*larkHarness, MemberIdentity, *httptest.Server, func([]string, any) error, []lark.ImageRef) {
	t.Helper()
	h := selfSendHarnessWithInputLog(t, len(inputLog) > 0 && inputLog[0])
	return configureImageSendHarness(t, h)
}

func configureImageSendHarness(t *testing.T, h *larkHarness) (*larkHarness, MemberIdentity, *httptest.Server, func([]string, any) error, []lark.ImageRef) {
	t.Helper()
	script, err := os.ReadFile(filepath.Join(h.dir, "lark-cli"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(script)
	start := strings.Index(text, "if a[:3]==['api','POST','/open-apis/im/v1/messages']:")
	end := strings.Index(text[start:], "path=a[2]") + start
	text = text[:start] + imageSendCLI + text[end:]
	text = strings.Replace(text, "'contact:user:search']", "'contact:user:search','im:chat:read','im:message:readonly','im:message.p2p_msg:get_as_user','im:message.group_msg:get_as_user','contact:user.basic_profile:readonly','im:resource']", 1)
	text = strings.Replace(text, "path=a[2]", `if a==['auth','status','--json']:
 print(json.dumps({'appId':'cli_fixture','identities':{'user':{'openId':'ou_reader','tokenStatus':'valid','refreshExpiresAt':'2099-01-01T00:00:00Z'}}}));sys.exit()
path=a[2]`, 1)
	text = strings.Replace(text, "elif path.startswith('/open-apis/im/v1/chats/'):data=", "elif path=='/open-apis/im/v1/chats/oc_group':data={'chat_mode':'group','tenant_key':'account_tenant','name':'准确测试群','chat_status':'normal','external':False,'group_message_type':'chat','moderation_permission':'all_members'}\nelif path.startswith('/open-apis/im/v1/chats/'):data=", 1)
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	id, bindings := enableSelfSend(t, h, h.members[0], 0)
	// Both target grants stay independent; no image operation or new business grant.
	b := tool.Binding{AgentID: id.AgentID, MembershipID: id.MembershipID, Tool: "lark", PlatformID: h.c.ID, ActionAuthorization: &tool.ActionAuthorization{Version: 1, Revision: 1, Grants: []tool.ActionGrant{{Operation: "message.send_self", TargetKind: "object", ObjectType: "user"}}}}
	b.ActionAuthorization.Grants = append(b.ActionAuthorization.Grants, tool.ActionGrant{Operation: "message.send", TargetKind: "object", ObjectType: "chat"})
	r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: id.RoomIdentity, Caller: humanCaller, ExpectedBindingRevision: new(bindings.Revision), Enabled: true, PlatformVersion: h.c.Version, Binding: b})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	server := httptest.NewServer(h.m)
	t.Cleanup(server.Close)
	cli := contractCLI(t, server.URL, h.svc, id, h.c.ToolEntryID)
	var refs []lark.ImageRef
	for i := range 4 {
		pixels := attachmentTestPNG(t, i%2 == 0)
		name := fmt.Sprintf("image-%d.png", i+1)
		if err := os.WriteFile(filepath.Join(h.dir, name), pixels, 0600); err != nil {
			t.Fatal(err)
		}
		imageID := attachmentTestID(i + 1)
		status, body := attachmentUploadHTTP(t, server, h.svc.info, imageID, pixels, false)
		if status != 200 {
			t.Fatal(status, string(body))
		}
		status, body = attachmentJSON(t, server, "/v1/send", apiRequest{ServiceInfo: h.svc.info, Caller: Caller{Author: Author{"human", humanOperator()}}, ClientMessageID: imageID, Attachments: []RoomAttachmentInput{{ID: imageID}}})
		if status != 200 {
			t.Fatal(status, string(body))
		}
		var receipt PostReceipt
		if err := json.Unmarshal(body, &receipt); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, lark.ImageRef{MessageSeq: receipt.Seq, AttachmentID: imageID})
	}
	if err := os.WriteFile(filepath.Join(h.dir, "ledger"), []byte(h.svc.store.actionRoot(id)), 0600); err != nil {
		t.Fatal(err)
	}
	return h, id, server, cli, refs
}

func imageActionRequest(t *testing.T, h *larkHarness, id MemberIdentity, cli func([]string, any) error, kind string, refs []lark.ImageRef, body, key string) agentActionRequest {
	t.Helper()
	q := selfSendRequest(t, cli, key)
	var input ActionInput
	raw, _ := json.Marshal(lark.ImageSendInput{Body: body, Images: refs})
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	q.Input = input
	if kind == "group" {
		// Freeze a real direct Room instruction, not a fabricated capability basis.
		receipt, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: key + "-authority", Text: "把指定说明与全部原图一起发到准确测试群"})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.svc.store.SaveAgent(AgentSnapshot{Version: formatVersion, MemberIdentity: id, TurnNumber: 1, LastOfferedUnreadSeq: receipt.Seq, Turn: &TurnRecord{Number: 1, Status: "running", SnapshotSeq: receipt.Seq, OfferedUnreadSeq: receipt.Seq}}); err != nil {
			t.Fatal(err)
		}
		locator := fmt.Sprintf(`{"provider":"lark","locator":{"candidate":{"provider":"lark","instance":"https://open.feishu.cn","tenant":{"namespace":"lark:tenant_key","value":"account_tenant"},"object":{"type":"chat","id":{"namespace":"lark:chat_id:cli_fixture","value":"oc_group"}}}},"turn_number":1}`)
		var target actionTargetView
		if err := cli([]string{"actions", "resolve", "--request", locator}, &target); err != nil {
			t.Fatal(err)
		}
		policy, err := h.svc.store.readAgentPolicy(id)
		if err != nil {
			t.Fatal(err)
		}
		q.TargetRef = target.TargetRef
		q.Operation = platform.OperationMessageSend
		q.PolicyToken = policy.PolicyToken
		q.Authority = agentActionAuthority{Type: "human_reply", RoomSeq: receipt.Seq, Reason: "按直接 Room 指令向准确测试群发送完整内容"}
	}
	return q
}

func assertImageTempCleaned(t *testing.T, h *larkHarness) {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(h.dir, "uploads"))
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var item struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal([]byte(line), &item) != nil {
			t.Fatal(line)
		}
		if _, err := os.Stat(item.Cwd); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("temporary image files retained", item.Cwd, err)
		}
	}
	assertActionImagePinsReleased(t, h.svc.store)
}

func TestLarkImagesManagedCLI(t *testing.T) {
	for _, kind := range []string{"self", "group"} {
		for _, mode := range []string{"single", "multiple", "caption", "fail-first", "fail-second", "fail-third", "fail-fourth", "unknown", "bad-read"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				parallelFixtureTest(t)
				h, id, _, cli, refs := imageSendHarness(t)
				originals := refs
				body := ""
				if mode == "single" {
					refs = refs[:1]
				}
				if mode == "unknown" {
					refs = refs[:2]
				}
				if mode == "caption" {
					body = "\n准确说明\n\n中文 **Markdown** @人 ./file\n"
				}
				q := imageActionRequest(t, h, id, cli, kind, refs, body, "complete-image-message")
				marker := map[string]string{"fail-first": "fail-1", "fail-second": "fail-2", "fail-third": "fail-3", "fail-fourth": "fail-4", "unknown": "unknown", "bad-read": "bad-read"}[mode]
				if marker != "" {
					if err := os.WriteFile(filepath.Join(h.dir, marker), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				// Native read/consumption state must not be changed by the action.
				before := historyTree(t, h.svc.store.dir)
				result, err := executeSelfSend(cli, q)
				want := "succeeded"
				if strings.HasPrefix(mode, "fail-") {
					want = "not_applied"
				}
				if mode == "unknown" || mode == "bad-read" {
					want = "unknown"
				}
				if err != nil || result.Status != want || result.Attempts != 1 {
					t.Fatal(result, err)
				}
				posts := selfSendPosts(t, h.dir)
				expected := 1
				if strings.HasPrefix(mode, "fail-") {
					expected = 0
				}
				if len(posts) != expected {
					t.Fatal("partial/duplicate message", posts)
				}
				records, err := h.svc.store.queryActions(id, q.ID, "", "")
				if err != nil || len(records) != 1 || records[0].Inspection.ContentDigest == "" || records[0].Request.Input != q.Input {
					t.Fatal(records, err)
				}
				if mode == "bad-read" && (result.Result == nil || !result.Result.ResultID.Valid()) {
					t.Fatal("known ID lost")
				}
				again, err := executeSelfSend(cli, q)
				if err != nil || again.Status != want || len(selfSendPosts(t, h.dir)) != expected {
					t.Fatal("same ID replayed", again, err)
				}
				bad := q
				bad.Input.Body = "changed"
				bad.Input.raw = ""
				if _, err := executeSelfSend(cli, bad); err == nil {
					t.Fatal("same ID changed content")
				}
				for _, selection := range [][]lark.ImageRef{originals[1:2], {originals[1], originals[0]}, originals[:3]} {
					changed := q
					raw, _ := json.Marshal(lark.ImageSendInput{Body: body, Images: selection})
					_ = json.Unmarshal(raw, &changed.Input)
					if changed.Input != q.Input {
						if _, err := executeSelfSend(cli, changed); err == nil {
							t.Fatal("same ID changed complete images")
						}
					}
				}
				if want == "unknown" {
					bad = q
					bad.ID = "same-content-another-id"
					if _, err := executeSelfSend(cli, bad); err == nil {
						t.Fatal("unknown resent")
					}
					if mode == "unknown" {
						replacement, _ := json.Marshal(lark.ImageSendInput{Images: originals[2:]})
						_ = json.Unmarshal(replacement, &bad.Input)
						if _, err := executeSelfSend(cli, bad); err == nil {
							t.Fatal("unknown escaped through equivalent source references")
						}
					}
				}
				after := historyTree(t, h.svc.store.dir)
				for path, hash := range before {
					if strings.Contains(path, "/actions/") {
						continue
					}
					if after[path] != hash {
						t.Errorf("action changed Room/input/history state: %s", path)
					}
				}
				// No non-action durable files may be introduced.
				for path := range after {
					if _, exists := before[path]; !exists && !strings.Contains(path, "/actions/") {
						t.Errorf("new non-action state: %s", path)
					}
				}
				if mode == "caption" {
					if err := os.Remove(filepath.Join(h.svc.store.attachmentPath(refs[0].AttachmentID), "data")); err != nil {
						t.Fatal(err)
					}
					if prior, err := executeSelfSend(cli, q); err != nil || prior.Status != "succeeded" {
						t.Fatal("original receipt depended on source lifetime", prior, err)
					}
					fresh := q
					fresh.ID = "missing-original-no-cache-fallback"
					if _, err := executeSelfSend(cli, fresh); err == nil {
						t.Fatal("new action used cached/missing original")
					}
					if len(selfSendPosts(t, h.dir)) != expected {
						t.Fatal("receipt lookup or missing source dispatched")
					}
				}
				assertImageTempCleaned(t, h)
			})
		}
	}
}

func TestLarkImagesOptionalPermission(t *testing.T) {
	h, id, _, cli, refs := imageSendHarness(t)
	script, _ := os.ReadFile(filepath.Join(h.dir, "lark-cli"))
	text := strings.Replace(string(script), ",'im:resource']", "]", 1)
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(text), 0700); err != nil {
		t.Fatal(err)
	}
	q := imageActionRequest(t, h, id, cli, "self", refs, "", "permission-blocked")
	if _, err := executeSelfSend(cli, q); err == nil || !strings.Contains(err.Error(), "image_upload_application_scopes_missing") {
		t.Fatal("missing upload permission admitted", err)
	}
	if len(selfSendPosts(t, h.dir)) != 0 {
		t.Fatal("image permission failure sent")
	}
	q = selfSendRequest(t, cli, "plain-still-works")
	if r, err := executeSelfSend(cli, q); err != nil || r.Status != "succeeded" {
		t.Fatal(r, err)
	}
	assertActionImagePinsReleased(t, h.svc.store)
}

func TestLarkImagesDispatchGuards(t *testing.T) {
	for _, inputLog := range []bool{false, true} {
		for _, mode := range []string{"concurrent", "cancel", "digest", "input", "policy"} {
			t.Run(fmt.Sprintf("log=%v/%s", inputLog, mode), func(t *testing.T) {
				parallelFixtureTest(t)
				h, id, _, cli, refs := imageSendHarness(t, inputLog)
				kind := "self"
				if mode == "policy" {
					kind = "group"
				}
				q := imageActionRequest(t, h, id, cli, kind, refs[:2], "完整说明", "guarded-image-action")
				if err := os.WriteFile(filepath.Join(h.dir, "pause-2"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.WriteFile(filepath.Join(h.dir, "release"), nil, 0600) })
				type outcome struct {
					r   referenceActionReceipt
					err error
				}
				done := make(chan outcome, 1)
				go func() { r, err := executeSelfSend(cli, q); done <- outcome{r, err} }()
				waitSelfSendFile(t, filepath.Join(h.dir, "upload-ready"))
				if _, err := h.svc.room.Call[ClearReceipt](context.Background(), ClearMessages{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ThroughSeq: refs[1].MessageSeq}); !errors.Is(err, ErrBusy) {
					t.Fatal("clear crossed image lease", err)
				}
				// Neither the Store mutex nor Room actor is held during a child process upload.
				if _, err := h.svc.room.Call[PostReceipt](context.Background(), PostMessage{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ClientMessageID: "while-uploading", Text: "并发追加"}); err != nil {
					t.Fatal(err)
				}
				var record storedAction
				path := h.svc.store.actionPath(id, q.ID)
				if err := readSnapshot(path, &record, followupBytes); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "concurrent":
					repeat, err := executeSelfSend(cli, q)
					if err != nil || repeat.Status != "processing" {
						t.Fatal("concurrent same ID entered adapter", repeat, err)
					}
				case "cancel":
					h.m.mu.Lock()
					h.m.cancelPlatformCallsLocked(func(call *platformCall) bool { return call.owner == id.RoomIdentity && call.agentID == id.AgentID })
					h.m.mu.Unlock()
				case "digest":
					record.Action.Inspection.ContentDigest = strings.Repeat("f", 64)
					h.svc.store.mu.Lock()
					err := h.svc.store.saveActionLocked(id, record.Action)
					h.svc.store.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				case "input":
					var changed map[string]any
					_ = json.Unmarshal(actionInputJSON(record.Action.Request.Input), &changed)
					changed["body"] = "替换后的说明"
					raw, _ := json.Marshal(changed)
					_ = json.Unmarshal(raw, &record.Action.Request.Input)
					h.svc.store.mu.Lock()
					err := h.svc.store.saveActionLocked(id, record.Action)
					h.svc.store.mu.Unlock()
					if err != nil {
						t.Fatal(err)
					}
				case "policy":
					_, err := h.svc.store.changeAgentPolicy(id, agentCallerIdentity(id), agentPolicyChange{Type: "save", ID: "new-policy", TurnNumber: 1, SourceSeq: record.Action.Basis.Source.Seq, Basis: "用户更新长期要求", Body: &agentPolicyBody{Text: "回复前先确认处理人"}})
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(h.dir, "release"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				select {
				case result := <-done:
					want := "not_applied"
					posts := 0
					if mode == "concurrent" {
						want = "succeeded"
						posts = 1
					}
					if result.err != nil || result.r.Status != want || result.r.Attempts != 1 || len(selfSendPosts(t, h.dir)) != posts {
						t.Fatal(result, want, selfSendPosts(t, h.dir))
					}
				case <-time.After(20 * time.Second):
					t.Fatal("upload did not settle")
				}
				assertImageTempCleaned(t, h)
				if _, err := h.svc.room.Call[ClearReceipt](context.Background(), ClearMessages{RoomIdentity: id.RoomIdentity, Caller: humanCaller, ThroughSeq: refs[1].MessageSeq}); err != nil {
					t.Fatal("retention pin survived settlement", err)
				}
			})
		}
	}
}

func TestLarkImagesLedgerFailureReleasesSource(t *testing.T) {
	for _, inputLog := range []bool{false, true} {
		for _, mode := range []string{"intent", "attempt", "unknown-intent"} {
			t.Run(fmt.Sprintf("log=%v/%s", inputLog, mode), func(t *testing.T) {
				parallelFixtureTest(t)
				h, id, _, cli, refs := imageSendHarness(t, inputLog)
				q := imageActionRequest(t, h, id, cli, "self", refs, "", "durable-before-upload")
				store := h.svc.store
				store.mu.Lock()
				ops := store.ops
				var writes int
				pinned := false
				store.ops.createTemp = func(dir, pattern string) (snapshotFile, error) {
					if filepath.Base(dir) == "records" {
						writes++
						pinned = len(store.roomHistoryPins) == 1
						if mode == "intent" && writes == 1 || mode == "attempt" && writes == 2 {
							return nil, errors.New("injected ledger failure")
						}
					}
					return ops.createTemp(dir, pattern)
				}
				if mode == "unknown-intent" {
					store.ops.syncDir = func(path string) error {
						if filepath.Base(path) == "records" {
							return errors.New("injected uncertain persistence")
						}
						return ops.syncDir(path)
					}
				}
				store.mu.Unlock()
				result, err := executeSelfSend(cli, q)
				if mode == "attempt" {
					if err != nil || result.Status != "not_applied" {
						t.Fatal(result, err)
					}
				} else if err == nil {
					t.Fatal("ledger failure hidden", result)
				}
				if !pinned {
					t.Fatal("test failed before source prepared")
				}
				if len(selfSendPosts(t, h.dir)) != 0 {
					t.Fatal("message before durable intent/attempt")
				}
				if data, _ := os.ReadFile(filepath.Join(h.dir, "uploads")); len(data) != 0 {
					t.Fatal("upload before durable intent/attempt")
				}
				assertActionImagePinsReleased(t, store)
			})
		}
	}
}

func TestLarkImagesPermissionRecoveryKeepsTextIndependent(t *testing.T) {
	for _, mode := range []string{"application", "user", "unchecked"} {
		t.Run(mode, func(t *testing.T) {
			parallelFixtureTest(t)
			h, id, _, cli, refs := imageSendHarness(t)
			path := filepath.Join(h.dir, "lark-cli")
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			script := string(b)
			code, hint := "image_upload_application_scopes_missing", "configure_application"
			switch mode {
			case "application":
				script = strings.Replace(script, ",'im:resource']", "]", 1)
			case "user":
				script = strings.Replace(script, "'granted':a[a.index('--scope')+1].split(),'missing':[]", "'granted':[s for s in a[a.index('--scope')+1].split() if s!='im:resource'],'missing':['im:resource']", 1)
				script = strings.Replace(script, "'ok':True,'granted':[s", "'ok':False,'granted':[s", 1)
				code, hint = "image_upload_user_scopes_missing", "authorize_user"
			case "unchecked":
				script = strings.Replace(script, "if a==['auth','scopes','--json']:", "if a==['auth','scopes','--json'] and os.path.exists(base+'/unchecked'):sys.exit(1)\nif a==['auth','scopes','--json']:", 1)
				if err = os.WriteFile(filepath.Join(h.dir, "unchecked"), nil, 0600); err != nil {
					t.Fatal(err)
				}
				code, hint = "image_upload_query_failed", "retry_check"
			}
			if err = os.WriteFile(path, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			q := imageActionRequest(t, h, id, cli, "self", refs[:2], "完整内容", "permission-repair")
			if _, err = executeSelfSend(cli, q); err == nil || !strings.Contains(err.Error(), code) || !strings.Contains(err.Error(), hint) {
				t.Fatal("wrong repair guidance", err, code, hint)
			}
			if len(selfSendPosts(t, h.dir)) != 0 {
				t.Fatal("permission gap sent a message")
			}
			if b, _ := os.ReadFile(filepath.Join(h.dir, "uploads")); len(b) != 0 {
				t.Fatal("permission gap uploaded images")
			}
			// A failed check blocks all unverified writes. For optional scope gaps, text
			// remains usable without changing bindings, grants, accounts or user intent.
			if mode != "unchecked" {
				q = selfSendRequest(t, cli, "text-after-image-gap")
				if r, err := executeSelfSend(cli, q); err != nil || r.Status != "succeeded" {
					t.Fatal(r, err)
				}
			}
			assertActionImagePinsReleased(t, h.svc.store)
		})
	}
}

func TestLarkImagesBuiltinActionFieldDiagnostics(t *testing.T) {
	h, _, _, cli, _ := imageSendHarness(t)
	for _, c := range []struct{ operation, input, field string }{
		{"resolve", `{"locator":{"candidate":{"target":{"private":"do-not-echo"}}},"turn_number":1}`, "locator.candidate.target"},
		{"execute", `{"unexpected":"do-not-echo"}`, "unexpected"},
	} {
		var out any
		err := cli([]string{"tools", "run", h.c.ToolEntryID, "actions", c.operation, "--request", c.input}, &out)
		if err == nil || !strings.Contains(err.Error(), c.field) || strings.Contains(err.Error(), "do-not-echo") {
			t.Fatal("missing safe field diagnostic", c.operation, err)
		}
	}
	if len(selfSendPosts(t, h.dir)) != 0 {
		t.Fatal("malformed request sent")
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, "uploads")); len(b) != 0 {
		t.Fatal("malformed request uploaded")
	}
}
