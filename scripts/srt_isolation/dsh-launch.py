#!/usr/bin/env python3
"""Test-only whole-DSH wrapper. Policy is an explicit, host-written file."""
import json
import os
from pathlib import Path
import sys

if sys.argv[1:2] != ['--policy'] or len(sys.argv) < 4:
    raise SystemExit('explicit experiment policy required')
policy = Path(sys.argv[2])
cfg = json.loads(Path(os.environ['TEAMX_SRT_CONFIG']).read_text())
if policy.parent != Path(cfg['lab']) / 'policies':
    raise SystemExit('policy outside experiment authority')
node = cfg['node']
os.execv(node, [node, cfg['srt'] + '/node_modules/@anthropic-ai/sandbox-runtime/dist/cli.js',
                # SRT excludes loopback/private addresses in NO_PROXY by default.
                # Clear that bypass before Node starts: the sole permitted model
                # listener must still traverse SRT's exact host:port proxy policy.
                '--settings', str(policy), '--', '/usr/bin/env', 'NO_PROXY=', 'no_proxy=',
                'NODE_USE_ENV_PROXY=1', node,
                cfg['dsh'] + '/node_modules/@deepseek-ai/dsh/lib/bin.js', *sys.argv[3:]])
