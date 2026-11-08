import { test } from 'node:test';
import assert from 'node:assert/strict';
import { checkSmoke, checkRealDSH, resultForScope } from './package-acceptance-lib.mjs';

test('smoke evidence must belong to the exact archive and capability profile', () => {
  const manifest = { target: 'darwin-arm64', profile: 'core' };
  const report = { ...manifest, status: 'passed', archive_sha256: 'abc' };
  checkSmoke(report, 'abc', manifest);
  assert.throws(() => checkSmoke(report, 'different', manifest));
  assert.throws(() => checkSmoke(report, 'abc', { ...manifest, profile: 'full' }));
  assert.throws(() => checkSmoke({ ...report, status: 'failed' }, 'abc', manifest));
});

test('restart readiness, fixture or unsettled cleanup cannot prove real Session recovery', () => {
  const report = { status: 'passed', fixture: false, cancelToolStopped: true,
    dshProcessStopped: true, resumedOriginalSession: true, sessionId: 'original',
    afterRestart: { result: { stopReason: 'end_turn' } } };
  checkRealDSH(report);
  for (const change of [{ fixture: true }, { resumedOriginalSession: false },
    { afterRestart: undefined }, { cancelToolStopped: false }, { dshProcessStopped: false },
    { cleanupError: 'lock held' }, { sessionId: '' }])
    assert.throws(() => checkRealDSH({ ...report, ...change }));
});

test('omitted, skipped and failed checks keep local acceptance incomplete', () => {
  const checks = { archive_smoke: { status: 'passed' }, real_dsh_acp: { status: 'passed' }, browser: { status: 'passed' } };
  assert.equal(resultForScope(checks), 'passed');
  for (const status of ['not_run', 'failed', undefined])
    assert.equal(resultForScope({ ...checks, browser: { status } }), 'incomplete');
  assert.equal(resultForScope({}), 'incomplete');
});
