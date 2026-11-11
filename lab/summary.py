#!/usr/bin/env python3
"""Median T/B/M per commit and phase, plus the task's E formula with the
first commit as baseline (only meaningful as a ratio; the platform's
baseline and scale are not published)."""
import json
import statistics as s
import sys

rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
tags = []
for r in rows:
    if r["tag"] not in tags:
        tags.append(r["tag"])
med = {}
print("| commit | rounds | write T s | write B MiB | write M MiB | query T s | query B MiB | query M MiB | recall unf/fil | order/dist bad | E vs first |")
print("|---|---|---|---|---|---|---|---|---|---|---|")
f = lambda r: r / (1 + r)  # noqa: E731
for t in tags:
    rs = [r for r in rows if r["tag"] == t]
    m = {ph: {k: s.median(r[ph][k] for r in rs) for k in ("T_s", "B_mib", "M_mib")} for ph in ("write", "query")}
    med[t] = m
    b = med[tags[0]]

    def e(ph):
        rt = (b[ph]["T_s"] + 0.001) / (m[ph]["T_s"] + 0.001)
        rb = (b[ph]["B_mib"] + 1) / (m[ph]["B_mib"] + 1)
        rm = (b[ph]["M_mib"] + 1) / (m[ph]["M_mib"] + 1)
        return 0.6 * f(rt) + 0.2 * f(rb) + 0.2 * f(rm)
    E = (e("write") + 2 * e("query")) / 3
    q = rs[-1]["query"]
    print(f"| {t[:7]} | {len(rs)} | {m['write']['T_s']:.3f} | {m['write']['B_mib']:.1f} | {m['write']['M_mib']:.1f} | "
          f"{m['query']['T_s']:.3f} | {m['query']['B_mib']:.1f} | {m['query']['M_mib']:.1f} | "
          f"{min(r['query']['recall_unf'] for r in rs):.3f}/{min(r['query']['recall_fil'] for r in rs):.3f} | "
          f"{sum(r['query']['order_mismatch'] for r in rs)}/{sum(r['query']['dist_mismatch'] for r in rs)} | {E:.4f} |")
