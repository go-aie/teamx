#!/usr/bin/env python3
"""Opt-in, disposable two-screen AIO feasibility acceptance (no daily TeamX IO)."""

import argparse
import hashlib
import json
from pathlib import Path
import platform
import secrets
import subprocess
import time

ROOT = Path(__file__).resolve().parents[1]
CONTEXT = ROOT / 'packaging/computer/aio'


def command(*args, timeout=120, check=True):
    return subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=timeout, check=check)


def write(path, value):
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true', help='explicitly build and run real Docker acceptance')
    parser.add_argument('--output', required=True, type=Path, help='new evidence directory')
    parser.add_argument('--keep-on-failure', action='store_true', help='retain only this disposable container for diagnosis')
    args = parser.parse_args()
    if not args.run:
        parser.error('--run is required; this probe never starts Docker implicitly')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    name = 'teamx-computer-probe-' + secrets.token_hex(5)
    image = 'teamx-computer-feasibility:' + name
    state, files = name + '-state', name + '-files'
    report = {'passed': False, 'name': name, 'host_arch': platform.machine(),
              'platform': 'linux/amd64', 'limits': {'cpus': 4, 'memory_gib': 6, 'shm_gib': 1},
              'source_sha256': {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                                for p in sorted(CONTEXT.rglob('*')) if p.is_file() and '__pycache__' not in p.parts}}
    created = False
    try:
        report['docker'] = json.loads(command('docker', 'version', '--format', '{{json .Server}}').stdout)
        build = command('docker', 'build', '--platform', report['platform'], '-t', image, str(CONTEXT), timeout=900)
        (output / 'build.log').write_text(build.stdout + build.stderr)
        inspection = json.loads(command('docker', 'image', 'inspect', image).stdout)[0]
        report['image_id'] = inspection['Id']
        report['image_repo_digests'] = inspection.get('RepoDigests', [])
        started = time.monotonic()
        report['run_started_at_unix'] = time.time()
        command('docker', 'run', '-d', '--platform', report['platform'], '--name', name,
                '--cpus', '4', '--memory', '6g', '--shm-size', '1g', '--security-opt', 'no-new-privileges',
                '--mount', f'type=volume,src={state},dst=/var/lib/teamx-computer',
                '--mount', f'type=volume,src={files},dst=/workspace', image)
        created = True
        inspection = json.loads(command('docker', 'inspect', name).stdout)[0]
        report['container'] = {key: inspection[key] for key in ('Id', 'Image', 'HostConfig', 'Mounts', 'NetworkSettings')}
        assert not inspection['HostConfig']['Privileged']
        assert all(m['Type'] == 'volume' and m['Name'] in (state, files) for m in inspection['Mounts'])
        assert not inspection['HostConfig'].get('PortBindings')
        report['exercise_started_after_run_seconds'] = time.monotonic() - started
        exercise = command('docker', 'exec', name, 'python3', '/opt/teamx-computer-prototype/verify.py', 'exercise', timeout=420)
        (output / 'exercise.log').write_text(exercise.stdout + exercise.stderr)
        report['run_to_exercise_complete_seconds'] = time.monotonic() - started
        restarted = time.monotonic()
        command('docker', 'restart', '-t', '20', name, timeout=45)
        persistence = command('docker', 'exec', name, 'python3', '/opt/teamx-computer-prototype/verify.py', 'persistence', timeout=180)
        (output / 'persistence.log').write_text(persistence.stdout + persistence.stderr)
        report['restart_to_verified_seconds'] = time.monotonic() - restarted
        versions = command('docker', 'exec', name, 'python3', '-c', '''
import hashlib,json,subprocess,urllib.request
from pathlib import Path
info=json.load(urllib.request.urlopen('http://127.0.0.1:18311/v2/sandbox'))['data']
print(json.dumps({'aiod':info['version'], 'browser':subprocess.check_output(['/usr/local/bin/browser','--version'],text=True).strip(),
'browser_mcp':json.loads(Path('/opt/fnm/node-versions/v22.23.2/installation/lib/node_modules/@agent-infra/mcp-server-browser/package.json').read_text())['version'],
'worker_openapi':json.load(urllib.request.urlopen('http://127.0.0.1:18411/openapi.json'))['info'],
'binaries':{p:hashlib.sha256(Path(p).read_bytes()).hexdigest() for p in ['/usr/local/bin/aiod','/usr/local/bin/computer-use']},
'packages':subprocess.check_output(['dpkg-query','-W','xfce4-session','tigervnc-standalone-server','dbus'],text=True)}))
''')
        report['versions'] = json.loads(versions.stdout)
        report['passed'] = True
    except Exception as error:
        report['error'] = repr(error)
        if isinstance(error, subprocess.CalledProcessError):
            (output / 'failure.log').write_text((error.stdout or '') + (error.stderr or ''))
        raise
    finally:
        if created:
            command('docker', 'cp', name + ':/evidence/.', str(output), check=False)
            (output / 'container.log').write_text(command('docker', 'logs', name, check=False).stdout)
            if not report['passed']:
                command('docker', 'cp', name + ':/run/teamx-computer/.', str(output / 'runtime-logs'), check=False)
            if report['passed'] or not args.keep_on_failure:
                stopped = command('docker', 'stop', '-t', '20', name, timeout=45, check=False)
                inspection = json.loads(command('docker', 'inspect', name).stdout)[0]['State']
                report['cleanup'] = {'stop_exit': stopped.returncode, 'state': inspection,
                                     'stopped': not inspection['Running'] and inspection['Pid'] == 0}
                if report['cleanup']['stopped']:
                    report['cleanup']['container_removed'] = command('docker', 'rm', name).returncode == 0
                    report['cleanup']['volumes_removed'] = command('docker', 'volume', 'rm', state, files).returncode == 0
                else:
                    report['passed'] = False
            else:
                report['retained_for_diagnosis'] = {'container': name, 'volumes': [state, files]}
        write(output / 'report.json', report)
    print(json.dumps({'passed': report['passed'], 'output': str(output)}, ensure_ascii=False))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
