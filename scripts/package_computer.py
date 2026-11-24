#!/usr/bin/env python3
"""Package the optional linux/amd64 Computer build context without requiring Docker."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

ROOT = Path(__file__).resolve().parents[1]
PACKAGING = ROOT / 'packaging/computer/aio'


def stage_runtime(output):
    output = Path(output)
    output.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, GOOS='linux', GOARCH='amd64', CGO_ENABLED='0')
    subprocess.run(['go', 'build', '-trimpath', '-o', str(output / 'teamx-computer'), './cmd/teamx-computer'], cwd=ROOT, env=env, check=True)
    for name in ['tools.lock.json', 'dependencies.lock.json', 'Dockerfile.runtime']:
        shutil.copy2(PACKAGING / name, output / ('Dockerfile' if name == 'Dockerfile.runtime' else name))
    sources = [ROOT / 'go.mod', ROOT / 'go.sum']
    for directory in ['internal/computer', 'cmd/teamx-computer', 'builtin/computer']:
        sources.extend(sorted((ROOT / directory).rglob('*.go')))
    manifest = {
        'platform': 'linux/amd64',
        'revision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=ROOT, text=True).strip(),
        'working_tree_modified': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=ROOT)),
        'go_version': subprocess.check_output(['go', 'version'], text=True).strip(),
        'source_sha256': {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest() for p in sources},
        'files_sha256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(output.iterdir())},
    }
    (output / 'source.json').write_text(json.dumps(manifest, indent=2) + '\n')
    return manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True, help='new .tar.gz archive')
    args = parser.parse_args()
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    # Exclusive creation prevents overwriting an accepted package.
    with output.open('xb') as archive:
        with tempfile.TemporaryDirectory(prefix='teamx-computer-package-') as work:
            context = Path(work) / 'teamx-computer-runtime'
            stage_runtime(context)
            with tarfile.open(fileobj=archive, mode='w:gz') as tar:
                tar.add(context, arcname=context.name)
    digest = hashlib.sha256(output.read_bytes()).hexdigest()
    output.with_name(output.name + '.sha256').write_text(digest + '  ' + output.name + '\n')
    print(json.dumps({'archive': str(output), 'sha256': digest, 'platform': 'linux/amd64'}))


if __name__ == '__main__':
    main()
