#!/usr/bin/env python3
"""Run bounded synthetic Linux isolation acceptance; never mount a user home."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parents[2]


def command(*args, **kw):
    return subprocess.run(args, check=True, **kw)


def settle(name):
    result = {"settled": False, "removed": False}
    # Diagnostics and stop failures do not prevent the independent state check.
    try:
        subprocess.run(["docker", "stop", "-t", "10", name], capture_output=True, timeout=30)
    except (subprocess.SubprocessError, OSError) as error:
        result["stop_error"] = type(error).__name__
    try:
        state = json.loads(subprocess.check_output(["docker", "inspect", "--format", "{{json .State}}", name], text=True, timeout=20))
        result["settled"] = not state["Running"] and state["Pid"] == 0
        if result["settled"]:
            command("docker", "rm", name, stdout=subprocess.DEVNULL, timeout=20)
            result["removed"] = True
    except (subprocess.SubprocessError, OSError, ValueError) as error:
        result["inspect_or_remove_error"] = type(error).__name__
    return result


def download_binary(spec, dest, name):
    with tempfile.TemporaryDirectory() as temporary:
        archive = Path(temporary) / "release.tgz"
        with urllib.request.urlopen(spec["url"], timeout=90) as response:
            archive.write_bytes(response.read())
        if hashlib.sha256(archive.read_bytes()).hexdigest() != spec["sha256"]:
            raise RuntimeError(name + " archive digest mismatch")
        with tarfile.open(archive) as tar:
            member = next(m for m in tar.getmembers() if m.isfile() and Path(m.name).name == name)
            with tar.extractfile(member) as stream, dest.open("wb") as output:
                shutil.copyfileobj(stream, output)
        dest.chmod(0o755)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True, help="new evidence directory")
    parser.add_argument("--image", default="teamx-space-isolation:experiment")
    parser.add_argument("--reuse-image", action="store_true")
    args = parser.parse_args()
    args.output = args.output.resolve()
    args.output.mkdir(mode=0o700)  # Refuse overwrite, including earlier failed evidence.
    architecture = subprocess.check_output(["docker", "info", "--format", "{{.Architecture}}"], text=True).strip()
    arch = {"aarch64": "arm64", "x86_64": "amd64"}[architecture]
    with tempfile.TemporaryDirectory(prefix="teamx-isolation-build-") as temporary:
        build = Path(temporary)
        if not args.reuse_image:
            for name in ("Dockerfile", "launch.py", "probe.py", "outer-policy.json"):
                shutil.copyfile(ROOT / "scripts/space_isolation" / name, build / name)
            for name in ("package.json", "package-lock.json"):
                shutil.copyfile(ROOT / "packaging/dsh" / name, build / name)
            glab = json.loads((ROOT / "tools/gitlab/glab.lock.json").read_text())["platforms"]["linux_" + arch]
            lark = json.loads((ROOT / "packaging/dependencies.lock.json").read_text())["dependencies"]["lark-cli"]["platforms"]["linux-" + arch]
            node = json.loads((ROOT / "packaging/dependencies.lock.json").read_text())["dependencies"]["node"]["platforms"]["linux-" + arch]
            download_binary(node, build / "node", "node")
            download_binary(glab, build / "glab", "glab")
            download_binary(lark, build / "lark-cli", "lark-cli")
        env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
        command("go", "test", "-tags", "e2e", "-c", "-o", str(build / "acceptance.test"), "./internal/agent/dsh", cwd=ROOT, env=env)
        if not args.reuse_image:
            command("docker", "build", "-t", args.image, str(build))
        image = subprocess.check_output(["docker", "image", "inspect", args.image, "--format", "{{.Id}}"], text=True).strip()
        harness = build / "harness"
        harness.mkdir()
        shutil.copyfile(build / "acceptance.test", harness / "acceptance.test")
        (harness / "acceptance.test").chmod(0o755)
        for script in ("launch.py", "probe.py", "outer-policy.json"):
            shutil.copyfile(ROOT / "scripts/space_isolation" / script, harness / script)
        (harness / "Dockerfile").write_text("FROM " + args.image + "\nCOPY acceptance.test launch.py probe.py outer-policy.json /opt/experiment/\n")
        image = subprocess.check_output(["docker", "build", "--quiet", str(harness)], text=True).strip()
        name = "teamx-isolation-" + uuid.uuid4().hex[:12]
        peer = name + "-peer"
        volume = name + "-data"
        report = {"image": image, "architecture": arch, "production_changes": False, "real_model": False,
                  "source_sha256": {str(path.relative_to(ROOT)): hashlib.sha256(path.read_bytes()).hexdigest()
                                    for path in [ROOT / "internal/agent/dsh/isolation_linux_real_test.go", *sorted(p for p in (ROOT / "scripts/space_isolation").iterdir() if p.is_file()), ROOT / "packaging/dsh/package-lock.json"]}}
        created = False
        peer_created = False
        volume_created = False
        try:
            command("docker", "volume", "create", volume, stdout=subprocess.DEVNULL)
            volume_created = True
            # A real reachable peer listener exists on the daemon bridge; the
            # candidate network namespace must be unable to reach that address.
            command("docker", "run", "-d", "--name", peer, "--cap-drop", "ALL", "--read-only",
                    "--security-opt", "no-new-privileges", "--user", "10002:10002", "--pids-limit", "32",
                    "--entrypoint", "python3", image, "-m", "http.server", "18081", "--bind", "0.0.0.0", "--directory", "/opt/experiment", stdout=subprocess.DEVNULL)
            peer_created = True
            peer_ip = subprocess.check_output(["docker", "inspect", "--format", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", peer], text=True).strip()
            command("docker", "exec", peer, "python3", "-c", "import urllib.request; assert urllib.request.urlopen('http://127.0.0.1:18081/').status == 200")
            report["peer_listener_verified"] = True
            # No host mounts, docker socket, published ports, host PID/network or
            # unconfined seccomp. Only trusted test supervisor keeps these caps.
            create_args = ["docker", "create", "--init", "--name", name, "--network", "none", "--read-only",
                    "--cap-drop", "ALL", "--cap-add", "SETUID", "--cap-add", "SETGID", "--cap-add", "CHOWN",
                    "--cap-add", "DAC_OVERRIDE", "--cap-add", "KILL",
                    "--security-opt", "no-new-privileges", "--pids-limit", "192", "--memory", "1g", "--cpus", "2",
                    "--mount", "type=volume,source=" + volume + ",target=/lab",
                    "--tmpfs", "/tmp:rw,nosuid,nodev,mode=1777,size=64m",
                    "-e", "TEAMX_ISOLATION_REAL=1", "-e", "ISOLATION_PEER_IP=" + peer_ip]
            command(*create_args, image, stdout=subprocess.DEVNULL)
            created = True
            with (args.output / "run.log").open("wb") as log:
                completed = subprocess.run(["docker", "start", "-a", name], stdout=log, stderr=subprocess.STDOUT, timeout=480)
            state = json.loads(subprocess.check_output(["docker", "inspect", name], text=True))[0]
            report.update(exit_code=state["State"]["ExitCode"], running=state["State"]["Running"],
                          security={k: state["HostConfig"][k] for k in ("NetworkMode", "ReadonlyRootfs", "CapDrop", "CapAdd", "SecurityOpt", "Binds", "PidMode", "Privileged", "Init")})
            command("docker", "cp", name + ":/lab/report.json", str(args.output / "report.json"))
            if completed.returncode or report["exit_code"]:
                raise RuntimeError("acceptance failed; inspect " + str(args.output / "run.log"))
            if state["State"]["Running"] or state["State"]["Pid"] != 0:
                raise RuntimeError("first worker did not settle")
            command("docker", "rm", name, stdout=subprocess.DEVNULL)
            created = False
            command(*create_args, "-e", "ISOLATION_RESTART_ONLY=1", image, stdout=subprocess.DEVNULL)
            created = True
            with (args.output / "restart.log").open("wb") as log:
                completed = subprocess.run(["docker", "start", "-a", name], stdout=log, stderr=subprocess.STDOUT, timeout=120)
            state = json.loads(subprocess.check_output(["docker", "inspect", "--format", "{{json .State}}", name], text=True))
            report["replacement_exit_code"] = state["ExitCode"]
            command("docker", "cp", name + ":/lab/container-restart.json", str(args.output / "container-restart.json"))
            if completed.returncode or state["ExitCode"]:
                raise RuntimeError("container replacement resume failed")
        finally:
            if created:
                report["cleanup"] = settle(name)
                report["cleanup_settled"] = report["cleanup"]["settled"]
            if peer_created:
                report["peer_cleanup"] = settle(peer)
                report["peer_cleanup_settled"] = report["peer_cleanup"]["settled"]
            if volume_created and report.get("cleanup_settled"):
                command("docker", "volume", "rm", volume, stdout=subprocess.DEVNULL)
                report["synthetic_volume_removed"] = True
            (args.output / "runner.json").write_text(json.dumps(report, indent=2) + "\n")
        if not report.get("cleanup_settled") or not report.get("peer_cleanup_settled"):
            raise RuntimeError("experiment cleanup is not settled; retain evidence and resources")


if __name__ == "__main__":
    main()
