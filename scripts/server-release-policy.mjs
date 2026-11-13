// Public projection of private, archive-specific acceptance. A viewport or
// fixture is useful evidence, but cannot qualify a physical browser/device.
import { must } from './package-lib.mjs';

export const browserNames = ['desktop_chromium', 'desktop_safari', 'ios_safari', 'android_chrome'];
export const browserFields = ['login', 'model_first_use', 'model_settings', 'member_model_selection',
  'conversation', 'platform_authorization_return', 'draft_refresh', 'network_recovery',
  'background_return', 'touch_keyboard_rotation'];
export const modelFields = ['real_first_use_without_export_key', 'default_and_fixed_references',
  'entry_update', 'key_rotation', 'delete_replace', 'waiting_and_partial_failure',
  'refresh_original_operation', 'restart_subsequent_call', 'request_and_execution_snapshot_verified'];
export const platformFields = ['gitlab_read', 'lark_read', 'notification_collect_deliver',
  'authorization_return', 'authorized_action', 'remote_result_verified'];
const flags = names => Object.fromEntries(names.map(name => [name, false]));
const sha = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const date = value => typeof value === 'string' && Number.isFinite(Date.parse(value));

export function webDeliveryTemplate(m) {
  return { model_delivery_sha256: m.server_deployment?.model_delivery_sha256 ?? null,
    models: { ...flags(modelFields), upstream_scope: null, dsh_identity_sha256: null,
      other_upstreams: 'not_run', older_dsh: 'not_run' },
    browsers: Object.fromEntries(browserNames.map(name => [name, { status: 'not_run',
      physical_device: false, browser_version: null, os_version: null, evidence_sha256: null,
      observations: flags(browserFields) }])),
    platforms: { ...flags(platformFields), authorization_sha256: null, evidence_sha256: null },
    observation: { started_at: null, finished_at: null, duration_seconds: 0, samples: 0,
      max_gap_seconds: 0, evidence_sha256: null, same_archive_and_installation: false,
      collection_backlog_reviewed: false, rate_limit_recovery_reviewed: false,
      failures_reviewed: false },
    download_upgrade_restore_network_verified: false,
    authentication_context_reset_and_limits_documented: false };
}

export function checkWebDelivery(o, m) {
  must(sha(m.server_deployment?.model_delivery_sha256) &&
    o?.model_delivery_sha256 === m.server_deployment.model_delivery_sha256,
  'Web delivery model baseline mismatch');
  const model = o.models;
  must(modelFields.every(f => model?.[f] === true) && sha(model.dsh_identity_sha256) &&
    ['same_upstream_multiple_entries', 'different_upstreams'].includes(model.upstream_scope) &&
    ['passed', 'not_run'].includes(model.other_upstreams) && ['passed', 'not_run'].includes(model.older_dsh) &&
    (model.upstream_scope !== 'different_upstreams' || model.other_upstreams === 'passed'),
  'Web delivery real model observations incomplete');
  must(o.browsers && Object.keys(o.browsers).length === browserNames.length &&
    Object.keys(o.browsers).every(name => browserNames.includes(name)), 'Web delivery browser matrix incomplete');
  const browsers = {};
  for (const name of browserNames) {
    const b = o.browsers[name];
    must(['passed', 'not_run'].includes(b?.status), `Invalid browser evidence: ${name}`);
    if (b.status === 'not_run') { browsers[name] = { status: 'not_run' }; continue; }
    must(b.physical_device === true && sha(b.evidence_sha256) &&
      [b.browser_version, b.os_version].every(s => typeof s === 'string' && s.trim().length > 0 && s.length <= 128) &&
      browserFields.every(f => b.observations?.[f] === true), `Physical browser observations incomplete: ${name}`);
    browsers[name] = { status: 'passed', physical_device: true, browser_version: b.browser_version,
      os_version: b.os_version, evidence_sha256: b.evidence_sha256, observations: flags(browserFields) };
    for (const f of browserFields) browsers[name].observations[f] = true;
  }
  must(['desktop_chromium', 'desktop_safari'].some(name => browsers[name].status === 'passed'),
    'Web delivery needs a supported desktop browser');
  must(platformFields.every(f => o.platforms?.[f] === true) && sha(o.platforms.authorization_sha256) &&
    sha(o.platforms.evidence_sha256), 'Web delivery authorized platform observations incomplete');
  const s = o.observation;
  must(date(s?.started_at) && date(s.finished_at) && Date.parse(s.finished_at) <= Date.now() &&
    s.duration_seconds === (Date.parse(s.finished_at) - Date.parse(s.started_at)) / 1000 && s.duration_seconds >= 86400 &&
    Number.isInteger(s.samples) && s.samples >= 481 && Number.isFinite(s.max_gap_seconds) &&
    s.max_gap_seconds > 0 && s.max_gap_seconds <= 180 && (s.samples - 1) * s.max_gap_seconds >= s.duration_seconds &&
    sha(s.evidence_sha256) && ['same_archive_and_installation', 'collection_backlog_reviewed',
      'rate_limit_recovery_reviewed', 'failures_reviewed'].every(f => s[f] === true),
  'Web delivery requires reviewed observation of at least 24 hours');
  must(o.download_upgrade_restore_network_verified === true &&
    o.authentication_context_reset_and_limits_documented === true, 'Web delivery maintenance/documentation incomplete');
  return { model_delivery_sha256: o.model_delivery_sha256,
    models: { ...Object.fromEntries(modelFields.map(f => [f, true])), upstream_scope: model.upstream_scope,
      dsh_identity_sha256: model.dsh_identity_sha256, other_upstreams: model.other_upstreams, older_dsh: model.older_dsh },
    browsers, supported_browsers: browserNames.filter(name => browsers[name].status === 'passed'),
    pending_browsers: browserNames.filter(name => browsers[name].status === 'not_run'),
    platforms: { ...Object.fromEntries(platformFields.map(f => [f, true])),
      authorization_sha256: o.platforms.authorization_sha256, evidence_sha256: o.platforms.evidence_sha256 },
    observation: Object.fromEntries(['started_at', 'finished_at', 'duration_seconds', 'samples', 'max_gap_seconds',
      'evidence_sha256', 'same_archive_and_installation', 'collection_backlog_reviewed',
      'rate_limit_recovery_reviewed', 'failures_reviewed'].map(f => [f, s[f]])),
    download_upgrade_restore_network_verified: true, authentication_context_reset_and_limits_documented: true };
}
