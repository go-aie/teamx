package lark

const pinnedFixture = `#!/usr/bin/env -S python3 -S
import sys,os,json
base=os.path.dirname(os.path.realpath(sys.argv[0]));a=sys.argv[3:]
assert sys.argv[1:3]==['--profile','fixture']
root=os.environ.get('LARKSUITE_CLI_CONFIG_DIR','')
mode=open(base+'/mode').read()
if a==['config','show']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','dpopMode':'required'}));sys.exit()
if a==['--version']:print('lark-cli version 1.0.99');sys.exit()
assert root and root!=base
cfg=json.load(open(root+'/config.json'));app=cfg['apps'][0]
assert app['appSecret']=={'source':'keychain','id':'appsecret:cli_fixture'}
assert len(app['users'])==1 and app['dpopMode']=='required'
user=app['users'][0]['userOpenId']
assert user=='ou_reader'
assert 'LARKSUITE_CLI_PROFILE' not in os.environ
assert 'LARKSUITE_CLI_WORKSPACE' not in os.environ
assert 'LARKSUITE_CLI_USER_ACCESS_TOKEN' not in os.environ
if mode=='revoked':print('{}');sys.exit(1)
if a==['whoami','--as','user']:
 print(json.dumps({'profile':'fixture','appId':'cli_fixture','brand':'feishu','identity':'user','available':True,'onBehalfOf':{'openId':user}}));sys.exit()
if a[:3]==['api','GET','/open-apis/authen/v1/user_info']:
 print(json.dumps({'ok':True,'identity':'user','data':{'open_id':user,'tenant_key':'tenant','name':'fixed user'}}));sys.exit()
if a==['auth','scopes','--json']:
 print(json.dumps({'appId':'cli_fixture','scopes':['im:message.send_as_user','im:message']}));sys.exit()
if a[:2]==['auth','check']:
 if mode=='missing':print(json.dumps({'ok':False,'granted':['im:message'],'missing':['im:message.send_as_user']}));sys.exit(1)
 print(json.dumps({'ok':True,'granted':['im:message.send_as_user','im:message'],'missing':[]}));sys.exit()
raise Exception('unexpected operation')
`
