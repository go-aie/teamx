#!/usr/bin/env python3
"""Build a separate module, then exercise public startup and controlled tools."""
import argparse, json, os, pathlib, selectors, shutil, signal, subprocess, time, urllib.request

parser=argparse.ArgumentParser()
parser.add_argument('--teamx-root',required=True)
parser.add_argument('--output',required=True)
a=parser.parse_args()
source=pathlib.Path(__file__).resolve().parent
root=pathlib.Path(a.teamx_root).resolve()
out=pathlib.Path(a.output).resolve(); out.mkdir(parents=True,exist_ok=True)
module=out/'module'; shutil.copytree(source,module)
bins=out/'bin'; bins.mkdir()
env={k:v for k,v in os.environ.items() if not k.startswith(('TEAMX_','DSH_'))}
env['GOWORK']='off'

def run(args,cwd=module,**kwargs):
 p=subprocess.run(args,cwd=cwd,env=env,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=90,**kwargs)
 if p.returncode: raise RuntimeError(f'{args[0:3]} exit={p.returncode}: {p.stderr.decode()}')
 return p.stdout

run(['go','mod','edit','-replace=github.com/go-aie/teamx='+str(root)])
run(['go','build','-mod=mod','-o',str(bins/'teamx'),'./cmd/teamx'])
run(['go','build','-mod=mod','-o',str(bins/'teamx-fixture-echo'),'./cmd/echo'])
# Only the fixture itself must avoid internal imports; TeamX's public packages
# may naturally implement their behavior with private packages in their module.
for path in module.rglob('*.go'):
 assert 'github.com/go-aie/teamx/internal/' not in path.read_text(),path
binary=str(bins/'teamx')
check=json.loads(run([binary,'extensions','check']))
assert check['extensions']==['local-example'] and [t['name'] for t in check['tools']]==['local-echo'],check
for args in [['--help'],['version'],['serve','--help'],['service','--help']]:
 # flag.ErrHelp is returned for serve; ordinary main preserves that behavior.
 p=subprocess.run([binary]+args,env=env,capture_output=True,timeout=10)
 if args!=['serve','--help']: assert p.returncode==0,(args,p.stderr.decode())

home=out/'home';home.mkdir(mode=0o700)
teams=home/'teams'/'local-tools';teams.mkdir(parents=True)
result=home/'calls.json'
fixture=module/'acp_fixture.py'
team={'id':'local-tools','name':'Local tools','description':'Offline public extension fixture','opening_message':'Local fixture',
 'runtime':{'dsh':{'binary_path':str(fixture),'artifact':{'source':'offline-acp','version':'1'}},'model':{'protocol':'openai-completions','base_url':'http://127.0.0.1:1/v1','model_id':'fixture','api_key_env':'TEAMX_EXTERNAL_FIXTURE_KEY'}},
 'members':[{'id':'A','name':'A','instructions':'Only the deterministic local fixture is executed.'}]}
