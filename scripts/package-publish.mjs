import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';
import { extract, hash, json, must, run, writeJSON } from './package-lib.mjs';
import { checkAssessment, inspectCandidate, releasePlan } from './package-release.mjs';
import { hasDependency } from './package-profiles.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const digest = path => hash(fs.readFileSync(path));
const positive = n => /^[1-9]\d*$/.test(String(n)) && Number.isSafeInteger(Number(n));
const ownerRepo = repo => /^[\w.-]+\/[\w.-]+$/.test(repo || '');
export const candidateName = (version, row, attempt) => `candidate-${version}-${row.target}-${row.profile}-${attempt}`;
export const assessmentName = (row, attempt) => `release-assessment-${row.target}-${row.profile}-${attempt}`;

export function validateCandidateRun(info, artifacts, context, matrix) {
  must(ownerRepo(context.repository) && positive(context.candidate_run_id) && positive(context.candidate_run_attempt), 'Invalid candidate run identity');
  must(info.id === Number(context.candidate_run_id) && info.run_attempt === Number(context.candidate_run_attempt) &&
    info.repository?.full_name === context.repository && info.head_repository?.full_name === context.repository &&
    info.event === 'push' && info.path === '.github/workflows/release.yml' && info.head_sha === context.commit &&
    info.head_branch === context.version && info.status === 'completed' && info.conclusion === 'success',
    'Candidate run must be the successful tag workflow at this exact commit/attempt');
  const required = matrix.flatMap(row => [candidateName(context.version, row, context.candidate_run_attempt),
    `candidate-verification-${context.version}-${row.target}-${row.profile}-${context.candidate_run_attempt}`]);
  for (const name of required) {
    const found = artifacts.filter(a => a.name === name);
    must(found.length === 1 && found[0].expired === false && positive(found[0].id), `Missing, expired or ambiguous candidate artifact: ${name}`);
  }
}

// A non-404 failure is never evidence that a release or tag does not exist.
export async function githubRequest(repository, endpoint, { method = 'GET', body, missing = false } = {}) {
  must(ownerRepo(repository), 'Invalid GitHub repository');
  must(process.env.GH_TOKEN, 'GH_TOKEN is required');
  const response = await fetch(`https://api.github.com/repos/${repository}/${endpoint}`, {
    method, headers: { Authorization: `Bearer ${process.env.GH_TOKEN}`, Accept: 'application/vnd.github+json',
      'X-GitHub-Api-Version': '2022-11-28', ...(body ? { 'Content-Type': 'application/json' } : {}) },
    ...(body ? { body: JSON.stringify(body) } : {}), signal: AbortSignal.timeout(60000)
  });
  if (response.status === 404 && missing) return undefined;
  must(response.ok, `GitHub ${method} ${endpoint} failed (HTTP ${response.status})`);
  return response.json();
}

async function list(repository, endpoint, field, api = githubRequest) {
  const result = [];
  for (let page = 1; ; page++) {
    const response = await api(repository, `${endpoint}?per_page=100&page=${page}`);
    const entries = field ? response[field] : response;
    must(Array.isArray(entries), 'Invalid GitHub listing');
    result.push(...entries);
    if (entries.length < 100) return result;
  }
}

export async function candidateContext(version, runID, attempt, sourceRoot = root, env = process.env, api = githubRequest) {
  const plan = releasePlan(sourceRoot, version);
  must(env.GITHUB_REF === `refs/tags/${version}` && env.GITHUB_SHA === plan.commit,
    'Dispatch publication on the exact candidate tag');
  const context = { repository: env.GITHUB_REPOSITORY, version, commit: plan.commit,
    candidate_run_id: Number(runID), candidate_run_attempt: Number(attempt) };
  must(positive(runID) && positive(attempt) && ownerRepo(context.repository), 'Invalid candidate run/repository');
  const info = await api(context.repository, `actions/runs/${context.candidate_run_id}`);
  const artifacts = await list(context.repository, `actions/runs/${context.candidate_run_id}/artifacts`, 'artifacts', api);
  validateCandidateRun(info, artifacts, context, plan.matrix.include);
  return { ...plan, context };
}

function checkContext(context, sourceRoot) {
  must(ownerRepo(context.repository) && positive(context.candidate_run_id) && positive(context.candidate_run_attempt) &&
    positive(context.publish_run_id) && positive(context.publish_run_attempt), 'Invalid release context');
  const plan = releasePlan(sourceRoot, context.version);
  must(plan.commit === context.commit, 'Release context commit differs');
  return plan.matrix.include;
}

