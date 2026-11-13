// Maintainer attestations cover checks that need a clean user machine or an
// interactive platform account. They are not automated-test results. Private
// evidence stays with the tester; only fixed observations and its digest travel.
import { must } from './package-lib.mjs';
import { hasDependency, profileNames } from './package-profiles.mjs';
import { webDeliveryTemplate, checkWebDelivery } from './server-release-policy.mjs';

const same = (a, b) => JSON.stringify(a) === JSON.stringify(b);
const sha = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const native = {
  'darwin-arm64': ['darwin', 'arm64', 'arm64'], 'darwin-amd64': ['darwin', 'x64', 'x86_64'],
  'linux-arm64': ['linux', 'arm64', 'aarch64'], 'linux-amd64': ['linux', 'x64', 'x86_64']
};
const clean = ['fresh_extraction', 'independent_home', 'host_go_absent', 'host_node_absent',
  'host_platform_clis_absent', 'system_dependencies_within_baseline'];
const git = ['bundled_git', 'host_git_absent', 'helper_executed', 'tls_verified', 'https_clone', 'local_commit'];
const platform = ['isolated_accounts', 'gitlab_login', 'gitlab_read', 'gitlab_bind',
  'lark_login', 'lark_read', 'lark_bind'];

export const residentServerTarget = m => m.profile === 'standard' && ['linux-amd64', 'darwin-arm64'].includes(m.target);
export const residentServerFields = m => [
  'system_manager_install', 'login_or_boot_start', 'explicit_stop_stays_stopped',
  'bounded_restart', 'child_process_cleanup', 'no_export_key_first_setup',
  'existing_identity_models_preserved', 'consistent_backup_restore', 'old_runtime_retained',
  'committed_model_operation_recovery', 'no_completed_member_repeated', 'no_unknown_action_replayed',
  'platform_unattended_credentials', 'browser_closed_collect_deliver_execute',
  ...(m.target === 'linux-amd64' ? ['ssh_logout_survives', 'host_reboot_survives', 'lark_account_pinned_send'] : ['logout_sleep_limits_documented'])
];

export function qualificationTemplate(receipt) {
  const m = receipt.manifest, [platformName, process_arch, machine] = native[m.target];
  const fields = {
    clean_machine: [...clean, ...(hasDependency(m.profile, 'git') ? ['host_git_absent'] : [])],
    downloaded_system_security: m.target.startsWith('darwin-') ?
      ['quarantine_present', 'quarantine_preserved', 'user_approved', 'launched_after_resolution'] :
      ['baseline_runtime_verified', 'dynamic_libraries_within_baseline'],
    ...(hasDependency(m.profile, 'git') ? { git_tls_clone: git } : {}),
    ...(hasDependency(m.profile, 'glab') || hasDependency(m.profile, 'lark-cli') ? { platform_login_read_bind: platform } : {}),
    ...(residentServerTarget(m) ? { resident_server: residentServerFields(m), web_delivery: [] } : {})
  };
  return { target: m.target, profile: m.profile, archive_sha256: receipt.archive_sha256,
    environment: { platform: platformName, process_arch, machine, baseline: m.qualification_baseline },
    checks: Object.fromEntries(Object.entries(fields).map(([key, names]) => [key, {
      status: 'not_run', fixture: false, method: 'maintainer_manual', tested_at: null, evidence_sha256: null,
      observations: { ...Object.fromEntries(names.map(name => [name, false])),
        ...(key === 'web_delivery' ? webDeliveryTemplate(m) : {}),
        ...(key === 'downloaded_system_security' && m.target.startsWith('darwin-') ? { first_launch: null } : {}),
        ...(key === 'platform_login_read_bind' ? { remote_writes: 0 } : {}),
        ...(key === 'resident_server' ? { model_delivery_sha256: m.server_deployment?.model_delivery_sha256 ?? null,
          migration: 'not_run' } : {}) }
    }])) };
}

export function qualificationFor(document, receipt) {
  if (!document) return undefined;
  const m = receipt.manifest;
  must(document.schema_version === 1 && document.version === m.version && document.commit === m.source.commit &&
    Array.isArray(document.records), 'Qualification source/version mismatch');
  const keys = document.records.map(r => `${r.target}/${r.profile}`);
  must(new Set(keys).size === keys.length && document.records.every(r => native[r.target] && profileNames.includes(r.profile)),
    'Duplicate or unknown qualification target/profile');
  return document.records.find(r => r.target === m.target && r.profile === m.profile);
}

