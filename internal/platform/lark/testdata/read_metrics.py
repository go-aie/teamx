#!/usr/bin/env python3
"""Acceptance-only CLI proxy. Records categories/timings, never arguments/data."""
import json
from pathlib import Path
import re
import subprocess
import sys
import time

root = Path(__file__).resolve().parent
cfg = json.loads((root / "metrics-config.json").read_text())
args = sys.argv[1:]
assert args[:2] == ["--profile", cfg["profile"]]
a = args[2:]
kind = None
if a == ["--version"]:
    kind = "version"
elif a == ["whoami", "--as", "user"]:
    kind = "identity"
elif len(a) >= 3 and a[0] == "api" and "--as" in a and a[a.index("--as") + 1] == "user":
    method, path = a[1:3]
    if method == "GET":
        if path == "/open-apis/authen/v1/user_info":
            kind = "identity"
        elif path == "/open-apis/im/v1/messages":
            kind = "history"
        elif path == "/open-apis/im/v1/messages/mget":
            kind = "details"
        elif re.fullmatch(r"/open-apis/im/v1/chats/oc_[A-Za-z0-9_-]+(?:/members)?", path):
            kind = "relationship"
    elif method == "POST":
        kind = {
            "/open-apis/im/v1/messages/search": "search",
            "/open-apis/im/v1/chat_p2p/batch_query": "relationship",
            "/open-apis/contact/v3/users/basic_batch": "names",
        }.get(path)
assert kind is not None, "operation outside acceptance read allowlist"
log = root / "calls.jsonl"
assert not log.exists() or len(log.read_text().splitlines()) < 1200, "acceptance CLI budget exhausted"
started = time.monotonic()
result = subprocess.run([cfg["executable"], *args])
with log.open("a", encoding="utf-8") as f:
    f.write(json.dumps({"phase": cfg["phase"], "kind": kind, "seconds": time.monotonic() - started, "exit": result.returncode}) + "\n")
sys.exit(result.returncode)
