import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import { execFileSync, spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { deploymentConfig, sourceCommit, makeSourceBundle, deployLocal, shellQuote } from './deploy-linux.mjs';
import { deployRemote, deploymentSmoke, saveDeployment, runRetention, waitForRetention } from './deploy-linux-remote.mjs';

const git=(repo,...args)=>execFileSync('git',['-C',repo,...args],{encoding:'utf8'}).trim();
const runtime={platform:'linux',arch:'x64',machine:'x86_64',uid:0};
const sshTarget={host:'root@example.test'};

function fixture(t, options={}) {
  const root=fs.realpathSync(fs.mkdtempSync(p.join(os.tmpdir(),"teamx-deploy ' fixture-")));
  t.after(()=>fs.rmSync(root,{recursive:true,force:true}));
  const repo=p.join(root,'repo');fs.mkdirSync(repo);
  git(repo,'init','-q');git(repo,'config','user.name','Fixture');git(repo,'config','user.email','fixture@localhost');
  const write=(name,text)=>{const file=p.join(repo,name);fs.mkdirSync(p.dirname(file),{recursive:true});fs.writeFileSync(file,text);};
  write('README.md','committed source');
  write('scripts/package-progress.mjs',`export const source = 'committed progress';\n`);
  write('scripts/deploy-linux-remote.mjs',`import fs from 'node:fs';import {source} from './package-progress.mjs';
const request=JSON.parse(fs.readFileSync(process.argv[2],'utf8'));
fs.writeFileSync(request.job+'/runner-executed',source);
`);
  for (const file of ['scripts/package-smoke.mjs','scripts/package-srt.mjs','scripts/check-dsh-native.mjs','scripts/fixtures/acp.mjs','scripts/package-quick.mjs']) write(file,'fixture verifier: '+file);
  write('scripts/package-smoke.mjs',`import fs from 'node:fs';import {createHash} from 'node:crypto';
const hash=file=>createHash('sha256').update(fs.readFileSync(file)).digest('hex');
const report={status:'passed',smoke_level:'full',target:'linux-amd64',profile:'standard',archive_sha256:hash(process.argv[2])};
for(const [key,file] of Object.entries({smoke_sha256:'scripts/package-smoke.mjs',srt_check_sha256:'scripts/package-srt.mjs',native_check_sha256:'scripts/check-dsh-native.mjs',fixture_sha256:'scripts/fixtures/acp.mjs'}))report[key]=hash(file);
fs.writeFileSync(process.argv[process.argv.indexOf('--report')+1],JSON.stringify(report));
`);
  const installer=`#!/bin/bash
set -eu
printf '%s\\0' "$@" > "$(dirname "$0")/installer-args"
${options.failInstaller ? 'exit 7' : ''}
while [[ $# -gt 0 ]];do
 case "$1" in
  --upgrade) upgrade=true;shift;;
  --server-root) server=$2;shift 2;;
  --bin-dir) bin=$2;shift 2;;
  *) shift 2;;
 esac
done
mkdir -p "$server" "$bin"
touch "$server/service.json"
${options.noopUpgrade ? 'if [ ! -e "$server/current" ];then' : ''}
release="releases/$(basename "$(dirname "$0")")"
mkdir -p "$server/$release"
rm -f "$server/current"
ln -s "$release" "$server/current"
${options.noopUpgrade ? 'fi' : ''}
cat > "$bin/teamx" <<'PROGRAM'
#!/bin/sh
if [ "$1" = --version ];then
 echo 'teamx dev-FIXTURE commit=COMMIT dirty=false'
elif [ "$2" = prune ];then
 printf '%s\\n' "$@" > ${shellQuote(p.join(root,'prune-args'))}
 ${options.failPrune ? 'exit 9' : `echo '{"retained_releases":["current","previous"],"deleted_releases":[],"retained_backups":[],"deleted_backups":[]}'`}
else
 echo '{"authenticated":${options.badStatus ? 'false' : 'true'},"ready":${options.notReady ? 'false' : 'true'}}'
fi
PROGRAM
sed "s/COMMIT/$(git -C "$(dirname "$0")/source" rev-parse HEAD)/g" "$bin/teamx" > "$bin/teamx.pending"
mv "$bin/teamx.pending" "$bin/teamx"
chmod +x "$bin/teamx"
`;
  write('packaging/install.sh', installer);
  write('scripts/package.mjs',`
import fs from 'node:fs';import p from 'node:path';import {execFileSync} from 'node:child_process';import {createHash} from 'node:crypto';
const arg=name=>process.argv[process.argv.indexOf(name)+1];
fs.appendFileSync(${JSON.stringify(p.join(root,'build-count'))},'build\\n');
const commit=execFileSync('git',['rev-parse','HEAD'],{encoding:'utf8'}).trim();
const out=arg('--output-dir'),name='teamx-dev-'+commit.slice(0,12)+'-linux-amd64-standard';
const stage=p.join(p.dirname(out),'fixture-payload'),bundle=p.join(stage,name);fs.mkdirSync(bundle,{recursive:true});fs.mkdirSync(out);
const installer=fs.readFileSync('packaging/install.sh','utf8');
fs.writeFileSync(p.join(bundle,'install.sh'),${options.badInstaller ? "installer.replace('#!/bin/bash', '#!/bin/sh')" : 'installer'});
fs.writeFileSync(p.join(bundle,'manifest.json'),JSON.stringify({channel:'dev',target:'linux-amd64',profile:'standard',source:{commit,dirty:false}}));
const archive=p.join(out,name+'.tar.gz');execFileSync('tar',['-czf',archive,'-C',stage,...(${JSON.stringify(!!options.manifestFirst)}?[name+'/manifest.json',name+'/install.sh']:[name])],{env:{...process.env,COPYFILE_DISABLE:'1'}});
const digest=createHash('sha256').update(fs.readFileSync(archive)).digest('hex');
fs.writeFileSync(archive+'.sha256',digest+'  '+name+'.tar.gz\\n');
fs.writeFileSync(p.join(out,name+'-report.json'),JSON.stringify({status:${JSON.stringify(options.badReport?'failed':'passed')},smoke_level:arg('--smoke'),quick_sha256:createHash('sha256').update(fs.readFileSync('scripts/package-quick.mjs')).digest('hex'),archive_sha256:digest,target:'linux-amd64',profile:'standard',
...Object.fromEntries(Object.entries({smoke_sha256:'scripts/package-smoke.mjs',srt_check_sha256:'scripts/package-srt.mjs',native_check_sha256:'scripts/check-dsh-native.mjs',fixture_sha256:'scripts/fixtures/acp.mjs'}).map(([key,file])=>[key,createHash('sha256').update(fs.readFileSync(file)).digest('hex')]))}));
`);
  git(repo,'add','.');git(repo,'commit','-qm','fixture source');
  const commit=sourceCommit(repo);
  const mocks=p.join(root,'retention-mocks');fs.mkdirSync(mocks);
  fs.writeFileSync(p.join(mocks,'systemctl'),'#!/bin/sh\nexit 3\n',{mode:0o755});
  fs.writeFileSync(p.join(mocks,'systemd-run'),`#!/bin/sh\nprintf '%s\\n' "$@" > ${shellQuote(p.join(root,'scheduled-retention'))}\n`,{mode:0o755});
  const config=deploymentConfig({work_dir:p.join(root,'jobs-root'),server_root:p.join(root,'server'),home:p.join(root,'private'),build_path:[mocks],bin_dir:p.join(root,'commands'),execution:'srt',srt_endpoints:['http://127.0.0.1:18088','https://model.example'],deployment_config:p.join(root,'private-initial.toml')});
  function request(name,extra={}) {
    const job=p.join(config.work_dir,'jobs',name);fs.mkdirSync(job,{recursive:true});
    const wantedCommit=sourceCommit(repo);
    makeSourceBundle(repo,p.join(job,'source.bundle'),wantedCommit);
    return {config,commit:wantedCommit,job,...extra};
  }
  return {root,repo,config,commit,request,write,builds:()=>fs.readFileSync(p.join(root,'build-count'),'utf8').trim().split('\n').length};
}

test('source bundle pins HEAD and excludes working tree edits and untracked credentials',t=>{
  const f=fixture(t);f.write('README.md','uncommitted change');f.write('.teamx/auth/admin-token','never upload');
  const req=f.request('snapshot');
  const restored=p.join(f.root,'restored');fs.mkdirSync(restored);git(restored,'init','-q');git(restored,'fetch',p.join(req.job,'source.bundle'),'HEAD');git(restored,'checkout','-q','--detach','FETCH_HEAD');
  assert.equal(git(restored,'rev-parse','HEAD'),f.commit);
  assert.equal(fs.readFileSync(p.join(restored,'README.md'),'utf8'),'committed source');
  assert.equal(fs.existsSync(p.join(restored,'.teamx/auth/admin-token')),false);
});

test('dry run performs no upload or remote job creation',async t=>{
  const f=fixture(t);const result=await deployLocal(f.repo,f.config,{...sshTarget,dryRun:true});
  assert.equal(result.commit,f.commit);assert.equal(fs.existsSync(f.config.work_dir),false);
});

test('committed private state and unsafe configuration are rejected before SSH',t=>{
  const f=fixture(t);f.write('.teamx/config.toml','private');git(f.repo,'add','.');git(f.repo,'commit','-qm','unsafe fixture');
  assert.throws(()=>sourceCommit(f.repo),/Private\/generated/);
  for(const config of [{work_dir:f.config.home,home:f.config.home},{build_path:['/path\ncommand']},{unknown:true}])assert.throws(()=>deploymentConfig(config));
  assert.equal(f.config.keep_releases,2);assert.equal(f.config.keep_backups,2);
  for(const count of [0,1,1.5,'2',null])assert.throws(()=>deploymentConfig({keep_releases:count}));
  for(const count of [0,-1,1.5,'2',null])assert.throws(()=>deploymentConfig({keep_backups:count}));
  assert.equal(execFileSync('sh',['-c',`printf %s ${shellQuote("quote' $() `literal`")}`],{encoding:'utf8'}),"quote' $() `literal`");
});

test('SSH target is required only in arguments and never accepted in JSON',async t=>{
  const f=fixture(t);
  for(const key of ['host','port'])assert.throws(()=>deploymentConfig({...f.config,[key]:key==='host'?sshTarget.host:22}),new RegExp(`--${key}, not in deployment JSON`));
  for(const host of [undefined,'','-oProxyCommand=bad','-user@example.test','user@host;command','https://example.test','example.test\ncommand'])await assert.rejects(deployLocal(f.repo,f.config,{host,dryRun:true}),/Specify --host/);
  for(const port of [0,65536,1.2,'22',NaN])await assert.rejects(deployLocal(f.repo,f.config,{...sshTarget,port,dryRun:true}),/Invalid SSH --port/);
  for(const host of ['teamx-linux','user@example.test','user@[2001:db8::1]'])await deployLocal(f.repo,f.config,{host,port:2222,dryRun:true});
  assert.equal(fs.existsSync(f.config.work_dir),false);
});

test('CLI accepts target flags and rejects missing or malformed values before SSH',t=>{
  const f=fixture(t),script=fileURLToPath(new URL('./deploy-linux.mjs',import.meta.url));
  const config=p.join(f.root,'config.json');fs.writeFileSync(config,JSON.stringify(f.config));
  for(const [args,message] of [
    [[],/Specify --host/],
    [['--host'],/--host requires/],
    [['--host','teamx-linux','--port'],/--port requires/],
    [['--host','teamx-linux','--port','bad'],/--port requires/],
    [['--host','teamx-linux','--port','65536'],/Invalid SSH --port/]
  ]){
    const r=spawnSync(process.execPath,[script,'--config',config,'--dry-run',...args],{encoding:'utf8'});
    assert.equal(r.status,1);assert.match(r.stderr,message);
  }
  const r=spawnSync(process.execPath,[script,'--config',config,'--host','teamx-linux','--port','2222','--dry-run'],{encoding:'utf8'});
  assert.equal(r.status,0,r.stderr);assert.match(r.stdout,/Target: teamx-linux/);
});

test('remote build-only verifies actual archive without touching service',async t=>{
  const f=fixture(t);const result=await deployRemote(f.request('build-only',{build_only:true}),runtime);
  assert.equal(result.status,'built');assert.equal(fs.existsSync(p.join(f.config.server_root,'service.json')),false);
  assert.equal(fs.existsSync(f.config.home),false);
  assert.equal(fs.existsSync(p.join(f.root,'prune-args')),false);
});

test('one metadata pass accepts either member order and refuses an installer differing from committed source', async t => {
  for (const manifestFirst of [false, true]) {
    const f = fixture(t, { manifestFirst });
    const realTar = execFileSync('/bin/sh', ['-c', 'command -v tar'], { encoding: 'utf8' }).trim();
    const tools = p.join(f.root, 'tools'), record = p.join(f.root, 'metadata-tar-calls');
    fs.mkdirSync(tools);
    fs.writeFileSync(p.join(tools, 'tar'), `#!/bin/sh\nprintf '%s\\n' "$*" >> ${shellQuote(record)}\nexec ${shellQuote(realTar)} "$@"\n`, { mode: 0o755 });
    const request = f.request('metadata', { build_only: true });
    const previous = process.env.PATH;
    let result;
    try {
      process.env.PATH = tools + ':' + previous;
      result = await deployRemote(request, runtime);
    } finally { process.env.PATH = previous; }
    assert.equal(result.status, 'built');
    assert.equal(fs.readFileSync(record, 'utf8').trim().split('\n').filter(line => line.startsWith('-xOf ')).length, 1);
    assert.equal(fs.readFileSync(p.join(request.job, 'install.sh'), 'utf8'), fs.readFileSync(p.join(f.repo, 'packaging/install.sh'), 'utf8'));
  }
  const f = fixture(t, { badInstaller: true });
  await assert.rejects(deployRemote(f.request('foreign-installer'), runtime), /installer differs/);
  assert.equal(fs.existsSync(f.config.server_root), false);
});

test('build-only artifacts are reused for deployment and a new commit builds again',async t=>{
  const f=fixture(t);
  const first=await deployRemote(f.request('prepare',{build_only:true}),runtime);
  assert.equal(first.artifact_cache.reused,false);assert.equal(f.builds(),1);
  const deployed=await deployRemote(f.request('deploy'),runtime);
  assert.equal(deployed.artifact_cache.reused,true);assert.equal(f.builds(),1);
  assert.equal(deployed.archive_sha256,first.archive_sha256);assert.equal(deployed.status,'deployed');
  const receipt=JSON.parse(fs.readFileSync(p.join(p.dirname(deployed.archive),'../deployment.json'),'utf8'));
  assert.ok(receipt.duration_ms>=0);assert.ok(receipt.timings.some(x=>x.stage==='reuse verified archive' && x.status==='passed'));
  assert.ok(!receipt.timings.some(x=>x.stage==='build and full smoke'));
  f.write('README.md','next commit');git(f.repo,'add','README.md');git(f.repo,'commit','-qm','new source');
  const next=await deployRemote(f.request('next',{build_only:true}),runtime);
  assert.equal(next.artifact_cache.reused,false);assert.equal(f.builds(),2);assert.notEqual(next.source_commit,first.source_commit);
});

test('an installer failure retains a verified artifact for retry without rebuilding',async t=>{
  const f=fixture(t,{failInstaller:true});
  for(const name of ['first-failure','retry-failure'])await assert.rejects(deployRemote(f.request(name),runtime),/exited 7/);
  assert.equal(f.builds(),1);
  const receipt=JSON.parse(fs.readFileSync(p.join(f.config.work_dir,'jobs/retry-failure/deployment.json'),'utf8'));
  assert.equal(receipt.status,'failed');assert.equal(receipt.artifact_cache.reused,true);
  assert.ok(receipt.timings.some(x=>x.status==='failed'));
});

test('modified archives, foreign cache identity and changed smoke reports block reuse before installing',async t=>{
  for(const variant of ['archive','identity','report']){
    const f=fixture(t),built=await deployRemote(f.request('build',{build_only:true}),runtime);
    const cached=built.artifact_cache.path;
    if(variant==='archive')fs.appendFileSync(p.join(cached,p.basename(built.archive)),'modified');
    if(variant==='identity'){
      const file=p.join(cached,'record.json'),record=JSON.parse(fs.readFileSync(file,'utf8'));
      record.identity.commit='b'.repeat(40);fs.writeFileSync(file,JSON.stringify(record));
    }
    if(variant==='report'){
      const file=p.join(cached,p.basename(built.archive).replace('.tar.gz','-report.json'));
      const report=JSON.parse(fs.readFileSync(file,'utf8'));report.smoke_sha256='0'.repeat(64);fs.writeFileSync(file,JSON.stringify(report));
    }
    await assert.rejects(deployRemote(f.request('refuse'),runtime),/checksum mismatch|identity mismatch|verifier mismatch/);
    assert.equal(fs.existsSync(f.config.server_root),false);assert.equal(f.builds(),1);
  }
});

test('remote installer automatically selects install then upgrade and preserves private home',async t=>{
  const f=fixture(t);
  const install=f.request('install');
  const first=await deployRemote(install,runtime);assert.equal(first.action,'install');assert.equal(first.status,'deployed');
  const firstArgs=fs.readFileSync(p.join(install.job,'installer-args'),'utf8').split('\0');
  for(const flag of ['--home','--user','--execution','--deployment-config','--srt-endpoint'])assert.ok(firstArgs.includes(flag));
  assert.ok(!firstArgs.includes('--upgrade'));
  fs.mkdirSync(f.config.home);fs.writeFileSync(p.join(f.config.home,'identity'),'original identity');
  const upgrade=f.request('upgrade');
  const second=await deployRemote(upgrade,runtime);assert.equal(second.action,'upgrade');assert.equal(second.status,'deployed');
  const nextArgs=fs.readFileSync(p.join(upgrade.job,'installer-args'),'utf8').split('\0');
  for(const flag of ['--home','--user','--execution','--deployment-config','--srt-endpoint'])assert.ok(!nextArgs.includes(flag));
  assert.ok(nextArgs.includes('--upgrade'));assert.ok(nextArgs.includes('--backup-dir'));
  assert.ok(second.backup.startsWith(f.config.backup_root+'/before-'));
  assert.equal(fs.readFileSync(p.join(f.config.home,'identity'),'utf8'),'original identity');
  assert.equal(second.retention.status,'queued');
  assert.equal(fs.existsSync(p.join(f.root,'prune-args')),false);
  const retained=await runRetention(JSON.parse(fs.readFileSync(p.join(upgrade.job,'retention-request.json'),'utf8')));
  const cleanup=fs.readFileSync(p.join(f.root,'prune-args'),'utf8').trim().split('\n');
  assert.deepEqual(cleanup,['service','prune','--server-root',f.config.server_root,'--keep-releases','2','--keep-backups','2','--from',second.backup]);
  assert.deepEqual(retained.result.retained_releases,['current','previous']);
  assert.ok(second.timings.some(x=>x.stage==='queue release and backup retention' && x.status==='passed'));
});

test('failed verification prevents installation; installer failure produces a failed receipt',async t=>{
  const bad=fixture(t,{badReport:true}),badReq=bad.request('bad-report');
  await assert.rejects(deployRemote(badReq,runtime),/Build report mismatch/);
  assert.equal(fs.existsSync(bad.config.server_root),false);
  const failed=fixture(t,{failInstaller:true}),req=failed.request('failed-install');
  await assert.rejects(deployRemote(req,runtime),/exited 7/);
  assert.equal(JSON.parse(fs.readFileSync(p.join(req.job,'deployment.json'),'utf8')).status,'failed');
  assert.equal(fs.existsSync(p.join(failed.root,'prune-args')),false);
});

test('identity check failure skips cleanup; cleanup failure preserves deployed success',async t=>{
  const bad=fixture(t,{badStatus:true});
  await assert.rejects(deployRemote(bad.request('bad-status'),runtime),/did not authenticate/);
  assert.equal(fs.existsSync(p.join(bad.root,'prune-args')),false);
  const unready=fixture(t,{notReady:true});
  await assert.rejects(deployRemote(unready.request('not-ready'),runtime),/not ready/);
  assert.equal(fs.existsSync(p.join(unready.root,'prune-args')),false);
  const f=fixture(t,{failPrune:true});f.config.keep_releases=3;f.config.keep_backups=4;
  const req=f.request('cleanup-failed'),result=await deployRemote(req,runtime);
  assert.equal(result.status,'deployed');assert.equal(result.retention.status,'queued');
  await assert.rejects(runRetention(JSON.parse(fs.readFileSync(p.join(req.job,'retention-request.json'),'utf8'))),/exited 9/);
  assert.equal(JSON.parse(fs.readFileSync(p.join(req.job,'retention.json'),'utf8')).status,'failed');
  const args=fs.readFileSync(p.join(f.root,'prune-args'),'utf8').trim().split('\n');
  assert.equal(args[args.indexOf('--keep-releases')+1],'3');assert.equal(args[args.indexOf('--keep-backups')+1],'4');
  assert.equal(args.includes('--from'),false);
  assert.equal(JSON.parse(fs.readFileSync(p.join(req.job,'deployment.json'),'utf8')).status,'deployed');
});

test('same-archive no-op does not enroll a nonexistent backup',async t=>{
  const f=fixture(t,{noopUpgrade:true});
  await deployRemote(f.request('install'),runtime);
  const req=f.request('no-op');const result=await deployRemote(req,runtime);
  await runRetention(JSON.parse(fs.readFileSync(p.join(req.job,'retention-request.json'),'utf8')));
  assert.equal(result.status,'deployed');assert.equal(result.backup,undefined);
  assert.equal(fs.readFileSync(p.join(f.root,'prune-args'),'utf8').trim().split('\n').includes('--from'),false);
});

test('SSH transport executes the committed runner and helper despite working tree changes',async t=>{
  const f=fixture(t),mock=p.join(f.root,'mocks');fs.mkdirSync(mock);
  f.write('scripts/deploy-linux-remote.mjs',`throw Error('uncommitted runner must not execute');\n`);
  f.write('scripts/package-progress.mjs',`throw Error('uncommitted helper must not execute');\n`);
  const program=p.join(mock,'ssh.mjs');
  fs.writeFileSync(program,`import {spawnSync} from 'node:child_process';import fs from 'node:fs';const cmd=process.argv.at(-1);fs.appendFileSync(${JSON.stringify(p.join(f.root,'ssh-arguments'))},JSON.stringify(process.argv.slice(2))+'\\n');if(!cmd.startsWith('umask '))fs.writeFileSync(${JSON.stringify(p.join(f.root,'remote-command'))},cmd);const r=spawnSync('sh',['-c',cmd],{stdio:'inherit'});process.exit(r.status??1);`);
  fs.writeFileSync(p.join(mock,'ssh'),`#!/bin/sh\nexec ${shellQuote(process.execPath)} ${shellQuote(program)} "$@"\n`,{mode:0o755});
  const previous=process.env.PATH;process.env.PATH=mock+':'+previous;t.after(()=>{process.env.PATH=previous;});
  const result=await deployLocal(f.repo,f.config,{...sshTarget,port:2222,buildOnly:true});
  const request=JSON.parse(fs.readFileSync(p.join(result.job,'request.json'),'utf8'));
  assert.equal(request.commit,f.commit);assert.equal(request.build_only,true);
  for(const [source,target] of [['scripts/deploy-linux-remote.mjs','runner.mjs'],['scripts/package-progress.mjs','package-progress.mjs']])
    assert.equal(fs.readFileSync(p.join(result.job,target),'utf8').trim(),git(f.repo,'show',`${f.commit}:${source}`));
  assert.equal(fs.readFileSync(p.join(result.job,'runner-executed'),'utf8'),'committed progress');
  assert.equal(Object.hasOwn(request.config,'host'),false);assert.equal(Object.hasOwn(request.config,'port'),false);
  const calls=fs.readFileSync(p.join(f.root,'ssh-arguments'),'utf8').trim().split('\n').map(JSON.parse);
  assert.equal(calls.length,2);
  for(const args of calls){assert.equal(args.at(-2),sshTarget.host);assert.equal(args[args.indexOf('-p')+1],'2222');}
  assert.equal(git(f.repo,'bundle','list-heads',p.join(result.job,'source.bundle')).split(/\s/)[0],f.commit);
  assert.ok(fs.readFileSync(p.join(f.root,'remote-command'),'utf8').includes('runner.mjs'));
});

test('non-native targets fail before source extraction or deployment',async t=>{
  const f=fixture(t),req=f.request('wrong-target');
  await assert.rejects(deployRemote(req,{...runtime,arch:'arm64'}),/Native Linux amd64/);
  assert.equal(fs.existsSync(p.join(req.job,'source')),false);
});

test('SSH failure aborts transport instead of reporting a completed build',async t=>{
  const f=fixture(t),mock=p.join(f.root,'failed-ssh');fs.mkdirSync(mock);
  fs.writeFileSync(p.join(mock,'ssh'),'#!/bin/sh\nexit 23\n',{mode:0o755});
  const previous=process.env.PATH;process.env.PATH=mock+':'+previous;t.after(()=>{process.env.PATH=previous;});
  await assert.rejects(deployLocal(f.repo,f.config,{...sshTarget,buildOnly:true}),/exited/);
  assert.equal(fs.existsSync(f.config.server_root),false);
});

test('an unavailable installed commit selects full smoke without a fatal Git diagnostic',t=>{
  const f=fixture(t),module=new URL('./deploy-linux-remote.mjs',import.meta.url).href;
  const result=spawnSync(process.execPath,['--input-type=module','-e',`import {deploymentSmoke} from ${JSON.stringify(module)};console.log(deploymentSmoke(process.argv[1],process.argv[2]));`,f.repo,'b'.repeat(40)],{encoding:'utf8'});
  assert.equal(result.status,0);assert.equal(result.stdout,'full\n');assert.equal(result.stderr,'');
});

test('application changes use quick checks; full smoke upgrades a cached report without rebuilding',async t=>{
  const f=fixture(t);
  const first=await deployRemote(f.request('initial'),runtime);
  assert.equal(first.smoke_level,'full');
  const baseline=f.commit;
  f.write('internal/web/assets/app.js','application change');git(f.repo,'add','.');git(f.repo,'commit','-qm','application');
  assert.equal(deploymentSmoke(f.repo,baseline),'quick');
  assert.equal(deploymentSmoke(f.repo,baseline,true),'full');
  assert.equal(deploymentSmoke(f.repo,'b'.repeat(40)),'full');
  const quick=await deployRemote(f.request('quick',{build_only:true}),runtime);
  assert.equal(quick.smoke_level,'quick');assert.equal(f.builds(),2);
  const full=await deployRemote(f.request('promote',{build_only:true,full_smoke:true}),runtime);
  assert.equal(full.smoke_level,'full');assert.equal(full.artifact_cache.reused,true);assert.equal(f.builds(),2);
  const again=await deployRemote(f.request('cached-full',{build_only:true,full_smoke:true}),runtime);
  assert.equal(again.smoke_level,'full');assert.ok(!again.timings.some(s=>s.stage==='full smoke for cached archive'));
  f.write('internal/room/service_backup.go','installer change');git(f.repo,'add','.');git(f.repo,'commit','-qm','installer');
  assert.equal(deploymentSmoke(f.repo,baseline),'full');
});

test('deployment receipts publish complete private JSON atomically',t=>{
  const f=fixture(t),file=p.join(f.root,'receipt.json');
  for(const status of ['preparing','deploying','deployed']){
    saveDeployment(file,{status});assert.deepEqual(JSON.parse(fs.readFileSync(file,'utf8')),{status});
    assert.equal(fs.statSync(file).mode&0o777,0o600);assert.equal(fs.existsSync(file+'.pending'),false);
  }
});

test('retention scheduling failure is recorded without changing successful deployment',async t=>{
  const f=fixture(t);
  fs.writeFileSync(p.join(f.config.build_path[0],'systemd-run'),'#!/bin/sh\nexit 8\n',{mode:0o755});
  const req=f.request('cannot-queue');
  const result=await deployRemote(req,runtime);
  assert.equal(result.status,'deployed');assert.equal(result.retention.status,'failed');
  assert.equal(JSON.parse(fs.readFileSync(p.join(req.job,'retention.json'),'utf8')).status,'failed');
  assert.equal(fs.existsSync(p.join(f.root,'prune-args')),false);
});

test('the next installation waits for earlier cleanup without serializing its build',async t=>{
  const f=fixture(t),req=f.request('queued-cleanup');
  const result=await deployRemote(req,runtime);
  fs.writeFileSync(p.join(f.config.build_path[0],'systemctl'),'#!/bin/sh\necho active\n',{mode:0o755});
  let finished=false;
  const timer=setTimeout(()=>{
    finished=true;
    saveDeployment(result.retention.receipt,{status:'passed',finished_at:new Date().toISOString()});
  },100);
  try {
    await waitForRetention(req,{...process.env,PATH:f.config.build_path.join(':')+':'+process.env.PATH},0);
    assert.equal(finished,true,'installation bypassed active earlier cleanup');
  } finally { clearTimeout(timer); }
});
