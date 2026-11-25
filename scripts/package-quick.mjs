import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import {spawn, execFileSync} from 'node:child_process';
import {once} from 'node:events';
import {fileURLToPath} from 'node:url';

export async function checkLoginAssets(endpoint) {
  endpoint=new URL(endpoint).origin;
  const get=path=>fetch(endpoint+path,{signal:AbortSignal.timeout(5000),redirect:'error'});
  const queue=['/access'],seen=new Set();
  const dependencies=/(?:src|href)=["']([^"']+)["']|^\s*import\s+(?:[^;'"()]*?\sfrom\s*)?["']([^"']+)["']|@import\s+["']([^"']+)["']/gm;
  while(queue.length){
    const path=queue.shift();if(seen.has(path))continue;seen.add(path);
    if(seen.size>100)throw Error('Login dependency graph exceeds 100 assets');
    const response=await get(path);
    const type=path==='/access'?'text/html':/\.m?js$/.test(path)?'javascript':path.endsWith('.css')?'text/css':path.endsWith('.svg')?'image/svg+xml':'';
    if(!response.ok || type&&!response.headers.get('content-type')?.includes(type))throw Error(`Login dependency ${path}: HTTP ${response.status}`);
    const body=await response.text();
    for(const match of body.matchAll(dependencies))for(const ref of match.slice(1)){
      if(!ref)continue;const resolved=new URL(ref,endpoint+path);
      if(resolved.origin===endpoint&&resolved.pathname.startsWith('/ui/'))queue.push(resolved.pathname);
    }
  }
  const method=await get('/v1/access/method');
  if(!method.ok || !(await method.json()).provider)throw Error('Login method unavailable');
  for(const path of ['/v1/access/session','/ui/app.js'])if((await get(path)).status!==401)throw Error('Private application route accessible before login: '+path);
  return {assets:[...seen],private_routes:'protected'};
}

// Check application startup and the real login dependency graph without
// unpacking/re-hashing a runtime that this build has already assembled.
export async function checkApplication(bundle) {
  const manifest=JSON.parse(fs.readFileSync(p.join(bundle,'manifest.json'),'utf8'));
  const temp=fs.mkdtempSync(p.join(os.tmpdir(),'teamx-quick-'));
  const home=p.join(temp,'home'), cli=p.join(bundle,'bin/teamx');
  const env={HOME:temp,PATH:[p.join(bundle,'bin'),p.join(bundle,'runtime/node/bin'),'/usr/bin','/bin'].join(':'),LANG:'C.UTF-8'};
  const run=args=>execFileSync(cli,args,{env,encoding:'utf8',timeout:30000,stdio:['ignore','pipe','pipe']});
  let child, exited, output='', errors='';
  const checks=[];
  try {
    if(!run(['--version']).startsWith(`teamx ${manifest.version} commit=${manifest.source.commit} dirty=${manifest.source.dirty}`))throw Error('Application build identity mismatch');
    checks.push('application build identity');
    run(['init','--bundle-root',bundle,'--home',home,'--non-interactive']);
    run(['access','init','--config',p.join(home,'config.toml')]);
    child=spawn(cli,['bundle','--bundle-root',bundle,'--home',home,'--listen','127.0.0.1:0'],{env,stdio:['ignore','pipe','pipe']});
    exited=once(child,'exit');
    child.stdout.on('data',b=>{output+=b;});child.stderr.on('data',b=>{errors+=b;});
    let endpoint;
    const deadline=Date.now()+30000;
    while(!endpoint && Date.now()<deadline){
      if(child.exitCode!==null)throw Error('Application exited before readiness: '+errors);
      for(const line of output.split('\n')){try{endpoint=JSON.parse(line).endpoint || endpoint;}catch{}}
      if(!endpoint)await new Promise(resolve=>setTimeout(resolve,25));
    }
    if(!endpoint)throw Error('Application readiness timed out: '+errors);
    const get=(path,options={})=>fetch(endpoint+path,{...options,signal:AbortSignal.timeout(5000)});
    await checkLoginAssets(endpoint);
    checks.push('anonymous login assets and transitive dependencies');
    const token=fs.readFileSync(p.join(home,'auth/admin-token'),'utf8').trim();
    const login=await get('/v1/access/login',{method:'POST',headers:{Origin:endpoint,'Content-Type':'application/json'},body:JSON.stringify({token})});
    const cookie=login.headers.get('set-cookie')?.split(';')[0];
    if(!login.ok || !cookie)throw Error('Application login failed');
    const setup=await get('/v1/setup',{headers:{Cookie:cookie}});
    if(!setup.ok || (await setup.json()).setup_required!==true)throw Error('Authenticated setup unavailable');
    checks.push('private route authorization and authenticated setup');
    child.kill('SIGTERM');
    const result=await Promise.race([exited,new Promise((_,reject)=>{const timer=setTimeout(()=>reject(Error('Application shutdown timed out')),10000);timer.unref();})]);
    if(result[0]!==0)throw Error('Application shutdown failed: '+errors);
    checks.push('clean application shutdown');
    return {schema_version:1,status:'passed',smoke_level:'quick',target:manifest.target,profile:manifest.profile,checks};
  } finally {
    if(child && child.exitCode===null && child.signalCode===null){child.kill('SIGKILL');await exited;}
    fs.rmSync(temp,{recursive:true,force:true});
  }
}

if(process.argv[1] && p.resolve(process.argv[1])===fileURLToPath(import.meta.url)){
  try {const report=await checkApplication(p.resolve(process.argv[2]));fs.writeFileSync(process.argv[3],JSON.stringify(report,null,2)+'\n');}
  catch(error){console.error('Application check: '+error.message);process.exitCode=1;}
}
