"""Compare interleaved runs without combining workloads or random seeds."""
import argparse
import collections
import json
import statistics

parser = argparse.ArgumentParser()
parser.add_argument("paths", nargs="+")
parser.add_argument("--baseline", default="round2")
args = parser.parse_args()
groups = collections.defaultdict(list)
seen = set()
for path in args.paths:
    with open(path, encoding="utf-8-sig") as stream:
        for line in stream:
            r = json.loads(line)
            if r["reference_bad"]:
                raise ValueError("independent truth mismatch")
            key = tuple(r[k] for k in ("rows", "dim", "queries", "seed", "data"))
            run_key = key + (r["name"], r["phase"], r["round"])
            if run_key in seen:
                raise ValueError("duplicate run: analyze separate experiments separately")
            seen.add(run_key)
            groups[key].append(r)

reports = []
for key, runs in sorted(groups.items()):
    baseline = {}
    for phase in ("write", "query"):
        first = sorted((r for r in runs if r["name"] == args.baseline and r["phase"] == phase),
                       key=lambda r: r["round"])[:3]
        baseline[phase] = {m: statistics.median(r[m] for r in first)
                           for m in ("t_s", "b_bytes", "m_bytes")}
    for name in sorted({r["name"] for r in runs}):
        report = dict(zip(("rows", "dim", "queries", "seed", "data"), key))
        report["name"] = name
        paired = collections.defaultdict(dict)
        for phase in ("write", "query"):
            phase_runs = [r for r in runs if r["name"] == name and r["phase"] == phase]
            report[phase] = {m: statistics.median(r[m] for r in phase_runs)
                             for m in ("t_s", "b_bytes", "m_bytes", "warmup_s")}
            report[phase]["rounds"] = len(phase_runs)
            if phase == "query":
                report["checked_queries"] = sum(r["reference_checked"] for r in phase_runs)
                report["response_hashes"] = sorted({r["response_hash"] for r in phase_runs})
            for r in phase_runs:
                paired[r["round"]][phase] = r
        scores = []
        for pair in paired.values():
            score = 0
            for phase, pw in (("write", 1 / 3), ("query", 2 / 3)):
                for metric, weight, eps in (("t_s", .6, .001), ("b_bytes", .2, 1048576), ("m_bytes", .2, 1048576)):
                    ratio = (baseline[phase][metric] + eps) / (pair[phase][metric] + eps)
                    score += pw * weight * ratio / (1 + ratio)
            scores.append(score)
        report["local_E"] = {"baseline": args.baseline, "median": statistics.median(scores), "rounds": scores}
        reports.append(report)
print(json.dumps(reports, indent=2))
