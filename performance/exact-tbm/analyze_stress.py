"""Summarize raw stress telemetry; no invented weights for update/recovery."""
import argparse
import json

parser = argparse.ArgumentParser()
parser.add_argument("paths", nargs="+")
args = parser.parse_args()
reports = []
for path in args.paths:
    with open(path, encoding="utf-8-sig") as stream:
        rows = [json.loads(line) for line in stream if line.strip()]
    config = next(r for r in rows if r["phase"] == "config")
    report = {"source": path, "config": config, "phases": []}
    for r in rows:
        if "t_s" in r:
            phase = {k: r[k] for k in ("phase", "t_s", "wall_s", "b_bytes", "r_bytes", "w_bytes", "m_bytes",
                                       "max_sampled_anon", "max_sampled_file", "max_sampled_rss", "memory_events")}
            phase.update({k: r[k] for k in ("name", "round", "position", "reference_checked", "reference_rows_checked", "reference_bad", "killed_before_ready", "log_bytes", "affected_rows") if k in r})
            report["phases"].append(phase)
        if r["phase"] in ("basic_workflow_success", "success", "failure", "process_read_failure", "recovery_comparison_success"):
            report[r["phase"]] = r
    reports.append(report)
print(json.dumps(reports, indent=2))
