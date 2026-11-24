#!/usr/bin/env python3
"""Bounded SRT/Linux prerequisite experiment; no production launch or credentials.

This entry point can disqualify a host, but cannot pass S3a.1: when prerequisites
fail, do not install speculative acceptance code or weaken the host to continue.
"""
import argparse
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request
import urllib.error
import uuid

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def command(*args, timeout=30, **kwargs):
    return subprocess.run(args, check=True, timeout=timeout, **kwargs)


def download(spec, target):
    for attempt in range(3):
        try:
            with urllib.request.urlopen(spec["url"], timeout=60) as response, target.open("wb") as output:
                shutil.copyfileobj(response, output)
            break
        except (urllib.error.URLError, TimeoutError, OSError):
            if attempt == 2:
                raise
    if digest(target) != spec["sha256"]:
        raise RuntimeError("locked archive digest mismatch: " + target.name)


def settle(name):
    receipt = {"settled": False, "removed": False}
    try:
        subprocess.run(["docker", "stop", "-t", "10", name], capture_output=True, timeout=25)
    except (OSError, subprocess.SubprocessError) as error:
        receipt["stop_error"] = type(error).__name__
    try:
        result = command("docker", "inspect", "--format", "{{json .State}}", name, capture_output=True, text=True)
        state = json.loads(result.stdout)
        receipt.update(running=state["Running"], pid=state["Pid"])
        receipt["settled"] = not state["Running"] and state["Pid"] == 0
        if receipt["settled"]:
            command("docker", "rm", name, capture_output=True)
            receipt["removed"] = True
    except (OSError, subprocess.SubprocessError, ValueError) as error:
        receipt["inspect_or_remove_error"] = type(error).__name__
    return receipt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True, help="new private evidence directory")
    args = parser.parse_args()
    output = args.output.resolve()
    output.mkdir(mode=0o700)  # Refuse overwrite of old success or failure evidence.
    report = {"status": "failed", "production_changes": False, "real_model": False,
              "srt_route_accepted": False, "host_type": "disposable_docker_linux",
              "started_at": datetime.now(timezone.utc).isoformat()}
    name = "teamx-srt-probe-" + uuid.uuid4().hex[:12]
    volume = name + "-data"
    container_created = volume_created = False
    result_code = 1
    try:
        report["source_head"] = command("git", "rev-parse", "HEAD", cwd=ROOT, capture_output=True, text=True).stdout.strip()
        report["source_scope"] = "working tree; listed file hashes are authoritative, not HEAD alone"
        sources = [p for p in HERE.iterdir() if p.is_file()]
        sources += [ROOT / "packaging/dependencies.lock.json", ROOT / "packaging/dsh/package.json",
                    ROOT / "packaging/dsh/package-lock.json"]
        report["source_sha256"] = {str(p.relative_to(ROOT)): digest(p) for p in sorted(sources)}
        locks = json.loads((HERE / "release-lock.json").read_text())
        info = command("docker", "info", "--format", "{{json .}}", capture_output=True, text=True)
        daemon = json.loads(info.stdout)
        arch = {"aarch64": "arm64", "x86_64": "amd64"}[daemon["Architecture"]]
        report["docker_host"] = {key: daemon[key] for key in
                                 ("OSType", "Architecture", "KernelVersion", "ServerVersion", "SecurityOptions")}
        if daemon["OSType"] != "linux":
            raise RuntimeError("Linux Docker daemon required")
        node_lock = json.loads((ROOT / locks["node_lock"]).read_text())["dependencies"]["node"]
        report["node_archive"] = node_lock["platforms"]["linux-" + arch]
        report["srt_archive"] = locks["srt"]
        with tempfile.TemporaryDirectory(prefix="teamx-srt-build-") as temporary:
            build = Path(temporary)
            download(locks["srt"], build / "srt.tgz")
            download(report["node_archive"], build / "node.tar.xz")
            with tarfile.open(build / "node.tar.xz") as archive:
                member = next(m for m in archive.getmembers() if m.isfile() and m.name.endswith("/bin/node"))
                with archive.extractfile(member) as source, (build / "node").open("wb") as target:
                    shutil.copyfileobj(source, target)
            (build / "node").chmod(0o755)
            (build / "node.tar.xz").unlink()
            (build / "srt.tgz").unlink()
            # Explicit build context: never copy a home, .teamx, .env or repo.
            for filename in ("Dockerfile", "package.json", "package-lock.json", "preflight.mjs"):
                shutil.copyfile(HERE / filename, build / filename)
            (build / "dsh").mkdir()
            for filename in ("package.json", "package-lock.json"):
                shutil.copyfile(ROOT / "packaging/dsh" / filename, build / "dsh" / filename)
            with (output / "build.log").open("wb") as log:
                command("docker", "build", "--progress=plain", "-t", name, str(build), timeout=360,
                        stdout=log, stderr=subprocess.STDOUT)
        report["image"] = command("docker", "image", "inspect", name, "--format", "{{.Id}}",
                                  capture_output=True, text=True).stdout.strip()
        command("docker", "volume", "create", volume, capture_output=True)
        volume_created = True
        command("docker", "create", "--name", name, "--init", "--read-only", "--cap-drop", "ALL",
                "--security-opt", "no-new-privileges", "--pids-limit", "192", "--memory", "1g", "--cpus", "2",
                "--mount", "type=volume,source=" + volume + ",target=/lab",
                "--tmpfs", "/tmp:rw,nosuid,nodev,size=64m,mode=1777", report["image"], capture_output=True)
        container_created = True
        with (output / "run.log").open("wb") as log:
            # docker start -a's status is not the authoritative process exit code.
            subprocess.run(["docker", "start", "-a", name], stdout=log, stderr=subprocess.STDOUT, timeout=100)
        state = json.loads(command("docker", "inspect", name, capture_output=True, text=True).stdout)[0]
        report["container_exit_code"] = state["State"]["ExitCode"]
        report["security"] = {key: state["HostConfig"][key] for key in
                              ("Privileged", "CapAdd", "CapDrop", "SecurityOpt", "PidMode", "NetworkMode", "Binds", "ReadonlyRootfs", "Init")}
        report["container_user"] = state["Config"]["User"]
        for filename in ("report.json", "policy.json", "linux-packages.tsv"):
            command("docker", "cp", name + ":/lab/" + filename, str(output / filename), capture_output=True)
        probe = json.loads((output / "report.json").read_text())
        report["probe_report_sha256"] = digest(output / "report.json")
        report["status"] = probe["status"]
        result_code = state["State"]["ExitCode"] or 3  # A preflight cannot pass the full acceptance.
    except (OSError, subprocess.SubprocessError, ValueError, KeyError, RuntimeError) as error:
        report.update(status="runner_failed", error=str(error))
    finally:
        if container_created:
            report["cleanup"] = settle(name)
        if volume_created and (not container_created or report.get("cleanup", {}).get("removed")):
            try:
                command("docker", "volume", "rm", volume, capture_output=True)
                report["synthetic_volume_removed"] = True
            except (OSError, subprocess.SubprocessError) as error:
                report["volume_cleanup_error"] = str(error)
        if container_created and not report.get("cleanup", {}).get("removed"):
            report["status"], result_code = "cleanup_pending", 1
        if volume_created and not report.get("synthetic_volume_removed"):
            report["status"], result_code = "cleanup_pending", 1
        (output / "runner.json").write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"status": report["status"], "evidence": str(output), "exit_code": result_code}))
    return result_code


if __name__ == "__main__":
    raise SystemExit(main())
