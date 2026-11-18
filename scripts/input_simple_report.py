#!/usr/bin/env python3
"""Summarize immutable list/read model trials without replacing raw judgments."""
import argparse
import collections
import hashlib
import json
import math
from pathlib import Path

EXPECTED = {"direct": 10, "paged": 10, "return": 10, "accounts": 5,
            "platform": 5, "retry": 5, "recovery": 5, "exposure": 5}
ARMS = ("simple", "four", "fifo")


def distribution(values):
    values = sorted(value for value in values if value is not None)
    if not values:
        return {"n": 0}
    return {"n": len(values), "mean": sum(values) / len(values),
            "p50": values[math.ceil(len(values) * .5) - 1],
            "p90": values[math.ceil(len(values) * .9) - 1], "max": values[-1]}


def token_usage(rows):
    usage = rows.get("provider_usage", [])
    if not usage:
        return None
    incoming = outgoing = 0
    for item in usage:
        incoming += item.get("prompt_tokens", item.get("input_tokens", 0))
        outgoing += item.get("completion_tokens", item.get("output_tokens", 0))
    return {"input": incoming, "output": outgoing, "total": incoming + outgoing}


def summarize(root):
    grouped = collections.defaultdict(list)
    raw = []
    review_path = root / "review.json"
    reviews = json.loads(review_path.read_text())["trials"] if review_path.exists() else {}
    for path in sorted(root.glob("*/evidence.json")):
        data = path.read_bytes()
        row = json.loads(data)
        verdict = reviews.get(str(path.relative_to(root)))
        if verdict and verdict["sha256"] != hashlib.sha256(data).hexdigest():
            raise ValueError(f"review does not match immutable evidence: {path}")
        calls = row.get("calls", [])
        invalid = failed = list_calls = read_calls = results = repeated = 0
        prior_arguments = set()
        first_read = first_response = None
        direct_prelookup = False
        target_seen = False
        for call in calls:
            signature = (call["tool"], json.dumps(call.get("arguments"), sort_keys=True))
            repeated += signature in prior_arguments
            prior_arguments.add(signature)
            list_calls += call["tool"] == "inbox_list"
            if row["group"] == "direct" and not target_seen and call["tool"] in ("inbox_list", "inbox_stats", "inbox_preview"):
                direct_prelookup = True
            if call["tool"] == "inbox_read" and "MARK_A1" in call.get("result", ""):
                target_seen = True
            read_calls += call["tool"] == "inbox_read"
            results += len(call.get("result", "").encode())
            error = bool(call.get("error"))
            try:
                result = json.loads(call.get("result", "{}"))
            except json.JSONDecodeError:
                result = {}
            error |= bool(result.get("error")) or bool(result.get("result", {}).get("isError"))
            if error:
                failed += 1
                if "Temporary read failure" not in json.dumps(call):
                    invalid += 1
            if first_read is None and call["tool"] == "inbox_read" and not error:
                first_read = call.get("at_seconds")
                if call.get("response_flushed_at_seconds") and row.get("prompt_started_at_seconds"):
                    first_response = call["response_flushed_at_seconds"] - row["prompt_started_at_seconds"][0]
        usage = token_usage(row)
        sample = {"file": str(path), "sha256": hashlib.sha256(data).hexdigest(),
                  "group": row["group"], "arm": row["arm"], "repeat": row["repeat"],
                  "raw_status": row["status"], "raw_issues": row.get("issues", []),
                  "status": verdict["status"] if verdict else row["status"],
                  "issues": verdict["issues"] if verdict else row.get("issues", []),
                  "error": row.get("error"), "calls": len(calls), "list_calls": list_calls,
                  "read_calls": read_calls, "failed_calls": failed, "invalid_calls": invalid,
                  "repeated_arguments_calls": repeated,
                  "schema_bytes": row.get("schema_bytes"), "prompt_bytes": sum(len(x.encode()) for x in row.get("prompts", [])),
                  "instruction_bytes": len(row.get("instructions", "").encode()),
                  "result_bytes": results, "model_request_bytes": row.get("model_request_bytes"),
                  "model_response_bytes": row.get("model_response_bytes"), "usage": usage,
                  "elapsed_seconds": row.get("elapsed_seconds"),
                  "first_read_request_seconds": first_read, "first_body_response_seconds": first_response,
                  "shared_with_fifo": row.get("shared_with_fifo", False), "known_target_prelookup": direct_prelookup}
        raw.append(sample)
        grouped[(row["group"], row["arm"])].append(sample)
    return aggregate(str(root), raw, str(review_path) if review_path.exists() else None)


