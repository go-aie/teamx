#!/usr/bin/env python3
"""Explicit, isolated two-Computer acceptance for online TeamX settings."""
import argparse
import json
import os
from pathlib import Path
import platform
import secrets
import shutil
import tempfile
import time
import urllib.request
from computer_runtime_acceptance import command as runtime_command, ROOT, PACKAGING

docker_env = {}

def command(*args, **kwargs):
    if args and args[0] == "docker" and docker_env:
        kwargs.setdefault("env", docker_env)
    return runtime_command(*args, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--agent-config', type=Path, help='also run explicit real DSH/model Computer acceptance')
    args = parser.parse_args()
    if not args.run:
        parser.error('--run is required')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    prefix = 'teamx-computer-connection-' + secrets.token_hex(5)
    image = 'teamx-computer-runtime:' + prefix
    created, volumes = [], []
    visual_code = ''.join(str(secrets.randbelow(10)) for _ in range(8))
    report = {'passed': False, 'host_arch': platform.machine(), 'platform': 'linux/amd64'}
    try:
        with tempfile.TemporaryDirectory(prefix=prefix) as work:
            work = Path(work)
            # Public fixed-digest builds need no registry credentials. Keep this
            # run independent of desktop keychain helpers and user's config.
            socket = runtime_command('docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}').stdout.strip()
            client = work / 'docker-client'
            client.mkdir(mode=0o700)
            (client / 'config.json').write_text('{"auths":{}}')
            docker_env.update(os.environ, DOCKER_CONFIG=str(client), DOCKER_HOST=socket)
            command('go', 'build', '-o', str(work / 'teamx-computer'), './cmd/teamx-computer',
                    env=dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0'))
            for source in ['tools.lock.json', 'dependencies.lock.json']:
                shutil.copy2(PACKAGING / source, work / source)
            shutil.copy2(PACKAGING / 'Dockerfile.runtime', work / 'Dockerfile')
            result = command('docker', 'build', '--platform', 'linux/amd64', '-t', image, str(work), timeout=900, check=False)
            (output / 'build.log').write_text(result.stdout + result.stderr)
            if result.returncode:
                raise RuntimeError('isolated image build failed; inspect build.log')
            report['image'] = json.loads(command('docker', 'image', 'inspect', image).stdout)[0]['Id']
            secret = work / 'computer-token'
            secret.write_text(secrets.token_hex(32) + '\n')
            secret.chmod(0o600)
            endpoints = []
            for label in (['old', 'new', 'agent'] if args.agent_config else ['old', 'new']):
                name = prefix + '-' + label
                state, files = name + '-state', name + '-files'
                volumes.extend([state, files])
                command('docker', 'create', '--init', '--platform', 'linux/amd64', '--name', name,
                        '--cpus', '4', '--memory', '6g', '--shm-size', '1g', '--security-opt', 'no-new-privileges',
                        '--mount', f'type=volume,src={state},dst=/var/lib/teamx-computer',
                        '--mount', f'type=volume,src={files},dst=/workspace',
                        '--publish', '127.0.0.1::18090', '--entrypoint', '/bin/sleep', image, 'infinity')
                created.append(name)
                command('docker', 'cp', str(secret), name + ':/run/computer-token')
                command('docker', 'cp', str(PACKAGING / 'prototype/fixture.py'), name + ':/opt/fixture.py')
                site = work / 'site.py'
                site.write_text("from fixture import Site\nfrom http.server import ThreadingHTTPServer\nclass AgentSite(Site):\n    def do_GET(self):\n        if self.path == '/visual':\n            return self.reply('<html><body style=\"background:white;margin:40px\"><h1>Visual acceptance</h1><img src=\"/pixels.svg\"></body></html>')\n        if self.path == '/pixels.svg':\n            return self.reply('<svg xmlns=\"http://www.w3.org/2000/svg\" width=\"800\" height=\"320\"><rect width=\"800\" height=\"320\" fill=\"#fff\"/><text x=\"40\" y=\"200\" fill=\"#142d66\" font-size=\"120\" font-family=\"DejaVu Sans\">" + visual_code + "</text></svg>', 'image/svg+xml')\n        return super().do_GET()\nThreadingHTTPServer(('127.0.0.1', 18765), AgentSite).serve_forever()\n")
                command('docker', 'cp', str(site), name + ':/opt/site.py')
                command('docker', 'start', name)
                command('docker', 'exec', name, 'chown', '0:0', '/run/computer-token')
                command('docker', 'exec', '-d', name, '/bin/sh', '-c',
                        'exec /usr/local/bin/teamx-computer -listen 0.0.0.0:18090 -token-file /run/computer-token > /run/connection-runtime.log 2>&1')
                command('docker', 'exec', '-d', name, 'python3', '/opt/site.py')
                inspected = json.loads(command('docker', 'inspect', name).stdout)[0]
                port = inspected['NetworkSettings']['Ports']['18090/tcp'][0]['HostPort']
                endpoint = 'http://127.0.0.1:' + port
                endpoints.append(endpoint)
                deadline = time.monotonic() + 45
                while True:
                    try:
                        req = urllib.request.Request(endpoint + '/inspect', b'{}',
                            {'Authorization': 'Bearer ' + secret.read_text().strip(), 'Content-Type': 'application/json'})
                        with urllib.request.urlopen(req, timeout=2) as response:
                            assert response.status == 200
                        break
                    except Exception:
                        if time.monotonic() >= deadline:
                            raise
                        time.sleep(0.2)
            env = dict(os.environ, TEAMX_COMPUTER_CONNECTION_REAL='1', TEAMX_COMPUTER_TOKEN_FILE=str(secret),
                       TEAMX_COMPUTER_ENDPOINT=endpoints[0], TEAMX_COMPUTER_REPLACEMENT=endpoints[1],
                       TEAMX_COMPUTER_EVIDENCE=str(output), TEAMX_COMPUTER_HOME=str(work / 'host-home'))
            if args.agent_config:
                command('go', 'build', '-o', str(work / 'teamx'), './cmd/teamx')
                model_env = dict(env, TEAMX_COMPUTER_AGENT_REAL='1', TEAMX_COMPUTER_CLI=str(work / 'teamx'), TEAMX_INPUT_COMPARISON_CONFIG=str(args.agent_config.resolve()),
                                 TEAMX_COMPUTER_ENDPOINT=endpoints[2], TEAMX_COMPUTER_VISUAL_CODE=visual_code, TEAMX_COMPUTER_CONTAINER=created[2])
                model_result = command('go', 'test', '-tags=e2e', './internal/room', '-run', '^TestComputerRealAgentSessionCapabilities$',
                                       '-count=1', '-timeout=14m', '-v', env=model_env, timeout=870, check=False)
                (output / 'agent-model.log').write_text(model_result.stdout + model_result.stderr)
                if model_result.returncode:
                    raise RuntimeError('Agent/DSH/model acceptance failed; inspect agent-model.log')
            result = command('go', 'test', './internal/room', '-run', '^TestComputerRealConnectionSettings$',
                             '-count=1', '-timeout=6m', '-v', env=env, timeout=380, check=False)
            (output / 'connection.log').write_text(result.stdout + result.stderr)
            if result.returncode:
                raise RuntimeError('connection acceptance failed; inspect connection.log')
            for name in created:
                command('docker', 'cp', name + ':/var/lib/teamx-computer/runtime/runtime.json', str(output / (name.rsplit('-', 1)[1] + '-runtime.json')))
            # Prove a saved disable survives an actual TeamX restart while
            # both Runtime containers are stopped and their processes are gone.
            for name in created:
                command('docker', 'stop', '-t', '45', name, timeout=60)
                stopped = json.loads(command('docker', 'inspect', name).stdout)[0]['State']
                assert not stopped['Running'] and stopped['Pid'] == 0
            offline = command('go', 'test', './internal/room', '-run', '^TestComputerRealConnectionSettings$',
                              '-count=1', '-timeout=45s', '-v', env=dict(env, TEAMX_COMPUTER_CONNECTION_PHASE='offline'), timeout=60, check=False)
            (output / 'offline-restart.log').write_text(offline.stdout + offline.stderr)
            if offline.returncode:
                raise RuntimeError('disabled restart failed; inspect offline-restart.log')
            report['offline_restart_after_containers_stopped'] = True
            report['passed'] = True
    finally:
        cleanup = []
        for name in reversed(created):
            command('docker', 'cp', name + ':/run/connection-runtime.log', str(output / (name.rsplit('-', 1)[1] + '-runtime.log')), check=False)
            command('docker', 'stop', '-t', '45', name, timeout=60, check=False)
            state = json.loads(command('docker', 'inspect', name).stdout)[0]['State']
            assert not state['Running'] and state['Pid'] == 0
            command('docker', 'rm', name)
            cleanup.append({'container': name, 'stopped': True, 'removed': True})
        if volumes:
            command('docker', 'volume', 'rm', *volumes)
        report['cleanup'] = cleanup
        report['volumes_removed'] = True
        (output / 'report.json').write_text(json.dumps(report, ensure_ascii=False, indent=2))
    print(json.dumps(report, ensure_ascii=False))


if __name__ == '__main__':
    main()
