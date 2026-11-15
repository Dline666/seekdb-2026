"""Archive reproducible ROUND6 source patches after validation.

Reads the unified source snapshots, never the diagnostic copies. Only source,
design notes and small evidence files are archived; binaries and fixtures are
not copied. An existing output directory is never overwritten.

Requires Python and git on the executing host. Windows Python/git can read the
WSL snapshots through a UNC --source-root; pass explicit --residual-design and
--bootstrap-design UNC paths too. All manifest paths remain POSIX-relative,
and write_bytes keeps generated artifacts LF-only on either operating system.
"""

import argparse
from datetime import datetime, timezone
import difflib
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import tempfile


BASELINE = "33485b53c039a6cc3d2c4fa5b40d064365cf6eee"
CANDIDATES = ("heap", "residual", "flatrows", "reader", "bootstrap", "heapflat", "final", "wide")
REJECTED = {"residual", "reader"}
REPO = Path(__file__).resolve().parents[3]
MAX_TEXT_BYTES = 4 << 20


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def canonical_json(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":")).encode("utf-8")


def text_lf(data, label):
    if len(data) > MAX_TEXT_BYTES or b"\0" in data:
        raise ValueError(f"Refusing oversized or binary artifact: {label}")
    data.decode("utf-8")
    normalized = data.replace(b"\r\n", b"\n")
    if b"\r" in normalized:
        raise ValueError(f"Unexpected lone CR in {label}")
    return normalized


def safe_relative(name):
    path = Path(name)
    if path.is_absolute() or ".." in path.parts or not re.fullmatch(r"[A-Za-z0-9_./-]+", name):
        raise ValueError(f"Unsupported source path: {name!r}")
    return name


def source_snapshot(module):
    raw, normalized = {}, {}
    for path in sorted(module.rglob("*")):
        if "__pycache__" in path.relative_to(module).parts:
            continue
        if path.is_symlink():
            raise ValueError(f"Refusing source symlink: {path}")
        if not path.is_file():
            continue
        name = safe_relative(path.relative_to(module).as_posix())
        raw[name] = path.read_bytes()
        normalized[name] = text_lf(raw[name], path)
    if "go.mod" not in raw or not raw:
        raise ValueError(f"Not a complete module snapshot: {module}")
    return raw, normalized


def hashes(files):
    return {name: sha256(data) for name, data in sorted(files.items())}


