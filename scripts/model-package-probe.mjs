// Final native archive acceptance: immutable payload, real browser first-use,
// real provider/tool round trip and resumed dialogue. Requires a managed source
// home solely to read the explicitly selected provider parameters/key.
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {hash,json,inventory,extract,must} from './package-lib.mjs';
import {acceptBrowser} from './package-browser.mjs';
const [archiveArg,sourceArg,outputArg]=process.argv.slice(2);
must(archiveArg&&sourceArg&&outputArg,'model-package-probe.mjs ARCHIVE MANAGED_SOURCE_HOME NEW_OUTPUT_DIR');
const archive=path.resolve(archiveArg),source=path.resolve(sourceArg),output=path.resolve(outputArg);
fs.mkdirSync(output,{mode:0o700});
const report={status:'failed',scope:'native standard archive smoke reference + browser + real first-use/restart',checks:{},release_ready:false,remote_platform_writes:0};
const env={...process.env};for(const k of Object.keys(env))if(/KEY|TOKEN|SECRET|DSH|TEAMX/.test(k))delete env[k];env.OPENAI_API_KEY='';
async function child(argv,name){
 const log=fs.openSync(path.join(output,name+'.log'),'wx',0o600);
 try{await new Promise((resolve,reject)=>{const p=spawn(process.execPath,argv,{env,stdio:['ignore',log,log]});p.once('error',reject);p.once('exit',code=>code===0?resolve():reject(Error(`${name} failed: ${code}; inspect ${name}.log`)));});}finally{fs.closeSync(log);}
}
try{
 report.archive_sha256=hash(fs.readFileSync(archive));
 const extracted=path.join(output,'archive');extract(archive,extracted);
 const entries=fs.readdirSync(extracted);must(entries.length===1,'Archive must have one root');
 const bundle=path.join(extracted,entries[0]),manifest=json(path.join(bundle,'manifest.json'));
 must(manifest.target===`${process.platform}-${process.arch==='x64'?'amd64':process.arch}`,'Requires native matching archive');
 report.target=manifest.target;report.profile=manifest.profile;report.source=manifest.source;
 const expected=json(path.join(bundle,'checksums.txt'));
 const unchanged=()=>must(JSON.stringify(inventory(bundle).filter(f=>f.path!=='checksums.txt'))===JSON.stringify(expected),'Immutable archive payload changed');
 unchanged();report.checks.payload='passed';
 const browserOutput=path.join(output,'browser');fs.mkdirSync(browserOutput,{mode:0o700});
 report.checks.browser=await acceptBrowser({bundle,output:browserOutput,env});
 fs.writeFileSync(path.join(browserOutput,'report.json'),JSON.stringify(report.checks.browser,null,2),{mode:0o600});
 process.stdout.write('final archive production browser and independent home passed\n');
 const realOutput=path.join(output,'first-use');fs.mkdirSync(realOutput,{mode:0o700});
 await child([fileURLToPath(new URL('./model-first-use-probe.mjs',import.meta.url)),realOutput,source,bundle,'archive'],'first-use');
 report.checks.real_first_use=json(path.join(realOutput,'report.json'));
 must(report.checks.real_first_use.status==='passed','Real first-use did not pass');
 process.stdout.write('final archive real first-use, fixed tool and resumed dialogue passed\n');
 unchanged();report.checks.immutable_after='passed';report.browser=report.checks.browser.browser;
 report.environment={platform:process.platform,arch:process.arch,release:os.release()};
 report.status='passed';
}catch(error){report.error=error.stack;throw error;}
finally{fs.writeFileSync(path.join(output,'report.json'),JSON.stringify(report,null,2),{mode:0o600});}
