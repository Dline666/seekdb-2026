"""Generate a reviewable ROUND7 report in a NEW directory; never run a workload.

Example (after all comparisons, stress and capacity checks finish):
  python performance/exact-tbm/round7/report.py --selected partial-balanced \
      --results /tmp/seekdb-round7/results --output /tmp/seekdb-round7-report

Only the three generated documents are written. Sources, evidence, the official
ROUND7.md and existing output directories are never copied or overwritten.
Links deliberately target the eventual performance/exact-tbm/ROUND7.md location.
"""

import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import math
from pathlib import Path
import re
import statistics
import sys


BASELINE = "4a257014c9f43fd1dbb86b008d32cd93303b4d94"
BASE_SHA = "b52c80e51e26b5edd4c30fe2d377115f34d4de02ca6dff1a38888e872b12e98b"
MEMORY_LIMIT = 2 << 30
METRICS = ("t_s", "b_bytes", "m_bytes")
HERE = Path(__file__).resolve().parent
UPDATE_STAGES = ("startup", "load", "index_query", "category_update",
                 "point_update_front", "point_update_tail", "updated_query",
                 "updated_census", "shutdown", "recovery", "recovered_query",
                 "recovered_census", "final_shutdown")
STRESS_STAGES = (
    "initial_startup", "write", "query_after_write", "updates_deletes_rollback",
    "query_after_updates", "kill_after_acknowledged_commits",
    "recovery_after_commit_kill", "query_after_commit_recovery",
    "uncommitted_tail_flushed", "kill_with_uncommitted_transaction",
    "kill_during_recovery", "final_recovery", "query_after_final_recovery",
    "final_full_census", "bulk_update", "query_after_bulk_update",
    "kill_after_bulk_commit", "recovery_after_bulk_commit",
    "query_after_bulk_recovery", "final_full_census", "graceful_shutdown")
KILLS = ("kill_after_acknowledged_commits", "kill_with_uncommitted_transaction",
         "kill_during_recovery", "kill_after_bulk_commit")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def integer(value, expected=None):
    return type(value) is int and (expected is None or value == expected)


def finite(value):
    return type(value) in (int, float) and math.isfinite(value) and value >= 0


def canonical(name):
    return Path(name.split(":", 1)[0]).name


def ratio(candidate, baseline):
    return candidate / baseline if baseline else None


def median(values):
    values = [v for v in values if v is not None]
    return statistics.median(values) if values else None


class Evidence:
    """Track input hashes and reject evidence changing during report generation."""

    def __init__(self, directory):
        self.directory = directory.resolve()
        self.files = {}

    def text(self, name):
        require(Path(name).name == name, f"Evidence must be a direct child: {name}")
        path = self.directory / name
        require(path.is_file() and not path.is_symlink(), f"Missing regular evidence: {path}")
        raw = path.read_bytes()
        digest = hashlib.sha256(raw).hexdigest()
        require(self.files.setdefault(name, digest) == digest, f"Evidence changed: {name}")
        return raw.decode("utf-8-sig")

    def json(self, name):
        return json.loads(self.text(name))

    def records(self, name):
        return [json.loads(line) for line in self.text(name).splitlines() if line.strip()]

    def unchanged(self):
        for name, digest in self.files.items():
            require(hashlib.sha256((self.directory / name).read_bytes()).hexdigest() == digest,
                    f"Evidence changed before publication: {name}")


def failed_attempts_archive(evidence):
    """Preserve interrupted artifacts verbatim, including invalid/empty JSON."""
    directory = evidence.directory / "failed-attempts"
    if not directory.exists():
        return None
    require(directory.is_dir() and not directory.is_symlink(), "Invalid failed-attempts archive")
    files = []
    for path in sorted(directory.rglob("*")):
        require(not path.is_symlink(), "Failed-attempts archive contains a symlink")
        if path.is_dir():
            continue
        require(path.is_file(), "Failed-attempts archive contains a non-regular file")
        raw = path.read_bytes()
        files.append({"file": path.relative_to(evidence.directory).as_posix(),
                      "bytes": len(raw), "sha256": hashlib.sha256(raw).hexdigest()})
    require(files, "Empty failed-attempts archive")
    incident_file = "stress-environment-failure.json"
    incident = evidence.json(incident_file)
    require(isinstance(incident, dict) and incident, "Missing stress environment failure explanation")
    return {"directory": "failed-attempts", "files": files,
            "environment_incident_file": incident_file, "environment_incident": incident,
            "not_successful_validation_evidence": True}


def analyzer(relative):
    # Reuse existing read-only validators, without creating __pycache__ files.
    path = HERE / relative
    spec = importlib.util.spec_from_file_location("round7_report_" + path.parent.name + path.stem, path)
    module = importlib.util.module_from_spec(spec)
    previous = sys.dont_write_bytecode
    try:
        sys.dont_write_bytecode = True
        spec.loader.exec_module(module)
    finally:
        sys.dont_write_bytecode = previous
    return module


def check_zero(record, label):
    require(integer(record.get("reference_bad"), 0), f"{label}: missing integer reference_bad=0")


def check_events(record, label):
    events = record.get("memory_events", {})
    for key in ("oom", "oom_kill"):
        require(integer(events.get(key), 0), f"{label}: missing/nonzero {key}")
    if "oom_group_kill" in events:
        require(integer(events["oom_group_kill"], 0), f"{label}: nonzero group kill")


def check_measured(record, label):
    require(record.get("memory_peak_scope") == "phase_reset_fd", f"{label}: peak reset missing")
    require(integer(record.get("sample_period_ms"), 50), f"{label}: sample period changed")
    check_events(record, label)
    for metric in ("t_s", "wall_s"):
        require(finite(record.get(metric)), f"{label}: invalid {metric}")
    for metric in ("m_bytes", "b_bytes", "r_bytes", "w_bytes"):
        require(integer(record.get(metric)) and record[metric] >= 0, f"{label}: invalid {metric}")
    require(record["m_bytes"] > 0 and record["b_bytes"] == record["r_bytes"] + record["w_bytes"],
            f"{label}: missing peak or inconsistent I/O")


def measured_peaks(records):
    return [{"phase": r["phase"], "name": r.get("name"), "round": r.get("round"),
             "kernel_peak_bytes": r["m_bytes"],
             "kernel_peak_above_limit_bytes": max(0, r["m_bytes"] - MEMORY_LIMIT),
             "sampled_peak": r.get("sampled_peak"),
             "sampled_current_above_limit_bytes": (
                 max(0, r["sampled_peak"]["memory_current"] - MEMORY_LIMIT)
                 if integer((r.get("sampled_peak") or {}).get("memory_current")) else None)}
            for r in records if "m_bytes" in r and "t_s" in r]


def one(records, phase):
    matches = [r for r in records if r.get("phase") == phase]
    require(len(matches) == 1, f"Expected one {phase}, got {len(matches)}")
    return matches[0]


