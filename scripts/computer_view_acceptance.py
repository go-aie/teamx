#!/usr/bin/env python3
"""Isolated managed Computer viewing/input acceptance; no daily data or accounts."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import tempfile
import time
import urllib.request
from computer_runtime_acceptance import command as run, ROOT, PACKAGING


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--agent-config', type=Path, help='use two real DSH/model Agents alongside the browser')
    parser.add_argument('--group', action='store_true', help='also exercise the same-Room member picker')
    args = parser.parse_args()
    if not args.run:
        parser.error('--run is required')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    name = 'teamx-computer-view-' + secrets.token_hex(5)
    image = 'teamx-computer-runtime:' + name
    volumes = [name + '-state', name + '-files']
    created = False
    report = {'passed': False, 'host_arch': platform.machine(), 'platform': 'linux/amd64',
              'source_sha256': {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                                for base in [ROOT / 'internal/computer', ROOT / 'cmd/teamx-computer']
                                for p in sorted(base.rglob('*.go'))}}
    with tempfile.TemporaryDirectory(prefix=name) as work:
        work = Path(work)
        docker_config = work / 'docker-client'
        docker_config.mkdir(mode=0o700)
        (docker_config / 'config.json').write_text('{"auths":{}}')
        socket = run('docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}').stdout.strip()
        docker_env = dict(os.environ, DOCKER_CONFIG=str(docker_config), DOCKER_HOST=socket)
        def command(*argv, **kw):
            if argv[0] == 'docker':
                kw.setdefault('env', docker_env)
            return run(*argv, **kw)
        try:
            command('go', 'build', '-o', str(work / 'teamx-computer'), './cmd/teamx-computer',
                    env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0'))
            for f in ['Dockerfile.runtime', 'tools.lock.json', 'dependencies.lock.json']:
                shutil.copy2(PACKAGING / f, work / ('Dockerfile' if f.startswith('Dockerfile') else f))
            built = command('docker', 'build', '--platform', 'linux/amd64', '-t', image, str(work), timeout=900, check=False)
            (output / 'build.log').write_text(built.stdout + built.stderr)
            if built.returncode:
                raise RuntimeError('image build failed; see build.log')
            report['image'] = json.loads(command('docker', 'image', 'inspect', image).stdout)[0]['Id']
            token = work / 'token'
            token.write_text(secrets.token_hex(32) + '\n')
            token.chmod(0o600)
            command('docker', 'create', '--init', '--platform', 'linux/amd64', '--name', name,
                    '--cpus', '4', '--memory', '6g', '--shm-size', '1g', '--security-opt', 'no-new-privileges',
                    '--mount', f'type=volume,src={volumes[0]},dst=/var/lib/teamx-computer',
                    '--mount', f'type=volume,src={volumes[1]},dst=/workspace',
                    '--publish', '127.0.0.1::18090', '--publish', '127.0.0.1::18765',
                    '--entrypoint', '/bin/sleep', image, 'infinity')
            created = True
            fixture = ROOT / 'internal/room/testdata/computer_view_site.py'
            command('docker', 'cp', str(fixture), name + ':/opt/site.py')
            command('docker', 'cp', str(token), name + ':/run/computer-token')
            command('docker', 'start', name)
            command('docker', 'exec', name, 'chown', '0:0', '/run/computer-token')
            command('docker', 'exec', '-d', name, '/bin/sh', '-c',
                    'exec /usr/local/bin/teamx-computer -listen 0.0.0.0:18090 -token-file /run/computer-token > /run/view-runtime.log 2>&1')
            command('docker', 'exec', '-d', name, 'python3', '/opt/site.py')
            ports = json.loads(command('docker', 'inspect', name).stdout)[0]['NetworkSettings']['Ports']
            endpoint = 'http://127.0.0.1:' + ports['18090/tcp'][0]['HostPort']
            fixture_url = 'http://127.0.0.1:' + ports['18765/tcp'][0]['HostPort']
            deadline = time.monotonic() + 45
            while True:
                try:
                    req = urllib.request.Request(endpoint + '/inspect', b'{}', {'Authorization': 'Bearer ' + token.read_text().strip()})
                    with urllib.request.urlopen(req, timeout=2) as res:
                        assert res.status == 200
                    break
                except Exception:
                    if time.monotonic() >= deadline:
                        raise
                    time.sleep(.2)
            if args.agent_config:
                command('go', 'build', '-o', str(work / 'teamx'), './cmd/teamx')
            node_modules = ROOT / 'packaging/verifier/node_modules'
            env = dict(os.environ, TEAMX_COMPUTER_VIEW_REAL='1', TEAMX_COMPUTER_VIEW_GROUP='1' if args.group else '0', TEAMX_COMPUTER_ENDPOINT=endpoint,
                       TEAMX_COMPUTER_TOKEN_FILE=str(token), TEAMX_COMPUTER_VIEW_EVIDENCE=str(output),
                       TEAMX_COMPUTER_FIXTURE_URL=fixture_url, NODE_PATH=str(node_modules))
            if args.agent_config:
                env.update(TEAMX_COMPUTER_COMBINED_REAL='1', TEAMX_INPUT_COMPARISON_CONFIG=str(args.agent_config.resolve()), TEAMX_COMPUTER_CLI=str(work / 'teamx'))
            result = command('go', 'test', '-tags=e2e', './internal/room', '-run', '^TestComputerRealViewingAndHandoff$',
                             '-count=1', '-timeout=12m', '-v', env=env, timeout=750, check=False)
            (output / 'browser.log').write_text(result.stdout + result.stderr)
            if result.returncode:
                raise RuntimeError('view/input acceptance failed; see browser.log')
            report['passed'] = True
        finally:
            if created:
                command('docker', 'cp', name + ':/run/view-runtime.log', str(output / 'runtime.log'), check=False)
                command('docker', 'stop', '-t', '45', name, timeout=60, check=False)
                stopped = json.loads(command('docker', 'inspect', name).stdout)[0]['State']
                assert not stopped['Running'] and stopped['Pid'] == 0
                command('docker', 'rm', name)
                command('docker', 'volume', 'rm', *volumes)
            report['cleanup'] = {'container_removed': created, 'volumes_removed': created}
            (output / 'report.json').write_text(json.dumps(report, indent=2))
    print(json.dumps(report))

if __name__ == '__main__':
    main()
