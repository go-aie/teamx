#!/usr/bin/env python3
"""Capture source-only architecture evidence; never inspect a running home.

Run from any directory: python3 scripts/architecture-baseline.py OUTPUT.json
References are lexical search hits, not a type-resolved Go call graph. Preserve
the original evidence and use a new output file for each subsequent comparison.
"""

import datetime
import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[1]
MODULE = "github.com/go-aie/teamx/"
# Concrete contract/type names give each work line a reproducible caller search.
CONTRACTS = {
    "persistence": "snapshotWriter admitSnapshotWrite save removeSnapshot check readSnapshot markUncertain closeInputOwners lockDirectory lockDirectoryFile lockBundleHome unlockDirectory healthMu uncertain writerClosing writes writesDone",
    "history": "historyQueries historyCall prepareHistory newHistoryMCPHTTP inputMCPFor historyToolDefinitions historyReadMaterials clearHistory discardHistoryResults",
    "platform_entries": "resolveToolBindingLocked checkToolEntryLocked withToolEntry constrainToolRequest sourcePlatformLocked entrySkill",
    "platform_assembly": "Catalog TypeDefinition NewCatalog Builtins SourceBindings defaultPlatformCatalog connectionProvider initPlatform defaultNotificationAdapters toolDefinitions NotificationCapabilities notificationAdapters inputStorageCodecs ReferenceCapabilities referenceCapabilities",
    "input_owners": "MemberInputActor InboxActor sharedInputStorage openPlatformInputStorage closeInputOwners inputOwnerValue",
    "input_execution": "inputSession newInputSession receiveGroupBodies finishMCPTurn",
    "scheduler": "TurnScheduler NewTurnScheduler RequestTurn TurnGranted CancelTurnRequest ReleaseTurn StopTurnGrants InspectTurnScheduler TurnSchedulerState bindTurnScheduler",
    "members": "MemberAgent Backend SessionOptions dshBackend dshSession ownedSession rotationLease",
    "platform_calls": "Calls WaitCalls platformCalls platformCall preparePlatformCallLocked finishPlatformCallLocked finishPlatformCall cancelPlatformCallsLocked waitPlatformCalls",
    "platform_configuration": "roomManager oauthFlights oauthLeases gitlabAuthSessions larkAuthSessions scionAuthSessions",
    "followups": "followupEvent followupUnit queryActions executeAction naturalPolicy",
    "models": "modelService modelController modelCredentials",
    "workspaces": "workspaceRecord workspaceBinding prepareWorkspace",
    "calendars": "CalendarService CalendarActor calendarDelivery",
    "access": "serverAccess executionBinding issueInputBinding issueMCPExecution",
    "conversation": "Room RoomSnapshot RoomMessage humanReadStore roomVisible",
    "assembly_transport": "Run runServerOnce serveRoomManager newService newRoomManager shutdownRuntime mcpHTTP runCLIWithInput",
}


