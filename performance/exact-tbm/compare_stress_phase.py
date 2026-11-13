"""A phase-only T/B/M comparison, separate from the official write/query E."""
import argparse
import collections
import json
import pathlib
import statistics

p = argparse.ArgumentParser()
p.add_argument("paths", nargs="+")
p.add_argument("--phase", required=True, help="one phase, or comma-separated phases to total per run")
p.add_argument("--baseline", default="round2")
a = p.parse_args()
groups = collections.defaultdict(list)
phases = set(a.phase.split(","))
for path in a.paths:
    with open(path, encoding="utf-8-sig") as stream:
        records = [json.loads(line) for line in stream if line.strip()]
    config = records[0]
    paired = collections.defaultdict(dict)
    for r in records:
        if r.get("reference_bad", 0):
            raise ValueError("truth mismatch")
        if r["phase"] in phases:
            if r["memory_events"]["oom_kill"]:
                raise ValueError("OOM run cannot be scored")
            name = r.get("name", pathlib.Path(config.get("binary", "")).name)
            key = (name, r.get("round", 0))
            if r["phase"] in paired[key]:
                raise ValueError("duplicate phase in run")
            paired[key][r["phase"]] = r
    for (name, _), pair in paired.items():
        if set(pair) != phases:
            raise ValueError("incomplete run")
        groups[name].append({"t_s": sum(r["t_s"] for r in pair.values()),
                             "b_bytes": sum(r["b_bytes"] for r in pair.values()),
                             "m_bytes": max(r["m_bytes"] for r in pair.values())})
metrics = ("t_s", "b_bytes", "m_bytes")
base = {k: statistics.median(r[k] for r in groups[a.baseline][:3]) for k in metrics}
out = []
for name, runs in sorted(groups.items()):
    scores = []
    for r in runs:
        score = 0
        for metric, weight, eps in (("t_s", .6, .001), ("b_bytes", .2, 1048576), ("m_bytes", .2, 1048576)):
            ratio = (base[metric] + eps) / (r[metric] + eps)
            score += weight * ratio / (1 + ratio)
        scores.append(score)
    out.append({"name": name, "phase": a.phase, "rounds": len(runs),
                "median": {k: statistics.median(r[k] for r in runs) for k in metrics},
                "phase_E": {"baseline": a.baseline, "median": statistics.median(scores), "runs": scores},
                "note": "Phase-only local comparison; not official overall E or a platform score."})
print(json.dumps(out, indent=2))
