package room

const larkFixture = `#!/usr/bin/env -S python3 -S
import sys,json,os,time
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[3:]
mode=open(base+'/mode').read()
if a==['--version']:print('lark-cli version 1.0.99');sys.exit()
user='ou_other' if mode=='switch' and os.path.exists(base+'/read') else 'ou_reader'
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':user}}));sys.exit()
if a[2]=='/open-apis/authen/v1/user_info':
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':user,'tenant_key':'tenant','name':'中文用户'}}));sys.exit()
open(base+'/read','w').write('yes')
if mode=='block':time.sleep(60)
print(json.dumps({'ok':True,'identity':'user','data':{'items':[{'message_id':'om_one','chat_id':'oc_chat','msg_type':'text','create_time':'100000','update_time':'100000','body':{'content':'{"text":"fixture message"}'}}],'has_more':False}}))
`
