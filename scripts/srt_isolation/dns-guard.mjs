// Controlled resolver seam in the pinned, unmodified SRT implementation.
// This is not an OS-DNS / real rebinding end-to-end claim.
const { createResolvedAddressGuard } = await import(
  '/mnt/srt/node_modules/@anthropic-ai/sandbox-runtime/dist/sandbox/resolved-address-guard.js');
const checks = {};
for (const [label, address] of Object.entries({
  loopback4: '127.0.0.1', loopback6: '::1', mapped: '::ffff:127.0.0.1',
  metadata4: '169.254.169.254', metadata_ali: '100.100.100.200',
  metadata6: 'fd00:ec2::254', private4: '10.1.2.3', private6: 'fd12::1',
  nat64: '64:ff9b::7f00:1', local_interface: '192.0.2.9',
})) {
  const guard = createResolvedAddressGuard({ deniedResolvedAddresses: ['10.0.0.0/8', 'fc00::/7'],
    localAddresses: () => ['192.0.2.9'] });
  checks[`${label}_denied`] = !guard.permits('allowed.test', address, 443);
}
const except = createResolvedAddressGuard({ allowedDomains: ['127.0.0.1:444'], localAddresses: () => [] });
checks.explicit_ip_port_allowed = except.permits('allowed.test', '127.0.0.1', 444);
checks.other_port_stays_denied = !except.permits('allowed.test', '127.0.0.1', 445);
checks.reserved_localhost_cannot_rebind_public = !except.permits('localhost', '198.51.100.9', 444);
let calls = 0;
const guard = createResolvedAddressGuard({ localAddresses: () => [], resolve: (name, opts, callback) => {
  calls++; callback(null, [{ address: calls === 1 ? '198.51.100.8' : '127.0.0.1', family: 4 }]);
} });
await new Promise(resolve => guard.lookupFor(443)('allowed.test', { all: true }, (err, addresses) => {
  checks.single_resolution_pins_checked_address = !err && calls === 1 && addresses[0].address === '198.51.100.8';
  resolve();
}));
let blockedCalls = 0;
const refused = createResolvedAddressGuard({ resolve: (name, opts, callback) => {
  blockedCalls++; callback(null, [{ address: '127.0.0.1', family: 4 }]);
} });
await new Promise(resolve => refused.lookupFor(443)('allowed.test', { all: true }, (err) => {
  checks.hostname_private_answer_fails_before_dial = err?.code === 'ERR_SRT_RESOLVED_ADDRESS_DENIED' && blockedCalls === 1;
  resolve();
}));
console.log(JSON.stringify({ evidence: 'controlled_resolver_seam', checks }));
process.exitCode = Object.values(checks).every(Boolean) ? 0 : 1;
