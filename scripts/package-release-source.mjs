import { must, run } from './package-lib.mjs';

export function assertReleaseSource(root, version, expectedCommit) {
  must(/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/.test(version || ''),
    'Release requires --version vX.Y.Z');
  const git = (...args) => run('git', args, { cwd: root }).trim();
  const commit = git('rev-parse', 'HEAD');
  must(!git('status', '--porcelain', '--untracked-files=all'), 'Release requires a clean worktree');
  // Use the tag namespace explicitly; a branch with the same name is insufficient.
  let taggedCommit;
  try { taggedCommit = run('git', ['rev-parse', '--verify', `refs/tags/${version}^{commit}`],
    { cwd: root, stdio: ['ignore', 'pipe', 'pipe'] }).trim(); }
  catch { throw Error('Release requires an existing version tag'); }
  must(taggedCommit === commit, 'Tag does not match HEAD');
  if (expectedCommit) must(commit === expectedCommit, 'Release source commit mismatch');
  return commit;
}

// Local release builds need no CI context; CI archives bind the exact build run.
export function releaseBatch(env = process.env) {
  if (!env.GITHUB_RUN_ID && !env.GITHUB_RUN_ATTEMPT) return undefined;
  for (const name of ['GITHUB_RUN_ID', 'GITHUB_RUN_ATTEMPT'])
    must(/^[1-9]\d*$/.test(env[name] || '') && Number.isSafeInteger(Number(env[name])), 'Invalid CI build batch');
  return { run_id: Number(env.GITHUB_RUN_ID), run_attempt: Number(env.GITHUB_RUN_ATTEMPT) };
}