(teams/'team.json').write_text(json.dumps(team))
# Never reuse real credentials or the user's home. This token exists only here.
token='local-extension-fixture-only-12345'
(home/'token').write_text(token);(home/'token').chmod(0o600)
config=home/'config.toml'
config.write_text('[server]\nlisten="127.0.0.1:0"\ndata_dir="./data"\nteams_dir="./teams"\n[access]\noperator_id="owner_11111111111111111111111111111111"\nlogin_enabled=true\nadmin_token_file="./token"\n[env]\nTEAMX_EXTERNAL_FIXTURE_KEY="offline-model-only"\nTEAMX_EXTERNAL_RESULT='+json.dumps(str(result))+'\n')
config.chmod(0o600)
log=open(out/'server.log','wb')
p=subprocess.Popen([binary,'serve','--config',str(config)],env=env,stdout=subprocess.PIPE,stderr=log)
try:
 sel=selectors.DefaultSelector();sel.register(p.stdout,selectors.EVENT_READ)
 assert sel.select(20),'server readiness timed out'
 line=p.stdout.readline()
 if not line: raise RuntimeError('server failed before readiness: '+(out/'server.log').read_text())
 ready=json.loads(line);endpoint=ready['endpoint'];epoch=ready['service_epoch']
 caller={'kind':'human','id':'owner_11111111111111111111111111111111'}
 def request(path,body=None):
  data=None if body is None else json.dumps(body).encode()
  req=urllib.request.Request(endpoint+path,data=data,headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
  try:
   with urllib.request.urlopen(req,timeout=15) as r: return json.load(r)
  except urllib.error.HTTPError as e: raise RuntimeError(f'{path}: HTTP {e.code} {e.read().decode()}')
 def cli(args):
  return run([binary]+args,cwd=home) # env + explicit token path, no credentials in argv
 cli_env=dict(env,TEAMX_ENDPOINT=endpoint,TEAMX_ADMIN_TOKEN_FILE=str(home/'token'))
 def human(args,success=True):
  r=subprocess.run([binary]+args,env=cli_env,capture_output=True,timeout=20)
  assert (r.returncode==0)==success,(args,r.returncode,r.stderr.decode())
  return r.stdout
 room=request('/v1/rooms/create',{'service_epoch':epoch,'caller':caller,'team_id':'local-tools','create_id':'external','name':'External fixture'})
 identity={k:room[k] for k in ['room_id','room_instance']}
 opened=request('/v1/rooms/open',dict(identity,service_epoch=epoch,caller=caller,open_id='open-local'))
 deadline=time.monotonic()+20
 while opened['status']=='running':
  assert time.monotonic()<deadline,'room activation timed out'
  time.sleep(.05)
  opened=request('/v1/rooms/open?service_epoch='+epoch+'&caller_id=owner_11111111111111111111111111111111&open_id=open-local')
 assert opened['status']=='completed',opened
 service=opened['service']
 members=json.loads(human(['member','list','--room',identity['room_id'],'--json']))['members']
 assert len(members)==1,members
 member=members[0]
 bindings=request('/v1/tools/bindings?service_epoch='+epoch+'&caller_id=owner_11111111111111111111111111111111&room_id='+identity['room_id']+'&room_instance='+identity['room_instance'])
 assert [t['name'] for t in bindings['catalog']]==['local-echo'],bindings
 binding={'agent_id':member['agent_id'],'membership_id':member['membership_id'],'tool':'local-echo'}
 bound=request('/v1/tools/bind',dict(identity,service_epoch=epoch,caller=caller,expected_binding_revision=0,binding=binding,enabled=True))
 assert len(bound['bindings'])==1 and not bound['bindings'][0].get('platform_id'),bound
 # No platform connection or workspace binding is created. Only the member's
 # ordinary local runtime directory exists for the offline ACP subprocess.
 platforms=request('/v1/platforms?service_epoch='+epoch+'&caller_id=owner_11111111111111111111111111111111')
 assert not platforms.get('platforms'),platforms
 human(['member','run','--room',identity['room_id'],'--agent',member['agent_id']])
 human(['send','--room',identity['room_id'],'--text','Run the deterministic local tool fixture','--id','external-local-tool'])
 deadline=time.monotonic()+25
 while not result.exists():
  if time.monotonic()>deadline:
   status=human(['member','list','--room',identity['room_id'],'--json']).decode()
   raise RuntimeError('no tool result: '+status+'\n'+(out/'server.log').read_text())
  time.sleep(.05)
 reports=json.loads(result.read_text())
 discovered=json.loads(reports[0]['stdout'])
 assert reports[0]['code']==0 and [t['name'] for t in discovered]==['local-echo'],reports
 echo=json.loads(reports[1]['stdout'])
 assert reports[1]['code']==0 and echo=={'args':['literal space','--','$HOME'],'input':'hello\x00external'},reports
 assert reports[2]['code']==23,reports
 # Exercise the normal cancellation and process/Call cleanup before shutdown.
 human(['member','pause','--room',identity['room_id'],'--agent',member['agent_id'],'--cancel'])
 # An admin token cannot impersonate the member despite knowing its identity.
 try:
  request('/v1/tools/list',dict(service,caller={'kind':'agent','id':member['agent_id'],'membership_id':member['membership_id']}))
  raise AssertionError('admin identity bypassed execution authorization')
 except RuntimeError as e:
  assert 'HTTP 403' in str(e) or 'HTTP 401' in str(e),e
 summary={'status':'passed','external_module':'example.com/teamx-local-tool','local_replace':str(root),'selected':check,'calls':reports,'member_authorization':'real temporary execution token via offline ACP fixture','real_model':False,'remote_business_writes':0}
 (out/'result.json').write_text(json.dumps(summary,ensure_ascii=False,indent=2)+'\n')
finally:
 p.terminate()
 try: code=p.wait(timeout=35)
 except subprocess.TimeoutExpired:
  p.kill();p.wait();raise AssertionError('server did not settle on shutdown')
 log.close()
assert code==0,('server cleanup failed',code,(out/'server.log').read_text())
print(json.dumps({'status':'passed','result':str(out/'result.json'),'server_exit':code}))
