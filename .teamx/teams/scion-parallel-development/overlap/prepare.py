#!/usr/bin/env python3
"""Prepare a self-contained base and Room prompt; --publish writes one new branch."""

import argparse
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import shutil
from string import Template
import subprocess
import sys
from urllib.parse import quote, urlsplit


HERE = Path(__file__).resolve().parent
FILES = ("overlap_todo.py", "test_overlap_todo.py", "OVERLAP-TODO.md")
AUTHOR = "TeamX overlap base <overlap-base@teamx.local>"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def object_id(value):
    return isinstance(value, str) and re.fullmatch(r"c1[0-9a-f]{64}", value)


def save_report(output, report):
    temp = output / "report.tmp"
    temp.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temp.chmod(0o600)
    temp.replace(output / "report.json")


def environment():
    # Keep the operator's ordinary native CLI login, not model/service credentials.
    keys = ("PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TMPDIR", "SYSTEMROOT",
            "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "SSL_CERT_FILE", "SSL_CERT_DIR",
            "HTTPS_PROXY", "HTTP_PROXY", "ALL_PROXY", "NO_PROXY", "https_proxy", "http_proxy",
            "all_proxy", "no_proxy", "SCION_CREDENTIALS", "SCION_CREDENTIALS_FILE", "SCION_CONFIG_FILE")
    env = {key: os.environ[key] for key in keys if key in os.environ}
    env.update(PYTHONDONTWRITEBYTECODE="1", SCION_NO_DAEMON="1")
    return env


def check_base(directory, output, env, name):
    result = subprocess.run(
        [sys.executable, "-m", "unittest", "-v", "test_overlap_todo"],
        cwd=directory, env=env, capture_output=True, text=True, timeout=60,
    )
    (output / name).write_text(result.stdout + result.stderr, encoding="utf-8")
    require(result.returncode == 0, f"base tests failed; see {output / name}")


class Scion:
    def __init__(self, args, env):
        executable = args.scn or shutil.which("scn") or str(Path.cwd() / "bin" / "scn")
        self.executable = str(Path(executable).resolve())
        self.env = env
        self.prefix = ["--endpoint", args.endpoint, "--hub", args.endpoint, "--repo", args.repo, "--json"]
        version = self.command(["--version"], Path.cwd()).strip()
        require(version == "scn 0.1.13", "scn 0.1.13 is required; pass --scn /absolute/path/scn")

    def command(self, arguments, cwd):
        result = subprocess.run([self.executable, *arguments], cwd=cwd, env=self.env,
                                capture_output=True, text=True, timeout=120)
        require(result.returncode == 0,
                f"scn failed ({result.returncode}): {result.stderr.strip()[:2000]}")
        return result.stdout

    def run(self, *arguments, cwd):
        return json.loads(self.command([*self.prefix, *arguments], cwd))


def branch_tips(scn, directory):
    page = scn.run("branch", "ls", cwd=directory)
    require(not page.get("truncated", False), "incomplete branch inventory")
    return {branch["name"]: branch["changeset"] for branch in page["branches"]}


def write_prompt(output, report):
    head = report.get("head", "<发布后完整 head，当前仅本地准备>")
    code_url = f'{report["endpoint"]}/{report["repo"]}/tree/{head}'
    values = {**report, "head": head, "code_url": code_url}
    prompt = Template((HERE / "prompt.txt").read_text(encoding="utf-8")).substitute(values)
    (output / "prompt.txt").write_text(prompt, encoding="utf-8")


