#!/usr/bin/env python3
"""Explicit real managed-Docker, browser and optional DSH/model acceptance.

Uses the shipped pinned image via production settings, creates only disposable
owned resources, and leaves the daily TeamX installation untouched. Normal
regression never invokes Docker or model calls.
"""
import argparse
import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--run', action='store_true')
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--agent-config', type=Path, help='also run two real DSH/model Agents')
    args = parser.parse_args()
    if not args.run:
        parser.error('--run is required')
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    report = dict(passed=False, host_os=platform.system(), host_arch=platform.machine(),
                  image=json.loads((ROOT / 'internal/computer/docker/image.lock.json').read_text()),
                  real_model=bool(args.agent_config), simulated_mobile_viewport=True)
    started = time.monotonic()
    try:
        with tempfile.TemporaryDirectory(prefix='teamx-managed-browser-') as work:
            env = dict(os.environ, TEAMX_COMPUTER_VIEW_REAL='1', TEAMX_COMPUTER_VIEW_MANAGED='1',
                       TEAMX_COMPUTER_VIEW_GROUP='1', TEAMX_COMPUTER_VIEW_EVIDENCE=str(output),
                       NODE_PATH=str(ROOT / 'packaging/verifier/node_modules'))
            if args.agent_config:
                cli = str(Path(work) / 'teamx')
                subprocess.run(['go', 'build', '-o', cli, './cmd/teamx'], cwd=ROOT, check=True)
                env.update(TEAMX_COMPUTER_COMBINED_REAL='1', TEAMX_INPUT_COMPARISON_CONFIG=str(args.agent_config.resolve()),
                           TEAMX_COMPUTER_CLI=cli)
            with (output / 'browser.log').open('w') as log:
                result = subprocess.run(['go', 'test', '-tags=e2e', './internal/room',
                                         '-run', '^TestComputerRealViewingAndHandoff$', '-count=1', '-timeout=18m', '-v'],
                                        cwd=ROOT, env=env, stdout=log, stderr=log, timeout=1140)
            report['exit_code'] = result.returncode
            report['passed'] = result.returncode == 0
            if not report['passed']:
                raise RuntimeError('Managed Computer acceptance failed; inspect browser.log')
    finally:
        report['elapsed_seconds'] = round(time.monotonic() - started, 2)
        (output / 'report.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))


if __name__ == '__main__':
    main()
