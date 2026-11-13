#!/usr/bin/env python3
"""Exercise the pinned Linux CLI's own storage with synthetic secrets only.

No host CLI configuration is mounted and containers have no network access.
This checks storage, not a real login, refresh, user identity, or sending.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import subprocess
import tarfile
import tempfile
from datetime import datetime, timezone


def run_probe(archive, image, report):
    root = Path(__file__).resolve().parents[1]
    lock = json.loads((root / "packaging/dependencies.lock.json").read_text())
    dep = lock["dependencies"]["lark-cli"]
    expected = dep["platforms"]["linux-amd64"]["sha256"]
    digest = hashlib.sha256(archive.read_bytes()).hexdigest()
    if digest != expected:
        raise RuntimeError("archive does not match the locked linux-amd64 SHA-256")
    image_info = json.loads(subprocess.check_output(
        ["docker", "image", "inspect", image], text=True))[0]
    docker_arch = subprocess.check_output(
        ["docker", "info", "--format", "{{.Architecture}}"], text=True).strip()
    if image_info["Architecture"] != "amd64":
        raise RuntimeError("use a Linux amd64 image (emulation is recorded separately)")
    result = {
        "schema_version": 1, "status": "failed", "cli_version": dep["version"],
        "recorded_at": datetime.now(timezone.utc).isoformat(),
        "archive_sha256": digest, "image_id": image_info["Id"],
        "image": image, "host": platform.platform(),
        "docker_arch": docker_arch,
        "execution": ("linux-amd64 emulated container" if docker_arch not in ("amd64", "x86_64")
                      else "linux-amd64 container") + "; not native server acceptance",
        "network": "none", "credential_material": "synthetic app secret only",
        "real_login": "not_run", "refresh": "not_run", "dpop": "not_run",
        "user_reads": "not_run", "notifications": "not_run", "send": "blocked_by_teamx_linux_pin_guard",
        "checks": [],
    }
    report.parent.mkdir(parents=True, exist_ok=True)
    try:
        with tempfile.TemporaryDirectory(prefix="teamx-lark-headless-") as tmp:
            work = Path(tmp)
            with tarfile.open(archive) as bundle:
                entry = bundle.getmember("lark-cli")
                if not entry.isfile():
                    raise RuntimeError("CLI archive entry is not a regular file")
                binary = work / "lark-cli"
                with bundle.extractfile(entry) as source:
                    binary.write_bytes(source.read())
                binary.chmod(0o755)
            for name in ("config", "data", "pinned"):
                (work / name).mkdir(mode=0o700)

            def cli(*args, config="config", stdin=None):
                completed = subprocess.run([
                    "docker", "run", "--rm", "--platform", "linux/amd64",
                    "--network", "none", "--read-only", "--cap-drop", "ALL",
                    "--security-opt", "no-new-privileges", "--user", f"{os.getuid()}:{os.getgid()}",
                    "-i", "-v", f"{work}:/probe", "-w", "/probe",
                    "-e", f"LARKSUITE_CLI_CONFIG_DIR=/probe/{config}",
                    "-e", "LARKSUITE_CLI_DATA_DIR=/probe/data",
                    "-e", "LARKSUITE_CLI_NO_UPDATE_NOTIFIER=1",
                    "-e", "LARKSUITE_CLI_NO_SKILLS_NOTIFIER=1",
                    image, "/probe/lark-cli", *args,
                ], input=stdin, text=True, capture_output=True, timeout=45)
                # Never return raw CLI diagnostics or secret material to the report.
                return completed

            def check(name, condition):
                if not condition:
                    raise RuntimeError(f"probe check failed: {name}")
                result["checks"].append(name)

            check("locked CLI version", cli("--version").stdout.strip() == dep["expected_version"])
            # init persists locally; its optional online app probe cannot
            # succeed here. Its exit code is not evidence of platform login.
            secret = "synthetic-headless-storage-probe-only"
            init = cli("config", "init", "--app-id", "cli_teamx_probe", "--name", "probe",
                       "--app-secret-stdin", stdin=secret + "\n")
            result["init_exit_code"] = init.returncode
            config_path = work / "config/config.json"
            check("official init persisted config without network", config_path.is_file())
            config = json.loads(config_path.read_text())
            check("config contains references, no plaintext secret", secret not in config_path.read_text())
            storage = work / "data/lark-cli"
            master = storage / "master.key"
            encrypted = storage / "appsecret_cli_teamx_probe.enc"
            check("official encrypted backend and private modes", master.is_file() and encrypted.is_file()
                  and master.stat().st_size == 32 and storage.stat().st_mode & 0o777 == 0o700
                  and all(p.stat().st_mode & 0o777 == 0o600 for p in (master, encrypted)))
            check("encrypted value does not contain synthetic plaintext", secret.encode() not in encrypted.read_bytes())

            def readable(directory):
                status = cli("auth", "status", "--json", config=directory)
                return status.returncode == 0 and json.loads(status.stdout).get("appId") == "cli_teamx_probe"

            check("new container resolves saved app secret without GUI or D-Bus", readable("config"))
            # Only public app identity and the official secret reference go into
            # the temporary config. Never copy raw tokens or encrypted storage.
            app = config["apps"][0]
            public = {key: app[key] for key in ("name", "appId", "appSecret", "brand")}
            public.update(defaultAs="user", dpopMode="preferred", users=[])
            (work / "pinned/config.json").write_text(json.dumps({"currentApp": app["appId"], "apps": [public]}))
            (work / "pinned/config.json").chmod(0o400)
            check("separate public config resolves same official store", readable("pinned"))
            config_path.write_text(json.dumps({"currentApp": "cli_other", "apps": []}))
            check("default config change does not change isolated app selection", readable("pinned"))
            master.write_bytes(b"corrupt-probe-key")
            check("corrupt master key fails closed after process restart", not readable("pinned"))
            result["status"] = "passed"
    finally:
        report.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--archive", required=True, type=Path, help="locked Linux amd64 lark-cli tar.gz")
    parser.add_argument("--image", default="ubuntu:24.04", help="already pulled Linux amd64 Docker image")
    parser.add_argument("--report", required=True, type=Path)
    opts = parser.parse_args()
    run_probe(opts.archive.resolve(), opts.image, opts.report.resolve())