export function checkQualification(receipt, record) {
  const m = receipt.manifest;
  must(record.archive_sha256 === receipt.archive_sha256 && record.target === m.target && record.profile === m.profile,
    'Qualification belongs to another archive');
  must(typeof m.qualification_baseline?.os === 'string' &&
    same([record.environment?.platform, record.environment?.process_arch, record.environment?.machine], native[m.target]) &&
    same(record.environment?.baseline, m.qualification_baseline), 'Qualification native environment/baseline mismatch');
  const required = ['clean_machine', 'downloaded_system_security',
    ...(hasDependency(m.profile, 'git') ? ['git_tls_clone'] : []),
    ...(hasDependency(m.profile, 'glab') || hasDependency(m.profile, 'lark-cli') ? ['platform_login_read_bind'] : []),
    ...(residentServerTarget(m) ? ['resident_server', 'web_delivery'] : [])];
  must(record.checks && Object.keys(record.checks).every(k => required.includes(k)), 'Unexpected qualification check');
  const accepted = {};
  for (const key of required) {
    const c = record.checks[key];
    if (!c) continue;
    must(c.status === 'passed' && c.fixture === false && c.method === 'maintainer_manual' && sha(c.evidence_sha256) &&
      typeof c.tested_at === 'string' && Number.isFinite(Date.parse(c.tested_at)) && Date.parse(c.tested_at) <= Date.now(),
      `Missing real maintainer evidence: ${key}`);
    const o = c.observations;
    let observations;
    if (key === 'clean_machine') {
      const fields = [...clean, ...(hasDependency(m.profile, 'git') ? ['host_git_absent'] : [])];
      must(fields.every(f => o?.[f] === true), 'Clean-machine observations incomplete');
      observations = Object.fromEntries(fields.map(f => [f, true]));
    } else if (key === 'downloaded_system_security' && m.target.startsWith('darwin-')) {
      must(o?.quarantine_present === true && o.quarantine_preserved === true &&
        ['blocked', 'allowed'].includes(o.first_launch) && o.launched_after_resolution === true &&
        (o.first_launch !== 'blocked' || o.user_approved === true), 'macOS downloaded-launch observations incomplete');
      observations = { quarantine_present: true, quarantine_preserved: true, first_launch: o.first_launch,
        user_approved: o.first_launch === 'blocked', launched_after_resolution: true };
    } else if (key === 'downloaded_system_security') {
      must(o?.baseline_runtime_verified === true && o.dynamic_libraries_within_baseline === true,
        'Linux system-baseline observations incomplete');
      observations = { baseline_runtime_verified: true, dynamic_libraries_within_baseline: true };
    } else if (key === 'web_delivery') {
      observations = checkWebDelivery(o, m);
      must(Date.parse(c.tested_at) >= Date.parse(observations.observation.finished_at),
        'Web delivery attestation predates observation');
    } else if (key === 'resident_server') {
      must(residentServerFields(m).every(f => o?.[f] === true), 'Resident Server observations incomplete');
      must(sha(m.server_deployment?.model_delivery_sha256) && o.model_delivery_sha256 === m.server_deployment.model_delivery_sha256,
        'Resident Server model delivery baseline mismatch');
      must(['not_applicable', 'resume_verified'].includes(o.migration), 'Finite model migration applicability/resume evidence missing');
      observations = { ...Object.fromEntries(residentServerFields(m).map(f => [f, true])),
        model_delivery_sha256: o.model_delivery_sha256, migration: o.migration };
    } else {
      const fields = key === 'git_tls_clone' ? git : platform;
      must(fields.every(f => o?.[f] === true), `Qualification observations incomplete: ${key}`);
      if (key === 'platform_login_read_bind') must(o.remote_writes === 0, 'Platform qualification must be read-only');
      observations = { ...Object.fromEntries(fields.map(f => [f, true])),
        ...(key === 'platform_login_read_bind' ? { remote_writes: 0 } : {}) };
    }
    accepted[key] = { status: 'passed', fixture: false, method: 'maintainer_manual',
      tested_at: c.tested_at, evidence_sha256: c.evidence_sha256, observations };
  }
  return { archive_sha256: receipt.archive_sha256, target: m.target, profile: m.profile,
    environment: { platform: record.environment.platform, process_arch: record.environment.process_arch,
      machine: record.environment.machine, baseline: m.qualification_baseline }, checks: accepted };
}
