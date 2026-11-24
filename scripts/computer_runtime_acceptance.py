#!/usr/bin/env python3
"""Explicit, disposable production AIO Runtime acceptance; no daily TeamX IO."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
PACKAGING = ROOT / 'packaging/computer/aio'


def command(*args, timeout=180, check=True, env=None):
    return subprocess.run(args, cwd=ROOT, text=True, capture_output=True, timeout=timeout, check=check, env=env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    parser.add_argument('--keep-on-failure', action='store_true')
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if not args.run:
        parser.error('--run is required; Docker is never detected implicitly')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    name = 'teamx-computer-runtime-' + secrets.token_hex(5)
    image = 'teamx-computer-runtime:' + name
    state, files = name + '-state', name + '-files'
    report = {'passed': False, 'name': name, 'platform': 'linux/amd64', 'host_arch': platform.machine(),
              'source_sha256': {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                                for base in [ROOT / 'internal/computer', ROOT / 'cmd/teamx-computer']
                                for p in sorted(base.rglob('*.go'))}}
    created = False
    try:
        with tempfile.TemporaryDirectory(prefix='teamx-computer-runtime-') as work:
            work = Path(work)
            env = dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0')
            command('go', 'build', '-o', str(work / 'teamx-computer'), './cmd/teamx-computer', env=env)
            for source in ['tools.lock.json', 'dependencies.lock.json']:
                shutil.copy2(PACKAGING / source, work / source)
            shutil.copy2(PACKAGING / 'Dockerfile.runtime', work / 'Dockerfile')
            build = command('docker', 'build', '--platform', 'linux/amd64', '-t', image, str(work), timeout=900)
            (output / 'build.log').write_text(build.stdout + build.stderr)
            report['image'] = json.loads(command('docker', 'image', 'inspect', image).stdout)[0]['Id']
            secret = work / 'computer-token'
            secret.write_text(secrets.token_hex(32) + '\n')
            secret.chmod(0o600)
            command('docker', 'create', '--init', '--platform', 'linux/amd64', '--name', name,
                    '--cpus', '4', '--memory', '6g', '--shm-size', '1g', '--security-opt', 'no-new-privileges',
                    '--mount', f'type=volume,src={state},dst=/var/lib/teamx-computer',
                    '--mount', f'type=volume,src={files},dst=/workspace',
                    '--publish', '127.0.0.1::18090', '--entrypoint', '/bin/sleep', image, 'infinity')
            created = True
            command('docker', 'cp', str(secret), name + ':/run/computer-token')
            fixture = work / 'acceptance'
            fixture.mkdir()
            shutil.copy2(PACKAGING / 'prototype/fixture.py', fixture / 'fixture.py')
            (fixture / 'site.py').write_text('''import time
from http.server import ThreadingHTTPServer
from fixture import Site
class SlowSite(Site):
    def do_GET(self):
        if self.path.startswith('/slow?'):
            time.sleep(45)
        return super().do_GET()
ThreadingHTTPServer(('127.0.0.1',18765),SlowSite).serve_forever()
''')
            command('docker', 'cp', str(fixture), name + ':/opt/acceptance')
            command('docker', 'start', name)
            command('docker', 'exec', name, 'chown', '0:0', '/run/computer-token')
            def launch_runtime():
                command('docker', 'exec', '-d', name, '/bin/sh', '-c',
                        'exec /usr/local/bin/teamx-computer -listen 0.0.0.0:18090 -token-file /run/computer-token >> /run/runtime.log 2>&1')
            launch_runtime()
            command('docker', 'exec', '-d', name, 'python3', '/opt/acceptance/site.py')
            inspected = json.loads(command('docker', 'inspect', name).stdout)[0]
            report['limits'] = {k: inspected['HostConfig'][k] for k in ['NanoCpus', 'Memory', 'ShmSize', 'Privileged', 'SecurityOpt', 'Init']}
            report['mounts'] = inspected['Mounts']
            assert not inspected['HostConfig']['Privileged']
            assert all(m['Type'] == 'volume' and m['Name'] in [state, files] for m in inspected['Mounts'])
            port = inspected['NetworkSettings']['Ports']['18090/tcp'][0]['HostPort']
            env = dict(os.environ, TEAMX_COMPUTER_REAL='1', TEAMX_COMPUTER_TOKEN_FILE=str(secret),
                       TEAMX_COMPUTER_ENDPOINT='http://127.0.0.1:' + port, TEAMX_COMPUTER_EVIDENCE=str(output))
            for phase in ['exercise', 'recover']:
                # Bounded readiness checks inspect only; no mutation is retried.
                import urllib.request
                deadline = time.monotonic() + 45
                while True:
                    try:
                        req = urllib.request.Request(env['TEAMX_COMPUTER_ENDPOINT'] + '/inspect', b'{}',
                                                     {'Authorization': 'Bearer ' + secret.read_text().strip(), 'Content-Type': 'application/json'})
                        with urllib.request.urlopen(req, timeout=2) as response:
                            assert response.status == 200
                        break
                    except Exception:
                        if time.monotonic() > deadline:
                            raise
                        time.sleep(0.2)
                started = time.monotonic()
                result = command('go', 'test', '-count=1', '-timeout=6m', '-run', '^TestRealRuntime$', '-v', './internal/computer/aio',
                                 env=dict(env, TEAMX_COMPUTER_REAL_PHASE=phase), timeout=380, check=False)
                (output / (phase + '.log')).write_text(result.stdout + result.stderr)
                report[phase + '_seconds'] = time.monotonic() - started
                if result.returncode:
                    raise RuntimeError(phase + ' acceptance failed; see bounded logs')
                if phase == 'exercise':
                    crash = command('docker', 'exec', name, 'python3', '-c', """
