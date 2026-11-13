import { test } from 'node:test';
import assert from 'node:assert/strict';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as p from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { snapshotPlan } from './package-ci.mjs';
import { candidateName, verificationName } from './package-publish.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
const workflow = fs.readFileSync(p.join(root, '.github/workflows/release.yml'), 'utf8');

// Read the workflow's indented source blocks, rather than introduce a YAML
// runtime dependency into packaging tests. Full YAML/expression validation is
// performed separately by actionlint. Unsupported field forms fail explicitly.
function field(source, indent, key) {
  const matches = [...source.matchAll(new RegExp(`^ {${indent}}${key}:([^\n]*)$`, 'gm'))];
  assert.equal(matches.length, 1, `Expected one ${key} field at indentation ${indent}`);
  const match = matches[0], tail = source.slice(match.index + match[0].length + 1).split('\n');
  const end = tail.findIndex(line => line.trim() && line.search(/\S/) <= indent);
  return { value: match[1].trim(), body: tail.slice(0, end < 0 ? undefined : end).join('\n') };
}
const jobsBlock = field(workflow, 0, 'jobs').body;
const jobs = Object.fromEntries([...jobsBlock.matchAll(/^  ([\w-]+):$/gm)]
  .map(([, name]) => [name, field(jobsBlock, 2, name).body]));
const steps = job => field(job, 4, 'steps').body.split(/(?=^      - )/m).filter(text => text.trim());
function action(job, name) {
  const found = steps(job).filter(step => new RegExp(`^      - uses: actions/${name}@`, 'm').test(step));
  assert.equal(found.length, 1, `Expected one ${name} action`);
  return found[0];
}
const withValue = (step, key) => field(field(step, 8, 'with').body, 10, key).value;
function runCode(step) {
  const run = field(step, 8, 'run');
  if (!['|', '>-'].includes(run.value)) return run.value;
  const lines = run.body.split('\n').map(line => line.replace(/^ {10}/, ''));
  return run.value === '|' ? lines.join('\n') : lines.map(line => line.trim()).join(' ');
}
function evaluate(source, context) {
  return source.replace(/\$\{\{\s*(.*?)\s*\}\}/g, (_, expression) => {
    const decoded = /^fromJSON\(([^)]+)\)\.([\w-]+)$/.exec(expression);
    const read = path => path.split('.').reduce((value, key) => {
      assert.ok(Object.hasOwn(value, key), `Unknown workflow output ${path}`);
      return value[key];
    }, context);
    return decoded ? JSON.parse(read(decoded[1]))[decoded[2]] : read(expression);
  });
}
function dependencies(job) {
  const value = field(job, 4, 'needs').value;
  return value.startsWith('[') ? value.slice(1, -1).split(',').map(name => name.trim()) : [value];
}
const context = {
  github: { run_id: '101', run_attempt: '2' },
  needs: {
    checks: { outputs: { version: 'v1.2.3', commit: 'a'.repeat(40), context: JSON.stringify({
      repository: 'example/teamx', version: 'v1.2.3', commit: 'a'.repeat(40),
      workflow: '.github/workflows/release.yml', run_id: 101, run_attempt: 2
    }) } },
    assemble: { outputs: { artifact_id: '9001', checksums_sha256: 'b'.repeat(64) } }
  }
};
context.needs.assemble.outputs.context = context.needs.checks.outputs.context;