def verify_stress(evidence, selected, selected_sha, label=None):
    label = label or selected + "-stress"
    summary = evidence.json(label + "-summary.json")
    run = evidence.json(label + "-run.json")
    records = evidence.records(label + ".jsonl")
    require(summary.get("validation_passed") is True and run.get("validation_passed") is True,
            "Selected stress validation has not passed")
    require(all(summary.get(k) is True for k in ("single_version_capacity_check", "no_comparison_E",
                                                 "per_phase_peak_reset_verified")),
            "Stress validation/scope markers missing")
    require(integer(run.get("exit_code"), 0) and integer(run.get("build_exit_code"), 0),
            "Selected stress process/build did not finish successfully")
    require(run.get("candidate") == selected and run.get("candidate_sha256") == selected_sha,
            "Selected stress binary hash does not match its manifest")
    require(run.get("stress_label", label) == label and
            Path(run.get("data_dir", "")).name == label,
            "Selected stress label/data-directory identity mismatch")
    require(integer(run.get("official_submissions"), 0), "Unexpected formal submission")
    require(records and records[0].get("phase") == "config" and records[-1].get("phase") == "success",
            "Selected stress is incomplete")
    config, final = records[0], records[-1]
    require(summary.get("config") == config and summary.get("success") == final,
            "Stress summary differs from raw config/success")
    expected = {"rows": 550000, "dim": 1024, "queries_per_state": 12, "batch": 64,
                "raw_vector_bytes": 2252800000, "exceeds_2gib": True, "bulk_update": True,
                "memory_limit": MEMORY_LIMIT, "memory_swap_max": 0, "cpu_max": "200000 100000",
                "index_mem_mb": 256, "durability": "explicit fdatasync"}
    require(all(config.get(k) == v and type(config.get(k)) is type(v) for k, v in expected.items()),
            "Stress configuration does not prove the required >2GiB workload")
    require(config.get("binary") == run.get("candidate_binary") and
            canonical(config["binary"]) == selected and config.get("data_dir") == run.get("data_dir"),
            "Stress candidate/data-directory identity mismatch")
    require(config.get("env") == ["GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20"],
            "Stress runtime environment changed")
    phases = [r for r in records if "t_s" in r]
    require([r.get("phase") for r in phases] == list(STRESS_STAGES) and summary.get("phases") == phases,
            "Stress phases are incomplete, reordered or differ from summary")
    for r in records:
        require(r.get("phase") not in ("failure", "failed_phase", "failed_request", "process_read_failure"),
                "Stress contains a failed phase")
        if any(k in r for k in ("reference_bad", "reference_checked", "reference_rows_checked", "point_rows_checked")):
            check_zero(r, "stress " + r["phase"])
        if "memory_events" in r:
            check_events(r, "stress " + r["phase"])
    for r in phases:
        check_measured(r, "stress " + r["phase"])
        if r["phase"] == "initial_startup" or ("recovery" in r["phase"] and not r["phase"].startswith("query_")):
            require(r.get("startup_meter_started_before_exec") is True, "Stress startup timing incomplete")
    query_phases = [r for r in phases if r["phase"].startswith("query_")]
    require(len(query_phases) == 6 and all(integer(r.get("reference_checked"), 12) for r in query_phases),
            "Stress is missing a 12-query truth state")
    require(all(finite(r.get("oracle_s")) for r in query_phases), "Stress oracle timing missing")
    censuses = [r for r in phases if r["phase"] == "final_full_census"]
    require(len(censuses) == 2 and all(integer(r.get("reference_rows_checked"), 549997) for r in censuses),
            "Stress does not contain both full live-row censuses")
    written = one(records, "write")
    require(integer(written.get("rows_acknowledged"), 550000) and
            integer(written.get("raw_vector_bytes"), 2252800000) and
            integer(written.get("log_bytes")) and written["log_bytes"] > 2252800000 and
            written.get("generator_excluded_from_t") is True,
            "Stress initial load is incomplete")
    require(integer(one(records, "bulk_update").get("affected_rows"), 549997), "Incomplete bulk update")
    for phase in KILLS:
        require(one(records, phase).get("signal") == "SIGKILL", "Missing stress SIGKILL: " + phase)
    during = one(records, "kill_during_recovery")
    require(during.get("killed_before_ready") is True and integer(during.get("delay_ms"), 10),
            "Recovery interruption was not verified before ready")
    flushed, discarded = one(records, "uncommitted_tail_flushed"), one(records, "uncommitted_tail_discarded")
    check_zero(flushed, "stress uncommitted_tail_flushed")
    check_zero(discarded, "stress uncommitted_tail_discarded")
    tail = flushed.get("uncommitted_tail_bytes")
    require(integer(tail) and tail > 0 and flushed.get("transaction_committed") is False and
            integer(flushed.get("reference_checked"), 1) and integer(flushed.get("point_rows_checked"), 3) and
            integer(discarded.get("point_rows_checked"), 3), "Uncommitted transaction truth is incomplete")
    require(flushed["log_bytes"] == flushed["committed_log_bytes"] + tail and
            discarded.get("committed_log_bytes") == discarded.get("recovered_log_bytes") == flushed["committed_log_bytes"] and
            discarded.get("discarded_tail_bytes") == tail, "Uncommitted tail was not discarded exactly")
    for key, value in {"independent_topk_queries": 73, "census_rows_checked": 1099994,
                       "transaction_point_rows_checked": 6, "sigkill_events": 4,
                       "memory_limit_bytes": MEMORY_LIMIT, "uncommitted_tail_bytes": tail}.items():
        require(integer(summary.get(key), value), "Stress summary count mismatch: " + key)
    require(integer(final.get("reference_checked"), 73) and
            sum(r.get("reference_checked", 0) for r in phases) == 73 and
            final.get("full_census_checked") is True and final.get("exceeds_2gib") is True and
            final.get("cgroup_reused_across_restarts") is True,
            "Stress final checks or restart accounting are incomplete")
    basic = one(records, "basic_workflow_success")
    require(integer(basic.get("reference_checked"), 49) and basic.get("full_census_checked") is True and
            basic.get("cgroup_reused_across_restarts") is True, "Stress basic workflow is incomplete")
    check_zero(basic, "stress basic success")
    check_events(basic, "stress basic success")
    require(final.get("raw_vector_bytes") == 2252800000 and final.get("data_dir") == config["data_dir"] and
            integer(final.get("log_bytes")) and final["log_bytes"] > 2252800000,
            "Stress final data identity/size mismatch")
    check_zero(final, "stress success")
    check_events(final, "stress success")
    peak = final.get("whole_workflow_m_bytes")
    require(integer(peak) and peak >= max(r["m_bytes"] for r in phases) and
            summary.get("whole_workflow_peak_bytes") == peak and
            summary.get("peak_above_configured_limit_bytes") == max(0, peak - MEMORY_LIMIT),
            "Stress whole-workflow peak mismatch")
    return {"label": label, "summary_file": label + "-summary.json", "raw_file": label + ".jsonl",
            "run_file": label + "-run.json", "validated": True, "summary": summary,
            "phase_peaks": measured_peaks(phases), "binary_sha256": selected_sha}


def verify_capacity(evidence, selected, label=None):
    label = label or selected + "-capacity"
    summary, run = evidence.json(label + "-summary.json"), evidence.json(label + "-run.json")
    records = evidence.records(label + ".jsonl")
    require(integer(run.get("exit_code"), 0), "Selected capacity process did not finish")
    require(records and records[0].get("phase") == "config" and records[-1].get("phase") == "success",
            "Selected capacity run is incomplete")
    config, final = records[0], records[-1]
    require(summary.get("single_version_capacity_check") is True and summary.get("no_comparison_E") is True and
            summary.get("validation") == final, "Capacity summary lacks matching raw success")
    expected = {"mode": "generic_sql_interleaved", "rows": 550000, "live_rows": 549997,
                "dim": 1024, "rounds": 1, "census_rounds": 1, "raw_vector_bytes": 2252800000,
                "memory_limit": MEMORY_LIMIT, "memory_swap_max": 0, "cpu_max": "200000 100000",
                "index_mem_mb": 256, "sync": "fdatasync", "extended_cases": False,
                "fixture_model": "round3_final_all_c9",
                "fixture_source": "/tmp/seekdb-round3-stress-final/db/data.log"}
    require(all(config.get(k) == v and type(config.get(k)) is type(v) for k, v in expected.items()),
            "Capacity configuration mismatch")
    bins = config.get("bins", [])
    require(len(bins) == 1 and canonical(bins[0]) == selected and
            config.get("suites") == ["point", "order", "census"], "Capacity candidate/suites mismatch")
    argv = run.get("argv", [])
    require("-bins" in argv and argv[argv.index("-bins") + 1] == bins[0], "Capacity run binary mismatch")
    require("-work-dir" in argv and argv[argv.index("-work-dir") + 1] == config.get("work_dir") and
            "-fixture" in argv and argv[argv.index("-fixture") + 1] == config["fixture_source"] and
            config.get("env") == ["GOMAXPROCS=2", "GOMEMLIMIT=1288490188", "GOGC=20"],
            "Capacity fixture/work directory/runtime environment mismatch")
    phases = [r for r in records if r.get("phase", "").endswith(("_startup", "_query", "_shutdown"))]
    require([r["phase"] for r in phases] == [s + "_" + p for s in config["suites"]
                                             for p in ("startup", "query", "shutdown")],
            "Capacity measured phases incomplete or reordered")
    require(summary.get("phases") == phases, "Capacity summary phases differ from raw records")
    for r in records:
        require(r.get("phase") not in ("failure", "failed_phase", "failed_request", "process_read_failure"),
                "Capacity contains a failed phase")
        if "reference_bad" in r or "reference_checked" in r or "reference_rows_checked" in r:
            check_zero(r, "capacity " + r["phase"])
        if "memory_events" in r:
            check_events(r, "capacity " + r["phase"])
    for r in phases:
        check_measured(r, "capacity " + r["phase"])
        require(r.get("name") == selected and r.get("binary") == bins[0] and
                integer(r.get("round"), 0) and integer(r.get("position"), 0),
                "Capacity phase identity mismatch")
    queries = [r for r in phases if r["phase"].endswith("_query")]
    require(all(integer(r.get("reference_rows_checked"), expected)
                for r, expected in zip(queries, (1, 10, 549997))) and
            all(integer(r.get("reference_checked"), 1) and integer(r.get("queries"), 1) for r in queries),
            "Capacity query/census counts mismatch")
    check_zero(final, "capacity success")
    check_events(final, "capacity success")
    require(integer(final.get("reference_checked"), 3) and integer(final.get("reference_rows_checked"), 550008),
            "Capacity final counts mismatch (validation records must not be double-counted)")
    evictions = [r for r in records if r.get("phase") == "fixture_cache_evicted"]
    require(len(evictions) == 6 and all(integer(r.get("resident_pages")) and 0 <= r["resident_pages"] <= 1
                                      for r in evictions), "Capacity cold-cache evidence incomplete")
    fixture = one(records, "fixture_ready")
    private_log = config["work_dir"].rstrip("/") + "/db/data.log"
    require(fixture.get("sha256") == final.get("fixture_sha256_unchanged") and
            re.fullmatch(r"[0-9a-f]{64}", fixture.get("sha256", "")) and
            final.get("original_fixture_metadata_unchanged") is True and
            fixture.get("private_log") == final.get("private_log") == private_log and
            fixture.get("signature") and fixture.get("source_signature"),
            "Capacity fixture changed")
    expected_order = [p for suite in config["suites"] for p in
                      ("fixture_cache_evicted", suite + "_startup", "fixture_cache_evicted",
                       suite + "_query", "query_validation", suite + "_shutdown")]
    roles = set(expected_order)
    require([r.get("phase") for r in records if r.get("phase") in roles] == expected_order,
            "Capacity cache/measurement/validation order changed")
    definitions = [r for r in records if r.get("phase") == "suite_definition"]
    validations = [r for r in records if r.get("phase") == "query_validation"]
    require(len(definitions) == len(validations) == 3, "Capacity definitions/validations incomplete")
    for i, suite in enumerate(config["suites"]):
        definition = definitions[i]
        statements = definition.get("statements", [])
        require(definition.get("suite") == suite and integer(definition.get("rounds"), 1) and
                len(statements) == len(definition.get("labels", [])) == 1 and
                all(isinstance(s, str) for s in statements), "Capacity SQL definition missing")
        sql_hash = hashlib.sha256("\n".join(statements).encode()).hexdigest()
        require(definition.get("sql_sha256") == sql_hash, "Capacity SQL definition hash mismatch")
        validation = validations[i]
        require(validation.get("suite") == suite and integer(validation.get("reference_checked"), 1) and
                integer(validation.get("reference_rows_checked"), (1, 10, 549997)[i]),
                "Capacity independent validation counts mismatch")
        require(all(r.get("sql_sha256") == sql_hash for r in phases[i*3:i*3+3] + [validation]),
                "Capacity phase SQL hash differs from its definition")
        require(integer(queries[i].get("resident_pages_before_query")) and
                queries[i]["resident_pages_before_query"] == evictions[i*2+1]["resident_pages"],
                "Capacity query does not match its cold-cache check")
        for eviction in evictions[i*2:i*2+2]:
            require(eviction.get("path") == private_log and
                    eviction.get("method") == "file_sync_then_posix_fadvise_dontneed",
                    "Capacity evicted the wrong file")
    validator = analyzer("../sqlcompare/analyze.py")
    groups, _ = validator.read_runs([evidence.directory / (label + ".jsonl")])
    rebuilt = [{"suite": key[-1], "runs": list(runs.values())} for key, runs in groups.items()]
    require(summary.get("validated_service_totals") == rebuilt, "Capacity service totals differ from raw phases")
    return {"summary_file": label + "-summary.json", "raw_file": label + ".jsonl",
            "run_file": label + "-run.json", "validated": True, "summary": summary,
            "phase_peaks": measured_peaks(phases), "config": config,
            "identity_limit": "Capacity metadata identifies the binary path but has no before/after binary SHA; stress SHA is verified separately."}


