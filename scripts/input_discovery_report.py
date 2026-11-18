#!/usr/bin/env python3
"""Summarize every frozen real-DSH comparison run, including failed runs.

Usage: python3 scripts/input_discovery_report.py EVIDENCE_DIR OUTPUT_JSON [ORACLE_REVIEW_JSON]
No network access; never reads runtime/session or credential files.
"""
import hashlib
import json
import math
import statistics
import sys
from pathlib import Path


def distribution(values):
    values = sorted(values)
    if not values:
        return None
    return {"median": statistics.median(values), "p90": values[math.ceil(.9 * len(values)) - 1], "min": values[0], "max": values[-1]}


def summarize(root, review=None):
    rows = []
    reviewed = review.get("runs", {}) if review else {}
    for path in sorted(root.glob("*/evidence.json")):
        report = json.loads(path.read_text())
        usages = report.get("provider_usage", [])
        tokens = [u.get("prompt_tokens", u.get("input_tokens")) for u in usages]
        output_tokens = [u.get("completion_tokens", u.get("output_tokens")) for u in usages]
        calls = report.get("calls", [])
        original_status = report["status"]
        reviewed_row = reviewed.get(str(path.relative_to(root)))
        if reviewed_row:
            report["status"] = reviewed_row["status"]
            report["issues"] = reviewed_row.get("issues", [])
        # Count reference-to-label mismatches and unrelated *attempts* as waste,
        # even when formal delivery ultimately fails the correctness check.
        delivered = report.get("final_state", {}).get("delivered", [])
        unrelated = sum(label.startswith("历史更新 ") for label in delivered)
        row = {k: report.get(k) for k in ("scenario", "mode", "repeat", "pilot", "status", "issues", "error", "stop_reason", "model", "schema_sha256", "runtime_agent", "tool_calls", "invalid_calls", "result_bytes", "model_requests", "model_request_bytes", "model_response_bytes", "elapsed_seconds", "target_first_page_wait_seconds")}
        row["original_status"] = original_status
        row.update({"evidence": str(path.relative_to(root)), "evidence_sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "prompt_tokens": sum(tokens) if tokens and None not in tokens else None, "completion_tokens": sum(output_tokens) if output_tokens and None not in output_tokens else None, "unrelated_deliveries": unrelated, "delivery_order": delivered, "remaining": report.get("final_state", {}).get("remaining"), "discovery_calls": sum(c["name"] != "inbox_read" for c in calls)})
        row["invalid_calls"] = row["invalid_calls"] or 0
        rows.append(row)
    scenarios = sorted({r["scenario"] for r in rows})
    aggregates = []
    metrics = ["tool_calls", "invalid_calls", "prompt_tokens", "completion_tokens", "result_bytes", "model_requests", "model_request_bytes", "model_response_bytes", "elapsed_seconds", "target_first_page_wait_seconds"]
    for scenario in scenarios:
        for mode in ("four", "two"):
            group = [r for r in rows if r["scenario"] == scenario and r["mode"] == mode]
            expected = 10 if scenario in ("direct", "deep-blocked", "preview-return") else 5
            aggregate = {"scenario": scenario, "mode": mode, "runs": len(group), "expected_runs": expected, "passed": sum(r["status"] == "passed" for r in group)}
            aggregate["success_rate"] = aggregate["passed"] / len(group) if group else 0
            aggregate["distributions"] = {metric: distribution([r[metric] for r in group if r[metric] is not None and not (metric == "target_first_page_wait_seconds" and r[metric] == 0)]) for metric in metrics}
            aggregates.append(aggregate)
    by_key = {(a["scenario"], a["mode"]): a for a in aggregates}
    gates = {"complete_frozen_sample": len(rows) == 110 and not any(r["pilot"] for r in rows) and all(a["runs"] == a["expected_runs"] for a in aggregates), "candidate_all_correct": all(r["status"] == "passed" for r in rows if r["mode"] == "two"), "success_not_lower_per_scenario": True, "call_p90_not_higher_per_scenario": True, "invalid_p90_not_higher_per_scenario": True, "multistep_invalid_calls_reduced": False, "direct_no_extra_calls": False}
    comparisons = []
    for scenario in scenarios:
        four, two = by_key[scenario, "four"], by_key[scenario, "two"]
        comparison = {"scenario": scenario}
        gates["success_not_lower_per_scenario"] &= two["success_rate"] >= four["success_rate"]
        for metric, gate in (("tool_calls", "call_p90_not_higher_per_scenario"), ("invalid_calls", "invalid_p90_not_higher_per_scenario")):
            a, b = four["distributions"][metric], two["distributions"][metric]
            ok = a is not None and b is not None and b["p90"] <= a["p90"]
            gates[gate] &= ok
            comparison[metric + "_p90_not_higher"] = ok
        for metric in ("prompt_tokens", "model_request_bytes", "result_bytes", "elapsed_seconds", "target_first_page_wait_seconds"):
            a, b = four["distributions"][metric], two["distributions"][metric]
            comparison[metric + "_median_ratio_two_over_four"] = b["median"] / a["median"] if a and b and a["median"] else None
        comparisons.append(comparison)
    direct = [r for r in rows if r["mode"] == "two" and r["scenario"] == "direct"]
    gates["direct_no_extra_calls"] = len(direct) == 10 and all(r["tool_calls"] == 2 and r["status"] == "passed" for r in direct)
    def wasted(mode):
        return sum(r["invalid_calls"] for r in rows if r["mode"] == mode and r["scenario"] in ("deep-blocked", "preview-return"))
    gates["multistep_invalid_calls_reduced"] = wasted("two") < wasted("four")
    return {"oracle_review_policy": review.get("policy") if review else None, "evidence_root": str(root), "p90_method": "nearest rank, ceil(0.9*n)", "all_runs_included": True, "gates": gates, "automatic_correctness_and_path_gates_pass": all(gates.values()), "decision": "manual_review_required: cost/wait regressions, failures and scope must be examined before adoption", "aggregates": aggregates, "comparisons": comparisons, "runs": rows}


if __name__ == "__main__":
    root, output = map(Path, sys.argv[1:3])
    review = json.loads(Path(sys.argv[3]).read_text()) if len(sys.argv) > 3 else None
    report = summarize(root, review)
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"runs": len(report["runs"]), "gates": report["gates"]}, ensure_ascii=False))
