"""Interleave isolated versions through the same durable update workflow."""
import argparse
from pathlib import Path
import subprocess

p = argparse.ArgumentParser()
p.add_argument("--stress", required=True, type=Path)
p.add_argument("--bin-dir", required=True, type=Path)
p.add_argument("--out-dir", required=True, type=Path)
p.add_argument("--data-root", required=True, type=Path)
p.add_argument("--rounds", type=int, default=3)
p.add_argument("--rows", type=int, default=100000)
p.add_argument("--dim", type=int, default=256)
a = p.parse_args()
a.out_dir.mkdir(parents=True, exist_ok=True)
a.data_root.mkdir(parents=True, exist_ok=True)
for round_id in range(a.rounds):
    names = ["round2", "stream"]
    if round_id % 2:
        names.reverse()
    for name in names:
        label = f"update-{round_id}-{name}"
        print(label, flush=True)
        with (a.out_dir / f"{label}.jsonl").open("x") as out, (a.out_dir / f"{label}.stderr.txt").open("x") as err:
            subprocess.run([str(a.stress), "-binary", str(a.bin_dir / name),
                            "-data-dir", str(a.data_root / label), "-rows", str(a.rows),
                            "-dim", str(a.dim), "-queries", "2", "-bulk-update"],
                           stdout=out, stderr=err, check=True)