test('release accepts only tags, preserves checks and grants write permission only to publication', () => {
  const triggers = field(workflow, 0, 'on').body;
  assert.deepEqual([...triggers.matchAll(/^  ([\w-]+):/gm)].map(([, name]) => name), ['push']);
  assert.equal(field(field(triggers, 2, 'push').body, 4, 'tags').value, "['v*']");
  assert.equal(field(field(workflow, 0, 'permissions').body, 2, 'contents').value, 'read');
  assert.equal(field(field(workflow, 0, 'concurrency').body, 2, 'cancel-in-progress').value, 'false');
  assert.equal(field(field(workflow, 0, 'concurrency').body, 2, 'group').value, 'release-${{ github.ref }}');
  assert.deepEqual(Object.keys(jobs), ['checks', 'build', 'verify', 'assemble', 'publish']);
  for (const [name, job] of Object.entries(jobs)) {
    assert.doesNotMatch(job, /^    (?:if|environment|continue-on-error):/m);
    assert.doesNotMatch(job, /^        continue-on-error:/m);
    if (name === 'publish') assert.equal(field(field(job, 4, 'permissions').body, 6, 'contents').value, 'write');
    else assert.doesNotMatch(job, /^    permissions:/m);
    for (const step of steps(job).filter(step => /^        run:/m.test(step)))
      assert.equal(spawnSync('/bin/bash', ['-n'], { input: runCode(step) }).status, 0);
  }
  const checks = steps(jobs.checks).filter(step => /^        run:/m.test(step)).map(runCode).join('\n');
  for (const command of ['package-publish.mjs plan', 'check-packaging.mjs', 'node --test scripts/*.test.mjs',
    'make test', 'go vet ./...', 'node --test internal/web/tests/web*_test.mjs']) assert.ok(checks.includes(command), command);
  assert.doesNotMatch(workflow, /TEAMX_RELEASE_MODEL|qualification|package-acceptance|playwright|chromium|--candidate|inputs\./i);
  assert.equal(fs.existsSync(p.join(root, '.github/workflows/release-publish.yml')), false);
});

test('every required check, build and verification gates assembly and publication', () => {
  assert.deepEqual(dependencies(jobs.build), ['checks']);
  assert.deepEqual(dependencies(jobs.verify), ['checks', 'build']);
  assert.deepEqual(dependencies(jobs.assemble), ['checks', 'build', 'verify']);
  assert.deepEqual(dependencies(jobs.publish), ['checks', 'assemble']);
  for (const job of [jobs.build, jobs.verify]) {
    assert.equal(field(field(job, 4, 'strategy').body, 6, 'fail-fast').value, 'false');
    assert.equal(field(field(job, 4, 'strategy').body, 6, 'matrix').value, '${{ fromJSON(needs.checks.outputs.matrix) }}');
  }
});

test('all eight built archives and verification reports map to their same-run artifact names', () => {
  const matrix = ['core', 'standard'].flatMap(profile => snapshotPlan(root, profile).matrix.include);
  assert.equal(matrix.length, 8);
  for (const row of matrix) {
    const inputs = { ...context, matrix: row };
    assert.equal(evaluate(withValue(action(jobs.build, 'upload-artifact'), 'name'), inputs), candidateName('v1.2.3', row, 2));
    assert.equal(evaluate(withValue(action(jobs.verify, 'download-artifact'), 'name'), inputs), candidateName('v1.2.3', row, 2));
    assert.equal(evaluate(withValue(action(jobs.verify, 'upload-artifact'), 'name'), inputs), verificationName('v1.2.3', row, 2));
  }
  assert.equal(withValue(action(jobs.verify, 'upload-artifact'), 'path'), '${{ runner.temp }}/verification.json');
  const verify = steps(jobs.verify).find(step => /^        id: verify$/m.test(step));
  assert.ok(runCode(verify).includes('"$RUNNER_TEMP/verification.json"'));
  const downloads = steps(jobs.assemble).filter(step => step.includes('uses: actions/download-artifact@'));
  assert.deepEqual(downloads.map(step => evaluate(withValue(step, 'pattern'), context)),
    ['candidate-v1.2.3-*-*-2', 'candidate-verification-v1.2.3-*-*-2']);
  for (const download of downloads) assert.equal(withValue(download, 'merge-multiple'), 'false');
  for (const job of [jobs.verify, jobs.assemble, jobs.publish])
    assert.doesNotMatch(job, /^          (?:run-id|repository|github-token):/m);
});