def prepare(args):
    parsed = urlsplit(args.endpoint)
    require(parsed.scheme == "https" and parsed.netloc and parsed.hostname and not parsed.username
            and not parsed.password and parsed.path in ("", "/") and not parsed.query and not parsed.fragment,
            "endpoint must be an HTTPS origin without credentials or a path")
    args.endpoint = args.endpoint.rstrip("/")
    parts = args.repo.split("/")
    require(len(parts) >= 2 and all(re.fullmatch(r"[A-Za-z0-9_.-]+", part)
                                  and part not in (".", "..") for part in parts), "canonical repo required")
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}", args.run_id), "invalid run ID")
    require(args.subject and not any(c in args.subject for c in "\r\n"), "expected subject required")
    output = Path(args.output or Path.cwd() / ".local" / "scion-overlap" / args.run_id).resolve()
    output.mkdir(parents=True, exist_ok=False, mode=0o700)
    report = {"status": "preparing", "run_id": args.run_id, "endpoint": args.endpoint, "repo": args.repo,
              "branch": f"teamx/overlap-base/{args.run_id}", "subject": args.subject,
              "remote_writes_may_have_started": False}
    save_report(output, report)
    try:
        env = environment()
        if args.credentials_file:
            path = Path(args.credentials_file).expanduser().resolve()
            require(path.is_file(), "explicit logged-in credentials file not found")
            env.update(SCION_CREDENTIALS="file", SCION_CREDENTIALS_FILE=str(path))
        base = output / "base"
        base.mkdir()
        for name in FILES:
            shutil.copyfile(HERE / "base" / name, base / name)
        check_base(base, output, env, "base-tests.txt")
        report["file_sha256"] = {name: hashlib.sha256((base / name).read_bytes()).hexdigest() for name in FILES}
        if not args.publish:
            report["status"] = "local_only"
            write_prompt(output, report)
            return output, report

        scn = Scion(args, env)
        report.update(phase="verifying_identity", scn_executable=scn.executable)
        save_report(output, report)
        identity = scn.run("whoami", args.endpoint, cwd=output)
        report["whoami"] = identity
        save_report(output, report)
        actual_subject, actual_endpoint = identity.get("subject"), identity.get("endpoint")
        require(actual_endpoint == args.endpoint,
                f"native scn instance mismatch: expected {args.endpoint!r}, got {actual_endpoint!r}")
        require(isinstance(actual_subject, str) and actual_subject.strip()
                and actual_subject.lower() != "anonymous",
                f"native scn is not logged in at {args.endpoint} (subject={actual_subject!r}). "
                "TeamX's managed login is separate; reuse an already-logged-in file with "
                "--credentials-file /absolute/path/credentials.json, or run native scn login.")
        require(actual_subject == args.subject,
                f"native scn account mismatch: expected {args.subject!r}, got {actual_subject!r}. "
                "Use the correct login; change --subject only when intentionally using that account "
                "and the matching TeamX platform.")
        repo = scn.run("repo", "info", args.repo, cwd=output)
        require(repo["name"] == args.repo and not repo.get("archived"), "canonical repo unavailable")
        initial = branch_tips(scn, output)
        source = initial.get("main")
        require(object_id(source), "main must have a full Scion head")
        require(report["branch"] not in initial, "base branch already exists; inspect instead of retrying")
        report["source_head"] = source
        workspace = output / "workspace"
        report["phase"] = "cloning"
        save_report(output, report)
        scn.run("clone", args.repo, str(workspace), "--ref", "main", cwd=output)
        native = json.loads((workspace / ".scion" / "state.json").read_text())
        status = scn.run("status", cwd=workspace)
        require(native["repo"] == args.repo and native["endpoint"] == args.endpoint
                and native["base"] == source and status["base"] == source and status["ref"] == "main"
                and status.get("pending") is None
                and not any(status.get(key) for key in ("added", "modified", "deleted", "conflicted")),
                "clone identity/baseline changed; no branch has been created")
        require(not any((workspace / name).exists() for name in FILES),
                "main already contains experiment files; refusing to overwrite")
        report.update(phase="creating_branch", remote_writes_may_have_started=True)
        save_report(output, report)
        scn.run("switch", "-c", report["branch"], cwd=workspace)
        for name in FILES:
            shutil.copyfile(base / name, workspace / name)
        check_base(workspace, output, env, "workspace-tests.txt")
        report["phase"] = "committing"
        save_report(output, report)
        report["commit"] = scn.run("commit", "-m", f"test: prepare overlap base {args.run_id}",
                                    "--author", AUTHOR, *FILES, cwd=workspace)
        report["phase"] = "publishing"
        save_report(output, report)
        report["land"] = scn.run("land", cwd=workspace)
        report["phase"] = "verifying_remote"
        save_report(output, report)
        tips = branch_tips(scn, workspace)
        head = tips.get(report["branch"])
        require(object_id(head) and head != source, "published head missing")
        report["head"] = head
        require(tips.get("main") == source, "main changed during preparation; inspect the recorded versions")
        shown = scn.run("show", head, cwd=workspace)
        commit = shown["commit"]
        require(commit["id"] == head and commit["author"] == "TeamX overlap base"
                and commit["email"] == "overlap-base@teamx.local" and commit["parents"] == [source],
                "unexpected published commit identity or baseline")
        history = scn.run("reflog", report["branch"], cwd=workspace)
        require(any(entry["changeset"] == head and entry["actor"] == args.subject
                    and entry["move"] == "fast-forward" for entry in history["entries"]),
                "no matching remote publication receipt")
        report["reflog"] = history
        published = output / "published-base"
        published.mkdir()
        for name in FILES:
            file = scn.run("cat", f"{head}:{name}", cwd=workspace)
            require(file["revision"] == head and file["path"] == name and not file["binary"]
                    and file["content"].encode("utf-8") == (base / name).read_bytes(),
                    f"remote content mismatch: {name}")
            (published / name).write_text(file["content"], encoding="utf-8")
        check_base(published, output, env, "published-tests.txt")
        report.update(status="published", phase="verified", code_url=f"{args.endpoint}/{args.repo}/tree/{head}",
                      branch_url=f'{args.endpoint}/{args.repo}/tree/{quote(report["branch"], safe="")}',
                      commit_url=f"{args.endpoint}/{args.repo}/commit/{head}")
        write_prompt(output, report)
        return output, report
    except Exception as error:
        report.update(status="failed", error=str(error))
        print(f"Preparation stopped: {error}\nEvidence: {output / 'report.json'}", file=sys.stderr)
        if report["remote_writes_may_have_started"]:
            print("Remote state may exist. Inspect this report/branch before another run; no automatic retry or cleanup.", file=sys.stderr)
        raise
    finally:
        save_report(output, report)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--publish", action="store_true", help="create and publish ONE new base branch")
    parser.add_argument("--scn", help="native scn 0.1.13 executable path")
    parser.add_argument("--endpoint", default="https://scion.opencsg.com")
    parser.add_argument("--repo", default="OpenCSG/teamx-demo")
    parser.add_argument("--subject", default="csgship", help="expected native login subject")
    parser.add_argument("--credentials-file", help="explicit already-logged-in native CLI credentials file")
    parser.add_argument("--run-id", default="overlap-" + datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ") + "-" + secrets.token_hex(3))
    parser.add_argument("--output", help="new output directory; existing directories are refused")
    args = parser.parse_args()
    try:
        output, report = prepare(args)
    except (ValueError, OSError, subprocess.SubprocessError, KeyError) as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    print(f'Status: {report["status"]}\nOutput: {output}\nBase branch: {report["branch"]}')
    if report["status"] == "published":
        print(f'Head: {report["head"]}\nCode: {report["code_url"]}\nRoom prompt: {output / "prompt.txt"}')
    else:
        print("Local base tests passed. No Scion command or remote write was made.\nUse --publish to prepare a real base and resolved Room prompt.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
