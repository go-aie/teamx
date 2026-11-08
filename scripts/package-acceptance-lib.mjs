import { must } from './package-lib.mjs';

export function checkSmoke(report, digest, manifest) {
  must(report.status === 'passed', 'Archive smoke did not pass');
  must(report.archive_sha256 === digest, 'Smoke belongs to another archive');
  must(report.target === manifest.target && report.profile === manifest.profile,
    'Smoke target/profile differs');
}

// Passing this deliberately narrower local scope never qualifies a release.
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
