//go:build integration || e2e

package lark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryCLIPageContractAndIdentityChange(t *testing.T) {
	parallelClientTest(t)
	c, dir, a := testClient(t)
	path := filepath.Join(dir, "lark-fixture")
	body, _ := os.ReadFile(path)
	code := strings.Replace(string(body), "assert a[:2]==['api','GET']", `if a[:2]==['im','+chat-list']:
 assert '--page-all' not in a and '--exclude-muted' not in a
 assert a[a.index('--types')+1]=='p2p,group' and a[a.index('--as')+1]=='user'
 open(base+'/read','w').write('yes')
 data={'chats':[{'chat_id':'oc_chat','chat_mode':'p2p','name':'fixture'}],'has_more':False,'page_token':''}
 if mode=='incomplete':del data['has_more']
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
assert a[:2]==['api','GET']`, 1)
	if err := os.WriteFile(path, []byte(code), 0700); err != nil {
		t.Fatal(err)
	}
	page, err := c.Chats(context.Background(), a, ChatsRequest{PageSize: 50})
	if err != nil || len(page.Chats) != 1 {
		t.Fatal(err)
	}
	setMode(t, dir, "incomplete")
	if _, err = c.Chats(context.Background(), a, ChatsRequest{PageSize: 50}); ErrorCode(err) != "invalid_response" {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "read"))
	setMode(t, dir, "switch")
	if _, err = c.Chats(context.Background(), a, ChatsRequest{PageSize: 50}); ErrorCode(err) != "identity_mismatch" {
		t.Fatal(err)
	}
}