import json,os,signal,subprocess
from pathlib import Path
state=json.loads(Path('/var/lib/teamx-computer/runtime/runtime.json').read_text())
record=next(s for s in state['screens'].values() if s['binding']['member']['agent_id']=='A')
uid=23000+record['resource']['index']
child=subprocess.Popen(['sleep','300'],user=uid,group=22000,extra_groups=[],start_new_session=True,
                       stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
found=[]
for p in Path('/proc').glob('[0-9]*'):
    try:
        args=(p/'cmdline').read_bytes().split(b'\\0')
        if args[0]==b'/usr/local/bin/teamx-computer' and b'-listen' in args:
            found.append(int(p.name))
    except FileNotFoundError:
        pass
assert len(found)==1,found
os.kill(found[0],signal.SIGKILL)
print(json.dumps({'runtime_pid':found[0],'background_pid':child.pid,'background_uid':uid,
                  'background_alive_after_runtime_crash':Path('/proc/'+str(child.pid)).exists()}))
""")
                    report['runtime_crash'] = json.loads(crash.stdout)
                    assert report['runtime_crash']['background_alive_after_runtime_crash']
                    launch_runtime()
            # The restarted daemon must have killed the orphan scope before accepting work.
            proof = command('docker', 'exec', name, 'python3', '-c', """
import json
from pathlib import Path
state=json.loads(Path('/var/lib/teamx-computer/runtime/runtime.json').read_text())
uids={23000+s['resource']['index'] for s in state['screens'].values()}
remaining=[]
for p in Path('/proc').glob('[0-9]*'):
    try:
        fields=dict(line.split(':',1) for line in (p/'status').read_text().splitlines() if ':' in line)
        if uids.intersection(map(int,fields['Uid'].split())):
            remaining.append(int(p.name))
    except FileNotFoundError:
        pass
print(json.dumps({'remaining_screen_processes':remaining,'statuses':[s['status'] for s in state['screens'].values()]}))
assert not remaining
""")
            report['stop_proof'] = json.loads(proof.stdout)
            protection = command('docker', 'exec', '--user', '23000:22000', name, 'python3', '-c', """
from pathlib import Path
for path in ['/run/computer-token','/var/lib/teamx-computer/runtime/runtime.json']:
    try:
        Path(path).read_bytes()
    except PermissionError:
        continue
    raise AssertionError('screen user can read private management material')
print('screen user cannot read management credential or metadata')
""")
            report['credential_protection'] = protection.stdout.strip()
            sandbox = command('docker', 'exec', '--user', '23000:22000', name, '/usr/local/bin/browser',
                              '--headless', '--disable-gpu', '--user-data-dir=/tmp/sandbox-probe', '--dump-dom', 'about:blank',
                              timeout=20, check=False)
            report['chromium_sandbox_probe'] = {'exit_code': sandbox.returncode, 'diagnostic': sandbox.stderr[-2500:]}
            command('docker', 'cp', name + ':/run/runtime.log', str(output / 'runtime.log'))
            command('docker', 'cp', name + ':/var/lib/teamx-computer/runtime/runtime.json', str(output / 'runtime-state.json'))
            report['passed'] = True
    except Exception as error:
        report['error'] = repr(error)
        if isinstance(error, subprocess.CalledProcessError):
            (output / 'failure.log').write_text((error.stdout or '') + (error.stderr or ''))
        raise
    finally:
        if created:
            command('docker', 'cp', name + ':/run/runtime.log', str(output / 'runtime.log'), check=False)
            (output / 'container.log').write_text(command('docker', 'logs', name, check=False).stdout + command('docker', 'logs', name, check=False).stderr)
            if not report['passed']:
                command('docker', 'cp', name + ':/run/teamx-computer/.', str(output / 'runtime-logs'), check=False)
            if not report['passed'] and args.keep_on_failure:
                report['retained_for_diagnosis'] = {'container': name, 'volumes': [state, files]}
            else:
                stopped = command('docker', 'stop', '-t', '45', name, check=False, timeout=60)
                status = json.loads(command('docker', 'inspect', name).stdout)[0]['State']
                report['cleanup'] = {'stopped': not status['Running'] and status['Pid'] == 0, 'stop_exit': stopped.returncode}
                if report['cleanup']['stopped']:
                    report['cleanup']['container_removed'] = command('docker', 'rm', name).returncode == 0
                    report['cleanup']['volumes_removed'] = command('docker', 'volume', 'rm', state, files).returncode == 0
                else:
                    report['passed'] = False
        (output / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps({'passed': report['passed'], 'output': str(output)}, ensure_ascii=False))


if __name__ == '__main__':
    main()
