import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import p from 'node:path';
import {execFileSync} from 'node:child_process';
import http from 'node:http';
import {checkApplication,checkLoginAssets} from './package-quick.mjs';

test('login probes follow multiline imports and reject protected modules or exposed private APIs',async t=>{
  let moduleStatus=401,privateStatus=401;
  const server=http.createServer((req,res)=>{
    if(req.url==='/access'){res.setHeader('content-type','text/html');res.end('<script type="module" src="/ui/login.mjs"></script>');}
    else if(req.url==='/ui/login.mjs'){res.setHeader('content-type','text/javascript');res.end("import {\n  login\n} from './dependency.mjs';");}
    else if(req.url==='/ui/dependency.mjs'){res.writeHead(moduleStatus,{'content-type':'text/javascript'});res.end('export const login=1;');}
    else if(req.url==='/v1/access/method'){res.end(JSON.stringify({provider:'token'}));}
    else {res.writeHead(privateStatus);res.end('{}');}
  });
  await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
  t.after(()=>new Promise(resolve=>server.close(resolve)));
  const url='http://127.0.0.1:'+server.address().port;
  await assert.rejects(checkLoginAssets(url),/dependency.mjs.*401/);
  moduleStatus=200;assert.ok((await checkLoginAssets(url)).assets.includes('/ui/dependency.mjs'));
  privateStatus=200;await assert.rejects(checkLoginAssets(url),/Private application route/);
});

test('quick check starts the real application, loads login dependencies and rejects a foreign build',async t=>{
  const bundle=fs.mkdtempSync(p.join(os.tmpdir(),'teamx-quick-test-'));
  t.after(()=>fs.rmSync(bundle,{recursive:true,force:true}));
  fs.mkdirSync(p.join(bundle,'bin'));
  const commit='a'.repeat(40);
  execFileSync('go',['build','-ldflags',`-X github.com/go-aie/teamx/internal/room.distributionVersion=dev-fixture -X github.com/go-aie/teamx/internal/room.distributionCommit=${commit} -X github.com/go-aie/teamx/internal/room.distributionDirty=false`,'-o',p.join(bundle,'bin/teamx'),'./cmd/teamx'],{stdio:'inherit'});
  fs.mkdirSync(p.join(bundle,'share/teamx'),{recursive:true});
  fs.cpSync('.teamx/teams',p.join(bundle,'share/teamx/teams'),{recursive:true});
  fs.writeFileSync(p.join(bundle,'bin/dsh'),'#!/bin/sh\necho fixture\n',{mode:0o755});
  const manifest={version:'dev-fixture',source:{commit,dirty:false},profile:'core',target:`${process.platform}-${process.arch==='x64'?'amd64':process.arch}`};
  fs.writeFileSync(p.join(bundle,'manifest.json'),JSON.stringify(manifest));
  const report=await checkApplication(bundle);
  assert.equal(report.status,'passed');assert.equal(report.smoke_level,'quick');
  assert.ok(report.checks.includes('private route authorization and authenticated setup'));
  manifest.source.commit='b'.repeat(40);fs.writeFileSync(p.join(bundle,'manifest.json'),JSON.stringify(manifest));
  await assert.rejects(checkApplication(bundle),/build identity mismatch/);
});
