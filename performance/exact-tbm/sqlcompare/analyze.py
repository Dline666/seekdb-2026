"""Validate completed SQL comparisons and summarize query/stage-total T/B/M.

Stage E is local only: it does not apply official write/query phase weights or
predict a platform score. One-round large census records remain diagnostic.
"""
import argparse
import collections
import json
import math
import pathlib
import statistics


METRICS = ("t_s", "b_bytes", "m_bytes")
WEIGHTS = (("t_s", 0.6, 0.001), ("b_bytes", 0.2, 1048576),
           ("m_bytes", 0.2, 1048576))


def require(condition, message):
    if not condition:
        raise ValueError(message)


def median_metrics(runs):
    return {key: statistics.median(run[key] for run in runs)
            for key in METRICS}


def stage_e(run, reference):
    value = 0.0
    for metric, weight, epsilon in WEIGHTS:
        ratio = (reference[metric] + epsilon) / (run[metric] + epsilon)
        value += weight * ratio / (1.0 + ratio)
    return value


def read_runs(paths):
    groups = collections.defaultdict(dict)
    workload_hashes = {}
    for path in paths:
        with open(path, encoding="utf-8-sig") as stream:
            records = [json.loads(line) for line in stream if line.strip()]
        require(records and records[0].get("phase") == "config",
                f"{path}: missing initial config")
        config = records[0]
        require(config.get("mode") == "generic_sql_interleaved",
                f"{path}: wrong driver mode")
        require(records[-1].get("phase") == "success",
                f"{path}: incomplete run; no final success marker")
        require(not any(r.get("phase") in ("failure", "failed_phase",
                                           "process_read_failure") for r in records),
                f"{path}: contains a failed phase")
        for record in records:
            require(record.get("reference_bad", 0) == 0,
                    f"{path}: truth mismatch")
            events = record.get("memory_events", {})
            require(events.get("oom", 0) == 0 and events.get("oom_kill", 0) == 0,
                    f"{path}: OOM run cannot be compared")

        dataset = (config["rows"], config["dim"], config["fixture_model"],
                   config["extended_cases"])
        pairs = collections.defaultdict(dict)
        for record in records:
            suite = record.get("suite")
            phase = record.get("phase")
            if not suite or phase not in (f"{suite}_startup", f"{suite}_query",
                                          f"{suite}_shutdown", "query_validation"):
                continue
            role = "validation" if phase == "query_validation" else phase[len(suite)+1:]
            key = (suite, record["name"], record["round"])
            require(role not in pairs[key], f"{path}: duplicate {key} {role}")
            pairs[key][role] = record

        names = [pathlib.Path(name).name for name in config["bins"]]
        expected = set()
        for suite in config["suites"]:
            count = config["census_rounds"] if suite == "census" else config["rounds"]
            for name in names:
                expected.update((suite, name, round_) for round_ in range(count))
        require(set(pairs) == expected,
                f"{path}: missing/unexpected version-round groups: {expected ^ set(pairs)}")

        checked_queries = checked_rows = 0
        for (suite, name, round_), pair in pairs.items():
            require(set(pair) == {"startup", "query", "shutdown", "validation"},
                    f"{path}: incomplete phases for {(suite, name, round_)}")
            query, validation = pair["query"], pair["validation"]
            hashes = {record["sql_sha256"] for record in pair.values()}
            require(len(hashes) == 1, f"{path}: phase workload hash mismatch")
            sql_hash = hashes.pop()
            group = (*dataset, suite)
            require(workload_hashes.setdefault(group, sql_hash) == sql_hash,
                    f"{path}: SQL differs across versions/rounds for {group}")
            for field in ("reference_checked", "reference_rows_checked"):
                require(query[field] == validation[field],
                        f"{path}: query/validation {field} mismatch")
            require(query["reference_checked"] == query["queries"] > 0,
                    f"{path}: not every query was independently checked")
            require(query["resident_pages_before_query"] <= 1,
                    f"{path}: SQL input was not cold")
            require(pair["startup"].get("startup_meter_started_before_exec") is True,
                    f"{path}: incomplete startup timing")
            for role in ("startup", "query", "shutdown"):
                record = pair[role]
                require(record.get("memory_peak_scope") == "phase_reset_fd",
                        f"{path}: memory peak was not reset for {role}")
                for metric in METRICS:
                    require(math.isfinite(record[metric]) and record[metric] >= 0,
                            f"{path}: invalid {role} {metric}")

            measured = [pair[role] for role in ("startup", "query", "shutdown")]
            total = {"t_s": sum(r["t_s"] for r in measured),
                     "b_bytes": sum(r["b_bytes"] for r in measured),
                     "m_bytes": max(r["m_bytes"] for r in measured)}
            record = {"round": round_, "position": query["position"],
                      "query_only": {key: query[key] for key in METRICS},
                      "measured_stage_total": total,
                      "reference_checked": query["reference_checked"],
                      "reference_rows_checked": query["reference_rows_checked"]}
            run_key = (name, round_)
            require(run_key not in groups[group],
                    f"{path}: duplicate version-round across input files: {group} {run_key}")
            groups[group][run_key] = record
            checked_queries += query["reference_checked"]
            checked_rows += query["reference_rows_checked"]
        require(records[-1]["reference_checked"] == checked_queries and
                records[-1]["reference_rows_checked"] == checked_rows,
                f"{path}: final validation totals do not match completed phases")
    return groups, workload_hashes