// No target code is executed here. Each target has already run its native
// acceptance; the release host rechecks all bytes, identities and qualifications.
export function assembleRelease(candidates, assessments, context, output, sourceRoot = root) {
  const matrix = checkContext(context, sourceRoot);
  const lock = json(p.join(sourceRoot, 'packaging/dependencies.lock.json'));
  must(matrix.every(row => !hasDependency(row.profile, 'git', lock) || !lock.targets[row.target].blockers.includes('git_tls_runtime')),
    'Linux full Git TLS runtime remains blocked');
  const expectedCandidates = matrix.map(row => candidateName(context.version, row, context.candidate_run_attempt)).sort();
  const expectedAssessments = matrix.map(row => assessmentName(row, context.publish_run_attempt)).sort();
  must(same(fs.readdirSync(candidates).sort(), expectedCandidates), 'Candidate matrix incomplete or unexpected');
  must(same(fs.readdirSync(assessments).sort(), expectedAssessments), 'Acceptance matrix incomplete or unexpected');
  must(!fs.existsSync(output), 'Release output already exists');
  const temporary = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx-release-stage-'));
  const stage = p.join(temporary, 'assets'), notices = p.join(temporary, 'licenses');
  fs.mkdirSync(stage); fs.mkdirSync(notices);
  const records = [];
  try {
    for (const row of matrix) {
      const dir = p.join(candidates, candidateName(context.version, row, context.candidate_run_attempt));
      const receipt = inspectCandidate(dir, row.target, row.profile, context.commit, context.version, sourceRoot);
      must(same(json(p.join(dir, 'candidate.json')), receipt), 'Candidate receipt differs at aggregation');
      const evidenceDir = p.join(assessments, assessmentName(row, context.publish_run_attempt));
      must(same(fs.readdirSync(evidenceDir), ['assessment.json']), 'Unexpected public acceptance files');
      const assessment = checkAssessment(receipt, json(p.join(evidenceDir, 'assessment.json')), sourceRoot);
      const assessmentFile = receipt.archive.replace('.tar.gz', '-assessment.json');
      writeJSON(p.join(stage, assessmentFile), assessment);
      fs.copyFileSync(p.join(dir, receipt.archive), p.join(stage, receipt.archive));
      fs.copyFileSync(p.join(dir, receipt.archive + '.sha256'), p.join(stage, receipt.archive + '.sha256'));
      const extracted = p.join(temporary, 'extracted');
      extract(p.join(stage, receipt.archive), extracted);
      const bundle = p.join(extracted, receipt.archive.slice(0, -7));
      for (const file of ['LICENSE', 'README.zh.md']) must(fs.statSync(p.join(bundle, file)).isFile(), `Missing release material: ${file}`);
      const thirdParty = p.join(bundle, 'THIRD_PARTY_NOTICES');
      must(fs.statSync(thirdParty).isDirectory(), 'Missing third-party notices');
      const licenseDirectory = p.join(notices, `${row.target}-${row.profile}`);
      fs.mkdirSync(licenseDirectory);
      fs.copyFileSync(p.join(bundle, 'LICENSE'), p.join(licenseDirectory, 'LICENSE'));
      // Full Git corresponding sources remain in each original bundle; don't
      // duplicate their large trees in the combined notice archive.
      fs.cpSync(thirdParty, p.join(licenseDirectory, 'THIRD_PARTY_NOTICES'), {
        recursive: true, filter: path => path !== p.join(thirdParty, 'git-source')
      });
      records.push({ target: row.target, profile: row.profile, archive: receipt.archive,
        archive_sha256: receipt.archive_sha256, manifest_sha256: assessment.manifest_sha256,
        assessment: assessmentFile, assessment_sha256: digest(p.join(stage, assessmentFile)),
        signing: row.target.startsWith('darwin-') ? receipt.security.signing.mode : 'not_applicable',
        licenses: `${row.target}-${row.profile}/`, release_ready: true });
      fs.rmSync(extracted, { recursive: true, force: true });
    }
    fs.writeFileSync(p.join(notices, 'README.zh.md'), '各目标的许可文本与部署材料见对应目录。full 的完整 Git 对应源码与构建材料保留在原发行归档的 THIRD_PARTY_NOTICES/git-source/ 中；其他依赖的许可也保留在各归档运行时目录。\n');
    run('tar', ['-czf', p.join(stage, 'LICENSES.tar.gz'), '-C', temporary, 'licenses'], { env: { ...process.env, COPYFILE_DISABLE: '1' } });
    fs.copyFileSync(p.join(sourceRoot, 'docs/installation.zh.md'), p.join(stage, 'README.zh.md'));
    writeJSON(p.join(stage, 'release.json'), { schema_version: 1, kind: 'qualified_release',
      release_ready: true, context, policy: { macos_signing: 'optional_unsigned_allowed' },
      dependency_lock_sha256: digest(p.join(sourceRoot, 'packaging/dependencies.lock.json')), bundles: records });
    const rows = records.map(r => `| ${r.target} | ${r.profile} | ${r.archive} |`).join('\n');
    const profiles = [...new Set(records.map(r => r.profile))].join('/');
    fs.writeFileSync(p.join(stage, 'release-notes.md'), `TeamX ${context.version}\n\n四个平台的 ${profiles} 发行包已完成归档身份、真实 DSH/ACP、浏览器及各自用户环境验收。包含平台 CLI 的包另有登录、只读与绑定验收，包含 Git 的包另有 Git/TLS 验收；人工验收与自动检查分别记录。推荐 standard，Git Workspace 及需要本地 Git 的命令需自行安装 Git。\n\nmacOS 当前允许未签名、未公证发行，下载后的系统提示及首次使用、升级方法见 README.zh.md。DSH 0.2.2-rc.1 为预发布依赖；Scion 暂缓。\n\n| 平台 | 能力 | 下载文件 |\n| --- | --- | --- |\n${rows}\n\n校验和见 SHA256SUMS，逐包验收见 *-assessment.json，许可汇总见 LICENSES.tar.gz。包内 manifest 保留候选构建时的状态，正式资格由外部 release.json 和绑定同一归档摘要的验收报告说明；没有重新构建或修改候选包。\n\n源码 commit：${context.commit}\n\n<!-- teamx-release:${context.commit} -->\n`);
    const files = fs.readdirSync(stage).sort();
    fs.writeFileSync(p.join(stage, 'SHA256SUMS'), files.map(name => `${digest(p.join(stage, name))}  ${name}\n`).join(''));
    fs.mkdirSync(p.dirname(output), { recursive: true });
    fs.cpSync(stage, output, { recursive: true, errorOnExist: true, force: false });
    return { checksums_sha256: digest(p.join(output, 'SHA256SUMS')), bundles: records.length };
  } finally { fs.rmSync(temporary, { recursive: true, force: true }); }
}

