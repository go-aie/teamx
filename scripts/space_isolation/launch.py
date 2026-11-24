#!/usr/bin/env python3
"""Test-only privilege drop before the real DSH; never installed by TeamX."""
import os
from pathlib import Path
import sys

uid = int(os.environ["ISOLATION_UID"])
state = Path(os.environ["DSH_HOME"]).parent
for parent, dirs, files in os.walk(state):
    os.chown(parent, uid, uid)
    for name in dirs + files:
        os.chown(Path(parent) / name, uid, uid, follow_symlinks=False)
os.setgroups([])
os.setgid(uid)
os.setuid(uid)
args = sys.argv[1:]
if "--outer-isolation" in args:
    args.remove("--outer-isolation")
    # Explicit experiment policy, distinct in the Go launch/recovery identity.
    # The outer UID/filesystem/network boundary is the subject of this trial.
    os.environ["DSH_PERMISSION_MODE"] = "danger-full-access"
    args += ["--patch", "/opt/experiment/outer-policy.json"]
os.execv("/usr/local/bin/node", ["node", "/opt/dsh/node_modules/@deepseek-ai/dsh/lib/bin.js", *args])
