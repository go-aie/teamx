#!/usr/bin/env python3
"""Summarize immutable stage-4 paired DSH evidence, including failed trials.

Usage: input_native_report.py EVIDENCE_ROOT OUTPUT [--recheck]
No network, credentials, provider calls or evidence rewriting.
"""
import hashlib
import json
import math
import statistics
import sys
from pathlib import Path

COUNTS = {"direct": 10, "deep-blocked": 10, "preview-return": 10,
          "ambiguous": 5, "zero-query": 5, "stats-only": 5,
          "preview-skip": 5, "recovery": 5, "ext-stats": 3,
          "ext-scope": 3, "ext-explore": 3, "ext-domain": 3}
METRICS = ("tool_calls", "invalid_calls", "schema_bytes", "result_bytes",
           "instructions_bytes", "initial_prompt_bytes", "model_request_bytes",
           "model_response_bytes", "prompt_tokens", "completion_tokens",
           "elapsed_seconds", "target_first_page_wait_seconds",
           "guidance_calls", "first_guidance_call", "reuse_tail_calls",
           "reuse_tail_invalid_calls", "guidance_result_bytes", "first_project_scope_calls",
           "first_project_scope_result_bytes", "first_project_scope_elapsed_seconds",
           "reuse_tail_result_bytes", "reuse_tail_elapsed_seconds")
EXPECTED_ERRORS = ("input_scope_ambiguous",
                   "input_catalog_expired", "input_snapshot_expired",
                   "input_stream_empty", "input_stream_blocked")