def run(*args):
    return subprocess.check_output(args, cwd=ROOT, text=True)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def capture():
    names = run("git", "ls-files", "-c", "-o", "--exclude-standard", "--", "cmd", "internal", "tools", "scripts").splitlines()
    contract_paths = ["Makefile", "go.mod", "go.sum", ".github/workflows/package.yml", ".github/workflows/release.yml", "docs/ARCHITECTURE-MODULE-BOUNDARIES-ANALYSIS.zh.md", "docs/AGENT-HISTORY-PLAN.zh.md"]
    contract_paths += [str(path.relative_to(ROOT)) for path in sorted((ROOT / "docs/implementation").glob("agent-history-*")) if path.is_file()]
    documents = [{"path": name, "sha256": digest((ROOT / name).read_bytes())} for name in contract_paths]
    files = []
    references = {key: [] for key in CONTRACTS}
    patterns = {key: re.compile(r"\b(?:" + "|".join(value.split()) + r")\b") for key, value in CONTRACTS.items()}
    tracked = set(run("git", "ls-files").splitlines())
    for name in sorted(set(names)):
        path = ROOT / name
        if not path.is_file() or path.suffix not in {".go", ".mjs", ".js", ".py"}:
            continue
        data = path.read_bytes()
        record = {"path": name, "sha256": digest(data), "tracked": name in tracked}
        if path.suffix == ".go":
            source = data.decode()
            record["build_tags"] = re.findall(r"(?m)^//go:build (.+)$", source)
            record["tests"] = re.findall(r"(?m)^func (Test\w+)\(", source)
            if name.startswith(("internal/room/", "internal/app/agents/", "internal/app/platforms/", "internal/platform/registry/")) and not name.endswith("_test.go"):
                for line_no, line in enumerate(source.splitlines(), 1):
                    if line.lstrip().startswith("//"):
                        continue
                    for key, pattern in patterns.items():
                        if pattern.search(line):
                            references[key].append({"path": name, "line": line_no, "text": line.strip()})
        files.append(record)
    decoder = json.JSONDecoder()
    raw = run("go", "list", "-deps", "-json", "./cmd/...", "./internal/...", "./tools/...")
    packages = []
    while raw.strip():
        package, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        if package["ImportPath"].startswith(MODULE):
            packages.append({key: package.get(key, []) for key in ("ImportPath", "Imports", "GoFiles", "IgnoredGoFiles", "TestGoFiles", "XTestGoFiles")})
    diff = run("git", "diff", "HEAD", "--", "cmd", "internal", "tools", "scripts", "docs")
    return {
        "captured_at": datetime.datetime.now(datetime.timezone.utc).isoformat(),
        "head": run("git", "rev-parse", "HEAD").strip(),
        "head_subject": run("git", "log", "-1", "--format=%s").strip(),
        "capture_phase": "Source snapshot with architecture guardrails; applied stages and worktree differences are described in their implementation records",
        "production_go_diff": [name for name in run("git", "diff", "HEAD", "--name-only", "--", "cmd", "internal", "tools").splitlines() if name.endswith(".go") and not name.endswith("_test.go")],
        "go": run("go", "version").strip(),
        "goenv": json.loads(run("go", "env", "-json", "GOOS", "GOARCH", "GOFLAGS", "GOWORK", "CGO_ENABLED")),
        "status": run("git", "status", "--short").splitlines(),
        "tracked_diff_sha256": digest(diff.encode()),
        "tracked_diff_stat": run("git", "diff", "HEAD", "--stat", "--", "cmd", "internal", "tools", "scripts", "docs").splitlines(),
        "inventory_scope": "Nonignored Go/JS/Python sources under cmd, internal, tools, scripts; no runtime/config/credential contents. Includes untracked source tests. References are lexical, including declarations and generated bindings; verify receivers before moving a caller.",
        "contract_documents": documents,
        "files": files,
        "production_packages": packages,
        "contract_searches": {key: value.split() for key, value in CONTRACTS.items()},
        "references": references,
    }


if __name__ == "__main__":
    if len(sys.argv) != 2:
        raise SystemExit("usage: architecture-baseline.py OUTPUT.json (new file)")
    evidence = capture()
    # Keep each source/reference record on one line for reviewable inventories.
    with open(sys.argv[1], "x") as output:
        output.write("{\n")
        for index, (key, value) in enumerate(evidence.items()):
            if index:
                output.write(",\n")
            output.write("  " + json.dumps(key) + ": ")
            if isinstance(value, list) and value and isinstance(value[0], dict):
                output.write("[\n    " + ",\n    ".join(json.dumps(item, ensure_ascii=False) for item in value) + "\n  ]")
            elif key == "references":
                output.write("{\n" + ",\n".join("    " + json.dumps(group) + ": [\n      " + ",\n      ".join(json.dumps(hit, ensure_ascii=False) for hit in hits) + "\n    ]" for group, hits in value.items()) + "\n  }")
            else:
                output.write(json.dumps(value, ensure_ascii=False))
        output.write("\n}")
        output.write("\n")
