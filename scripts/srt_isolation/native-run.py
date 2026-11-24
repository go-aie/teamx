#!/usr/bin/env python3
"""Install only in a new experiment directory, then run the approved non-root probe.

No apt installation, host chmod/ACL, daemon changes or existing container changes.
The private-mount option is explicit because it is an extra host privilege.
"""
import argparse
import json
from pathlib import Path, PurePosixPath
import shlex
import shutil
import subprocess
import tarfile
import tempfile
import uuid

from run import ROOT, HERE, command, digest, download


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--ssh', required=True)
    p.add_argument('--remote-base', choices=['/root/luop/teamx'], required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--allow-private-mount', action='store_true', required=True)
    p.add_argument('--test-binary', type=Path)
    p.add_argument('--independent', action='store_true', help='run SRT mechanism probes without installing or starting DSH')
    p.add_argument('--client-binary', type=Path, help='Linux amd64 standalone Go probe client')
    p.add_argument('--runtime-from', help='reuse immutable dependencies from an earlier experiment under the same base')
    args = p.parse_args()
    if args.independent and (not args.client_binary or args.test_binary):
        p.error('--independent requires --client-binary and excludes --test-binary')
    if args.runtime_from and (str(PurePosixPath(args.runtime_from).parent) != args.remote_base or
                              not PurePosixPath(args.runtime_from).name.startswith('srt-isolation-')):
        p.error('runtime source must be an earlier dedicated experiment under remote-base')
    output = args.output.resolve()
    output.mkdir(mode=0o700)
    remote = args.remote_base + '/srt-isolation-' + uuid.uuid4().hex[:12]
    ssh = ['ssh', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10', args.ssh]
    scp = ['scp', '-q', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10']
    report = {'status': 'runner_failed', 'remote_directory': remote, 'root_core': False,
              'private_mount_explicitly_authorized': True, 'production_changes': False,
              'srt_route_accepted': False, 'real_model': False}
    def remote_run(script, **kwargs):
        return command(*ssh, script, **kwargs)
    try:
        report['source_head'] = command('git', 'rev-parse', 'HEAD', cwd=ROOT, capture_output=True, text=True).stdout.strip()
        report['source_scope'] = 'working tree; listed source hashes are authoritative, not HEAD alone'
        available = int(remote_run('python3 -c ' + shlex.quote(
            "import os; s=os.statvfs('/root/luop/teamx'); print(s.f_bavail*s.f_frsize)"),
            capture_output=True, text=True).stdout)
        required = (1 if args.runtime_from or args.independent else 4) * 1024**3
        report['storage_admission'] = {'available_bytes': available, 'required_bytes': required,
                                       'runtime_reuse': 'hardlinks' if args.runtime_from else 'fresh_install'}
        if available < required:
            raise RuntimeError('insufficient free space for the bounded experiment; no remote directory created')
        sources = [f for f in HERE.iterdir() if f.is_file()]
        sources += [ROOT / 'packaging/dependencies.lock.json', ROOT / 'packaging/dsh/package-lock.json']
        test_source = ROOT / 'internal/agent/dsh/srt_linux_real_test.go'
        if test_source.exists() and not args.independent:
            sources.append(test_source)
        report['source_sha256'] = {str(f.relative_to(ROOT)): digest(f) for f in sorted(sources)}
        with tempfile.TemporaryDirectory(prefix='teamx-srt-native-') as temporary:
            build = Path(temporary)
            node = json.loads((ROOT / 'packaging/dependencies.lock.json').read_text())['dependencies']['node']['platforms']['linux-amd64']
            if not args.runtime_from:
                download(node, build / 'node.tar.xz')
            report['node_archive'] = node
            tools = json.loads((HERE / 'native-tools.lock.json').read_text())
            report['tools'] = tools
            if not args.runtime_from:
                for spec in tools['packages']:
                    download(spec, build / (spec['name'] + '.deb'))
            for name in ('native-launch.sh', 'preflight.mjs', 'dsh-launch.py'):
                shutil.copyfile(HERE / name, build / name)
            dependencies = [('srt', HERE)]
            if not args.independent:
                dependencies.append(('dsh', ROOT / 'packaging/dsh'))
            for name, source in dependencies:
                (build / name).mkdir()
                for filename in ('package.json', 'package-lock.json'):
                    shutil.copyfile(source / filename, build / name / filename)
            cfg = {'lab': '/mnt/preflight', 'srt': '/mnt/srt', 'dsh': '/mnt/dsh',
                   'bwrap': '/mnt/tools/usr/bin/bwrap', 'socat': '/mnt/tools/usr/bin/socat', 'rg': '/mnt/tools/usr/bin/rg'}
            (build / 'preflight-config.json').write_text(json.dumps(cfg))
            native_cfg = dict(cfg, lab='/mnt/lab', node='/mnt/node/bin/node', root='/mnt')
            (build / 'native-config.json').write_text(json.dumps(native_cfg))
            if args.test_binary:
                shutil.copyfile(args.test_binary, build / 'acceptance.test')
                (build / 'acceptance.test').chmod(0o755)
            if args.independent:
                for name in ('independent.py', 'payload.py', 'node-client.mjs', 'dns-guard.mjs'):
                    shutil.copyfile(HERE / name, build / name)
                shutil.copyfile(args.client_binary, build / 'probe-client')
                (build / 'probe-client').chmod(0o755)
            (build / 'native-entry.sh').write_text('''#!/bin/sh
set -u
readlink /proc/self/ns/mnt > /mnt/runtime-evidence/mount-namespace.id
/mnt/node/bin/node /mnt/preflight.mjs /mnt/preflight-config.json
result=$?
if [ "$result" != 3 ]; then exit "$result"; fi
if [ ! -f /mnt/acceptance.test ]; then exit 3; fi
export TEAMX_SRT_EXPERIMENT=1 TEAMX_SRT_CONFIG=/mnt/native-config.json
exec /mnt/acceptance.test -test.run='^TestLinuxSRTDSHCompatibility$' -test.v -test.timeout=8m
''')
            if args.independent:
                (build / 'native-entry.sh').write_text('''#!/bin/sh
set -eu
readlink /proc/self/ns/mnt > /mnt/runtime-evidence/mount-namespace.id
exec /usr/bin/python3 /mnt/independent.py /mnt/native-config.json
''')
            installer = '''#!/bin/sh
set -eu
cd "$(dirname "$0")"
experiment=$(pwd)
umask 022
mkdir install-home install-tmp cache preflight lab runtime-evidence
if [ ! -d node ]; then
mkdir node tools
tar -xf node.tar.xz --strip-components=1 -C node
for archive in bubblewrap.deb socat.deb ripgrep.deb; do dpkg-deb -x "$archive" tools; done
export HOME="$experiment/install-home" PATH="$experiment/node/bin:/usr/bin:/bin"
export TMPDIR="$experiment/install-tmp"
: > npm-user.rc
: > npm-global.rc
export NPM_CONFIG_CACHE="$experiment/cache" NPM_CONFIG_USERCONFIG="$experiment/npm-user.rc" NPM_CONFIG_GLOBALCONFIG="$experiment/npm-global.rc"
cd srt
npm ci --omit=dev --ignore-scripts --no-audit --fund=false --fetch-retries=1 --fetch-timeout=30000
cd ..
if [ -d dsh ]; then
cd dsh
npm ci --omit=dev --ignore-scripts --no-audit --fund=false --fetch-retries=1 --fetch-timeout=30000
cd ..
fi
fi
chmod 755 native-launch.sh native-entry.sh
chown nobody:nogroup preflight lab runtime-evidence
chmod 700 preflight lab runtime-evidence
'''
            if args.runtime_from:
                previous = shlex.quote(args.runtime_from)
                copy = ('test "$(realpath -e -- ' + previous + ')" = ' + previous + '\n'
                        'cp -al ' + previous + '/node ' + previous + '/tools .\n'
                        'cmp -s ' + previous + '/srt/package-lock.json srt/package-lock.json\n'
                        'cp -al ' + previous + '/srt/node_modules srt/\n')
                if not args.independent:
                    copy += ('cmp -s ' + previous + '/dsh/package-lock.json dsh/package-lock.json\n'
                             'cp -al ' + previous + '/dsh/node_modules dsh/\n')
                installer = installer.replace('umask 022\n', 'umask 022\n' + copy)
                report['runtime_from'] = args.runtime_from
            (build / 'install.sh').write_text(installer)
            report['bundle_files_sha256'] = {f.name: digest(f) for f in build.iterdir() if f.is_file()}
            bundle = output / 'upload.tar'
            with tarfile.open(bundle, 'w') as archive:
                for f in sorted(build.iterdir()):
                    archive.add(f, arcname=f.name)
            remote_run('umask 022; mkdir -- ' + shlex.quote(remote))
            command(*scp, str(bundle), args.ssh + ':' + remote + '/upload.tar', timeout=180)
            remote_run('tar --no-same-owner -xf ' + shlex.quote(remote + '/upload.tar') + ' -C ' + shlex.quote(remote))
            bundle.unlink()
        qremote = shlex.quote(remote)
        with (output / 'install.log').open('wb') as log:
            remote_run('/bin/sh ' + shlex.quote(remote + '/install.sh') + ' > ' + shlex.quote(remote + '/install.log') + ' 2>&1',
                       stdout=log, stderr=subprocess.STDOUT, timeout=600)
        with (output / 'run.log').open('wb') as log:
            result = subprocess.run([*ssh, shlex.quote(remote + '/native-launch.sh') + ' ' + qremote +
                                     ' > ' + shlex.quote(remote + '/run.log') + ' 2>&1'],
                                    stdout=log, stderr=subprocess.STDOUT, timeout=600)
        report['remote_exit_code'] = result.returncode
        for subdir in ('preflight', 'lab', 'runtime-evidence'):
            (output / subdir).mkdir()
        artifacts = ['install.log', 'run.log', 'runtime-evidence/mount-namespace.id']
        artifacts += ['lab/report.json'] if args.independent else ['preflight/report.json', 'preflight/policy.json', 'preflight/linux-packages.tsv']
        for name in artifacts:
            command(*scp, args.ssh + ':' + remote + '/' + name, str(output / name), timeout=60)
        subprocess.run([*scp, args.ssh + ':' + remote + '/lab/report.json', str(output / 'lab/report.json')],
                       capture_output=True, timeout=30)
        report['status'] = 'independent_probe' if args.independent else json.loads((output / 'preflight/report.json').read_text())['status']
        if (output / 'lab/report.json').exists():
            report['acceptance_status'] = json.loads((output / 'lab/report.json').read_text())['status']
            report['status'] = report['acceptance_status'] if args.independent else ('compatibility_failed' if report['acceptance_status'] == 'failed' else 'compatibility_only')
        namespace = (output / 'runtime-evidence/mount-namespace.id').read_text().strip()
        scan = "import glob,os,json; expected=" + repr(namespace) + "; found=[]\nfor p in glob.glob('/proc/[0-9]*/ns/mnt'):\n try:\n  if os.readlink(p)==expected: found.append(int(p.split('/')[2]))\n except OSError: pass\nprint(json.dumps({'mount_namespace':expected,'remaining_pids':found}))"
        receipt = json.loads(remote_run('python3 -c ' + shlex.quote(scan), capture_output=True, text=True).stdout)
        report['private_mount_cleanup'] = receipt
        if receipt['remaining_pids']:
            report['status'] = 'cleanup_pending'
            return_code = 1
        else:
            return_code = result.returncode if args.independent else (result.returncode or 3)
    except (OSError, subprocess.SubprocessError, ValueError, RuntimeError) as error:
        report['error'] = str(error)
        return_code = 1
        subprocess.run([*scp, args.ssh + ':' + remote + '/install.log', str(output / 'install.log')],
                       capture_output=True, timeout=30)
    finally:
        (output / 'runner.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps({'status': report['status'], 'evidence': str(output), 'remote_directory': remote, 'exit_code': return_code}))
    return return_code


if __name__ == '__main__':
    raise SystemExit(main())
