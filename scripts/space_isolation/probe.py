#!/usr/bin/env python3
"""Executed by the real DSH bash tool, as the untrusted Agent UID."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import urllib.error
import urllib.request

out = {"agent_uid": os.geteuid() == 10001, "supplementary_groups_empty": not os.getgroups()}
for name, path in (("glab_config", "/lab/b/private/glab/config.yml"),
                   ("lark_master_key", "/lab/b/private/lark-data/lark-cli/master.key"),
                   ("lark_ciphertext", "/lab/b/private/lark-data/lark-cli/appsecret_cli_isolation_probe.enc")):
    try:
        Path(path).read_bytes()
        out[name + "_denied"] = False
    except PermissionError:
        out[name + "_denied"] = True
for area in ("b", "c", "control", "service"):
    target = Path("/lab") / area / "private/secret"
    for operation in ("read", "write"):
        try:
            if operation == "read":
                target.read_bytes()
            else:
                with target.open("ab") as stream:
                    stream.write(b"UNAUTHORIZED")
            denied = False
        except PermissionError:
            denied = True
        out[area + "_" + operation + "_denied"] = denied
try:
    Path("escape/secret").read_bytes()
    out["symlink_denied"] = False
except PermissionError:
    out["symlink_denied"] = True
child = subprocess.run(["python3", "-c", "open('/lab/b/private/secret').read()"], capture_output=True)
out["child_denied"] = child.returncode != 0 and b"PermissionError" in child.stderr
try:
    Path("/proc/1/environ").read_bytes()
    out["supervisor_environ_denied"] = False
except PermissionError:
    out["supervisor_environ_denied"] = True
status = dict(line.split(":", 1) for line in Path("/proc/self/status").read_text().splitlines() if ":" in line)
out["capabilities_empty"] = int(status["CapEff"].strip(), 16) == 0
out["no_new_privileges"] = status["NoNewPrivs"].strip() == "1"
out["docker_socket_absent"] = not Path("/var/run/docker.sock").exists()
for name, address in (("metadata", "169.254.169.254"), ("host_gateway", "172.17.0.1"), ("other_worker", "172.17.0.2")):
    try:
        with socket.create_connection((address, 80), timeout=0.4):
            out[name + "_denied"] = False
    except OSError:
        out[name + "_denied"] = True
try:
    with socket.create_connection((os.environ["ISOLATION_PEER_IP"], 18081), timeout=0.4):
        out["verified_peer_denied"] = False
except OSError:
    out["verified_peer_denied"] = True
try:
    urllib.request.urlopen(sys.argv[1] + "/control", timeout=2)
    out["control_http_denied"] = False
except urllib.error.HTTPError as error:
    out["control_http_denied"] = error.code == 401
try:
    urllib.request.urlopen(sys.argv[1] + "/uid-canary", timeout=2)
    out["uid_canary_removed"] = False
except urllib.error.HTTPError as error:
    out["uid_canary_removed"] = error.code == 401
try:
    Path("/opt/experiment/unauthorized").write_text("wrong")
    out["release_write_denied"] = False
except OSError:
    out["release_write_denied"] = True
for binary, prefix in (("glab", "glab 1.127.0"), ("lark-cli", "lark-cli version 1.0.99")):
    completed = subprocess.run([binary, "--version"], capture_output=True, timeout=5)
    out[binary + "_installed"] = completed.returncode == 0 and completed.stdout.decode().startswith(prefix)
# Actual CLIs receive a forbidden foreign config directory. They may not adopt
# it, or fall back to a deployment account. No real account or network login.
for binary, argv, env in (
    ("glab", ["auth", "status", "--hostname", "gitlab.example.invalid"], {"GLAB_CONFIG_DIR": "/lab/b/private/glab"}),
    ("lark-cli", ["auth", "status"], {"LARKSUITE_CLI_CONFIG_DIR": "/lab/b/private/lark-config", "LARKSUITE_CLI_DATA_DIR": "/lab/b/private/lark-data"}),
):
    try:
        completed = subprocess.run([binary, *argv], env={**os.environ, **env}, capture_output=True, timeout=10)
        output = completed.stdout + completed.stderr
        out[binary + "_foreign_config_denied"] = completed.returncode != 0 and b"SYNTHETIC_B" not in output
    except subprocess.TimeoutExpired:
        out[binary + "_foreign_config_denied"] = False
Path("probe.json").write_text(json.dumps(out, indent=2))
print(json.dumps(out))
