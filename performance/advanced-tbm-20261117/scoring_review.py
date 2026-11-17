#!/usr/bin/env python3
"""Recompute local phase scores from retained samples without running a benchmark."""

import argparse
import json
import math
from pathlib import Path
from statistics import median


METRICS = (("t_s", 0.6, 0.001), ("b_bytes", 0.2, 1048576), ("m_bytes", 0.2, 1048576))


def phase_score(baseline, sample):
    total = 0.0
    for metric, weight, epsilon in METRICS:
        ratio = (baseline[metric] + epsilon) / (sample[metric] + epsilon)
        total += weight * ratio / (1 + ratio)
    return total


def recompute(results):
    groups = []
    for label, group in results["final_comparisons"].items():
        base, candidate = group["versions"]
        samples = [version["samples"] for version in (base, candidate)]
        anchor = {
            phase: {
                metric: median(s[metric] for s in samples[0][phase][:3])
                for metric, _, _ in METRICS
            }
            for phase in ("write", "query")
        }
        scores = []
        for version in samples:
            writes = version["write"]
            queries = version["query"]
            assert len(writes) == len(queries) == 5, label
            assert [s["round"] for s in writes] == [s["round"] for s in queries], label
            scores.append([
                (phase_score(anchor["write"], write) + 2 * phase_score(anchor["query"], query)) / 3
                for write, query in zip(writes, queries)
            ])
        write_t = [median(s["t_s"] for s in version["write"]) for version in samples]
        query_t = [median(s["t_s"] for s in version["query"]) for version in samples]
        groups.append({
            "label": label,
            "baseline": base["name"],
            "write_t_s": write_t,
            "query_t_s": query_t,
            "query_t_change_pct": (query_t[1] / query_t[0] - 1) * 100,
            "local_phase_E_first_three": [median(s[:3]) for s in scores],
            "local_phase_E_five": [median(s) for s in scores],
            "base_rounds_E": scores[0],
            "candidate_rounds_E": scores[1],
        })
    return groups


def check_equal(actual, expected, location="groups"):
    if isinstance(actual, dict):
        assert actual.keys() == expected.keys(), location
        for key in actual:
            check_equal(actual[key], expected[key], f"{location}.{key}")
    elif isinstance(actual, list):
        assert len(actual) == len(expected), location
        for index, (left, right) in enumerate(zip(actual, expected)):
            check_equal(left, right, f"{location}[{index}]")
    elif isinstance(actual, (int, float)):
        assert math.isclose(actual, expected, rel_tol=1e-12, abs_tol=1e-12), location
    else:
        assert actual == expected, location


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    directory = Path(__file__).resolve().parent
    parser.add_argument("--results", type=Path, default=directory / "RESULTS.json")
    parser.add_argument("--check", type=Path)
    args = parser.parse_args()
    groups = recompute(json.loads(args.results.read_text(encoding="utf-8")))
    if args.check:
        check_equal(groups, json.loads(args.check.read_text(encoding="utf-8"))["groups"])
        print(f"Verified all {len(groups)} groups against the stored scoring review.")
    else:
        print(json.dumps({"groups": groups}, indent=2))


if __name__ == "__main__":
    main()
