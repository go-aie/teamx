//go:build integration || e2e

package lark

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// All accepted native operations are reads. Tokens, account/profile selection
// and output shape are checked through the production process transport.
const documentFixture = `#!/usr/bin/env -S python3 -S
import json,os,sys,time
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[3:]
assert sys.argv[1:3]==['--profile','fixture']
mode=open(base+'/mode').read()
with open(base+'/calls','a') as f:f.write(json.dumps(a)+'\n')
if a==['--version']:print('lark-cli version 1.0.99');sys.exit()
user='ou_other' if mode=='switch' and os.path.exists(base+'/read') else 'ou_reader'
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':user}}));sys.exit()
if a[:3]==['api','GET','/open-apis/authen/v1/user_info']:
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':user,'tenant_key':'account_tenant','name':'fixture'}}));sys.exit()
if a==['auth','scopes','--json']:
 if mode=='app-unknown':print('{}');sys.exit()
 print(json.dumps({'appId':'cli_other' if mode=='app-identity' else 'cli_fixture','tokenType':'user','userScopes':[] if mode=='app-missing' else ['docx:document:readonly']}));sys.exit()
if a[:2]==['auth','check']:
 assert a==['auth','check','--scope','docx:document:readonly','--json']
 missing=['docx:document:readonly'] if mode=='user-missing' else []
 print(json.dumps({'ok':not missing,'granted':[] if missing else ['docx:document:readonly'],'missing':missing}));sys.exit(1 if missing else 0)
assert a==['docs','+fetch','--doc','https://example.feishu.cn/'+('wiki/Node' if '/wiki/' in a[3] else 'docx/Doc'),'--doc-format','markdown','--detail','simple','--scope','full','--as','user','--format','json']
open(base+'/read','w').write('yes')
if mode=='block':time.sleep(60)
if mode=='large':print('s'*((2<<20)+1));sys.exit()
if mode in ['denied','network','not-found']:
 print(json.dumps({'error':{'type':{'denied':'authorization','network':'network','not-found':'api'}[mode],'message':'private-secret-body'}}),file=sys.stderr);sys.exit(1)
doc={'document_id':'Doc','revision_id':2,'content':'# 完整正文','reference_map':{'comments':{'c1':{'data':'original-comment'}}}}
if mode=='malformed':del doc['content']
if mode=='tips':doc['tips']='upstream degradation'
print(json.dumps({'ok':True,'identity':'bot' if mode=='response-bot' else 'user','data':{'document':doc}}))
`

func TestDocumentFetchCLIAndAccountBoundary(t *testing.T) {
	for _, path := range []string{"docx/Doc", "wiki/Node"} {
		t.Run(path, func(t *testing.T) {
			c, dir, account := testClient(t)
			if err := os.WriteFile(c.profile.Executable, []byte(documentFixture), 0700); err != nil {
				t.Fatal(err)
			}
			result, err := c.FetchDocument(t.Context(), account, "https://example.feishu.cn/"+path+"?from=share#share-part")
			if err != nil || !result.Complete || !strings.Contains(string(result.Data), "original-comment") {
				t.Fatal(result, err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Count(string(calls), `"+fetch"`) != 1 || strings.Contains(string(calls), "share-part") {
				t.Fatal("unexpected CLI calls", string(calls))
			}
			setMode(t, dir, "tips")
			result, err = c.FetchDocument(t.Context(), account, "https://example.feishu.cn/"+path)
			if err != nil || result.Complete || !strings.Contains(string(result.Data), "upstream degradation") {
				t.Fatal(result, err)
			}
		})
	}
	for _, tc := range []struct{ mode, code, next string }{
		{"app-missing", "document_application_scopes_missing", "configure_application"},
		{"user-missing", "document_user_scopes_missing", "authorize_user"},
		{"app-unknown", "document_invalid_response", "retry_check"},
		{"app-identity", "identity_mismatch", ""},
		{"denied", "document_access_denied", "check_document_access"},
		{"not-found", "document_unavailable", "check_document_access"},
		{"switch", "identity_mismatch", ""},
		{"large", "output_limit", ""},
		{"malformed", "invalid_response", ""},
		{"response-bot", "invalid_response", ""},
		{"network", "network_error", ""},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			c, dir, account := testClient(t)
			if err := os.WriteFile(c.profile.Executable, []byte(documentFixture), 0700); err != nil {
				t.Fatal(err)
			}
			setMode(t, dir, tc.mode)
			result, err := c.FetchDocument(t.Context(), account, "https://example.feishu.cn/wiki/Node")
			if ErrorCode(err) != tc.code || len(result.Data) != 0 || strings.Contains(err.Error(), "private") {
				t.Fatal(result, err)
			}
			if tc.next != "" {
				var diagnostic *DocumentError
				if !errors.As(err, &diagnostic) || diagnostic.NextAction != tc.next {
					t.Fatal(err)
				}
			}
			if strings.HasSuffix(tc.mode, "missing") {
				if _, err := os.Stat(filepath.Join(dir, "read")); !os.IsNotExist(err) {
					t.Fatal("scope gap dispatched fetch")
				}
			}
		})
	}
}

func TestDocumentFetchCancellationAndInvalidURL(t *testing.T) {
	c, dir, account := testClient(t)
	if err := os.WriteFile(c.profile.Executable, []byte(documentFixture), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := c.FetchDocument(t.Context(), account, "https://example.feishu.cn/sheets/Sheet"); ErrorCode(err) != "unsupported_document_url" {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("invalid URL ran CLI")
	}
	setMode(t, dir, "block")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := c.FetchDocument(ctx, account, "https://example.feishu.cn/wiki/Node"); done <- err }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "read")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("fetch did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not settle CLI")
	}
	// No detached process or held profile gate can survive cancellation.
	setMode(t, dir, "ok")
	result, err := c.FetchDocument(t.Context(), account, "https://example.feishu.cn/wiki/Node")
	if err != nil || !json.Valid(result.Data) {
		t.Fatal(result, err)
	}
}