test('build, verify and assembly run their actual shell guards and refuse a partial retry', () => {
  for (const name of ['build', 'verify', 'assemble']) {
    const guard = steps(jobs[name])[0];
    const expected = evaluate(field(field(guard, 8, 'env').body, 10, 'RELEASE_ATTEMPT').value, context);
    for (const attempt of ['2', '3']) {
      const result = spawnSync('/bin/bash', ['-e', '-o', 'pipefail', '-c', runCode(guard)], {
        encoding: 'utf8', env: { RELEASE_ATTEMPT: expected, GITHUB_RUN_ATTEMPT: attempt }
      });
      assert.equal(result.status, attempt === '2' ? 0 : 1, name);
      if (attempt === '3') assert.match(result.stderr, /Re-run all jobs/);
    }
  }
});

test('publish retry downloads the original artifact and forwards the original context and seal through Bash', t => {
  const temporary = fs.mkdtempSync(p.join(os.tmpdir(), 'teamx workflow retry '));
  t.after(() => fs.rmSync(temporary, { recursive: true, force: true }));
  const bin = p.join(temporary, 'bin'), stub = p.join(temporary, 'publish-stub.mjs'); fs.mkdirSync(bin);
  // Execute the actual inline context writer. Record the publication call at
  // the process boundary; this isolated PATH contains no gh or network clients.
  fs.writeFileSync(p.join(bin, 'node'), `#!/bin/sh
if [ "$1" = '--input-type=module' ]; then exec "$WORKFLOW_TEST_NODE" "$@"; fi
if [ "$1" != 'scripts/package-publish.mjs' ] || [ "$2" != 'publish' ]; then exit 70; fi
exec "$WORKFLOW_TEST_NODE" "$WORKFLOW_TEST_STUB" "$@"
`, { mode: 0o755 });
  fs.writeFileSync(stub, `import fs from 'node:fs';
fs.writeFileSync(process.env.WORKFLOW_TEST_RECORD, JSON.stringify(process.argv.slice(2)));
process.exit(Number(process.env.WORKFLOW_TEST_EXIT));
`);
  const outputs = field(jobs.assemble, 4, 'outputs').body;
  assert.equal(field(outputs, 6, 'artifact_id').value, '${{ steps.upload.outputs.artifact-id }}');
  assert.equal(field(outputs, 6, 'context').value, '${{ needs.checks.outputs.context }}');
  assert.equal(field(outputs, 6, 'checksums_sha256').value, '${{ steps.seal.outputs.checksums_sha256 }}');
  const publish = steps(jobs.publish).at(-1), envBlock = field(publish, 8, 'env').body;
  assert.equal(field(envBlock, 10, 'GH_TOKEN').value, '${{ github.token }}');
  for (const [attempt, exitCode] of [['2', 0], ['3', 17]]) {
    const inputs = { ...context, github: { ...context.github, run_attempt: attempt } };
    assert.equal(evaluate(withValue(action(jobs.publish, 'download-artifact'), 'artifact-ids'), inputs), '9001');
    const runner = p.join(temporary, `attempt-${attempt}`), record = p.join(runner, 'command.json'); fs.mkdirSync(runner);
    const result = spawnSync('/bin/bash', ['-e', '-o', 'pipefail', '-c', runCode(publish)], { encoding: 'utf8', env: {
      PATH: bin, RUNNER_TEMP: runner, GITHUB_RUN_ATTEMPT: attempt, GH_TOKEN: 'fixture-only',
      RELEASE_CONTEXT: evaluate(field(envBlock, 10, 'RELEASE_CONTEXT').value, inputs),
      RELEASE_SEAL: evaluate(field(envBlock, 10, 'RELEASE_SEAL').value, inputs),
      WORKFLOW_TEST_NODE: process.execPath, WORKFLOW_TEST_STUB: stub, WORKFLOW_TEST_RECORD: record,
      WORKFLOW_TEST_EXIT: String(exitCode)
    } });
    assert.equal(result.status, exitCode, result.stderr);
    assert.deepEqual(JSON.parse(fs.readFileSync(p.join(runner, 'release-source.json'), 'utf8')),
      JSON.parse(context.needs.assemble.outputs.context));
    assert.deepEqual(JSON.parse(fs.readFileSync(record, 'utf8')), ['scripts/package-publish.mjs', 'publish',
      p.join(runner, 'release'), p.join(runner, 'release-source.json'), context.needs.assemble.outputs.checksums_sha256]);
  }
});