def distribution(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return None
    return {"n": len(values), "median": statistics.median(values),
            "p90": values[math.ceil(.9 * len(values)) - 1]}


def result_text(call):
    result = call.get("result") or {}
    return "\n".join(c.get("text", "") for c in result.get("content", []))


def analyze(path, root):
    report = json.loads(path.read_text())
    row = {k: report.get(k) for k in ("scenario", "mode", "repeat", "status",
           "issues", "error", "pilot", "cost_recheck", "model", "schema_sha256", *METRICS)}
    row.update(evidence=str(path.relative_to(root)), evidence_sha256=hashlib.sha256(path.read_bytes()).hexdigest(),
               instructions_bytes=len(report["instructions"].encode()),
               instructions_sha256=hashlib.sha256(report["instructions"].encode()).hexdigest(),
               initial_prompt_bytes=len(report["prompt"].encode()),
               raw_invalid_calls=report.get("invalid_calls", 0))
    for key, alternate in (("prompt_tokens", "input_tokens"), ("completion_tokens", "output_tokens")):
        values = [u.get(key, u.get(alternate)) for u in report.get("provider_usage", [])]
        row[key] = sum(values) if values and None not in values else None
    guidance = []
    invalid = []
    seen = {}
    first_native = None
    calls = report.get("calls", [])
    for i, call in enumerate(calls):
        args = call.get("arguments", {})
        if isinstance(args, str):
            args = json.loads(args)
        text = result_text(call)
        try:
            payload = json.loads(text)
        except (ValueError, TypeError):
            payload = {}
        if payload.get("guidance") or "Native filters: node and status" in text:
            guidance.append(i)
        if args.get("filter") and first_native is None:
            first_native = i
        err = call.get("error", "")
        reasons = []
        if err and not any(e in err for e in EXPECTED_ERRORS):
            reasons.append("parameter_or_budget_rejection")
        key = (call["name"], json.dumps(args, sort_keys=True))
        if key in seen and call["name"] != "inbox_read" and seen[key] == call.get("before"):
            reasons.append("repeated_discovery_without_information_change")
        seen[key] = call.get("before")
        if call["name"] == "inbox_read" and payload.get("stream", {}).get("label", "").startswith("历史更新 "):
            reasons.append("unrelated_formal_read")
        if reasons:
            invalid.append({"call_index": i, "reasons": reasons})
    row["invalid_calls"] = len(invalid)
    row["invalid_call_details"] = invalid
    row["guidance_calls"] = len(guidance)
    row["first_guidance_call"] = guidance[0] + 1 if guidance else None
    # This is an observed tail, not a claim that the provider reports token
    # usage separately per guidance/reuse phase. Full-run cost remains above.
    if report["scenario"] == "ext-scope":
        boundary = None
        for i, call in enumerate(calls):
            args = call.get("arguments", {})
            if isinstance(args, str): args = json.loads(args)
            filters = args.get("filter", args)
            if not call.get("error") and filters.get("project") == "acme/release-service" and not filters.get("iid"):
                boundary = i
                break
        row["reuse_tail_calls"] = len(calls) - boundary - 1 if boundary is not None else None
        row["reuse_tail_invalid_calls"] = sum(v["call_index"] > boundary for v in invalid) if boundary is not None else None
        row["first_project_scope_calls"] = boundary + 1 if boundary is not None else None
        row["first_project_scope_result_bytes"] = sum(len(result_text(c).encode()) for c in calls[:boundary+1]) if boundary is not None else None
        row["reuse_tail_result_bytes"] = sum(len(result_text(c).encode()) for c in calls[boundary+1:]) if boundary is not None else None
        row["first_project_scope_elapsed_seconds"] = calls[boundary].get("at_seconds") if boundary is not None else None
        row["reuse_tail_elapsed_seconds"] = report["elapsed_seconds"] - calls[boundary]["at_seconds"] if boundary is not None else None
    row["guidance_result_bytes"] = sum(len(result_text(calls[i]).encode()) for i in guidance)
    row["first_native_filter_call"] = first_native + 1 if first_native is not None else None
    return row


def summarize(root, recheck=False, review=None):
    counts = {k: v for k, v in COUNTS.items() if k in ("ambiguous", "recovery", "ext-explore", "ext-scope")} if recheck else COUNTS
    rows = [analyze(p, root) for p in sorted(root.glob("*/evidence.json"))]
    if review:
        for row in rows:
            revised = review["runs"].get(row["evidence"])
            if not revised or revised["evidence_sha256"] != row["evidence_sha256"]:
                raise ValueError("Review missing or mismatched: " + row["evidence"])
            row["original_status"], row["original_issues"] = row["status"], row["issues"]
            row["status"], row["issues"] = revised["status"], revised["issues"]
    groups, comparisons = [], []
    gates = {"expected_trials_complete": len(rows) == sum(counts.values()) * 2,
             "candidate_all_correct": bool(rows), "per_scenario_success_not_lower": True,
             "known_direct_call_p90_not_higher": True, "reuse_call_p90_not_higher": True,
             "reuse_invalid_p90_not_higher": True}
    for scenario, expected in counts.items():
        pair = []
        for mode in ("four", "native"):
            runs = [r for r in rows if r["scenario"] == scenario and r["mode"] == mode]
            group = {"scenario": scenario, "mode": mode, "expected": expected, "n": len(runs),
                     "passed": sum(r["status"] == "passed" for r in runs),
                     "distributions": {metric: distribution([r.get(metric) for r in runs]) for metric in METRICS}}
            gates["expected_trials_complete"] &= len(runs) == expected
            if mode == "native":
                gates["candidate_all_correct"] &= len(runs) == expected and all(r["status"] == "passed" for r in runs)
            pair.append(group)
            groups.append(group)
        control, candidate = pair
        gates["per_scenario_success_not_lower"] &= candidate["passed"] >= control["passed"]
        comparison = {"scenario": scenario}
        for metric in METRICS:
            a, b = [g["distributions"][metric] for g in pair]
            comparison[metric + "_median_ratio"] = b["median"] / a["median"] if a and b and a["median"] else None
            if scenario == "direct" and metric == "tool_calls":
                gates["known_direct_call_p90_not_higher"] &= bool(a and b and b["p90"] <= a["p90"])
            if scenario == "ext-scope" and metric in ("reuse_tail_calls", "reuse_tail_invalid_calls"):
                gate = "reuse_call_p90_not_higher" if metric == "reuse_tail_calls" else "reuse_invalid_p90_not_higher"
                gates[gate] &= bool(a and b and a["n"] == expected and b["n"] == expected and b["p90"] <= a["p90"])
        comparisons.append(comparison)
    return {"contract": "inbox_streams/v2", "evidence_root": str(root), "recheck": recheck,
            "all_trials_included": True, "semantic_review": review.get("policy") if review else None, "expected_state_errors_excluded_from_invalid_metric": EXPECTED_ERRORS,
            "raw_metrics_preserved": True, "p90": "nearest rank ceil(.9*n)",
            "delivery": "Same simulated reader in both arms; native production Catalog/codecs/schema; not real platform or durable runtime acceptance.",
            "guidance_cost": "Included in full-run calls, result bytes, tokens and elapsed time. Guidance result bytes/tail calls are observations; per-phase provider tokens/time are not separately attributable.",
            "gates": gates, "automatic_gates_pass": all(gates.values()),
            "decision": "No production adoption until correctness, paired rechecks and cost regressions have been reviewed.",
            "aggregates": groups, "comparisons": comparisons, "runs": rows}


if __name__ == "__main__":
    root, output = map(Path, sys.argv[1:3])
    review = next((json.loads(Path(a.split("=",1)[1]).read_text()) for a in sys.argv[3:] if a.startswith("--review=")), None)
    result = summarize(root, "--recheck" in sys.argv[3:], review)
    output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"trials": len(result["runs"]), "gates": result["gates"]}, ensure_ascii=False))
