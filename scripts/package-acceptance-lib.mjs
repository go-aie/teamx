import { must } from './package-lib.mjs';

export const acceptanceInputs = ['scripts/package-acceptance.mjs', 'scripts/package-acceptance-lib.mjs',
  'scripts/package-browser.mjs', 'scripts/package-lib.mjs', 'scripts/package-platform.mjs',
  'scripts/package-smoke.mjs', 'scripts/check-dsh-native.mjs', 'scripts/fixtures/acp.mjs', 'scripts/fixtures/models.mjs',
  'internal/room/bundle_real_e2e_test.go',
  'internal/room/dsh_real_test.go', 'internal/room/demo_test.go',
  'packaging/verifier/package.json', 'packaging/verifier/package-lock.json', 'scripts/package-profiles.mjs'];

export function checkSmoke(report, digest, manifest) {
  must(report.status === 'passed', 'Archive smoke did not pass');
  must(report.archive_sha256 === digest, 'Smoke belongs to another archive');
  must(report.target === manifest.target && report.profile === manifest.profile,
    'Smoke target/profile differs');
}

// Results cover only this local scope.
// A skipped model/browser, missing cleanup or a lazy restart without a real
// resumed prompt must not turn into a passing acceptance.
export function resultForScope(checks) {
  return ['archive_smoke', 'real_dsh_acp', 'browser'].every(k => checks[k]?.status === 'passed')
    ? 'passed' : 'incomplete';
}

export function checkRealDSH(report) {
  must(report.status === 'passed' && report.fixture === false, 'Real DSH acceptance did not pass');
  for (const key of ['cancelToolStopped', 'dshProcessStopped', 'resumedOriginalSession'])
    must(report[key] === true, `Missing real DSH evidence: ${key}`);
  must(report.afterRestart?.result?.stop_reason === 'end_turn' ||
    report.afterRestart?.result?.stopReason === 'end_turn', 'Resumed prompt did not complete');
  must(typeof report.sessionId === 'string' && report.sessionId.length > 0, 'Missing original Session ID');
  must(!report.cleanupError, 'Real DSH cleanup unconfirmed');
}