export function checkReleaseDirectory(directory, context, expectedSeal, sourceRoot = root) {
  const matrix = checkContext(context, sourceRoot);
  must(/^[a-f0-9]{64}$/.test(expectedSeal) && digest(p.join(directory, 'SHA256SUMS')) === expectedSeal, 'Release checksum seal differs');
  const entries = fs.readFileSync(p.join(directory, 'SHA256SUMS'), 'utf8').trimEnd().split('\n').map(line => {
    const match = /^([a-f0-9]{64})  ([A-Za-z0-9][A-Za-z0-9._-]*)$/.exec(line);
    must(match, 'Unsafe release checksum entry');
    return { sha256: match[1], name: match[2] };
  });
  const names = entries.map(e => e.name);
  must(new Set(names).size === names.length && !names.includes('SHA256SUMS') &&
    same(fs.readdirSync(directory).sort(), [...names, 'SHA256SUMS'].sort()), 'Release contains missing or unexpected assets');
  for (const name of [...names, 'SHA256SUMS']) must(fs.lstatSync(p.join(directory, name)).isFile(), 'Release asset is not a regular file');
  for (const entry of entries) must(digest(p.join(directory, entry.name)) === entry.sha256, `Release asset changed: ${entry.name}`);
  const report = json(p.join(directory, 'release.json'));
  must(report.kind === 'qualified_release' && report.release_ready === true && same(report.context, context) &&
    report.dependency_lock_sha256 === digest(p.join(sourceRoot, 'packaging/dependencies.lock.json')) &&
    same(report.bundles.map(r => `${r.target}/${r.profile}`).sort(), matrix.map(r => `${r.target}/${r.profile}`).sort()) &&
    report.bundles.every(r => r.release_ready === true), 'Qualified release identity/matrix differs');
  return [...entries, { name: 'SHA256SUMS', sha256: expectedSeal }];
}

async function remoteTag(context, api) {
  let ref = await api(context.repository, `git/ref/tags/${context.version}`), object = ref.object;
  for (let depth = 0; object?.type === 'tag' && depth < 8; depth++)
    object = (await api(context.repository, `git/tags/${object.sha}`)).object;
  must(object?.type === 'commit' && object.sha === context.commit, 'Remote tag moved or differs from accepted source');
}

async function remoteDigest(repository, asset) {
  const response = await fetch(`https://api.github.com/repos/${repository}/releases/assets/${asset.id}`, {
    headers: { Authorization: `Bearer ${process.env.GH_TOKEN}`, Accept: 'application/octet-stream',
      'X-GitHub-Api-Version': '2022-11-28' }, signal: AbortSignal.timeout(10 * 60000)
  });
  must(response.ok && !response.headers.get('content-type')?.includes('json'), 'Cannot download uploaded release asset');
  const hash = createHash('sha256');
  for await (const chunk of response.body) hash.update(chunk);
  return hash.digest('hex');
}