def run_git(repo, *args):
    result = subprocess.run(["git", "-C", str(repo), "-c", "core.autocrlf=false", *args],
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        raise RuntimeError(f"git {' '.join(args)} failed: {result.stderr.decode('utf-8', 'replace')}")
    return result.stdout


def verify_baseline_commit(repo, baseline, base):
    # Read the committed tree without touching the user's checkout or index.
    archive = run_git(repo, "archive", "--format=tar", baseline, "minivectordb")
    committed = {}
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as tree:
        for member in tree:
            if member.isdir():
                continue
            if not member.isfile() or not member.name.startswith("minivectordb/"):
                raise ValueError(f"Unexpected committed source entry: {member.name}")
            name = safe_relative(member.name[len("minivectordb/"):])
            with tree.extractfile(member) as stream:
                committed[name] = text_lf(stream.read(), member.name)
    if hashes(committed) != hashes(base):
        changed = sorted(name for name in set(committed) | set(base)
                         if committed.get(name) != base.get(name))
        raise ValueError(f"Unified base differs from {baseline} after CRLF normalization: {changed}")


def make_patch(base, candidate):
    chunks, changed = [], []
    for name in sorted(set(base) | set(candidate)):
        before, after = base.get(name), candidate.get(name)
        if before == after:
            continue
        changed.append(name)
        path = "minivectordb/" + name
        chunks.append(f"diff --git a/{path} b/{path}\n")
        if before is None:
            chunks.append("new file mode 100644\n")
        elif after is None:
            chunks.append("deleted file mode 100644\n")
        old = [] if before is None else before.decode("utf-8").splitlines(keepends=True)
        new = [] if after is None else after.decode("utf-8").splitlines(keepends=True)
        for line in difflib.unified_diff(old, new,
                                        fromfile="/dev/null" if before is None else "a/" + path,
                                        tofile="/dev/null" if after is None else "b/" + path):
            chunks.append(line if line.endswith("\n") else line + "\n\\ No newline at end of file\n")
    if not changed:
        raise ValueError("Candidate has no differences from the base")
    return "".join(chunks).encode("utf-8"), changed


def check_patch(patch, base, candidate):
    # Validate both git apply --check and the complete reconstructed tree.
    # Temporary directories contain source text only, never a binary/fixture.
    with tempfile.TemporaryDirectory(prefix="seekdb-round6-patch-check-") as tmp:
        checkout = Path(tmp)
        run_git(checkout, "init", "--quiet")
        for name, data in base.items():
            path = checkout / "minivectordb" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        patch_file = checkout / "candidate.patch"
        patch_file.write_bytes(patch)
        run_git(checkout, "apply", "--check", "--whitespace=nowarn", str(patch_file))
        run_git(checkout, "apply", "--whitespace=nowarn", str(patch_file))
        _, reconstructed = source_snapshot(checkout / "minivectordb")
        if hashes(reconstructed) != hashes(candidate):
            raise ValueError("Applied patch does not reconstruct the full candidate source snapshot")


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes((json.dumps(value, indent=2, ensure_ascii=False) + "\n").encode("utf-8"))


def copy_text(source, destination):
    raw = source.read_bytes()
    normalized = text_lf(raw, source)
    destination.parent.mkdir(parents=True, exist_ok=True)
    destination.write_bytes(normalized)
    return {"source": str(source), "raw_sha256": sha256(raw), "lf_sha256": sha256(normalized),
            "bytes": len(normalized)}


def residual_design(root, explicit):
    if explicit:
        return explicit.resolve()
    choices = sorted(root.parent.glob("seekdb-round6-residual-*/DESIGN.md"))
    if len(choices) != 1:
        raise ValueError(f"Expected one residual DESIGN.md; pass --residual-design explicitly: {choices}")
    return choices[0]


def archive(args):
    root, repo, output = args.source_root.resolve(), args.repo.resolve(), args.output.resolve()
    if output.exists():
        raise FileExistsError(f"Output already exists; choose a new --output directory: {output}")
    snapshots, raw_snapshots, snapshot_meta = {}, {}, {}
    for name in ("base", *CANDIDATES):
        module = root / name / "minivectordb"
        raw, normalized = source_snapshot(module)
        manifest_path = root / "results" / f"{name}-source-sha256.json"
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        if hashes(raw) != manifest:
            raise ValueError(f"{name} source has changed since its recorded build snapshot: {manifest_path}")
        raw_snapshots[name], snapshots[name] = raw, normalized
        snapshot_meta[name] = {
            "source": str(module), "file_count": len(raw),
            "raw_snapshot_sha256": sha256(canonical_json(hashes(raw))),
            "lf_snapshot_sha256": sha256(canonical_json(hashes(normalized))),
            "build_manifest_sha256": sha256(manifest_path.read_bytes()),
            "matches_recorded_build_source": True,
        }
    verify_baseline_commit(repo, args.baseline, snapshots["base"])
    # The original failed test used a no-op c1 -> c1 UPDATE. Archive the
    # corrected independently tested snapshot, retaining the failed log below.
    for name in ("bootstrap", "final"):
        test = snapshots[name].get("internal/engine/bootstrap_test.go", b"")
        if b"UPDATE docs SET cat='c2' WHERE id=1" not in test:
            raise ValueError(f"{name} is missing the corrected c2 overlay test")

    designs = {"residual": residual_design(root, args.residual_design),
               "bootstrap": args.bootstrap_design.resolve()}
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".round6-candidates-", dir=output.parent) as tmp:
        staged = Path(tmp) / "archive"
        staged.mkdir()
        for name in ("base", *CANDIDATES):
            write_json(staged / "snapshots" / f"{name}.json", {
                **snapshot_meta[name],
                "files": {path: {"raw_sha256": sha256(raw_snapshots[name][path]),
                                 "lf_sha256": sha256(data), "lf_bytes": len(data)}
                          for path, data in sorted(snapshots[name].items())},
            })
        candidates = {}
        for name in CANDIDATES:
            patch, changed = make_patch(snapshots["base"], snapshots[name])
            check_patch(patch, snapshots["base"], snapshots[name])
            (staged / f"{name}.patch").write_bytes(patch)
            candidates[name] = {
                "status": "rejected" if name in REJECTED else "pending",
                "patch": f"{name}.patch", "patch_sha256": sha256(patch),
                "baseline_commit": args.baseline,
                "base_snapshot_sha256": snapshot_meta["base"]["lf_snapshot_sha256"],
                "candidate_snapshot_sha256": snapshot_meta[name]["lf_snapshot_sha256"],
                "git_apply_check": "PASS", "reconstructed_snapshot_matches": True,
                "changed_files": [{
                    "path": "minivectordb/" + path,
                    "action": "added" if path not in snapshots["base"] else
                              "deleted" if path not in snapshots[name] else "modified",
                    "base_sha256": sha256(snapshots["base"][path]) if path in snapshots["base"] else None,
                    "candidate_sha256": sha256(snapshots[name][path]) if path in snapshots[name] else None,
                } for path in changed],
            }
            write_json(staged / f"{name}.metadata.json", candidates[name])

        evidence = {}
        for path in sorted((root / "results").iterdir()):
            preserve = path.name.startswith(("residual-", "reader-")) or path.name in {
                "bootstrap-tests.txt", "bootstrap-tests-fixed.txt",
            }
            if preserve and path.is_file() and path.suffix in {".txt", ".json", ".jsonl", ".md"}:
                evidence[path.name] = copy_text(path, staged / "evidence" / path.name)
        required = {"residual-tests.txt", "reader-tests.txt", "bootstrap-tests.txt", "bootstrap-tests-fixed.txt"}
        if not required.issubset(evidence):
            raise ValueError(f"Missing retained evidence: {sorted(required - set(evidence))}")
        design_meta = {name: copy_text(path, staged / "designs" / f"{name}.md")
                       for name, path in designs.items()}
        index = {
            "schema": "seekdb-round6-source-candidates-v1",
            "created_utc": datetime.now(timezone.utc).isoformat(),
            "baseline_commit": args.baseline, "base_matches_commit_after_crlf_normalization": True,
            "snapshot_hash_definition": "SHA256 of UTF-8 JSON path-to-SHA256 map; sorted keys, compact separators",
            "source_snapshots": snapshot_meta, "candidates": candidates,
            "designs": design_meta, "preserved_evidence": evidence,
            "decisions": {
                "pending": "All other candidates, including the combination named final, await the root agent's final retention decision. The name final does not mean selected.",
                "residual": "Rejected after independent local T/B/M evaluation; retain all available raw comparisons, summaries and test logs.",
                "reader": "Rejected after independent local T/B/M evaluation, including the 1024-dimensional write regression; retain all available evidence.",
                "bootstrap_test_failure": "Original c1 UPDATE was a no-op; corrected snapshot uses c2 to create an overlay. Both original failed and corrected test logs are preserved.",
            },
            "scope": "Local source archive only. No binary/fixture copies, tests, benchmarks, push or formal submission.",
        }
        write_json(staged / "index.json", index)
        (staged / "README.md").write_bytes((
            f"ROUND6 independent source candidates relative to `{args.baseline}`.\n\n"
            "Each patch includes all changed source and tests from the unified snapshot, "
            "uses `a/minivectordb/...` and `b/minivectordb/...` paths, and has been checked "
            "with `git apply --check` and full reconstruction in a temporary source tree. "
            "Apply exactly one patch to the baseline; combination patches are complete independent diffs.\n\n"
            "All candidates except the explicitly rejected residual/reader experiments have "
            "status `pending`. The experimental name `final` does not imply it was retained; "
            "the final retention decision belongs in the completed results metadata.\n\n"
            "`index.json`, per-candidate metadata and `snapshots/*.json` record patch, "
            "changed-file, baseline and full snapshot SHA256 values. Patches use LF; "
            "snapshot manifests record both original bytes and CRLF-normalized LF hashes.\n\n"
            "Residual and reader are rejected performance candidates, not omitted experiments. "
            "Their available raw measurements, summaries and test logs remain in `evidence/`. "
            "The original bootstrap overlay-test failure and corrected passing run are also "
            "preserved; archived bootstrap/final source uses the corrected c2 UPDATE. "
            "`designs/` preserves the original residual and bootstrap proposals, whose "
            "historical validation status must not replace the later test evidence.\n\n"
            "This archive does not establish a formal score. It contains no binaries or fixtures.\n"
        ).encode("utf-8"))
        # Reserve the destination exclusively after all validation has passed.
        # A prior archive, including a partial one, is never replaced.
        output.mkdir()
        for path in staged.iterdir():
            path.rename(output / path.name)
    print(json.dumps({"output": str(output), "baseline": args.baseline,
                      "candidates": list(CANDIDATES), "patch_checks": "PASS"}, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=REPO)
    parser.add_argument("--source-root", type=Path,
                        default=Path(os.environ.get("SEEKDB_ROUND6_ROOT", "/tmp/seekdb-round6")))
    parser.add_argument("--baseline", default=BASELINE)
    parser.add_argument("--output", type=Path,
                        default=REPO / "performance/exact-tbm/results/round6/candidates")
    parser.add_argument("--residual-design", type=Path)
    parser.add_argument("--bootstrap-design", type=Path,
                        default=Path("/tmp/seekdb-round6-bootstrap/DESIGN.md"))
    archive(parser.parse_args())
