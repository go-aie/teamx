package room

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func installChatSearchFixture(t *testing.T, h *larkHarness) {
	t.Helper()
	bin := filepath.Join(h.dir, "lark-cli")
	script, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	search := `if a[:2]==['im','+chat-search']:
 assert a[a.index('--query')+1]=='yanfa'
 assert '--disable-search-by-user' in a
 data={'chats':[{'chat_id':'oc_search_group','chat_mode':'DEFAULT','name':'研发群'}],'has_more':False,'page_token':''}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
if a[:2]==['contact','+search-user']:
 assert '--has-chatted' in a
 data={'users':[{'open_id':'ou_peer','localized_name':'研发同事','p2p_chat_id':'oc_search_peer'}],'has_more':True}
 print(json.dumps({'ok':True,'identity':'user','data':data}));sys.exit()
if a[:2]==['contact','+search-bot']:
 print(json.dumps({'ok':False,'error':{'type':'authorization'}}),file=sys.stderr);sys.exit(1)
`
	script = []byte(strings.Replace(string(script), "if a[:2]==['im','+chat-list']:", search+"\nif a[:2]==['im','+chat-list']:", 1))
	if err := os.WriteFile(bin, script, 0700); err != nil {
		t.Fatal(err)
	}
}
