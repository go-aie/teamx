// Offline packaging evidence only. Never copied into an archive or used by
// real-provider acceptance. The fixed ACP fixture does not call an upstream.
import fs from 'node:fs';
import path from 'node:path';
import {createHash} from 'node:crypto';
const hash = value => createHash('sha256').update(value).digest('hex');
export function seedOfflineModels(home, binary, version, agent = {source:'packaging-fixture',version:'1'}) {
  const credential = 'cred-'+'a'.repeat(32);
  const model = {protocol:'openai-completions',base_url:'http://127.0.0.1:1/v1',model_id:'packaging-fixture',api_key_env:'OPENAI_API_KEY'};
  const digest = hash(JSON.stringify({Model:model,Credential:credential}));
  for (const name of ['model-credentials','model-validations']) fs.mkdirSync(path.join(home,name),{mode:0o700,recursive:true});
  fs.writeFileSync(path.join(home,'model-credentials',credential),'offline-fixture-only',{mode:0o600});
  fs.writeFileSync(path.join(home,'model-validations',digest+'.json'),JSON.stringify({entry_id:'daily',revision:1,execution_digest:digest,credential_ref:credential,result:{identity:{binary_path:fs.realpathSync(binary),binary_sha256:hash(fs.readFileSync(fs.realpathSync(binary))),version,agent},text:true,tool_round_trip:true,cleanup:true}}),{mode:0o600});
  const config = path.join(home,'config.toml');
  let source = fs.readFileSync(config,'utf8').replace(/binary_path = [^\n]+/,`binary_path = ${JSON.stringify(binary)}`);
  const catalog = `[models]\nschema_version = 1\nversion = 1\ndefault = 'daily'\n[[models.entries]]\nid = 'daily'\nname = 'Offline fixture'\nrevision = 1\ncredential_ref = '${credential}'\nprotocol = 'openai-completions'\nbase_url = 'http://127.0.0.1:1/v1'\nmodel_id = 'packaging-fixture'\n`;
  source = source.replace(/\[models\][\s\S]*?(?=\n\[|$)/,catalog);
  fs.writeFileSync(config,source,{mode:0o600});
}
