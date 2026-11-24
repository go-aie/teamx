#!/usr/bin/env python3
"""Pinned SRT mechanism experiment. No DSH, real account or host configuration changes."""
import base64
import hashlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import signal
import socket
import ssl
import subprocess
import sys
import threading
import time
import urllib.request
import uuid

cfg = json.loads(Path(sys.argv[1]).read_text())
lab, root = Path(cfg['lab']), Path(cfg['root'])
runtime = root / 'runtime-evidence'
report = {'status': 'failed', 'evidence_scope': 'standalone_srt_mechanisms', 'dsh_started': False,
          'production_backend': False, 'srt_route_accepted': False, 'checks': {}, 'cases': {}, 'findings': [],
          'not_claimed': ['DSH integration', 'TeamX authorization integration', 'real platform or model',
                          'hard resource limits', 'real DNS rebinding end-to-end', 'remote Computer']}
checks = report['checks']
managed, servers, unix_sockets = [], [], []


def sha(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


def table():
    result = {}
    for p in Path('/proc').iterdir():
        if not p.name.isdecimal(): continue
        try:
            fields = (p/'stat').read_text().rsplit(')', 1)[1].split()
            result[int(p.name)] = (int(fields[1]), fields[19], fields[0])
        except (OSError, ValueError, IndexError): pass
    if os.getpid() not in result: raise RuntimeError('process observer unavailable')
    return result


class Execution:
    def __init__(self, label, area, mode):
        self.label, self.owned, self.lock, self.halt = label, {}, threading.Lock(), threading.Event()
        self.out = (runtime/(label+'.stdout')).open('wb')
        self.err = (runtime/(label+'.stderr')).open('wb')
        own = lab/area
        env = {'PATH': f"{root}/node/bin:{root}/tools/usr/bin:/usr/bin:/bin", 'HOME': str(own/'home'),
               'TMPDIR': str(own/'tmp'), 'LANG': 'C.UTF-8', 'LC_ALL': 'C.UTF-8'}
        command = [cfg['node'], cfg['srt']+'/node_modules/@anthropic-ai/sandbox-runtime/dist/cli.js',
                   '--settings', str(lab/'policies'/f'{area}.json'), '--', '/usr/bin/env',
                   'NO_PROXY=', 'no_proxy=', 'NODE_USE_ENV_PROXY=1',
                   'SSL_CERT_FILE='+str(lab/area/'state/config/ca.pem'),
                   'NODE_EXTRA_CA_CERTS='+str(lab/area/'state/config/ca.pem'),
                   'SRT_TEST_CA='+str(lab/area/'state/config/ca.pem'),
                   '/usr/bin/python3', str(root/'payload.py'), mode, str(own/'workspace/fixture.json')]
        self.process = subprocess.Popen(command, cwd=own/'workspace', env=env, stdin=subprocess.DEVNULL,
                                        stdout=self.out, stderr=self.err, close_fds=True, start_new_session=True)
        self.owned[self.process.pid] = table()[self.process.pid][1]
        self.thread = threading.Thread(target=self.observe, daemon=True)
        self.thread.start()
        managed.append(self)

    def observe(self):
        while not self.halt.wait(0.01):
            try: self.live()
            except Exception as error: self.observation_error = type(error).__name__

    def live(self):
        current = table()
        with self.lock:
            pending = True
            while pending:
                pending = False
                for pid, (parent, started, state) in current.items():
                    if parent in self.owned and parent in current and current[parent][1] == self.owned[parent] and pid not in self.owned:
                        self.owned[pid] = started
                        pending = True
            return {pid: current[pid] for pid, started in self.owned.items() if pid in current and current[pid][1] == started}

    def send(self, pid, sig):
        if pid in self.live():
            try: os.kill(pid, sig)
            except ProcessLookupError: pass

    def settle(self, grace=1):
        until = time.monotonic()+grace
        while self.live() and time.monotonic() < until: time.sleep(0.02)
        before = self.live()
        if before:
            # Freeze only start-time-matched owned processes before terminating them.
            for pid in before: self.send(pid, signal.SIGSTOP)
            for pid in list(self.live()):
                if pid != self.process.pid: self.send(pid, signal.SIGKILL)
            self.send(self.process.pid, signal.SIGKILL)
        self.process.wait(timeout=4)
        until = time.monotonic()+3
        while self.live() and time.monotonic() < until: time.sleep(0.02)
        receipt = {'settled': not self.live(), 'forced_cleanup': bool(before),
                   'observation_error': getattr(self, 'observation_error', None), 'observed_pids': len(self.owned)}
        self.halt.set()
        self.thread.join(timeout=1)
        self.out.close(); self.err.close()
        if not receipt['settled'] or receipt['observation_error']:
            raise RuntimeError('execution remains owned: '+self.label)
        return receipt

    def run(self):
        try: self.process.wait(timeout=35)
        except subprocess.TimeoutExpired: pass
        cleanup = self.settle()
        raw = (runtime/(self.label+'.stdout')).read_text()
        try: result = json.loads(raw)
        except ValueError: result = {'checks': {'valid_payload_receipt': False}, 'output': raw[:2048]}
        result['cleanup'], result['exit_code'] = cleanup, self.process.returncode
        result['stderr'] = (runtime/(self.label+'.stderr')).read_text()[-4096:]
        report['cases'][self.label] = result
        for name, ok in result.get('checks', {}).items(): checks[self.label+'/'+name] = ok
        checks[self.label+'/normal_exit_settled'] = cleanup['settled'] and not cleanup['forced_cleanup'] and result['exit_code'] == 0
        print(self.label, 'checks', len(result.get('checks', {})), 'exit', result['exit_code'], flush=True)
        return result


def await_file(path, timeout=12):
    end = time.monotonic()+timeout
    while time.monotonic() < end:
        if path.exists(): return
        time.sleep(0.02)
    raise RuntimeError('no readiness receipt: '+str(path))


class Fixture:
    def __init__(self, area, family=socket.AF_INET, tls_context=None):
        self.area, self.token = area, uuid.uuid4().hex
        self.ok, self.rejected, self.redirect = 0, 0, ''
        owner = self
        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.headers.get('Authorization') != 'Bearer '+owner.token:
                    owner.rejected += 1
                    self.send_response(401); self.end_headers(); return
                if self.path == '/redirect':
                    self.send_response(302); self.send_header('Location', owner.redirect); self.end_headers(); return
                owner.ok += 1
                body = (owner.area+'_OK').encode()
                self.send_response(200); self.send_header('Content-Length', str(len(body))); self.end_headers(); self.wfile.write(body)
            def log_message(self, *args): pass
        class Server(ThreadingHTTPServer): address_family = family
        self.server = Server(('::1' if family == socket.AF_INET6 else '127.0.0.1', 0), Handler)
        if tls_context: self.server.socket = tls_context.wrap_socket(self.server.socket, server_side=True)
        self.port = self.server.server_address[1]
        host = '[::1]' if family == socket.AF_INET6 else '127.0.0.1'
        self.url = ('https' if tls_context else 'http')+'://'+host+':'+str(self.port)
        self.thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.thread.start(); servers.append(self)


try:
    if os.getuid() == 0 or str(lab) != '/mnt/lab' or list(lab.iterdir()): raise RuntimeError('new non-root lab required')
    report['host'] = {'uid': os.getuid(), 'kernel': os.uname().release,
                      'status': [line for line in Path('/proc/self/status').read_text().splitlines()
                                 if line.startswith(('CapEff:', 'CapBnd:', 'NoNewPrivs:'))]}
    srt = Path(cfg['srt'])/'node_modules/@anthropic-ai/sandbox-runtime'
    report['versions'] = {'srt': json.loads((srt/'package.json').read_text())['version'],
                          'node': subprocess.check_output([cfg['node'], '--version'], text=True).strip(),
                          'bwrap': subprocess.check_output([cfg['bwrap'], '--version'], text=True).strip()}
    if report['versions']['srt'] != '0.0.83' or report['versions']['node'] != 'v24.22.2': raise RuntimeError('unexpected runtime')
    report['artifact_sha256'] = {str(p): sha(p) for p in [Path(cfg['node']), Path(cfg['bwrap']), Path(cfg['socat']),
        srt/'dist/cli.js', srt/'dist/sandbox/linux-sandbox-utils.js', srt/'dist/sandbox/resolved-address-guard.js',
        srt/'vendor/seccomp/x64/apply-seccomp', Path(cfg['srt'])/'package-lock.json', root/'probe-client']}
    env = {'PATH': f"{root}/node/bin:{root}/tools/usr/bin:/usr/bin:/bin", 'HOME': str(lab/'a/home'), 'TMPDIR': str(lab/'a/tmp')}
    for area in ('a', 'b', 'c', 'control', 'service', 'host-home', 'hardlink-source'):
        for part in ('private', 'workspace', 'home', 'tmp', 'state/agents', 'state/config', 'backend'):
            (lab/area/part).mkdir(parents=True, mode=0o700)
        (lab/area/'private/secret').write_text('SYNTHETIC_'+area)
        (lab/area/'backend/checkpoint').write_text('CORE_CHECKPOINT')
        (lab/area/'state/config/settings').write_text('CONFIG_OK')
    (lab/'policies').mkdir(mode=0o700)
    certdir = lab/'control/private'
    subprocess.run(['/usr/bin/openssl', 'req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1',
                    '-subj', '/CN=127.0.0.1', '-addext', 'subjectAltName=IP:127.0.0.1',
                    '-keyout', str(certdir/'key.pem'), '-out', str(certdir/'ca.pem')],
                   env=env, check=True, capture_output=True, timeout=10)
    for area in ('a', 'b'): (lab/area/'state/config/ca.pem').write_bytes((certdir/'ca.pem').read_bytes())
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain(certdir/'ca.pem', certdir/'key.pem')
    a, b, control = Fixture('A'), Fixture('B'), Fixture('CONTROL')
    secure = Fixture('A', tls_context=context); secure.token = a.token
    a.redirect = b.url+'/ok'
    b.redirect = a.url+'/ok'
    try: ipv6 = Fixture('V6', socket.AF_INET6)
    except OSError: ipv6 = None; report['findings'].append('Host IPv6 fixture unavailable; IPv6 network rejection not verified')
    # Live positive controls outside SRT, using explicit direct transport.
    direct = urllib.request.build_opener(urllib.request.ProxyHandler({}), urllib.request.HTTPSHandler(context=ssl.create_default_context(cafile=str(certdir/'ca.pem'))))
    for service in servers:
        r = direct.open(urllib.request.Request(service.url+'/ok', headers={'Authorization': 'Bearer '+service.token}), timeout=2)
        checks['baseline/'+service.area+('/https' if service is secure else '/http')+str(service.port)] = r.status == 200
        r.close()
    socket_path = lab/'control/private/control.sock'
    abstract = '\0teamx-srt-'+uuid.uuid4().hex
    for address in (str(socket_path), abstract):
        listener = socket.socket(socket.AF_UNIX); listener.bind(address); listener.listen(4); unix_sockets.append(listener)
        with socket.socket(socket.AF_UNIX) as connection: connection.connect(address)
    private = {'other_space': str(lab/'b/private/secret'), 'other_user': str(lab/'c/private/secret'),
               'core': str(lab/'control/private/secret'), 'own_service': str(lab/'service/private/secret'),
               'host_home': str(lab/'host-home/private/secret'), 'own_checkpoint': str(lab/'a/backend/checkpoint'),
               'fixture_tls_key': str(certdir/'key.pem')}
    originals = {p: Path(p).read_bytes() for p in private.values()}
    checks['baseline/same_uid_private_files_readable'] = all(originals.values())
    policies = {}
    for area, fixture in (('a', a), ('b', b)):
        own = lab/area
        aliases = {}
        for label, target in [('symlink', lab/'c/private/secret'), ('checkpoint_link', own/'backend/checkpoint')]:
            p = own/'workspace'/label; p.symlink_to(target); aliases[label] = str(p)
        (own/'workspace/dir-link').symlink_to(lab/'c/private', target_is_directory=True)
        aliases['directory_symlink'] = str(own/'workspace/dir-link/secret')
        (own/'workspace/relative-link').symlink_to('../../c/private/secret')
        aliases['relative_symlink'] = str(own/'workspace/relative-link')
        (own/'workspace/chain').symlink_to('relative-link'); aliases['chained_symlink'] = str(own/'workspace/chain')
        aliases['proc_root_alias'] = '/proc/self/root'+str(lab/'c/private/secret')
        os.link(lab/'hardlink-source/private/secret', own/'workspace/preexisting-hardlink')
        forbidden = {'other_space': (b if area == 'a' else a).url+'/ok', 'host_control': control.url+'/ok',
                     'metadata': 'http://169.254.169.254/latest/meta-data/', 'ali_metadata': 'http://100.100.100.200/',
                     'mapped_other_space': 'http://[::ffff:127.0.0.1]:'+str((b if area=='a' else a).port)+'/ok',
                     'unlisted_hostname': 'http://unlisted.invalid/'}
        if ipv6: forbidden['ipv6'] = ipv6.url+'/ok'
        payload = {'work': str(own/'workspace'), 'agent_state': str(own/'state/agents'),
                   'readonly_config': str(own/'state/config/settings'), 'private': private, 'aliases': aliases,
                   'allowed': fixture.url, 'token': fixture.token, 'expected_body': fixture.area+'_OK',
                   'forbidden_urls': forbidden, 'raw_addresses': {'host_allowed_port': ['127.0.0.1', fixture.port],
                     'other_space': ['127.0.0.1', (b if area=='a' else a).port]},
                   'unix_sockets': {'filesystem_socket': str(socket_path), 'abstract_socket': abstract},
                   'policy': str(lab/'policies'/f'{area}.json')}
        if area == 'a': payload['https_allowed'] = secure.url
        (own/'workspace/fixture.json').write_text(json.dumps(payload))
        allowed = ['127.0.0.1:'+str(fixture.port)]
        if area == 'a': allowed.append('127.0.0.1:'+str(secure.port))
        policy = {'network': {'allowedDomains': allowed, 'deniedDomains': [], 'strictAllowlist': True,
                   'allowAllUnixSockets': False, 'allowLocalBinding': False,
                   'deniedResolvedAddresses': ['10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', 'fc00::/7']},
                  'filesystem': {'denyRead': ['/'], 'allowRead': ['/usr', '/bin', '/sbin', '/lib', '/lib64', '/dev', '/proc',
                   '/etc/ssl', '/etc/alternatives', '/etc/ld.so.cache', '/etc/hosts', '/etc/resolv.conf',
                   '/etc/protocols', '/etc/services', '/etc/nsswitch.conf',
                   cfg['srt'], str(root/'node'), str(root/'tools'), str(root/'payload.py'), str(root/'node-client.mjs'),
                   str(root/'probe-client'), str(own/'workspace'), str(own/'state/config'), str(own/'state/agents'), str(own/'home'), str(own/'tmp')],
                   'allowWrite': [str(own/'workspace'), str(own/'state/agents'), str(own/'home'), str(own/'tmp')], 'denyWrite': []},
                  'bwrapPath': cfg['bwrap'], 'socatPath': cfg['socat'], 'ripgrep': {'command': cfg['rg']},
                  'enableWeakerNestedSandbox': False, 'enableWeakerNetworkIsolation': False, 'allowNestedUserNamespaces': False}
        (lab/'policies'/f'{area}.json').write_text(json.dumps(policy, indent=2)+'\n'); policies[area] = policy
    report['policies'], report['policy_sha256'] = policies, {area: sha(lab/'policies'/f'{area}.json') for area in policies}
    deps_js = "const{SandboxManager}=await import(process.argv[1]);console.log(JSON.stringify(SandboxManager.checkDependencies()))"
    deps = subprocess.run([cfg['node'], '--input-type=module', '-e', deps_js, str(srt/'dist/index.js')], env=env, capture_output=True, text=True, timeout=8)
    report['dependencies'] = json.loads(deps.stdout)
    if deps.returncode or report['dependencies']['errors'] or report['dependencies']['warnings']: raise RuntimeError('mandatory dependency failure or warning')
    for area in ('a', 'b'):
        result = Execution('files-'+area, area, 'files').run()
        obs = result.get('observations', {})
        if obs.get('preexisting_hardlink_readable') or obs.get('preexisting_hardlink_writable'):
            report['findings'].append({'area': area, 'class': 'preexisting_hardlink_alias', **obs})
        Execution('network-'+area, area, 'network').run()
    checks['host_private_originals_unchanged'] = all(Path(p).read_bytes() == content for p, content in originals.items())
    # B has exactly one deliberate unauthenticated request from its own test.
    checks['foreign_fixture_received_no_wrong_token'] = b.rejected == 1 and control.rejected == 0 and (not ipv6 or ipv6.rejected == 0)
    report['preexisting_hardlink_changed_protected_source'] = (lab/'hardlink-source/private/secret').read_text() == 'HARDLINK_CHANGED'
    dns = subprocess.run([cfg['node'], str(root/'dns-guard.mjs')], env=env, capture_output=True, text=True, timeout=8)
    report['dns_guard'] = json.loads(dns.stdout)
    for name, ok in report['dns_guard']['checks'].items(): checks['dns_guard/'+name] = ok
    checks['dns_guard_receipt_success'] = dns.returncode == 0
    # B stays live while A's wrapper, payload and host-side bridge fail.
    for fault in ('wrapper', 'payload', 'proxy'):
        for area in ('a', 'b'):
            for name in ('ready', 'late', 'network-failed', 'arm', 'armed'): (lab/area/'workspace'/name).unlink(missing_ok=True)
        other = Execution('parallel-b-'+fault, 'b', 'network-hold')
        current = Execution('fault-a-'+fault, 'a', 'network-hold')
        await_file(lab/'a/workspace/ready'); await_file(lab/'b/workspace/ready')
        for area in ('a', 'b'): (lab/area/'workspace/arm').write_text('arm')
        for area in ('a', 'b'): await_file(lab/area/'workspace/armed')
        if fault == 'wrapper': target = current.process.pid
        else:
            candidates = []
            for pid in current.live():
                try:
                    commandline = Path(f'/proc/{pid}/cmdline').read_bytes()
                    if fault == 'payload' and b'payload.py\x00network-hold' in commandline: candidates.append(pid)
                    if fault == 'proxy' and Path(f'/proc/{pid}/comm').read_text().strip() == 'socat' and os.readlink(f'/proc/{pid}/ns/net') == os.readlink('/proc/self/ns/net'): candidates.append(pid)
                except OSError: pass
            if not candidates: raise RuntimeError('no verified owned fault target: '+fault)
            target = candidates[0]
        current.send(target, signal.SIGKILL)
        time.sleep(0.2)
        raw_alive = len(current.live())
        receipt = current.settle(grace=0)
        checks['fault/'+fault+'/supervisor_settled'] = receipt['settled']
        if raw_alive: report['findings'].append({'class': 'external_supervisor_required', 'fault': fault, 'remaining_before_cleanup': raw_alive})
        checks['fault/'+fault+'/parallel_b_live'] = other.process.poll() is None and not (lab/'b/workspace/network-failed').exists()
        other.send(other.process.pid, signal.SIGTERM)
        other_receipt = other.settle(grace=1)
        report['cases']['fault-'+fault] = {'a_cleanup': receipt, 'b_cleanup': other_receipt, 'raw_survivors': raw_alive}
        time.sleep(6.2)
        checks['fault/'+fault+'/a_no_delayed_effect'] = not (lab/'a/workspace/late').exists()
        checks['fault/'+fault+'/b_no_delayed_effect'] = not (lab/'b/workspace/late').exists()
    report['status'] = 'mechanisms_verified_with_findings' if all(checks.values()) else 'mechanism_checks_failed'
except Exception as error:
    report['error'] = type(error).__name__+': '+str(error)
finally:
    pending = []
    for execution in managed:
        if not execution.halt.is_set():
            try: execution.settle(grace=0)
            except Exception: pending.append(execution.label)
    for fixture in servers:
        fixture.server.shutdown(); fixture.server.server_close(); fixture.thread.join(timeout=1)
    for listener in unix_sockets: listener.close()
    report['cleanup_pending'] = pending
    if pending: report['status'] = 'cleanup_pending'
    report['counts'] = {'passed': sum(v is True for v in checks.values()), 'failed': [k for k,v in checks.items() if not v]}
    (lab/'report.json').write_text(json.dumps(report, indent=2)+'\n')
    print(json.dumps({'status': report['status'], 'counts': report['counts']}), flush=True)
raise SystemExit(0 if report['status'] == 'mechanisms_verified_with_findings' else 2)
