#!/usr/bin/env python3
"""Extract bounded timing evidence from the pinned DSH v4 journals (Python 3.14+).

Reads only. Does not publish, resume members, or export prompts/credentials.
The output path must be new. Run after member turns have settled.
"""

import argparse
import itertools
import json
import re
from compression import zstd
from datetime import datetime, timezone
from pathlib import Path


def iso(ms):
    return datetime.fromtimestamp(ms / 1000, timezone.utc).isoformat()


def extract(room):
    config = json.loads((room / "room-config.json").read_text())
    modules = {"developer-a": "amount.py", "developer-b": "summary.py", "developer-c": "report.py"}
    members = []
    for member in config["members"]:
        agent = member["agent_id"]
        if agent not in modules:
            continue
        base = room / "agents" / member["membership_id"]
        candidates = []
        for journal in base.glob("session.json.dsh*/home/sessions/*/*/session.v4.jsonl.zstd"):
            with zstd.open(journal, "rt") as stream:
                events = [json.loads(line) for line in stream]
            header = events[0]
            if header.get("version") != 4 or Path(header["cwd"]).resolve() != (room / member["workspace"]).resolve():
                raise ValueError(f"journal version/workspace mismatch for {agent}")
            calls = {}
            for event in events:
                data = event.get("data", {})
                if event["type"] == "tool/call":
                    args = data.get("arguments", {})
                    if isinstance(args, str):
                        args = json.loads(args)
                    calls[data["callId"]] = {
                        "id": data["callId"], "turn": data["turn"], "name": data["name"],
                        "start_ms": event["time"], "args": args,
                    }
                elif event["type"] == "tool/result":
                    msg = data["message"]
                    call = calls.get(msg.get("toolCallId"))
                    if call:
                        call["end_ms"] = event["time"]
                        call["error"] = msg.get("isError", False)
                        call["output"] = "\n".join(block.get("text", "") for block in msg.get("content", []))
            writes = [c for c in calls.values() if c["name"] == "write"
                      and Path(c["args"].get("file_path", "")).name == modules[agent]
                      and "end_ms" in c and not c["error"]]
            for write in writes:
                checks = [c for c in calls.values() if c["name"] == "bash" and c["turn"] == write["turn"]
                          and c["start_ms"] > write["start_ms"] and "end_ms" in c
                          and "unittest" in c["args"].get("command", "")
                          and not c["error"] and "OK" in c["output"] and "FAILED" not in c["output"]
                          and re.search(r"Ran \d+ tests?", c["output"])]
                if not checks:
                    continue
                check = min(checks, key=lambda c: c["end_ms"])
                tools = [{"call_id": c["id"], "name": c["name"], "start_ms": c["start_ms"],
                          "end_ms": c["end_ms"], "tool_error": c["error"]}
                         for c in calls.values() if "end_ms" in c and c["turn"] == write["turn"]
                         and write["start_ms"] <= c["start_ms"] <= check["end_ms"]]
                turn_events = [e for e in events if e["type"] in ("turn/start", "turn/end")
                               and e.get("data", {}).get("turn") == write["turn"]]
                candidates.append({"agent_id": agent, "membership_id": member["membership_id"],
                                   "journal": str(journal.relative_to(room)), "dsh_turn": write["turn"],
                                   "first_source_write_ms": write["start_ms"], "tested_ms": check["end_ms"],
                                   "turn_events": [{"type": e["type"], "at": iso(e["time"]),
                                                    "reason": e["data"].get("reason")} for e in turn_events],
                                   "test_result": re.search(r"Ran \d+ tests?", check["output"])[0] + ", OK",
                                   "tools": tools})
        if not candidates:
            raise ValueError(f"no completed real source-write/test interval for {agent}")
        # First implementation of the assigned module; later integration is separate.
        members.append(min(candidates, key=lambda c: c["first_source_write_ms"]))
    if len(members) != 3:
        raise ValueError("three developer memberships required")
    overlaps = []
    for a, b in itertools.combinations(members, 2):
        start = max(a["first_source_write_ms"], b["first_source_write_ms"])
        end = min(a["tested_ms"], b["tested_ms"])
        if start < end:
            tools = []
            for x, y in itertools.product(a["tools"], b["tools"]):
                left = max(x["start_ms"], y["start_ms"], start)
                right = min(x["end_ms"], y["end_ms"], end)
                if left < right:
                    tools.append({"call_ids": [x["call_id"], y["call_id"]],
                                  "start": iso(left), "end": iso(right), "duration_ms": right - left})
            overlaps.append({"agents": [a["agent_id"], b["agent_id"]], "start": iso(start),
                             "end": iso(end), "duration_ms": end - start, "overlapping_tools": tools})
    if not overlaps:
        raise ValueError("no overlapping real source-write/test intervals")
    for member in members:
        member["first_source_write"] = iso(member["first_source_write_ms"])
        member["tested"] = iso(member["tested_ms"])
    start = max(m["first_source_write_ms"] for m in members)
    end = min(m["tested_ms"] for m in members)
    return {"status": "passed", "room_id": config["room_id"], "room_instance": config["room_instance"],
            "account_mode": "shared", "members": members, "pair_overlaps": overlaps,
            "three_developer_coding_overlap_ms": max(0, end - start),
            "boundary": "source write through first successful module test; DSH journal timestamps"}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--room-dir", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result = extract(args.room_dir.resolve())
    with args.output.open("x") as stream:
        json.dump(result, stream, ensure_ascii=False, indent=2)
        stream.write("\n")
    args.output.chmod(0o600)
    print(json.dumps({"status": result["status"], "pair_overlaps": result["pair_overlaps"],
                      "three_developer_coding_overlap_ms": result["three_developer_coding_overlap_ms"]}))