def comparison_kind(report):
    versions = report.get("versions", [])
    if not isinstance(versions, list) or len(versions) != 2:
        return None
    if all("write" in v and "query" in v for v in versions):
        return "ordinary"
    scopes = versions[0].get("scopes", {})
    if "pre_update" in scopes and "post_update" in scopes:
        return "large_update"
    if "indexed_update_ack_total" in scopes:
        return "update"
    if "service_total" in scopes:
        return "large"
    return None


def normalize_comparison(evidence, summary_file, report, kind, manifests, validators):
    raw_name = report["file"]
    records = evidence.records(raw_name)
    run_name = report.get("run_metadata", Path(raw_name).stem + "-run.json")
    run = evidence.json(run_name)
    names = [v["name"] for v in report["versions"]]
    require(integer(run.get("exit_code"), 0), summary_file + ": unfinished driver")
    require(integer(report.get("rounds"), 5) and report.get("complete_interleaving") is True,
            summary_file + ": incomplete five-round comparison")
    check_zero(report, summary_file)
    embedded = {canonical(k): v for k, v in report.get("workload", {}).get("binary_sha256", {}).items()}
    hashes = []
    for name in names:
        identity = canonical(name)
        digest = embedded.get(identity, manifests.get(identity))
        require(isinstance(digest, str) and re.fullmatch(r"[0-9a-f]{64}", digest),
                summary_file + ": missing binary identity for " + name)
        if identity in manifests:
            require(digest == manifests[identity], summary_file + ": binary identity changed")
        hashes.append(digest)
    smoke = ("smoke" in summary_file or hashes[0] == hashes[1] or
             report.get("workload", {}).get("allow_small") is True)
    validation = "revalidated_from_raw"
    if smoke:
        require(report.get("validation_passed") is True or kind == "ordinary", "Smoke lacks original validation")
        validation = "historical_functional_validation_only; excluded from optimization evidence"
        if raw_name == "update-driver-smoke-v2.jsonl":
            rebuilt = validators[kind].summarize(evidence.directory / raw_name, evidence.directory / run_name)
            require(rebuilt == report, "Current v2 functional smoke differs from revalidated raw data")
            validation = "current_hardened_functional_validation; excluded from optimization evidence"
        elif raw_name == "large-update-driver-smoke-v2.jsonl":
            rebuilt = validators[kind].summarize(evidence.directory / raw_name, evidence.directory / run_name, True)
            require(rebuilt == report, "Current v2 large-update smoke differs from revalidated raw data")
            validation = "current_hardened_functional_validation_with_writeback_proof; excluded from optimization evidence"
        elif raw_name == "large-update-driver-smoke.jsonl":
            validation = "historical_functional_smoke_with_invalid_writeback_attribution; excluded from optimization evidence"
    else:
        if kind == "ordinary":
            rebuilt = validators[kind].analyze(evidence.directory / raw_name)
        elif kind in ("large", "large_update"):
            rebuilt = validators[kind].summarize(evidence.directory / raw_name, evidence.directory / run_name,
                                               report["workload"].get("allow_small", False))
        else:
            rebuilt = validators[kind].summarize(evidence.directory / raw_name, evidence.directory / run_name)
        require(rebuilt == report, summary_file + ": stored summary differs from revalidated raw results")
    for r in records:
        if "reference_bad" in r or "reference_checked" in r or "reference_rows_checked" in r:
            check_zero(r, summary_file + ": " + r.get("phase", "record"))
    normalized = []
    for version in report["versions"]:
        scopes = version.get("scopes", {})
        if kind == "ordinary":
            scopes = {p: {"median": {m: version[p][m] for m in METRICS},
                          "runs": [r for r in records if r.get("name") == version["name"] and r.get("phase") == p]}
                      for p in ("write", "query")}
        if kind == "update":
            require(set(scopes) == set(UPDATE_STAGES) | {"indexed_update_ack_total", "measured_stage_sum"},
                    summary_file + ": missing one of the 13 update stages")
        if kind == "large_update":
            require(set(scopes) == {"startup", "pre_update", "category_update", "post_update",
                                   "census", "shutdown", "measured_stage_sum"},
                    summary_file + ": missing large-update scope")
        normalized.append({"name": version["name"], "scopes": scopes,
                           "local_E": version.get("local_E")})
    derived = {}
    for scope in normalized[0]["scopes"]:
        b, c = [v["scopes"][scope] for v in normalized]
        require(len(b["runs"]) == len(c["runs"]) == 5, summary_file + ": wrong run count")
        br = {r["round"]: r for r in b["runs"]}
        cr = {r["round"]: r for r in c["runs"]}
        require(set(br) == set(cr) == set(range(5)), summary_file + ": missing/duplicate rounds")
        paired = {m: [ratio(cr[r][m], br[r][m]) for r in range(5)] for m in METRICS}
        derived[scope] = {
            "ratio_of_medians": {m: ratio(c["median"][m], b["median"][m]) for m in METRICS},
            "median_of_paired_ratios": {m: median(paired[m]) for m in METRICS},
            "paired_ratios": paired,
            "paired_regression_rounds": {m: [i for i, v in enumerate(paired[m]) if v is not None and v > 1]
                                         for m in METRICS},
            "peak_max_bytes": {v["name"]: max(r["m_bytes"] for r in v["scopes"][scope]["runs"])
                               for v in normalized},
        }
        derived[scope]["peak_above_limit_bytes"] = {n: max(0, value - MEMORY_LIMIT)
                                                       for n, value in derived[scope]["peak_max_bytes"].items()}
    regressions = [{"scope": scope, "metric": m,
                    "ratio_of_medians": values["ratio_of_medians"][m],
                    "median_of_paired_ratios": values["median_of_paired_ratios"][m],
                    "slower_rounds": values["paired_regression_rounds"][m]}
                   for scope, values in derived.items() for m in METRICS
                   if any(x is not None and x > 1 for x in (values["ratio_of_medians"][m],
                                                          values["median_of_paired_ratios"][m]))]
    return {"summary_file": summary_file, "raw_file": raw_name, "run_file": run_name,
            "kind": kind, "functional_smoke": smoke, "validation": validation,
            "binary_sha256": dict(zip(names, hashes)), "names": names,
            "source_summary": report, "versions": normalized, "derived": derived,
            "regressions": regressions, "raw_phase_peaks": measured_peaks(records)}


def failure_evidence(evidence):
    result = []
    for name in ("partial-tests.txt", "partial-delete-repro-tests.txt", "partial-safe-delete-tests.txt",
                 "partial-balanced-allocation-diagnostic.txt", "partial-lean-allocation-diagnostic.txt"):
        content = evidence.text(name)
        markers = []
        for line in content.splitlines():
            if line.startswith("{"):
                try:
                    obj = json.loads(line)
                    if "exit_code" in obj:
                        markers.append(obj)
                except json.JSONDecodeError:
                    pass
        require(markers and integer(markers[-1].get("exit_code")), "Missing exit marker: " + name)
        result.append({"file": name, "exit_code": markers[-1]["exit_code"], "content": content})
    require(result[1]["exit_code"] == 1 and result[2]["exit_code"] == 0,
            "Original partial failure/fix evidence missing")
    return result


