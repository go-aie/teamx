#!/usr/bin/env python3
"""Collect selected commits and emit changelog-friendly output."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path


CONVENTIONAL_RE = re.compile(
    r"^(?P<type>[A-Za-z0-9_-]+)(?:\((?P<scope>[^)]+)\))?(?P<breaking>!)?: (?P<summary>.+)$"
)


def run_git(repo: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", *args],
        cwd=repo,
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout.strip()


def expand_revision(repo: Path, revision: str) -> list[str]:
    if ".." in revision or "^!" in revision or "..." in revision:
        output = run_git(repo, "rev-list", "--reverse", revision)
    else:
        output = run_git(repo, "rev-list", "--max-count=1", revision)
    commits = [line.strip() for line in output.splitlines() if line.strip()]
    if not commits:
        raise ValueError(f"revision produced no commits: {revision}")
    return commits


def get_commit(repo: Path, sha: str) -> dict[str, object]:
    fmt = "%H%x00%h%x00%s%x00%b"
    output = run_git(repo, "show", "-s", f"--format={fmt}", sha)
    full_sha, short_sha, subject, body = output.split("\x00", 3)
    match = CONVENTIONAL_RE.match(subject)
    info = {
        "sha": full_sha,
        "short_sha": short_sha,
        "subject": subject,
        "body": body.strip(),
        "type": None,
        "scope": None,
        "summary": subject,
        "breaking": False,
    }
    if match:
        info["type"] = match.group("type")
        info["scope"] = match.group("scope")
        info["summary"] = match.group("summary")
        info["breaking"] = bool(match.group("breaking"))
    return info


def collect(repo: Path, revisions: list[str]) -> list[dict[str, object]]:
    ordered_shas: list[str] = []
    seen: set[str] = set()
    for revision in revisions:
        for sha in expand_revision(repo, revision):
            if sha not in seen:
                seen.add(sha)
                ordered_shas.append(sha)
    return [get_commit(repo, sha) for sha in ordered_shas]


def to_markdown(commits: list[dict[str, object]]) -> str:
    lines = []
    for commit in commits:
        lines.append(f"- {commit['subject']} ({commit['short_sha']})")
    return "\n".join(lines)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Collect selected git commits and emit changelog-ready output."
    )
    parser.add_argument(
        "revisions",
        nargs="+",
        help="Commit SHAs or revision ranges such as base..head",
    )
    parser.add_argument(
        "--repo",
        default=".",
        help="Path to the git repository. Defaults to the current directory.",
    )
    parser.add_argument(
        "--format",
        choices=("markdown", "json"),
        default="markdown",
        help="Output format. Defaults to markdown.",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    repo = Path(args.repo).resolve()
    try:
        commits = collect(repo, args.revisions)
    except subprocess.CalledProcessError as exc:
        sys.stderr.write(exc.stderr)
        return exc.returncode or 1
    except ValueError as exc:
        sys.stderr.write(f"{exc}\n")
        return 1

    if args.format == "json":
        json.dump(commits, sys.stdout, indent=2)
        sys.stdout.write("\n")
    else:
        sys.stdout.write(to_markdown(commits))
        sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