// Injectable transport permits failure/recovery tests without creating a remote
// Release. No clobber: only a draft bearing this exact checksum seal can resume.
export async function publishRelease(directory, context, seal, { sourceRoot = root, api = githubRequest,
  downloadHash = remoteDigest, upload = (version, paths, repository) =>
    run('gh', ['release', 'upload', version, '--repo', repository, '--', ...paths], { stdio: 'inherit' }) } = {}) {
  const entries = checkReleaseDirectory(directory, context, seal, sourceRoot);
  await remoteTag(context, api);
  const marker = `<!-- teamx-assets:${seal} -->`;
  const body = fs.readFileSync(p.join(directory, 'release-notes.md'), 'utf8') + '\n' + marker + '\n';
  let release = await api(context.repository, `releases/tags/${context.version}`, { missing: true });
  if (release) must(release.draft === true && release.tag_name === context.version && release.body === body,
    'Existing release is published or belongs to different accepted assets; refusing overwrite');
  else release = await api(context.repository, 'releases', { method: 'POST', body: {
    tag_name: context.version, target_commitish: context.commit, name: `TeamX ${context.version}`, body, draft: true, prerelease: false
  } });
  must(positive(release.id) && release.draft === true && release.tag_name === context.version, 'Invalid draft release response');
  const verifyRemote = async () => {
    const assets = await list(context.repository, `releases/${release.id}/assets`, undefined, api);
    must(new Set(assets.map(a => a.name)).size === assets.length && assets.every(a => entries.some(e => e.name === a.name)),
      'Remote draft contains duplicate or unexpected assets');
    for (const asset of assets) {
      const expected = entries.find(e => e.name === asset.name);
      must(asset.state === 'uploaded' && asset.size === fs.statSync(p.join(directory, asset.name)).size &&
        (!asset.digest || asset.digest === `sha256:${expected.sha256}`) &&
        await downloadHash(context.repository, asset) === expected.sha256, `Remote asset bytes differ: ${asset.name}`);
    }
    return assets;
  };
  const existing = await verifyRemote();
  const missing = entries.filter(e => !existing.some(a => a.name === e.name));
  if (missing.length) await upload(context.version, missing.map(e => p.join(directory, e.name)), context.repository);
  checkReleaseDirectory(directory, context, seal, sourceRoot);
  must((await verifyRemote()).length === entries.length, 'Remote release matrix/assets incomplete');
  await remoteTag(context, api);
  const result = await api(context.repository, `releases/${release.id}`, { method: 'PATCH', body: { draft: false, make_latest: 'true' } });
  must(result.id === release.id && result.draft === false && result.tag_name === context.version, 'Release publication not confirmed');
  return { id: result.id, version: context.version, checksums_sha256: seal, assets: entries.length, status: 'published' };
}

if (process.argv[1] && fs.existsSync(process.argv[1]) && fs.realpathSync(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const [command, ...args] = process.argv.slice(2);
    if (command === 'plan' && args.length === 4) {
      const [version, runID, attempt, output] = args;
      const plan = await candidateContext(version, runID, attempt);
      const context = { ...plan.context, publish_run_id: Number(process.env.GITHUB_RUN_ID), publish_run_attempt: Number(process.env.GITHUB_RUN_ATTEMPT) };
      must(positive(context.publish_run_id) && positive(context.publish_run_attempt), 'Invalid publication run identity');
      must(!fs.existsSync(output), 'Publication context already exists');
      writeJSON(output, context);
      if (process.env.GITHUB_OUTPUT) fs.appendFileSync(process.env.GITHUB_OUTPUT,
        Object.entries({ ...plan, context }).map(([k, v]) => `${k}=${typeof v === 'object' ? JSON.stringify(v) : v}\n`).join(''));
    } else if (command === 'assemble' && args.length === 4) {
      const result = assembleRelease(args[0], args[1], json(args[2]), args[3]);
      if (process.env.GITHUB_OUTPUT) fs.appendFileSync(process.env.GITHUB_OUTPUT, `checksums_sha256=${result.checksums_sha256}\n`);
      console.log(JSON.stringify(result));
    } else if (command === 'publish' && args.length === 3) {
      console.log(JSON.stringify(await publishRelease(args[0], json(args[1]), args[2])));
    } else throw Error('Usage: package-publish.mjs plan VERSION CANDIDATE_RUN_ID ATTEMPT CONTEXT | assemble CANDIDATES ASSESSMENTS CONTEXT OUTPUT | publish ASSETS CONTEXT CHECKSUMS_SHA256');
  } catch (error) { console.error(`Publication failed: ${error.message}`); process.exitCode = 1; }
}