def command_log(evidence, filename, argv, cwd):
    lines = evidence.text(filename).splitlines()
    require(len(lines) >= 3, "Incomplete command log: " + filename)
    first, last = json.loads(lines[0]), json.loads(lines[-1])
    require(first.get("argv") == argv and first.get("cwd") == cwd and
            finite(first.get("start")) and first["start"] > 0,
            "Command/candidate identity mismatch: " + filename)
    require(integer(last.get("exit_code"), 0) and finite(last.get("wall_s")) and last["wall_s"] > 0,
            "Command did not complete successfully: " + filename)
    body = "\n".join(lines[1:-1])
    require(not re.search(r"(?m)^(?:FAIL(?:\s|$)|\s*--- FAIL:|panic:|fatal error:|WARNING: DATA RACE)", body),
            "Command log contains a failure: " + filename)
    return {"file": filename, "argv": argv, "cwd": cwd, "exit_code": 0,
            "wall_s": last["wall_s"], "validated": True}, body


def go_test_log(evidence, candidate, suffix, arguments, packages):
    result, body = command_log(evidence, candidate + "-" + suffix + ".txt",
                               ["/tmp/seekdb-round3-go/bin/go", "test", *arguments],
                               "/tmp/seekdb-round7/" + candidate + "/minivectordb")
    completed = re.findall(r"(?m)^ok\s+(\S+)\s+([0-9.]+)s$", body)
    require(len(completed) == len(packages) and {p for p, _ in completed} == set(packages),
            "Missing/duplicate successful Go test packages: " + result["file"])
    require(all(float(seconds) >= 0 for _, seconds in completed), "Invalid Go package timing")
    if "-v" in arguments:
        require(body.splitlines().count("PASS") == len(packages), "Missing verbose Go PASS markers")
    result["packages"] = [p for p, _ in completed]
    result["skipped_tests"] = re.findall(r"(?m)^--- SKIP: (\S+) ", body)
    return result, body


def verify_selected_tests(evidence, selected):
    """Require final-candidate logs, independently of any reuse/safe results."""
    packages = ["minivectordb/internal/" + p for p in ("engine", "proto", "sqlparse", "types")]
    full, full_body = go_test_log(evidence, selected, "tests", ["./...", "-count=1", "-v"], packages)
    fallback, fallback_body = go_test_log(evidence, selected, "fallback-tests",
                                          ["./internal/engine", "-count=1", "-v"], packages[:1])
    for body in (full_body, fallback_body):
        expected = ["TestPartialQuantDeleteEnforcesBudget", "TestPartialQuantFreezeAtUnalignedBoundary",
                    "TestPartialQuantExactMutationAndRecovery", "TestPartialQuantDimensionTail",
                    "TestPartialQuantEqualityBudgetFallback", "TestPartialQuantSharedBudgetAndGrowth",
                    "TestPartialQuantTooSmallForOneBlock"]
        if selected == "partial-balanced":
            expected += ["TestPartialBalancedIndependentTruth", "TestPartialBalancedCandidateOrder"]
        passed = re.findall(r"(?m)^--- PASS: (\S+) ", body)
        require(all(passed.count(name) == 1 for name in expected), "Missing selected partial-index regression tests")
    require(all(name in fallback["skipped_tests"] for name in
                ("TestAVXVNNIIntegerDot", "TestAVXVNNIFoldedScalar")) and
            fallback_body.count("AVX-VNNI unavailable") == 2,
            "Fallback log does not show AVX-VNNI unavailable")
    fallback["environment_evidence_limit"] = (
        "run.py fallback supplies MVDB_NO_AVXVNNI=1; this historical log does not snapshot env. "
        "The log verifies both AVX-VNNI kernel tests were unavailable/skipped and engine tests passed.")
    race, _ = go_test_log(evidence, selected, "race",
                          ["-race", "./internal/engine", "./internal/types", "-count=1"],
                          [packages[0], packages[-1]])
    root = "/tmp/seekdb-round7"
    repo = "/mnt/c/Users/27372/seekdb-review-avx-fix"
    binary = root + "/bin/" + selected
    crash, body = command_log(evidence, selected + "-crash.txt",
                              ["python3", repo + "/minivectordb/tools/crash_test.py", "--bin", binary,
                               "--rounds", "30"], "None")
    match = re.fullmatch(r"ok: 30 rounds, ([1-9][0-9]*) committed rows", body)
    require(match is not None, "Missing 30-round crash success")
    crash.update(rounds=30, committed_rows=int(match.group(1)))
    edges, body = command_log(evidence, selected + "-exact-edges.txt",
                              ["python3", repo + "/performance/exact-tbm/exact_edges.py", "--bin-dir",
                               root + "/bin", "--binary", selected, "--assert-correct"], "None")
    records = [json.loads(line) for line in body.splitlines()]
    require(len(records) == 9 and records[0].get("case") == "constant-expansion" and
            records[0].get("dim") == 512 and records[0].get("exact_squared") == 0 and
            records[0].get("expanded_squared", 0) > 0, "Missing exact edge diagnostic")
    minimum = [r for r in records[1:] if r.get("case") == "minimum-bigint"]
    ties = [r for r in records[1:] if r.get("case") == "constant-ties"]
    filters = ("", " WHERE category='a'")
    require(len(minimum) == len(ties) == 4 and
            {(r.get("dim"), r.get("where")) for r in minimum} == {(d, w) for d in (2, 128) for w in filters} and
            {(r.get("no_prefilter"), r.get("where")) for r in ties} == {(n, w) for n in (False, True) for w in filters},
            "Missing/duplicated exact edge combinations")
    for rows, expected in ((minimum, [[-(1 << 63), 0]]), (ties, [[i, 0] for i in range(1, 11)])):
        require(all(r.get("binary") == selected and r.get("got") == r.get("expected") == expected for r in rows),
                "Selected exact edge truth mismatch")
    edges.update(independent_cases=8, diagnostic_cases=1)
    protocol, body = command_log(evidence, selected + "-protocol.txt",
                                 ["python3", repo + "/protocol_check/run.py", "--command",
                                  json.dumps([binary, "--data-dir", "{data_dir}", "--sync", "fdatasync"]),
                                  "--output", root + "/" + selected + "-protocol"], "None")
    expected_cases = ["sql_crud", "sql_comparisons", "sql_ordering", "sql_empty", "sql_primary_key", "sql_unicode",
                      "sql_bigint", "sql_unsupported", "storage_restart", "vector_euclidean", "transaction_commit",
                      "transaction_rollback", "storage_mutations", "storage_multiple_tables", "vector_ties_and_filter",
                      "vector_float32", "vector_index_maintenance", "transaction_failed_insert",
                      "transaction_autocommit_atomic", "transaction_shutdown_rollback"]
    require(body.splitlines() == [case + ": PASS " for case in expected_cases] +
            ["Report: " + root + "/" + selected + "-protocol/result.json"],
            "Missing, duplicated or failed protocol checks")
    protocol["passed_cases"] = expected_cases
    return {"candidate": selected, "validated": True,
            "checks": {"go_all": full, "fallback_engine": fallback, "race": race,
                       "crash_30": crash, "exact_edges": edges, "protocol_20": protocol},
            "identity_limit": "Historical command logs identify candidate source/binary paths, without before/after SHA snapshots; selected stress SHA is separately verified."}


def verify_large_update_smoke(evidence, smoke):
    matches = [x for x in smoke if x["raw_file"] == "large-update-driver-smoke-v2.jsonl"]
    require(len(matches) == 1, "Current large-update v2 functional smoke is missing")
    item = matches[0]
    report = item["source_summary"]
    require(len(set(item["binary_sha256"].values())) == 1 and
            all(report.get(k) is True for k in ("producer_cgroup_verified", "reset_cgroup_verified",
                                                "durable_write_accounting_verified", "no_weighted_E")),
            "Large-update v2 functional smoke lacks same-binary/writeback proof")
    records = evidence.records(item["raw_file"])
    updates = [r for r in records if r.get("phase") == "category_update"]
    require(len(updates) == 10 and all(integer(r.get("w_bytes"), 258048) and
                                      integer(r.get("io_accounting_wait_s"), 0) and
                                      r.get("durable_write_accounting_verified") is True for r in updates),
            "Archived v2 smoke must contain ten verified 258048-byte writes without accounting waits")
    # The fixed write count belongs only to this tiny archived smoke. Actual
    # comparisons use the analyzer's growth-minus-one-page physical-write bound.
    selfcheck = evidence.json("large-update-validator-selfcheck.json")
    cases = selfcheck.get("cases", [])
    expected = {"valid-copy", "wrong-position", "missing-update", "wrong-affected", "wrong-census",
                "wrong-request", "false-peak", "zero-durable-writes", "wrong-growth", "false-write-verification",
                "false-producer-cgroup", "wrong-producer-cgroup", "wrong-reset-cgroup", "wrong-prefix-hash",
                "wrong-restored-hash", "unsynced-reset", "reset-before-prefix", "missing-prefix",
                "false-private-inode", "warm-pages", "oom", "oom-group", "nonzero-process-exit",
                "nonzero-driver-exit", "missing-driver-verification", "changed-driver", "missing-truth-flag",
                "wrong-topk-hash", "missing-success"}
    require(selfcheck.get("validator_selfcheck_passed") is True and
            selfcheck.get("not_database_performance_evidence") is True and
            Path(selfcheck.get("source_smoke", "")).name == item["raw_file"] and
            len(cases) == 29 and {c.get("case") for c in cases} == expected,
            "Current 29-case large-update validator selfcheck is incomplete")
    for case in cases:
        require(case.get("valid_copy_passed") is True if case["case"] == "valid-copy" else
                case.get("rejected") is True and isinstance(case.get("reason"), str) and bool(case["reason"]),
                "Large-update selfcheck did not accept/reject the expected case: " + case["case"])
    return {"summary_file": item["summary_file"], "validation": item["validation"],
            "update_write_bytes": [r["w_bytes"] for r in updates], "selfcheck": selfcheck,
            "not_database_performance_evidence": True}