def analyze(paths, baseline):
    groups, hashes = read_runs(paths)
    output = []
    for group, records in sorted(groups.items()):
        rows, dim, fixture_model, extended, suite = group
        by_name = collections.defaultdict(list)
        for (name, _), run in records.items():
            by_name[name].append(run)
        require(baseline in by_name, f"missing baseline {baseline} for {group}")
        for runs in by_name.values():
            runs.sort(key=lambda r: r["round"])
        reference_rounds = by_name[baseline][:3]
        expected_rounds = [r["round"] for r in by_name[baseline]]
        for name, runs in sorted(by_name.items()):
            require([r["round"] for r in runs] == expected_rounds,
                    f"mismatched rounds between {baseline} and {name} for {group}")
            item = {"suite": suite, "name": name, "rows": rows, "dim": dim,
                    "fixture_model": fixture_model, "extended_cases": extended,
                    "rounds": len(runs), "sql_sha256": hashes[group],
                    "single_diagnostic": suite == "census" and len(runs) == 1,
                    "fewer_than_three_rounds": len(runs) < 3,
                    "reference_checked": sum(r["reference_checked"] for r in runs),
                    "reference_rows_checked": sum(r["reference_rows_checked"] for r in runs),
                    "reference_bad": 0}
            for scope in ("query_only", "measured_stage_total"):
                reference = median_metrics([r[scope] for r in reference_rounds])
                scores = [stage_e(r[scope], reference) for r in runs]
                item[scope] = {
                    "label": ("query-only service phase" if scope == "query_only"
                              else "startup + query + shutdown service-stage sum"),
                    "is_complete_driver_wall_clock": False,
                    "median": median_metrics([r[scope] for r in runs]),
                    "runs": [{"round": r["round"], "position": r["position"],
                              **r[scope]} for r in runs],
                    "stage_E": {"baseline": baseline,
                                "reference_rounds": [r["round"] for r in reference_rounds],
                                "fixed_reference_metrics": reference,
                                "median": statistics.median(scores), "runs": scores},
                }
            item["note"] = (
                "Local SQL-stage E only; not the ordinary write/query overall E or a platform score. "
                "The startup + query + shutdown service-stage sum adds T/B and takes max M; "
                "it excludes fixture preparation, cold-cache eviction, and external truth generation/validation, "
                "and is not the complete driver's wall clock. "
                + ("Single census pair is diagnostic, not a stable speedup estimate."
                   if item["single_diagnostic"] else ""))
            output.append(item)
    return output


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="+")
    parser.add_argument("--baseline", default="base")
    args = parser.parse_args()
    print(json.dumps(analyze(args.paths, args.baseline), indent=2))