def aggregate(root, raw, review=None):
    grouped = collections.defaultdict(list)
    for row in raw:
        grouped[(row["group"], row["arm"])].append(row)
    tables = {}
    for (group, arm), rows in sorted(grouped.items()):
        counts = collections.Counter(row["status"] for row in rows)
        summary = {"n": len(rows), "status": dict(counts)}
        for field in ("calls", "list_calls", "read_calls", "failed_calls", "invalid_calls", "repeated_arguments_calls", "schema_bytes", "instruction_bytes",
                      "prompt_bytes", "result_bytes", "model_request_bytes", "model_response_bytes", "elapsed_seconds", "first_read_request_seconds", "first_body_response_seconds"):
            summary[field] = distribution([row[field] for row in rows])
        for field in ("input", "output", "total"):
            summary[field + "_tokens"] = distribution([row["usage"][field] if row["usage"] else None for row in rows])
        tables[group + "/" + arm] = summary
    complete = all(len(grouped[(group, arm)]) == n for group, n in EXPECTED.items() for arm in ARMS)
    correct = all(row["status"] == "passed" for row in raw if row["arm"] == "simple")
    success_not_lower = all(
        sum(row["status"] == "passed" for row in grouped[(group, "simple")]) >=
        sum(row["status"] == "passed" for row in grouped[(group, "four")]) for group in EXPECTED)
    costs = {}
    for group in ("direct", "return"):
        if not all(len(grouped[(group, arm)]) >= 10 for arm in ("simple", "four")):
            costs[group] = {"enough_samples": False}
            continue
        costs[group] = {"enough_samples": True}
        for field in ("calls", "invalid_calls"):
            costs[group][field + "_p90_not_higher"] = tables[group + "/simple"][field]["p90"] <= tables[group + "/four"][field]["p90"]
    known_direct = all(not row["known_target_prelookup"] for row in grouped[("direct", "simple")])
    totals = {}
    for arm in ARMS:
        rows = [row for row in raw if row["arm"] == arm]
        totals[arm] = {"n": len(rows), "calls": sum(row["calls"] for row in rows),
                       "list_calls": sum(row["list_calls"] for row in rows),
                       "read_calls": sum(row["read_calls"] for row in rows),
                       "invalid_calls": sum(row["invalid_calls"] for row in rows),
                       "result_bytes": sum(row["result_bytes"] for row in rows),
                       "usage_n": sum(row["usage"] is not None for row in rows),
                       "tokens": {field: sum(row["usage"][field] for row in rows if row["usage"])
                                  for field in ("input", "output", "total")}}
    return {"root": str(root), "expected_trials": 165, "actual_trials": len(raw),
            "review": review,
            "raw_status_counts": dict(collections.Counter(row["raw_status"] for row in raw)),
            "gates": {"complete": complete, "candidate_all_correct": complete and correct,
                      "per_scenario_success_not_lower": complete and success_not_lower,
                      "known_direct_without_fixed_prelookup": bool(grouped[("direct", "simple")]) and known_direct,
                      "costs": costs}, "groups": tables, "arm_totals": totals,
            "failures": [row for row in raw if row["status"] == "failed"],
            "capability_limits": [row for row in raw if row["status"] == "capability_limit"],
            "limitations": ["FIFO selective and cross-turn selection tasks are not equivalent capabilities.",
                            "Provider usage is end-to-end; phase usage is not separately attributable.",
                            "Repeated identical read arguments can be valid next batches; this count is not automatically an invalid call.",
                            "First-body timing uses response completion minus prompt start where captured; older trials only have request arrival.",
                            "Platform fixtures and isolated Rooms do not establish real platform/deployment readiness."],
            "trials": raw}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("roots", type=Path, nargs="+")
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--cohort-plan", type=Path)
    args = parser.parse_args()
    reports = [summarize(root) for root in args.roots]
    result = {"batches": reports}
    if args.cohort_plan:
        plan = json.loads(args.cohort_plan.read_text())
        by_name = {Path(report["root"]).name: report for report in reports}
        configurations = []
        for name in sorted(set(plan["groups"].values())):
            frozen = json.loads((Path(by_name[name]["root"]) / "frozen.json").read_text())
            configurations.append({key: frozen[key] for key in
                                   ("model", "common_instructions", "limit", "dsh_binary", "dsh_args", "dsh_artifact")})
        if any(config != configurations[0] for config in configurations[1:]):
            raise ValueError("continued cohort changed the frozen model, budget, instructions or runtime")
        trials = [row for group, name in plan["groups"].items()
                  for row in by_name[name]["trials"] if row["group"] == group]
        result["cohort"] = aggregate(str(args.cohort_plan), trials, "Per-source immutable review, where present")
        result["cohort"]["whole_group_selection"] = plan
        result["cohort"]["frozen_configuration_equal"] = True
    args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps([{ "root": report["root"], "actual_trials": report["actual_trials"],
                      "gates": report["gates"], "failed_trials": len(report["failures"]) } for report in reports], ensure_ascii=False))


if __name__ == "__main__":
    main()