def capacity_collision_evidence(evidence):
    label = "partial-reuse-capacity"
    run = evidence.json(label + "-run.json")
    stderr = evidence.text(label + ".stderr.txt")
    require(integer(run.get("exit_code"), 2) and not evidence.text(label + ".jsonl").strip() and
            "work-dir must not exist; use a fresh private directory" in stderr,
            "Archived capacity directory-collision evidence does not match the known driver refusal")
    fixed = verify_capacity(evidence, "partial-reuse", "partial-reuse-capacity-fixed")
    return {"classification": "driver_directory_collision_before_database_start; not_database_failure",
            "failed_run_file": label + "-run.json", "failed_stderr_file": label + ".stderr.txt",
            "failed_exit_code": run["exit_code"], "database_records": 0, "fixed_capacity": fixed,
            "not_selected_candidate_validation": True}


def link(name, label=None):
    return f"[{label or name}](results/round7/{name})"


def delta(value):
    return "—" if value is None else f"{(value - 1) * 100:+.2f}%"


def tbm(values):
    return f"{values['t_s']:.6f} / {int(values['b_bytes'])} / {int(values['m_bytes'])}"


def pair_delta(item, scope, metrics=METRICS):
    values = item["derived"][scope]
    return " / ".join(delta(values["ratio_of_medians"][m]) + ";" +
                      delta(values["median_of_paired_ratios"][m]) for m in metrics)


