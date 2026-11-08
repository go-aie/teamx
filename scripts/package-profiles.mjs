import * as p from 'node:path';
import { fileURLToPath } from 'node:url';
import { json, must } from './package-lib.mjs';

const root = p.resolve(p.dirname(fileURLToPath(import.meta.url)), '..');
export const profileNames = ['core', 'standard', 'full'];
export const readProfileLock = () => json(p.join(root, 'packaging/dependencies.lock.json'));

export function profileDependencies(profile, lock = readProfileLock()) {
  must(profileNames.includes(profile) && lock.profiles?.[profile], `Unknown profile: ${profile}`);
  return lock.profiles[profile].required_dependencies;
}

export function hasDependency(profile, dependency, lock) {
  return profileDependencies(profile, lock).includes(dependency);
}

export function checkManifestProfile(manifest, lock = readProfileLock()) {
  const expected = [...profileDependencies(manifest.profile, lock)].sort();
  must(JSON.stringify(Object.keys(manifest.dependencies || {}).sort()) === JSON.stringify(expected),
    'Manifest dependencies differ from profile');
}

export function releaseProfiles(lock) {
  const profiles = lock.release?.profiles;
  must(Array.isArray(profiles) && new Set(profiles).size === profiles.length &&
    ['core', 'standard'].every(name => profiles.includes(name)) &&
    profiles.every(name => profileNames.includes(name) && lock.profiles[name]), 'Incomplete or unknown release profiles');
  must(profiles.includes(lock.release.recommended_profile), 'Recommended profile is outside release scope');
  return profiles;
}
