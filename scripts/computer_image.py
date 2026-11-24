#!/usr/bin/env python3
"""Build a prebuilt Computer candidate, or publish its reviewed immutable lock.

No push occurs during build. Publishing requires the explicit publish command,
and uses existing Docker authentication; credentials never enter this script.
"""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import subprocess
import tempfile

from package_computer import ROOT, stage_runtime

REPOSITORY = 'ghcr.io/go-aie/teamx/computer'
LOCK = ROOT / 'internal/computer/docker/image.lock.json'


def command(*args):
    return subprocess.run(args, check=True, text=True, capture_output=True).stdout.strip()


def build(output, update_lock=False):
    output.mkdir(parents=True, exist_ok=False)
    context = output / 'context'
    source = stage_runtime(context)
    iid = output / 'image-id'
    metadata = output / 'build-metadata.json'
    # The pinned public base needs no keychain credentials. Isolate build from
    # desktop credential helpers, which may wait indefinitely in a service.
    host = os.environ.get('DOCKER_HOST') or command('docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}')
    with tempfile.TemporaryDirectory(prefix='teamx-computer-image-client-') as client:
        Path(client, 'config.json').write_text('{"auths":{}}')
        env = dict(os.environ, DOCKER_CONFIG=client, DOCKER_HOST=host)
        env.pop('DOCKER_CONTEXT', None)
        with (output / 'build.log').open('w') as log:
            result = subprocess.run(['docker', 'build', '--platform', 'linux/amd64',
                                     '--provenance=false', '--iidfile', str(iid), '--metadata-file', str(metadata), str(context)], env=env, stdout=log, stderr=log, timeout=1200)
        if result.returncode:
            raise RuntimeError('Computer image build failed; inspect build.log')
    meta = json.loads(metadata.read_text())
    image_id = meta['containerimage.digest']
    config_digest = meta.get('containerimage.config.digest')
    if not config_digest:
        # Some Docker Desktop exporters omit the config key from metadata.
        # The fixed single-stage build emits exactly one final config digest.
        exported = re.findall(r'exporting config (sha256:[0-9a-f]{64})', (output / 'build.log').read_text())
        if len(exported) != 1:
            raise RuntimeError('Cannot verify Computer config digest from build metadata')
        config_digest = exported[0]
    assert re.fullmatch(r'sha256:[0-9a-f]{64}', config_digest)
    assert image_id.startswith('sha256:') and len(image_id) == 71
    local_id = iid.read_text().strip()
    inspected = subprocess.run(['docker', 'image', 'inspect', image_id], text=True, capture_output=True)
    if inspected.returncode == 0:
        image = json.loads(inspected.stdout)[0]
        local_id = image['Id']
    else:
        image = json.loads(command('docker', 'image', 'inspect', local_id))[0]
    assert image['Os'] == 'linux' and image['Architecture'] == 'amd64'
    assert image['Config']['Labels']['org.opencontainers.image.source'] == 'https://github.com/go-aie/teamx'
    reference = REPOSITORY + ':runtime-' + image_id[7:23]
    command('docker', 'tag', local_id, reference)
    lock = dict(reference=reference, image_id=image_id, config_digest=config_digest, platform='linux/amd64')
    raw = json.dumps(lock, indent=2) + '\n'
    (output / 'image.lock.json').write_text(raw)
    (output / 'candidate.json').write_text(json.dumps(dict(
        **lock, published=False, source=source,
        dockerfile_sha256=hashlib.sha256((context / 'Dockerfile').read_bytes()).hexdigest(),
    ), indent=2) + '\n')
    if update_lock:
        LOCK.write_text(raw)
    print(json.dumps(dict(**lock, published=False, output=str(output))))


def publish(lock_path, output):
    lock = json.loads(lock_path.read_text())
    assert lock['platform'] == 'linux/amd64'
    expected = REPOSITORY + ':runtime-' + lock['image_id'][7:23]
    assert lock['reference'] == expected
    image = json.loads(command('docker', 'image', 'inspect', lock['reference']))[0]
    assert image['Id'] in (lock['image_id'], lock['config_digest'])
    assert image['Config']['Labels']['org.opencontainers.image.source'] == 'https://github.com/go-aie/teamx'
    # Refuse to overwrite an existing output receipt; publish only this lock.
    with output.open('x') as receipt:
        subprocess.run(['docker', 'push', lock['reference']], check=True)
        image = json.loads(command('docker', 'image', 'inspect', lock['reference']))[0]
        digests = [d for d in image['RepoDigests'] if d.startswith(REPOSITORY + '@sha256:')]
        assert len(digests) == 1
        receipt.write(json.dumps(dict(**lock, registry_digest=digests[0], published=True), indent=2) + '\n')
    published_lock = dict(lock, image_id=digests[0].split('@', 1)[1])
    output.with_name('published-image.lock.json').write_text(json.dumps(published_lock, indent=2) + '\n')
    print('Published reviewed Computer image; verify anonymous pull before adoption.')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    b = sub.add_parser('build')
    b.add_argument('--output', type=Path, required=True)
    b.add_argument('--update-lock', action='store_true')
    p = sub.add_parser('publish')
    p.add_argument('--lock', type=Path, default=LOCK)
    p.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.action == 'build':
        build(args.output.resolve(), args.update_lock)
    else:
        publish(args.lock.resolve(), args.output.resolve())


if __name__ == '__main__':
    main()