def budget_plan(rows=549997, dim=1024, budget=256 << 20):
    headroom, slots, scratch = budget // 16, 64 * rows, 4 * dim
    block_bytes = 128 * (dim + 32) + 16
    blocks = max(0, (budget - headroom - slots - scratch) // block_bytes)
    q = min(blocks * 128, rows)
    quant = q * (dim + 32) + 16 * blocks + scratch
    return {"live_rows": rows, "dim": dim, "budget_bytes": budget, "headroom_bytes": headroom,
            "slot_metadata_estimate_bytes": slots, "scratch_bytes": scratch,
            "bytes_per_block": block_bytes, "blocks": blocks, "quant_rows": q,
            "coverage_fraction": q / rows, "quantization_plus_scratch_bytes": quant,
            "total_estimate_before_eq_cache_bytes": slots + quant,
            "unallocated_bytes": budget - slots - quant,
            "full_quantization_estimate_bytes": rows * (64 + dim + 32) + scratch + 16 * ((rows + 127) // 128)}


def render(selected, selected_sha, comparisons, smoke, checks, failures, environment, publish_targets=()):
    ordinary = [x for x in comparisons if x["kind"] == "ordinary"]
    selected_ordinary = [x for x in ordinary if selected in [canonical(n) for n in x["names"]]]
    large = [x for x in comparisons if x["kind"] == "large"]
    updates = [x for x in comparisons if x["kind"] == "update"]
    large_updates = [x for x in comparisons if x["kind"] == "large_update"]
    stress, capacity = checks["stress"], checks["capacity"]
    s, plan = stress["summary"], budget_plan()
    base_large = [item for item in large if [canonical(n) for n in item["names"]] == ["base", selected]]
    require(len(base_large) == 1, "Selected candidate must have one direct base large-data comparison")
    headline = base_large[0]
    baseline_total, selected_total = [v["scopes"]["service_total"]["median"] for v in headline["versions"]]
    change = headline["derived"]["service_total"]["ratio_of_medians"]
    regression_highlights = []
    for suffix, title, metrics in (
            ("budget4m-constant", "constant（4 MiB 索引预算）", (("query", "t_s", "查询 T"), ("query", "m_bytes", "查询 M"))),
            ("dim1024", "dim1024", (("query", "t_s", "查询 T"), ("write", "t_s", "写入 T")))):
        matching = [x for x in selected_ordinary if x["summary_file"] == selected + "-" + suffix + "-summary.json" and
                    [canonical(n) for n in x["names"]] == ["base", selected]]
        require(len(matching) == 1, "Missing selected candidate regression comparison: " + suffix)
        item = matching[0]
        regression_highlights.append(link(item["summary_file"], title) + "：" + "、".join(
            label + " " + delta(item["derived"][phase]["ratio_of_medians"][metric])
            for phase, metric, label in metrics))
    reuse_decisions = []
    for item in large_updates:
        if item["names"] != ["partial-balanced", "partial-reuse"]:
            continue
        b, c = item["versions"]
        update_b, update_c = [v["scopes"]["category_update"]["median"] for v in (b, c)]
        total_b, total_c = [v["scopes"]["measured_stage_sum"]["median"] for v in (b, c)]
        reuse_decisions.append(
            f"撤下 reuse 的目标场景证据：{link(item['summary_file'])} 中 balanced→reuse 的 UPDATE "
            f"T {update_b['t_s']:.4f}→{update_c['t_s']:.4f} s，B {update_b['b_bytes']/1e9:.4f}→{update_c['b_bytes']/1e9:.4f} GB；"
            f"全部实测阶段 T {total_b['t_s']:.4f}→{total_c['t_s']:.4f} s，B {total_b['b_bytes']/1e9:.4f}→{total_c['b_bytes']/1e9:.4f} GB。"
            f"总 T 的中位数比/配对比变化为 {pair_delta(item, 'measured_stage_sum', ('t_s',))}；完整 T/B/M 与两种比值见后表。")
    lines = ["# ROUND7：受限内存下的 exact 派生索引", "",
             f"选定候选 `{selected}`；本报告只描述完成的本地结果。基线为 `{BASELINE}`，基线二进制 SHA-256 `{BASE_SHA}`。",
             f"选定二进制 SHA-256 `{selected_sha}`。未正式提测；所有比较保留五轮及负例。",
             f"最终选定版本对 base 的大表服务合计（startup+cold+replay+shutdown；五轮中位数）见 {link(headline['summary_file'])}："
             f"T {baseline_total['t_s']:.6f}→{selected_total['t_s']:.6f} s（{delta(change['t_s'])}），"
             f"B {baseline_total['b_bytes']}→{selected_total['b_bytes']} B（{delta(change['b_bytes'])}），"
             f"M {baseline_total['m_bytes']}→{selected_total['m_bytes']} B（{selected_total['m_bytes'] - baseline_total['m_bytes']:+d} B）。"
             "M 仍在 2 GiB 配置附近，不能把 T/B 收益解释为降低峰值内存。",
             "选定版本自身仍有负例（以下均为候选/基线的中位数之比变化）：" + "；".join(regression_highlights) + "。逐轮配对比及完整 T/B/M 见后表。",
             ("用户已授权本轮完成后，将分支 `" + environment["branch"] + "` 推送到 " +
              "、".join(publish_targets) + "。本报告在提交和推送前生成，远端发布结果另以 Git 提交记录核实。")
             if publish_targets else "本轮未推送。", "",
             "## 实现与取舍", "",
             ("最终保留 partial-balanced：仅包含预算内 partial 前缀及 balanced 分工；reuse、lean 均不进入生产补丁。"
              if selected == "partial-balanced" else f"本次报告参数选择 `{selected}`；候选取舍须以本次指定版本和全部对照为准。"),
             "partial 将量化前缀按 128 行块限制在派生索引预算内，尾部始终走 exact 距离；分块误差上界只用于保守排除，不能改变排序真值。",
             "预算同时计入数组 cap、slot map 的分配提示/历史高水位、量化数据和边界；map 成本仍为估算。删除后 free-list 扩容也必须重新检查预算，必要时禁用派生索引并回退 authoritative rows。",
             "balanced 将已编码前缀和未编码尾部分别分给 worker，同一堆先扫 coded 再扫 tail；候选 slot 顺序不能证明递增时沿用原分区。",
             "已撤下的 reuse 候选曾在已有 partial 索引的存活已编码 slot、同一完整映射内确认新旧 float32 向量字节相同后复用 codes/常数/保守界；新行、空槽复用、向量变化或映射不足仍重新编码。",
             "bytes.Equal 额外读取旧向量 payload，在受限页缓存下可能增加 I/O。目标大数据更新的全部实测阶段未取得综合收益，不能仅凭小数据约 36% 的更新收益保留 reuse；这里未把具体退步全部归因于字节比较。",
             *reuse_decisions,
             "lean 将 worker 分支移出闭包，分配诊断确实少 96–103 B 和 1 alloc/query；但两组直接对照的查询时间中位数及本地 E 退步，因此未保留。1024 维的配对中位数与中位数之比方向不同，必须同时看。",
             f"原 partial 全测试曾通过，但后补删除预算复现失败：5272 > 5264 B、2568 > 2560 B；safe 对应测试通过。证据：{link('partial-delete-repro-tests.txt')}、{link('partial-safe-delete-tests.txt')}。",
             f"partial-safe 的完整 Go 测试也已通过（{link(checks['superseded_safe']['go_all']['file'])}）；它是被 balanced 分工方案替代的正确性通过版本，不是因正确性失败被淘汰。最终候选必须通过自己的全部验证，不以 safe/reuse 的记录替代。", "",
             "## 最终候选的功能与恢复门禁", "",
             "报告重新核对以下日志的候选路径、实际命令、exit 0 和各自预期成功内容；缺少任何一项均拒绝生成。", "",
             "| 检查 | 已验证范围 | 原始日志 |", "|---|---|---|"]
    test_checks = checks["selected_tests"]["checks"]
    for key, label, detail in (
            ("go_all", "完整 Go 测试", "engine / proto / sqlparse / types 全部通过；包含 partial/balanced 回归"),
            ("fallback_engine", "禁用 AVX-VNNI 的 engine 测试", "engine 通过；两个 AVX-VNNI 专项明确 unavailable/SKIP"),
            ("race", "race", "engine 与 types 均通过"),
            ("crash_30", "崩溃测试", "30 轮通过；" + str(test_checks["crash_30"]["committed_rows"]) + " 个已提交行"),
            ("exact_edges", "exact 边界", "8 个独立边界组合与 1 个浮点展开诊断"),
            ("protocol_20", "协议/事务/存储", "20 项逐项 PASS，包含重启、更新、提交与回滚")):
        lines.append(f"| {label} | {detail} | {link(test_checks[key]['file'])} |")
    lines += ["", "硬件不支持的 AVX-512 路径及显式诊断测试仍按原测试套件跳过，完整 skip 名称保存在 summary.json。"
              "fallback 启动器 run.py 设置 MVDB_NO_AVXVNNI=1；历史日志没有环境变量快照，日志自身只能直接证明 AVX-VNNI 专项不可用且 engine 测试通过。",
              "这些历史命令日志记录候选源码/二进制路径，未记录运行前后 SHA；不能把它们描述成各自具有哈希锁定。独立新 stress 另行验证选定二进制 SHA。", "",
             "## 环境、计量与比值", "",
             "WSL ArchBench / Intel Core Ultra 5 125H；Go 1.22.2、linux/amd64、GOAMD64=v1；2 CPU、2 GiB cgroup、无 swap，exact，fdatasync。环境身份见 " + link("environment.json") + "。",
             "T 单位秒，B 为 cgroup 实际读写字节，M 为 cgroup 内核峰值字节（包含文件缓存）。交错对照表为五轮中位数，Mmax 为五轮最大值；stress/capacity 表为单次实测阶段值，均不截断到配置上限。",
             "变化列每项使用“中位数之比变化；逐轮候选/对照比的中位数变化”，正值代表该成本增加。零基准的比值为 —，原始零值仍保留。不同组不混合或重新加权。",
             "普通 E 使用首三轮对照固定中位数、T/B/M=0.6/0.2/0.2、write/query=1/3、2/3，平滑量 1 ms/1 MiB/1 MiB；仅是同组本地诊断，不是平台分数。",
             "普通查询 T 排除装载/首条预热，B/M 却覆盖整个进程；因此其查询时间不能单独代表首次派生索引成本。large 的 service_total 才包含启动、cold、replay、退出，T/B相加、M取最大。", "",
             "## 选定版本参与的全部普通组", "",
             "每行保留原始对照 → 候选方向，包含选定版本在任一侧的组；完整轮次及两种全部 T/B/M 比值见 " + link("all-comparisons.json") + "。", "",
             "| 组 / 阶段 | 对照 T/B/M | 候选 T/B/M | T变化：中位比；配对比 | E 对照→候选 |",
             "|---|---:|---:|---:|---:|"]
    for item in selected_ordinary:
        b, c = item["versions"]
        for phase in ("write", "query"):
            lines.append(f"| {link(item['summary_file'], Path(item['raw_file']).stem)} / {phase}（{'→'.join(item['names'])}） | "
                         f"{tbm(b['scopes'][phase]['median'])} | {tbm(c['scopes'][phase]['median'])} | "
                         f"{pair_delta(item, phase, ('t_s',))} | {b['local_E']['median']:.6f}→{c['local_E']['median']:.6f} |")
    lines += ["", "## 全部普通候选与负例", "",
              "没有因退步删除组或轮次。下表保留原对照名称；非 base 直接对照不能冒充基线对照。各指标退步轮编号完整保存在 JSON。", "",
              "| 组：对照→候选 | write T 中位比；配对比 | query T 中位比；配对比 | query B 中位比；配对比 | query M 中位比；配对比 | E 对照→候选 |",
              "|---|---:|---:|---:|---:|---:|"]
    for item in ordinary:
        b, c = item["versions"]
        lines.append(f"| {link(item['summary_file'], Path(item['raw_file']).stem)}：{'→'.join(item['names'])} | "
                     f"{pair_delta(item, 'write', ('t_s',))} | {pair_delta(item, 'query', ('t_s',))} | "
                     f"{pair_delta(item, 'query', ('b_bytes',))} | {pair_delta(item, 'query', ('m_bytes',))} | "
                     f"{b['local_E']['median']:.6f}→{c['local_E']['median']:.6f} |")
    lines += ["", "## 全部大表交错组", "",
              "fixture 最终有 549,997 行、1024 维、2,252,787,712 B 存活向量；所有存活类别均为 c9，WHERE c9 覆盖全表，不是 10% 选择率。",
              "每次启动和 cold 批前验证私有日志冷页；cold 只指批开始时，批内可复用。replay 沿用同进程、派生状态和剩余页缓存，不意味着超过 2 GiB 的向量全部驻留。", "",
              "| 组 / 阶段 | 对照→候选 T/B/M | ΔT / ΔB / ΔM（各：中位比；配对比） | Mmax 对照→候选 / 超配置字节 |",
              "|---|---:|---:|---:|"]
    for item in large:
        b, c = item["versions"]
        for phase in ("cold", "replay", "service_total"):
            d = item["derived"][phase]
            peaks = "→".join(str(d["peak_max_bytes"][n]) for n in item["names"])
            over = "→".join(str(d["peak_above_limit_bytes"][n]) for n in item["names"])
            lines.append(f"| {link(item['summary_file'], Path(item['raw_file']).stem)} / {phase} | "
                         f"{tbm(b['scopes'][phase]['median'])} → {tbm(c['scopes'][phase]['median'])} | "
                         f"{pair_delta(item, phase)} | {peaks} / {over} |")
    lines += ["", "large 的 startup/shutdown 及所有原始轮次同样保存在 JSON；每组 120 条独立响应检查、6 个唯一 SQL oracle，不能把重复响应数当成 120 个独立查询案例。", "",
              "## 建索引后的更新与恢复", "",
              "各组均为五轮交错、10,000×1024、4 MiB 索引预算。先执行 index_query，再做 category/front/tail 更新；ACK 包含 fdatasync 与同步索引维护。",
              "每组 180 条 TopK 响应、12 个状态/SQL oracle、20 次 census、200,000 行检查。全部 13 个实测阶段与两个合计保存在 JSON；下表保留主要阶段及全部三组对照。", "",
              "| 组 / 阶段 | 对照→候选 T/B/M | ΔT / ΔB / ΔM（各：中位比；配对比） |",
              "|---|---:|---:|"]
    for item in updates:
        b, c = item["versions"]
        for phase in ("index_query", "updated_query", "recovery", "recovered_query",
                      "indexed_update_ack_total", "measured_stage_sum"):
            lines.append(f"| {link(item['summary_file'], Path(item['raw_file']).stem)} / {phase} | "
                         f"{tbm(b['scopes'][phase]['median'])} → {tbm(c['scopes'][phase]['median'])} | "
                         f"{pair_delta(item, phase)} |")
    lines += ["", "更新组不是 >2GiB 容量测试。恢复前私有日志 sync/驱逐并验证冷页；初始/更新真值独立生成，恢复查询复用更新真值。measured_stage_sum 为 13 阶段 T/B 和、M 最大值，不构造加权 E。", "",
              "## 大数据索引更新直接对照", "",
              "550,000×1024 初始行、549,997 存活行，2 GiB / 无 swap。每轮 pre_update 建索引，再将全部 c9 改为 c8，随后 post_update 与 census；比较受限页缓存下复用量化与额外旧 payload 读取的综合成本。",
              "每组五轮 AB/BA，120 条 TopK 响应、12 个状态/SQL oracle、10 次 census / 5,499,970 行检查。全部七个 scope 如下，既保留更新 ACK，也保留首次构建、查询及服务合计；不计算跨阶段 E。", "",
              "| 组 / 阶段 | 对照→候选 T/B/M | ΔT / ΔB / ΔM（各：中位比；配对比） | Mmax 对照→候选 / 超配置字节 |",
              "|---|---:|---:|---:|"]
    for item in large_updates:
        b, c = item["versions"]
        for phase in ("startup", "pre_update", "category_update", "post_update", "census", "shutdown", "measured_stage_sum"):
            d = item["derived"][phase]
            peaks = "→".join(str(d["peak_max_bytes"][n]) for n in item["names"])
            over = "→".join(str(d["peak_above_limit_bytes"][n]) for n in item["names"])
            lines.append(f"| {link(item['summary_file'], Path(item['raw_file']).stem)} / {phase} | "
                         f"{tbm(b['scopes'][phase]['median'])} → {tbm(c['scopes'][phase]['median'])} | "
                         f"{pair_delta(item, phase)} | {peaks} / {over} |")
    lines += ["", "v2 将私有文件的首次 streaming copy+Sync 和后续 truncate+Sync 放在数据库同一 cgroup，校验 producer/reset/实测阶段归属；UPDATE 必须实际追加至少存活向量字节数，物理写入至少覆盖 log growth 减一页（最低 1 B），无等待落账。",
              "每轮数据库退出后核对原始 prefix / 全日志哈希，再 truncate+Sync 复原私有日志并驱逐缓存；这些复位、哈希和外部真值成本排除在阶段 T/B/M 外，并保留原始复位证明。UPDATE T 包含 ACK 后长度检查，不隐藏写回等待；该对照不替代 fresh-write/crash stress。", "",
              "## 选定版本的重新写入与崩溃恢复", "",
              f"{link(stress['summary_file'])} 与原始日志通过门禁：新写入 550,000×1024，初始向量 2,252,800,000 B > 2 GiB；最终存活 549,997 行。",
              f"实际校验 {s['independent_topk_queries']} 条 TopK（6×12+1）、2 次完整 census / {s['census_rows_checked']:,} 行、{s['transaction_point_rows_checked']} 次事务点行检查；{s['sigkill_events']} 次 SIGKILL，包含已提交后、未提交尾部、恢复未 ready、bulk commit 后。",
              f"未提交尾部 {s['uncommitted_tail_bytes']} B 已验证丢弃。全流程峰值 {s['whole_workflow_peak_bytes']} B，超过配置 {s['peak_above_configured_limit_bytes']} B；OOM/oom_kill 为零。", "",
              *([f"本次成功压力测试采用独立重跑标签 `{stress['label']}`。此前中断尝试的原始工件（包括空或不完整 JSON）按原字节保存在 "
                 + link("failed-attempts/", "failed-attempts 归档") + "，每个文件的长度及 SHA-256 保存在 summary.json；"
                 + "环境故障与恢复记录见 " + link(checks["failed_stress_attempts"]["environment_incident_file"]) + "。",
                 "恢复重跑的存储环境与原交错性能对照不同；该次重跑仅用于容量、exact、事务与崩溃恢复正确性检查，其阶段 T/B/M 不与原对照混比。", ""]
                if checks.get("failed_stress_attempts") else []),
              "| 实测阶段（含重复 census） | T / B / M | M 超配置字节 |",
              "|---|---:|---:|"]
    for phase in s["phases"]:
        lines.append(f"| {phase['phase']} | {tbm(phase)} | {max(0, phase['m_bytes'] - MEMORY_LIMIT)} |")
    lines += ["", f"旧日志容量检查另行通过：{link(capacity['summary_file'])}，point/order/census 各 1 条 SQL，共 3 条 SQL / 550,008 行；census 为 549,997 存活行。不是 6 条 SQL，validation 记录不重复计数。",
              "该 capacity 元数据只有二进制路径，没有运行前后 SHA；不能宣称该旧驱动自身提供哈希锁定。新 stress 的候选 SHA 与本次保存的二进制 manifest 一致。",
              "此前 partial-reuse-capacity 的私有目录与大表对照撞名，驱动在启动数据库前以 exit 2 拒绝，JSONL 为空；这是运行脚本命名问题，不是数据库查询/恢复失败。"
              + link("partial-reuse-capacity-run.json") + "、" + link("partial-reuse-capacity.stderr.txt") +
              " 均保留；独立目录重跑 " + link("partial-reuse-capacity-fixed-summary.json") +
              " 已通过。最终 balanced 使用自己的默认 capacity 标签，不用 reuse 的结果替代最终验证。", "",
              "| capacity 阶段 | T / B / M | M 超配置字节 |", "|---|---:|---:|"]
    for phase in capacity["summary"]["phases"]:
        lines.append(f"| {phase['phase']} | {tbm(phase)} | {max(0, phase['m_bytes'] - MEMORY_LIMIT)} |")
    lines += ["", "## 内存定位与预算", "",
              f"按 N={plan['live_rows']}、D={plan['dim']}、预算 {plan['budget_bytes']} B：headroom={plan['headroom_bytes']}，slot估计={plan['slot_metadata_estimate_bytes']}，scratch={plan['scratch_bytes']}；每 128 行块 {plan['bytes_per_block']} B，可容纳 {plan['blocks']} 块。",
              f"规划 Q={plan['quant_rows']} 行（{plan['coverage_fraction'] * 100:.8f}%），量化+scratch={plan['quantization_plus_scratch_bytes']} B，eq cache 前总估计={plan['total_estimate_before_eq_cache_bytes']} B，余量={plan['unallocated_bytes']} B；全量量化估计={plan['full_quantization_estimate_bytes']} B。",
              "B/16 是未分配余量，不是受保护 reservation 或 RSS 硬保证。删除、map 高水位和数组容量检查限制派生状态，不能承诺整个进程峰值不超过 2 GiB。",
              "partial-safe 已完成对照中，查询 anon 独立最大值约 58 MB→275 MB，量级接近约 216 MB 量化状态；启动已填满文件缓存，anon 增长挤占 file，所以 B 下降而 M 仍接近上限。不能将 anon 当成 Go live heap，未做 heap profile。",
              link("partial-memory-analysis.md") + " 仅对应 partial-safe-large，绝不是最终 reuse/balanced 压力结果。50 ms 同记录字段也由顺序读取组成，非原子；独立 anon/file 最大值不可相加，file_mapped 是 file 子集，RSS 与映射缓存重叠。",
              "内核 M 与采样 memory.current 的超限值分别保存在 all-comparisons/summary JSON，不把采样拼成 memory.peak 瞬间，也不把数值钳制到配置上限；缺少采样时记 null，不能记作零超限。", "",
              "## 功能 smoke 与证据边界", "",
              "以下只验证驱动/校验器功能；相同二进制或明确 smoke 数据不进入任何优化结论。早期 smoke 依赖当时摘要，不追溯声称通过后来加固的身份校验。", "",
              "| 功能验证 | 类型 | SHA 是否相同 | 原摘要真值数 | 校验范围 |", "|---|---|---|---:|---|"]
    for item in smoke:
        report = item["source_summary"]
        count = report.get("independent_response_checks_total", report.get("topk_response_checks_total", "见摘要"))
        scope = ("历史：写回归属无效" if item["raw_file"] == "large-update-driver-smoke.jsonl" else
                 "当前加固功能验证" if "current_hardened" in item["validation"] else "历史功能验证")
        lines.append(f"| {link(item['summary_file'])} | {item['kind']} | "
                     f"{len(set(item['binary_sha256'].values())) == 1} | {count} | {scope} |")
    lines += ["", "v1 update smoke 对应 " + link("update_run_initial.py") + " / " + link("update_analyze_initial.py") +
              "；只有 v2 smoke 与 " + link("update-validator-selfcheck-v2.json") +
              " 的 18 个案例（1 个有效副本 + 17 个损坏拒绝，含一致伪造 TopK）对应当前加固流程。它们不是候选性能/正确性测试。",
              "large 初期 wrapper 保存在 " + link("large_run_initial.py") + "；后来增加 --baseline，不表示 driver 源码或二进制变更。",
              "large_update v1 smoke 的写回 cgroup 归属错误，不能用其成功摘要证明物理写入计量正确；初始 driver/analyzer 保存在 "
              + link("large_update_initial.go") + " / " + link("large_update_analyze_initial.py") + "。",
              "large_update v2 smoke 已经当前分析器重验：copy/reset 同 cgroup、growth 对应物理写入下界；10 次 UPDATE 均写入 258,048 B。"
              + link("large-update-validator-selfcheck.json") +
              " 的 29 项全部通过（1 个有效副本 + 28 个损坏记录拒绝）；这是功能/校验器自检，不是 28 次数据库故障注入或候选性能证据。",
              "census 对比主键、类别和独立再生的范数，未投影比较每个向量坐标。恢复中 SIGKILL 证明发生在 ready 前，尚无引擎内部恢复进度 hook。",
              "所有阶段均排除外部 oracle 生成/解码/哈希/显式驱逐等准备工作；stress 写入 T 排除生成器；没有每请求 deadline，容量通过不等同正式时限通过。普通、large、update、stress 的计量范围不能混算。",
              "用户提供的正式 4100 分缺对应提交 SHA、平台 CPU 和分项 T/B/M。现有本地 E 和容量结果无法可靠换算成 5500，也不能据此上调此前粗估；本轮未消耗正式提测次数。",
              f"可机读证据：{link('summary.json')}、{link('all-comparisons.json')}。所有阶段、逐轮配对比、失败测试、超配置峰值与输入 SHA-256 均保留。", ""]
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--results", type=Path, default=Path("/tmp/seekdb-round7/results"))
    parser.add_argument("--output", "--output-dir", dest="output", type=Path, required=True)
    parser.add_argument("--selected", default="partial-balanced")
    parser.add_argument("--stress-label", help="Successful stress run label; failed attempts remain archived")
    parser.add_argument("--capacity-label", help="Successful capacity run label; failed attempts remain archived")
    parser.add_argument("--publish-target", action="append", default=[], help="User-authorized GitHub target after validation; this script never pushes")
    args = parser.parse_args()
    require(re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", args.selected), "Invalid selected candidate")
    require(args.stress_label is None or re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", args.stress_label), "Invalid stress label")
    require(args.capacity_label is None or re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", args.capacity_label), "Invalid capacity label")
    require(all(re.fullmatch(r"https://github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", value)
                for value in args.publish_target), "Invalid publication target")
    require(not args.output.exists(), "Output directory must be new; refusing to overwrite")
    require(args.results.is_dir(), "Missing results directory")
    initial_files = sorted(p.name for p in args.results.iterdir() if p.is_file())
    evidence = Evidence(args.results)
    environment = evidence.json("environment.json")
    require(environment.get("baseline_commit") == BASELINE, "Wrong formal baseline commit")
    require(integer(environment.get("official_submissions"), 0) and environment.get("pushed") is False,
            "Report cannot claim no push/submission from this environment")
    manifests = {}
    for path in sorted(args.results.glob("*-binary-sha256.json")):
        for name, digest in evidence.json(path.name).items():
            require(re.fullmatch(r"[0-9a-f]{64}", digest), "Invalid binary SHA: " + path.name)
            require(manifests.setdefault(name, digest) == digest, "Conflicting binary SHA: " + name)
    require(manifests.get("base") == BASE_SHA, "Formal baseline binary SHA mismatch")
    require(args.selected in manifests, "Selected binary manifest is missing")
    selected_sha = manifests[args.selected]
    # Mandatory gates run before creating an output directory or a final report.
    checks = {"stress": verify_stress(evidence, args.selected, selected_sha, args.stress_label),
              "capacity": verify_capacity(evidence, args.selected, args.capacity_label)}
    checks["selected_tests"] = verify_selected_tests(evidence, args.selected)
    safe_tests, _ = go_test_log(evidence, "partial-safe", "tests", ["./...", "-count=1", "-v"],
                               ["minivectordb/internal/" + p for p in ("engine", "proto", "sqlparse", "types")])
    checks["superseded_safe"] = {"candidate": "partial-safe", "go_all": safe_tests,
                                 "selection_status": "correctness_passed_superseded_by_balanced",
                                 "not_selected_candidate_validation": True}
    checks["failed_stress_attempts"] = failed_attempts_archive(evidence)
    validators = {"ordinary": analyzer("analyze.py"), "large": analyzer("large/analyze.py"),
                  "update": analyzer("update/analyze.py"), "large_update": analyzer("large_update/analyze.py")}
    comparisons, smoke, auxiliary = [], [], []
    represented = set()
    for path in sorted(args.results.glob("*-summary.json")):
        loaded = evidence.json(path.name)
        for report in loaded if isinstance(loaded, list) else [loaded]:
            require(isinstance(report, dict), "Unsupported summary entry: " + path.name)
            kind = comparison_kind(report)
            if kind:
                item = normalize_comparison(evidence, path.name, report, kind, manifests, validators)
                require(item["raw_file"] not in represented, "Comparison was summarized twice: " + item["raw_file"])
                represented.add(item["raw_file"])
                (smoke if item["functional_smoke"] else comparisons).append(item)
            else:
                auxiliary.append({"file": path.name, "summary": report})
    # A still-running or failed comparison must not silently disappear.
    for path in sorted(args.results.glob("*.jsonl")):
        if path.name in represented:
            continue
        with path.open(encoding="utf-8-sig") as stream:
            first = next((json.loads(line) for line in stream if line.strip()), {})
        is_comparison = (first.get("mode") in ("round7_large_topk_interleaved", "round7_indexed_update_interleaved",
                                                "round7_large_update_interleaved") or
                         first.get("phase") == "write" and "round" in first and "name" in first)
        require(not is_comparison, "Unfinished/unreported comparison retained; finish or explicitly audit it: " + path.name)
    require(all(any(item["kind"] == kind and args.selected in [canonical(n) for n in item["names"]]
                    for item in comparisons) for kind in ("ordinary", "large", "update")),
            "Selected candidate lacks ordinary, large or indexed-update comparison evidence")
    require(any(item["kind"] == "large_update" and args.selected in [canonical(n) for n in item["names"]]
                for item in comparisons), "Selected candidate lacks the required large-data update evidence")
    failures = failure_evidence(evidence)
    evidence.text("partial-memory-analysis.md")
    for name in ("update_run_initial.py", "update_analyze_initial.py", "large_run_initial.py",
                 "large_update_initial.go", "large_update_analyze_initial.py"):
        evidence.text(name)
    selfcheck = evidence.json("update-validator-selfcheck-v2.json")
    cases = selfcheck.get("cases", [])
    require(selfcheck.get("validator_selfcheck_passed") is True and
            selfcheck.get("not_database_performance_evidence") is True and len(cases) == 18 and
            len({c.get("case") for c in cases}) == 18 and
            sum(c.get("valid_copy_passed") is True for c in cases) == 1 and
            sum(c.get("rejected") is True for c in cases) == 17 and
            any(c.get("case") == "consistent-forged-topk" and c.get("rejected") is True for c in cases),
            "Current 18-case update validator selfcheck is incomplete")
    require(any(x["raw_file"] == "update-driver-smoke-v2.jsonl" for x in smoke),
            "Current hardened v2 functional smoke is missing")
    checks["update_validator_selfcheck"] = selfcheck
    checks["large_update_v2_functional_validation"] = verify_large_update_smoke(evidence, smoke)
    checks["capacity_directory_collision"] = capacity_collision_evidence(evidence)
    all_data = {"schema": "round7-report-v1", "baseline_commit": BASELINE, "baseline_binary_sha256": BASE_SHA,
                "selected": args.selected, "selected_binary_sha256": selected_sha,
                "comparisons": comparisons, "functional_smoke_only": smoke,
                "auxiliary_summaries": auxiliary, "failed_candidate_and_allocation_evidence": failures,
                "no_official_score_inference": True}
    summary = {"schema": "round7-report-v1", "created_utc": datetime.now(timezone.utc).isoformat(),
               "selected": args.selected, "selected_binary_sha256": selected_sha,
               "selection_decision": {"retained": args.selected,
                                      "retained_features": ["partial prefix with budget fixes", "balanced worker partition"]
                                      if args.selected == "partial-balanced" else None,
                                      "rejected_for_production": [n for n in ("partial-reuse", "partial-lean") if n != args.selected],
                                      "reuse_rejection_reason": "No combined T/B/M gain in the target large-data indexed-update workload; small-data update gains are insufficient."},
               "baseline_commit": BASELINE, "baseline_binary_sha256": BASE_SHA,
               "environment": environment, "validation": checks, "partial_budget_plan": budget_plan(),
               "comparison_counts": {kind: sum(x["kind"] == kind for x in comparisons)
                                     for kind in ("ordinary", "large", "update", "large_update")},
               "functional_smoke_count": len(smoke),
               "selected_comparisons": [x["summary_file"] for x in comparisons
                                        if args.selected in [canonical(n) for n in x["names"]]],
               "all_comparison_regressions": [{"file": x["summary_file"], "regressions": x["regressions"]}
                                              for x in comparisons],
               "input_sha256": dict(sorted(evidence.files.items())), "official_submissions": 0,
               "publication": {"branch": environment["branch"], "targets": args.publish_target,
                               "state_at_report_generation": "authorized_pending_push" if args.publish_target else "not_requested"},
               "official_score_estimate": None,
               "score_limit": "4100 has no verified commit SHA/platform CPU/TBM; do not infer 5500."}
    markdown = render(args.selected, selected_sha, comparisons, smoke, checks, failures, environment, args.publish_target)
    outputs = {"ROUND7.md": markdown,
               "all-comparisons.json": json.dumps(all_data, indent=2, ensure_ascii=False, allow_nan=False) + "\n",
               "summary.json": json.dumps(summary, indent=2, ensure_ascii=False, allow_nan=False) + "\n"}
    evidence.unchanged()
    require(failed_attempts_archive(evidence) == checks["failed_stress_attempts"],
            "Failed-attempts archive changed during report generation")
    require(sorted(p.name for p in args.results.iterdir() if p.is_file()) == initial_files,
            "Results directory changed during report generation; wait until all work finishes")
    args.output.mkdir(parents=True, exist_ok=False)
    for name, content in outputs.items():
        with (args.output / name).open("x", encoding="utf-8", newline="\n") as stream:
            stream.write(content)
    print(json.dumps({"output": str(args.output.resolve()), "selected": args.selected,
                      "comparisons": summary["comparison_counts"], "smoke": len(smoke),
                      "report_lines": len(markdown.splitlines()), "validation": "PASS"}, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, TypeError, AssertionError, OSError) as error:
        print("ROUND7 report refused: " + str(error), file=sys.stderr)
        raise SystemExit(1)
