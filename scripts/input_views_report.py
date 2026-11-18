#!/usr/bin/env python3
"""Summarize all inbox_views/v1 frozen runs without network/session access."""
import hashlib
import json
import math
import statistics
import sys
from pathlib import Path

SCENARIOS = {"direct": 10, "deep-blocked": 10, "preview-return": 10,
             "ambiguous": 5, "zero-query": 5, "stats-only": 5,
             "preview-skip": 5, "recovery": 5,
             "ext-stats": 3, "ext-scope": 3, "ext-explore": 3}
METRICS = ("tool_calls", "invalid_calls", "schema_bytes", "result_bytes",
           "model_request_bytes", "model_response_bytes", "prompt_tokens",
           "completion_tokens", "elapsed_seconds", "target_first_page_wait_seconds")


def distribution(values):
    values = sorted(v for v in values if v is not None)
    if not values:
        return None
    return {"n": len(values), "median": statistics.median(values),
            "p90": values[math.ceil(.9 * len(values)) - 1]}


def summarize(root, review=None):
    reviewed = review.get("runs", {}) if review else {}
    rows = []
    for path in sorted(root.glob("*/evidence.json")):
        report = json.loads(path.read_text())
        original_status = report["status"]
        revised = reviewed.get(str(path.relative_to(root)))
        if revised:
            if revised["evidence_sha256"] != hashlib.sha256(path.read_bytes()).hexdigest():
                raise ValueError("review does not match raw evidence: " + str(path))
            report["status"], report["issues"] = revised["status"], revised["issues"]
        row = {k: report.get(k) for k in ("scenario", "mode", "repeat", "status", "issues", "error", "pilot", "model", "schema_sha256", *METRICS)}
        for key, alternate in (("prompt_tokens", "input_tokens"), ("completion_tokens", "output_tokens")):
            values = [u.get(key, u.get(alternate)) for u in report.get("provider_usage", [])]
            row[key] = sum(values) if values and None not in values else None
        row["invalid_calls"] = row["invalid_calls"] or 0
        row["original_status"] = original_status
        row["evidence"] = str(path.relative_to(root))
        row["evidence_sha256"] = hashlib.sha256(path.read_bytes()).hexdigest()
        row["delivery_order"] = report.get("final_state", {}).get("delivered", [])
        rows.append(row)
    groups = []
    for scenario, expected in SCENARIOS.items():
        for mode in ("four", "views"):
            runs = [r for r in rows if r["scenario"] == scenario and r["mode"] == mode]
            groups.append({"scenario": scenario, "mode": mode, "n": len(runs), "expected": expected,
                           "passed": sum(r["status"] == "passed" for r in runs),
                           "distributions": {metric: distribution([r[metric] for r in runs]) for metric in METRICS}})
    gates = {"complete_128": len(rows) == 128 and all(g["n"] == g["expected"] for g in groups) and not any(r["pilot"] for r in rows),
             "candidate_all_correct": all(r["status"] == "passed" for r in rows if r["mode"] == "views"),
             "per_scenario_success_not_lower": True, "call_p90_not_higher": True,
             "invalid_p90_not_higher": True, "direct_no_extra_calls": True}
    comparisons = []
    for scenario in SCENARIOS:
        control, candidate = [g for g in groups if g["scenario"] == scenario]
        gates["per_scenario_success_not_lower"] &= candidate["passed"] >= control["passed"]
        for metric, gate in (("tool_calls", "call_p90_not_higher"), ("invalid_calls", "invalid_p90_not_higher")):
            a, b = control["distributions"][metric], candidate["distributions"][metric]
            gates[gate] &= bool(a and b and b["p90"] <= a["p90"])
        comparison = {"scenario": scenario}
        for metric in METRICS:
            a, b = control["distributions"][metric], candidate["distributions"][metric]
            comparison[metric + "_median_ratio"] = b["median"] / a["median"] if a and b and a["median"] else None
        comparisons.append(comparison)
    direct = [r for r in rows if r["mode"] == "views" and r["scenario"] == "direct"]
    gates["direct_no_extra_calls"] = len(direct) == 10 and all(r["tool_calls"] == 2 and r["status"] == "passed" for r in direct)
    return {"review_policy": review.get("policy") if review else None, "evidence_root": str(root), "all_runs_included": True, "p90": "nearest rank ceil(.9*n)",
            "gates": gates, "automatic_gates_pass": all(gates.values()),
            "decision": "Review failures and paired cost regressions before adoption; passing counts never waive correctness.",
            "aggregates": groups, "comparisons": comparisons, "runs": rows}


if __name__ == "__main__":
    root, output = map(Path, sys.argv[1:3])
    review = json.loads(Path(sys.argv[3]).read_text()) if len(sys.argv)>3 else None
    report = summarize(root,review)
    output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps({"runs": len(report["runs"]), "gates": report["gates"]}, ensure_ascii=False))
