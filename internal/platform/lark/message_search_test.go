package lark

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func searchRequest() MessageSearchRequest {
	return MessageSearchRequest{ChatType: "group", Start: time.Unix(99, 0), End: time.Unix(101, 0), PageSize: 50}
}

func TestMessageSearchInputBounds(t *testing.T) {
	c, dir, account := testClient(t)
	for _, mutate := range []func(*MessageSearchRequest){
		func(r *MessageSearchRequest) { r.ChatType = "all" },
		func(r *MessageSearchRequest) { r.Start = time.Time{} },
		func(r *MessageSearchRequest) { r.End = r.Start },
		func(r *MessageSearchRequest) { r.PageSize = 0 },
		func(r *MessageSearchRequest) { r.PageSize = 51 },
		func(r *MessageSearchRequest) { r.PageToken = "bad\n" },
		func(r *MessageSearchRequest) { r.ChatIDs = []string{"oc_one", "oc_one"} },
		func(r *MessageSearchRequest) { r.ChatIDs = []string{"om_wrong"} },
		func(r *MessageSearchRequest) { r.FromID = "cli_bot" },
		func(r *MessageSearchRequest) {
			for i := range 33 {
				r.ChatIDs = append(r.ChatIDs, fmt.Sprintf("oc_%d", i))
			}
		},
	} {
		r := searchRequest()
		mutate(&r)
		if _, err := c.SearchMessages(context.Background(), account, r); ErrorCode(err) != "invalid_request" {
			t.Fatalf("invalid input reached transport: %v", err)
		}
	}
	tooMany := []string{}
	for i := range 51 {
		tooMany = append(tooMany, fmt.Sprintf("om_%d", i))
	}
	for _, ids := range [][]string{nil, {}, {"oc_wrong"}, {"om_one", "om_one"}, tooMany} {
		if _, err := c.BatchMessages(context.Background(), account, ids); ErrorCode(err) != "invalid_request" {
			t.Fatal("invalid batch reached transport", err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "calls")); !os.IsNotExist(err) {
		t.Fatal("invalid requests must not start CLI")
	}
}

func TestMessageReadErrorCategories(t *testing.T) {
	for _, tc := range []struct {
		native int
		code   string
	}{
		{1020, "cursor_expired"}, {1021, "invalid_request"}, {99992402, "invalid_request"},
		{99992354, "details_unavailable"}, {99991400, "rate_limited"}, {12345, "query_failed"},
	} {
		err := messageReadError(classify([]byte(fmt.Sprintf(`{"error":{"type":"api","code":%d,"message":"private"}}`, tc.native))))
		if ErrorCode(err) != tc.code {
			t.Fatalf("native=%d got=%v", tc.native, err)
		}
	}
}

const messageSearchFixture = `if path in ['/open-apis/im/v1/messages/search','/open-apis/im/v1/messages/mget']:
 assert a[-4:]==['--as','user','--format','json']
 open(base+'/read','w').write('yes')
 if mode=='block':time.sleep(60)
 if mode=='large':print('s'*((2<<20)+1));sys.exit()
 if mode in ['permission','rate','cursor','network','detail_error','parameter_error','unknown_error']:
  typ={'permission':'authorization','network':'network'}.get(mode,'api')
  code={'rate':99991400,'cursor':1020,'detail_error':99992354,'parameter_error':1021,'unknown_error':54321}.get(mode,0)
  print(json.dumps({'error':{'type':typ,'code':code,'message':'private-secret-body'}}),file=sys.stderr);sys.exit(1)
 if mode=='malformed':print('{');sys.exit()
 params=json.loads(a[a.index('--params')+1])
 if path.endswith('/search'):
  assert a[1]=='POST' and set(params)<=set(['page_size','page_token']) and 1<=params['page_size']<=50
  body=json.loads(a[a.index('--data')+1]);assert body['query']==''
  f=body['filter'];assert set(f)<=set(['chat_type','chat_ids','from_ids','is_at_me','time_range'])
  assert f['time_range']=={'start_time':'1970-01-01T00:01:39Z','end_time':'1970-01-01T00:01:41Z'}
  if 'chat_ids' in f:assert len(f['chat_ids'])<=32
  if 'from_ids' in f:assert f['from_ids']==['ou_sender']
  m={'id':'om_one','meta_data':{'message_id':'om_one','chat_id':'oc_chat','from_id':'ou_sender','create_time':'1970-01-01T00:01:40Z','is_p2p_chat':f['chat_type']=='p2p'},'display_info':'summary must not become body'}
  items=[] if params.get('page_token')=='second' or mode=='empty_more' else [m]
  data={'items':items,'has_more':params.get('page_token')!='second'}
  if data['has_more']:data['page_token']='second'
  if mode=='loop':data['page_token']=params.get('page_token','');data['has_more']=True
  if mode=='bad_token':data['page_token']='bad\n'
  if mode=='missing_token':data.pop('page_token',None);data['has_more']=True
  if mode=='missing_more':del data['has_more']
  if mode=='notice':data['notice']='private-secret-body: results incomplete'
  if mode=='duplicate':data['items']=[m,m]
  if mode=='oversize':data['items']=[m]*(params['page_size']+1)
  if mode=='bad_id':m['meta_data']['message_id']='om_other'
  if mode=='wrong_chat':m['meta_data']['chat_id']='oc_other'
  if mode=='wrong_type':m['meta_data']['is_p2p_chat']=not m['meta_data']['is_p2p_chat']
  if mode=='missing_type':del m['meta_data']['is_p2p_chat']
  if mode=='wrong_sender':m['meta_data']['from_id']='ou_other'
  if mode=='bad_time':m['meta_data']['create_time']='100000'
  if mode=='outside_window':m['meta_data']['create_time']='1970-01-01T00:01:42Z'
  if mode=='unordered':
   second=json.loads(json.dumps(m));second['id']=second['meta_data']['message_id']='om_two';second['meta_data']['create_time']='1970-01-01T00:01:39Z';data['items']=[m,second]
 else:
  assert a[1]=='GET' and set(params)=={'message_ids'} and 1<=len(params['message_ids'])<=50
  def message(mid):
   return {'message_id':mid,'chat_id':'oc_chat','msg_type':'post','create_time':'100000','update_time':'100001','updated':True,'deleted':False,'root_id':'om_root','parent_id':'om_parent','thread_id':'omt_thread','sender':{'id':'ou_sender','id_type':'open_id','sender_type':'user','tenant_key':'external'},'body':{'content':json.dumps({'zh_cn':{'title':'标题','content':[[{'tag':'at','user_id':'all'}]]}})},'mentions':[{'key':'@_all','id':'all','name':'所有人'}]}
  data={'items':[message(mid) for mid in reversed(params['message_ids'])]}
  if mode=='partial':data['items']=data['items'][:1]
  if mode=='empty':data['items']=[]
  if mode=='duplicate':data['items']=[data['items'][0],data['items'][0]]
  if mode=='unrequested':data['items'][0]['message_id']='om_unrequested'
  if mode=='bad_time':data['items'][0]['create_time']='bad'
  if mode=='missing_body':del data['items'][0]['body']
  if mode=='missing_content':del data['items'][0]['body']['content']
  if mode=='missing_sender':del data['items'][0]['sender']
  if mode=='bad_reply':data['items'][0]['thread_id']='om_wrong'
  if mode=='deleted':data['items'][0]['deleted']=True
 if mode=='missing_items':del data['items']
 if mode=='null_items':data['items']=None
 print(json.dumps({'ok':True,'identity':'bot' if mode=='response_bot' else 'user','data':data}));sys.exit()
`
