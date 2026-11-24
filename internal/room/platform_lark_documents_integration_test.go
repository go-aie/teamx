//go:build integration || e2e

package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-aie/teamx/internal/platform/lark"
	"github.com/go-aie/teamx/internal/platform/tool"
)

func documentToolHarness(t *testing.T) *larkHarness {
	t.Helper()
	h := newLarkHarness(t)
	branch := `
import time
mode=open(base+'/doc-mode').read()
if a==['auth','scopes','--json']:
 print(json.dumps({'appId':'cli_fixture','tokenType':'user','userScopes':[] if mode=='app-missing' else ['docx:document:readonly']}));sys.exit()
if a[:2]==['auth','check']:
 assert a==['auth','check','--scope','docx:document:readonly','--json']
 missing=['docx:document:readonly'] if mode=='user-missing' else []
 print(json.dumps({'ok':not missing,'granted':[] if missing else ['docx:document:readonly'],'missing':missing}));sys.exit(1 if missing else 0)
if a[:2]==['docs','+fetch']:
 assert a==['docs','+fetch','--doc','https://example.feishu.cn/wiki/Node','--doc-format','markdown','--detail','simple','--scope','full','--as','user','--format','json']
 open(base+'/doc-read','w').write('yes')
 if mode=='block':time.sleep(60)
 if mode=='denied':
  print(json.dumps({'error':{'type':'authorization','message':'private-secret-body'}}),file=sys.stderr);sys.exit(1)
 print(json.dumps({'ok':True,'identity':'user','data':{'document':{'document_id':'Doc','content':'document-body-marker','reference_map':{'comments':{'c1':{'data':'original comment'}}}}}}));sys.exit()
`
	script := strings.Replace(larkFixtureScript(), "path=a[2]", branch+"\npath=a[2]", 1)
	if err := os.WriteFile(filepath.Join(h.dir, "lark-cli"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "doc-mode"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	bindCLITool(t, h.m, h.room.RoomIdentity, h.members[0], h.c, "lark", 0)
	return h
}

func TestLarkDocumentToolReadsURLWithoutSavedReferences(t *testing.T) {
	h := documentToolHarness(t)
	client, endpoint := toolPipeServer(t, h.m)
	env := cliToolEnv(endpoint, h.svc, h.members[0])
	var stdout, stderr bytes.Buffer
	err := runToolsWithClient(t.Context(), []string{"run", h.c.ToolEntryID, "docs", "fetch", "--url", "https://example.feishu.cn/wiki/Node#share-part"}, bytes.NewReader(nil), &stdout, &stderr, env, client)
	var result lark.DocumentResult
	if err != nil || json.Unmarshal(stdout.Bytes(), &result) != nil || !result.Complete || !strings.Contains(string(result.Data), "document-body-marker") || !strings.Contains(string(result.Data), "original comment") {
		t.Fatal(err, stdout.String(), stderr.String())
	}
	if h.m.platformCalls.Len() != 0 {
		t.Fatal("document call did not settle")
	}
	// Repair fields and the exact missing scope must survive HTTP -> CLI errors.
	for _, tc := range []struct{ mode, next string }{{"app-missing", "configure_application"}, {"user-missing", "authorize_user"}} {
		if err := os.Remove(filepath.Join(h.dir, "doc-read")); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.dir, "doc-mode"), []byte(tc.mode), 0600); err != nil {
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		err = runToolsWithClient(t.Context(), []string{"run", h.c.ToolEntryID, "docs", "fetch", "--url", "https://example.feishu.cn/wiki/Node"}, bytes.NewReader(nil), &stdout, &stderr, env, client)
		if err == nil || !strings.Contains(err.Error(), tc.next) || !strings.Contains(err.Error(), lark.DocumentReadScopes) || stdout.Len() != 0 {
			t.Fatal("document repair lost in CLI", err)
		}
		if _, err := os.Stat(filepath.Join(h.dir, "doc-read")); !os.IsNotExist(err) {
			t.Fatal("scope gap dispatched remote fetch")
		}
	}
	// Denial concerns this document only, without pausing the messaging account.
	if err := os.WriteFile(filepath.Join(h.dir, "doc-mode"), []byte("denied"), 0600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	err = runToolsWithClient(t.Context(), []string{"run", h.c.ToolEntryID, "docs", "fetch", "--url", "https://example.feishu.cn/wiki/Node"}, bytes.NewReader(nil), &stdout, &stderr, env, client)
	if err == nil || strings.Contains(stdout.String()+stderr.String(), "document-body-marker") || !strings.Contains(err.Error(), "document_access_denied") || !strings.Contains(err.Error(), "check_document_access") {
		t.Fatal(err, stdout.String(), stderr.String())
	}
	for _, p := range platformTestList(t, h.m) {
		if p.ID == h.c.ID && p.Validation.Status != "valid" {
			t.Fatal("document access denial paused messages")
		}
	}
	if _, err := h.m.readLark(t.Context(), larkReadRequest{ServiceInfo: h.svc.info, Tool: h.c.ToolEntryID, Caller: agentCaller(h.members[0].AgentID, h.members[0].MembershipID), MessageID: "om_000"}); err != nil {
		t.Fatal("message read lost after document denial", err)
	}
}

func TestLarkDocumentToolRejectsUnboundMembersAndRevokesInFlightRead(t *testing.T) {
	h := documentToolHarness(t)
	client, endpoint := toolPipeServer(t, h.m)
	var stdout, stderr bytes.Buffer
	args := []string{"run", h.c.ToolEntryID, "docs", "fetch", "--url", "https://example.feishu.cn/wiki/Node"}
	if err := runToolsWithClient(t.Context(), args, bytes.NewReader(nil), &stdout, &stderr, cliToolEnv(endpoint, h.svc, h.members[1]), client); err == nil {
		t.Fatal("borrowed another member's account")
	}
	if _, err := os.Stat(filepath.Join(h.dir, "doc-read")); !os.IsNotExist(err) {
		t.Fatal("unbound member dispatched CLI read")
	}
	if err := os.WriteFile(filepath.Join(h.dir, "doc-mode"), []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		var out, stderr bytes.Buffer
		err := runToolsWithClient(t.Context(), args, bytes.NewReader(nil), &out, &stderr, cliToolEnv(endpoint, h.svc, h.members[0]), client)
		if strings.Contains(out.String(), "document-body-marker") {
			err = nil
		}
		done <- err
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(h.dir, "doc-read")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("document read did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	binding := tool.Binding{AgentID: h.members[0].AgentID, MembershipID: h.members[0].MembershipID, Tool: "lark", PlatformID: h.c.ID}
	h.m.mu.Lock()
	f, err := h.m.readToolBindingsLocked(h.room.RoomIdentity)
	h.m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	rev := f.TargetRevisions[toolBindingKey(binding)]
	r := managerHTTP(h.m, "/v1/tools/bind", toolBindingChange{ServiceEpoch: h.m.epoch, RoomIdentity: h.room.RoomIdentity, Caller: humanCaller, ExpectedBindingRevision: &rev, Binding: binding})
	if r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revoked document escaped")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not join document process")
	}
	if h.m.platformCalls.Len() != 0 {
		t.Fatal("revoked call remained registered")
	}
}
