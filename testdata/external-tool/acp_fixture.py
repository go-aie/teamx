#!/usr/bin/env python3
# Deterministic offline transport fixture. This is not a DSH/model acceptance.
import json, os, pathlib, subprocess, sys
if '--version' in sys.argv:
 print('1.0.0'); sys.exit(0)
model=json.dumps(['teamx','fixture'],separators=(',',':'))
options=[{'id':'model','currentValue':model,'options':[{'group':'teamx','options':[{'value':model}]}]}]
session='external-local-fixture'
pending=None

def send(i,r):
 print(json.dumps({'jsonrpc':'2.0','id':i,'result':r}),flush=True)

for line in sys.stdin:
 f=json.loads(line)
 method=f.get('method')
 if not method: continue
 result={}
 if method=='initialize':
  result={'protocolVersion':1,'authMethods':[],'agentInfo':{'name':'external-fixture','version':'1'},'agentCapabilities':{'mcpCapabilities':{'http':True},'sessionCapabilities':{'close':{},'resume':{}}}}
 elif method=='session/new': result={'sessionId':session,'configOptions':options}
 elif method in ['session/resume','session/set_config_option']: result={'configOptions':options}
 elif method=='session/prompt':
  pending=f['id']
  reports=[]
  for args,body in [(['list'],b''),(['run','local-echo','literal space','--','$HOME'],b'hello\x00external'),(['run','local-echo','--fail'],b'')]:
   p=subprocess.run([os.environ['TEAMX_CLI'],'tools']+args,input=body,stdout=subprocess.PIPE,stderr=subprocess.PIPE,timeout=15)
   reports.append({'args':args,'code':p.returncode,'stdout':p.stdout.decode(),'stderr':p.stderr.decode()})
  path=pathlib.Path(os.environ['TEAMX_EXTERNAL_RESULT'])
  temporary=path.with_suffix('.tmp'); temporary.write_text(json.dumps(reports)); temporary.replace(path)
  # Keep the turn open until the operator's cancel arrives. No input receipt
  # or model-completion claim is manufactured by this transport fixture.
  continue
 elif method=='session/cancel':
  if pending is not None:
   send(pending,{'stopReason':'cancelled'}); pending=None
  continue
 if 'id' in f: send(f['id'],result)
