package lark

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const chatSearchFixture = `if a[:2] in [['im','+chat-search'],['contact','+search-user'],['contact','+search-bot']]:
 assert a[-4:]==['--as','user','--format','json']
 assert a[a.index('--query')+1]=='研发'
 assert '--page-all' not in a and '--exclude-muted' not in a and '--exclude-external-users' not in a
 open(base+'/read','w').write('yes')
 kind='group' if a[0]=='im' else 'user' if a[1]=='+search-user' else 'bot'
 if mode==kind+'_permission':
  print(json.dumps({'ok':False,'error':{'type':'authorization'}}),file=sys.stderr);sys.exit(1)
 if kind=='group':
  assert a[a.index('--search-types')+1]=='private,public_joined,external'
  assert a[a.index('--chat-modes')+1]=='group,topic' and '--disable-search-by-user' in a
  data={'chats':[{'chat_id':'oc_group','chat_mode':'DEFAULT','name':'研发群'},{'chat_id':'oc_topic','chat_mode':'THREAD','name':'研发话题'}],'has_more':True,'page_token':'next'}
  if '--page-token' in a:
   assert a[a.index('--page-token')+1]=='next'
   data={'chats':[],'has_more':False,'page_token':''}
  if mode=='bad_mode':data['chats'][0]['chat_mode']='p2p'
  if mode=='bad_cursor':data['page_token']=''
 else:
  assert '--has-chatted' in a and '--page-token' not in a
  if kind=='user':
   data={'users':[{'open_id':'ou_peer','localized_name':'同事','p2p_chat_id':'oc_peer'},{'open_id':'ou_empty','localized_name':'未聊过','p2p_chat_id':''}],'has_more':True}
   if mode=='bad_chat':data['users'][0]['p2p_chat_id']='ou_not_chat'
   if mode=='bad_name':data['users'][0]['localized_name']='bad\nname'
  else:
   data={'bots':[{'open_id':'ou_bot','name':'助手','chat_id':'oc_bot'}],'has_more':True}
   if mode=='duplicate':data['bots'][0]['chat_id']='oc_peer'
 if mode=='missing_more':del data['has_more']
 print(json.dumps({'ok':True,'identity':'bot' if mode=='response_bot' else 'user','data':data}));sys.exit()
`

func TestChatSearchNativeContracts(t *testing.T) {
	c, dir, a := testClient(t)
	script := strings.Replace(fixture, "assert a[:2]==['api','GET'] or a[:2]==['api','POST']", chatSearchFixture+"\nassert a[:2]==['api','GET'] or a[:2]==['api','POST']", 1)
	if err := os.WriteFile(c.profile.Executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	q := ChatSearchRequest{Query: "研发", Type: "all"}
	for _, tc := range []struct {
		mode         string
		count        int
		source, code string
	}{
		{"ok", 4, "user", "refine_query"},
		{"duplicate", 3, "bot", "refine_query"},
		{"user_permission", 3, "user", "permission_denied"},
		{"bot_permission", 3, "bot", "permission_denied"},
		{"group_permission", 2, "group", "permission_denied"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			setMode(t, dir, tc.mode)
			page, err := c.SearchChats(context.Background(), a, q)
			if err != nil || len(page.Chats) != tc.count {
				t.Fatal(page, err)
			}
			found := false
			for _, w := range page.SearchWarnings {
				if w.Source == tc.source && w.Code == tc.code {
					found = true
				}
			}
			if !found {
				t.Fatal(page.SearchWarnings)
			}
		})
	}
	setMode(t, dir, "ok")
	page, err := c.SearchChats(context.Background(), a, ChatSearchRequest{Query: "研发", Type: "group", PageToken: "next"})
	if err != nil || page.HasMore || len(page.Chats) != 0 || len(page.SearchWarnings) != 0 {
		t.Fatal(page, err)
	}
	page, err = c.SearchChats(context.Background(), a, ChatSearchRequest{Query: "研发", Type: "p2p"})
	if err != nil || page.HasMore || len(page.Chats) != 2 || len(page.SearchWarnings) != 2 {
		t.Fatal(page, err)
	}
	for _, mode := range []string{"bad_mode", "bad_cursor", "bad_chat", "bad_name", "missing_more", "response_bot", "switch"} {
		t.Run(mode, func(t *testing.T) {
			_ = os.Remove(filepath.Join(dir, "read"))
			setMode(t, dir, mode)
			page, err := c.SearchChats(context.Background(), a, q)
			if err == nil || page.Chats != nil {
				t.Fatal("unverified result", page, err)
			}
			if mode == "switch" && ErrorCode(err) != "identity_mismatch" {
				t.Fatal(err)
			}
		})
	}
}

func TestChatSearchRejectsInvalidInputBeforeCLI(t *testing.T) {
	c, dir, a := testClient(t)
	for _, q := range []ChatSearchRequest{
		{Query: "", Type: "all"}, {Query: " ", Type: "all"}, {Query: strings.Repeat("字", 51), Type: "all"},
		{Query: "test\n", Type: "all"}, {Query: string([]byte{0xff}), Type: "all"}, {Query: "test", Type: "unknown"},
		{Query: "test", Type: "p2p", PageToken: "next"}, {Query: "test", Type: "group", PageToken: "bad\n"},
	} {
		if _, err := c.SearchChats(context.Background(), a, q); ErrorCode(err) != "invalid_request" {
			t.Fatal(q, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("invalid input reached CLI", err)
	}
}
