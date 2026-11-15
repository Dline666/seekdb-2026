r"""Archive complete ROUND7 source candidates; never execute a build or test.

Run with Windows Python and Windows Git (WSL currently has no Git). Example:

  python performance/exact-tbm/round7/archive_candidates.py ^
    --source-root \\wsl.localhost\ArchBench\tmp\seekdb-round7 ^
    --candidates partial partial-delete-repro partial-safe ^
    --status partial=failed --status partial-delete-repro=failed ^
    --output performance/exact-tbm/results/round7/candidates

Pass --selected NAME only after making the final retention decision. NAME must
be a listed candidate, or 'base' to retain the baseline unchanged. Omission
means pending; a candidate's directory name never implies selection.

Every snapshot must match results/NAME-source-sha256.json byte for byte. The
base must also match the committed module after CRLF-to-LF normalization.
Every independent complete patch is checked with Windows Git and applied to
a temporary source tree, whose entire file set and LF hashes must match the
candidate. Outputs include per-file raw/LF hashes and the recorded manifests.
Existing output directories are never overwritten. No binaries, fixture
directories or result logs are copied; oversized/binary source entries fail.
"""

import argparse
from datetime import datetime, timezone
import difflib
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tarfile
import tempfile


BASELINE = "4a257014c9f43fd1dbb86b008d32cd93303b4d94"
REPO = Path(__file__).resolve().parents[3]
MAX_FILE_BYTES = 4 << 20
MAX_SNAPSHOT_BYTES = 32 << 20
SOURCE_SUFFIXES = {".go", ".s", ".S", ".c", ".h", ".cc", ".cpp", ".hpp",
                   ".mod", ".sum", ".md", ".py", ".sh", ".ps1"}
SOURCE_NAMES = {"Makefile", "Dockerfile", ".gitignore", ".gitattributes"}


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def hashes(files):
    return {name: sha256(data) for name, data in sorted(files.items())}


def tree_hash(files):
    encoded = json.dumps(hashes(files), sort_keys=True,
                         separators=(",", ":")).encode("utf-8")
    return sha256(encoded)


def safe_relative(name):
    path = PurePosixPath(name)
    if (not isinstance(name, str) or not re.fullmatch(r"[A-Za-z0-9_./-]+", name)
            or path.is_absolute() or any(part in {".", ".."} for part in name.split("/"))
            or str(path) != name):
        raise ValueError(f"Unsupported source path: {name!r}")
    return name


def safe_name(name):
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]*", name):
        raise ValueError(f"Unsupported candidate name: {name!r}")
    if name.casefold() in {"con", "prn", "aux", "nul",
                          *(f"com{i}" for i in range(1, 10)),
                          *(f"lpt{i}" for i in range(1, 10))}:
        raise ValueError(f"Reserved Windows filename: {name!r}")
    return name


def text_lf(data, label):
    if len(data) > MAX_FILE_BYTES or b"\0" in data:
        raise ValueError(f"Refusing oversized or binary source: {label}")
    data.decode("utf-8")
    normalized = data.replace(b"\r\n", b"\n")
    if b"\r" in normalized:
        raise ValueError(f"Unexpected lone CR in {label}")
    return normalized


def source_path(name):
    safe_relative(name)
    path = PurePosixPath(name)
    if path.suffix not in SOURCE_SUFFIXES and path.name not in SOURCE_NAMES:
        raise ValueError(f"Refusing non-source entry (no silent exclusion): {name}")


def read_small(path):
    if path.is_symlink() or getattr(path, "is_junction", lambda: False)():
        raise ValueError(f"Refusing linked artifact: {path}")
    with path.open("rb") as stream:
        data = stream.read(MAX_FILE_BYTES + 1)
    if len(data) > MAX_FILE_BYTES:
        raise ValueError(f"Refusing oversized artifact: {path}")
    return data


def source_snapshot(module):
    if (not module.is_dir() or module.is_symlink()
            or getattr(module, "is_junction", lambda: False)()):
        raise ValueError(f"Not a regular module directory: {module}")
    raw, normalized, total = {}, {}, 0
    portable_names = set()
    for path in sorted(module.rglob("*")):
        relative = path.relative_to(module)
        if "__pycache__" in relative.parts:
            continue
        if path.is_symlink() or getattr(path, "is_junction", lambda: False)():
            raise ValueError(f"Refusing source link: {path}")
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError(f"Unexpected source entry: {path}")
        name = safe_relative(relative.as_posix())
        source_path(name)
        if name.casefold() in portable_names:
            raise ValueError(f"Case-colliding source paths on Windows: {name}")
        portable_names.add(name.casefold())
        data = read_small(path)
        total += len(data)
        if total > MAX_SNAPSHOT_BYTES:
            raise ValueError(f"Refusing oversized source tree: {module}")
        raw[name], normalized[name] = data, text_lf(data, path)
    if "go.mod" not in raw:
        raise ValueError(f"Not a complete module snapshot: {module}")
    return raw, normalized


def run_git(git, repo, *args):
    result = subprocess.run(
        [git, "-C", str(repo), "-c", "core.autocrlf=false",
         "-c", "core.safecrlf=false", "-c", f"core.attributesFile={os.devnull}",
         *args], stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode:
        detail = result.stderr.decode("utf-8", "replace")
        raise RuntimeError(f"git {' '.join(args)} failed: {detail}")
    return result.stdout


def verify_baseline(git, repo, baseline, base):
    resolved = run_git(git, repo, "rev-parse", "--verify",
                       f"{baseline}^{{commit}}").decode("ascii").strip()
    if resolved != baseline:
        raise ValueError("The baseline must be its complete commit SHA")
    archive = run_git(git, repo, "archive", "--format=tar", baseline, "minivectordb")
    raw, normalized, total = {}, {}, 0
    with tarfile.open(fileobj=io.BytesIO(archive), mode="r:") as tree:
        for member in tree:
            if member.isdir():
                continue
            if not member.isfile() or not member.name.startswith("minivectordb/"):
                raise ValueError(f"Unexpected committed entry: {member.name}")
            name = safe_relative(member.name[len("minivectordb/"):])
            source_path(name)
            if name in raw or member.size > MAX_FILE_BYTES:
                raise ValueError(f"Duplicate or oversized committed entry: {name}")
            total += member.size
            if total > MAX_SNAPSHOT_BYTES:
                raise ValueError("Committed source exceeds the source archive limit")
            with tree.extractfile(member) as stream:
                data = stream.read(MAX_FILE_BYTES + 1)
            raw[name], normalized[name] = data, text_lf(data, member.name)
    if hashes(normalized) != hashes(base):
        changed = sorted(name for name in set(normalized) | set(base)
                         if normalized.get(name) != base.get(name))
        raise ValueError(f"Base snapshot differs from commit after LF normalization: {changed}")
    return raw, normalized


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
        for line in difflib.unified_diff(
                old, new, fromfile="/dev/null" if before is None else "a/" + path,
                tofile="/dev/null" if after is None else "b/" + path):
            chunks.append(line if line.endswith("\n") else
                          line + "\n\\ No newline at end of file\n")
    return "".join(chunks).encode("utf-8"), changed


def check_patch(git, patch, base, candidate):
    with tempfile.TemporaryDirectory(prefix="seekdb-round7-patch-check-") as tmp:
        checkout = Path(tmp)
        run_git(git, checkout, "init", "--quiet", "--template=")
        for name, data in base.items():
            path = checkout / "minivectordb" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(data)
        patch_file = checkout / "candidate.patch"
        patch_file.write_bytes(patch)
        run_git(git, checkout, "apply", "--check", "--allow-empty",
                "--whitespace=nowarn", str(patch_file))
        run_git(git, checkout, "apply", "--allow-empty",
                "--whitespace=nowarn", str(patch_file))
        raw, normalized = source_snapshot(checkout / "minivectordb")
        if hashes(raw) != hashes(candidate) or hashes(normalized) != hashes(candidate):
            raise ValueError("Applied patch does not reconstruct the complete LF candidate tree")


def write_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes((json.dumps(value, indent=2, ensure_ascii=False) + "\n").encode("utf-8"))


def file_manifest(raw, normalized):
    return {name: {"raw_sha256": sha256(raw[name]), "raw_bytes": len(raw[name]),
                   "lf_sha256": sha256(data), "lf_bytes": len(data)}
            for name, data in sorted(normalized.items())}


def validate_choices(args):
    names = [safe_name(name) for name in args.candidates]
    if any(name.casefold() in {"base", "baseline-commit"} for name in names):
        raise ValueError("'base' and 'baseline-commit' are reserved for baseline manifests")
    if len({name.casefold() for name in names}) != len(names):
        raise ValueError("Candidate names must be unique on Windows")
    if args.selected is not None and args.selected not in {"base", *names}:
        raise ValueError("--selected must be 'base' or one of --candidates")
    statuses = {name: "pending" for name in names}
    seen = set()
    for value in args.status:
        name, separator, status = value.partition("=")
        if not separator or name not in statuses or status not in {"pending", "failed", "rejected"}:
            raise ValueError(f"Expected --status NAME=pending|failed|rejected: {value!r}")
        if name in seen:
            raise ValueError(f"Duplicate status for {name}")
        statuses[name] = status
        seen.add(name)
    if args.selected in statuses:
        if statuses[args.selected] != "pending":
            raise ValueError("A failed/rejected candidate cannot be selected")
        statuses[args.selected] = "selected"
    return names, statuses


def archive(args):
    if os.name != "nt":
        raise RuntimeError("Run this script with Windows Python and Windows Git; use a UNC --source-root")
    names, statuses = validate_choices(args)
    root, repo, output = args.source_root.resolve(), args.repo.resolve(), args.output.resolve()
    if args.output.exists() or args.output.is_symlink() or output.exists():
        raise FileExistsError(f"Output already exists; choose a fresh --output: {output}")
    protected = [repo / "minivectordb", *(root / name / "minivectordb" for name in ["base", *names])]
    if any(path == output or path in output.parents for path in protected):
        raise ValueError("The output directory must not be inside a source module")
    if not re.fullmatch(r"[0-9a-f]{40}", args.baseline):
        raise ValueError("--baseline must be a complete lowercase commit SHA")
    git_version = run_git(args.git, repo, "--version").decode("utf-8").strip()
    raw_trees, trees, metadata, build_manifests = {}, {}, {}, {}
    for name in ["base", *names]:
        module = root / name / "minivectordb"
        raw, normalized = source_snapshot(module)
        manifest_path = root / "results" / f"{name}-source-sha256.json"
        manifest_raw = read_small(manifest_path)
        manifest_lf = text_lf(manifest_raw, manifest_path)
        manifest = json.loads(manifest_lf)
        if not isinstance(manifest, dict) or hashes(raw) != manifest:
            raise ValueError(f"{name} differs from its recorded source manifest: {manifest_path}")
        raw_trees[name], trees[name] = raw, normalized
        build_manifests[name] = manifest_raw, manifest_lf
        metadata[name] = {
            "source": str(module), "file_count": len(raw),
            "raw_snapshot_sha256": tree_hash(raw), "lf_snapshot_sha256": tree_hash(normalized),
            "recorded_manifest": f"build-manifests/{name}-source-sha256.json",
            "recorded_manifest_source": str(manifest_path),
            "recorded_manifest_raw_sha256": sha256(manifest_raw),
            "recorded_manifest_lf_sha256": sha256(manifest_lf),
            "matches_recorded_source": True,
        }
    commit_raw, commit_lf = verify_baseline(args.git, repo, args.baseline, trees["base"])
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".round7-candidates-", dir=output.parent) as tmp:
        staged = Path(tmp) / "archive"
        staged.mkdir()
        for name in ["base", *names]:
            write_json(staged / "snapshots" / f"{name}.json", {
                **metadata[name], "files": file_manifest(raw_trees[name], trees[name]),
            })
            path = staged / metadata[name]["recorded_manifest"]
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(build_manifests[name][1])
        write_json(staged / "snapshots" / "baseline-commit.json", {
            "commit": args.baseline, "raw_snapshot_sha256": tree_hash(commit_raw),
            "lf_snapshot_sha256": tree_hash(commit_lf),
            "files": file_manifest(commit_raw, commit_lf),
        })
        candidates = {}
        for name in names:
            patch, changed = make_patch(trees["base"], trees[name])
            check_patch(args.git, patch, trees["base"], trees[name])
            (staged / f"{name}.patch").write_bytes(patch)
            candidates[name] = {
                "status": statuses[name], "selected": args.selected == name,
                "patch": f"{name}.patch", "patch_sha256": sha256(patch), "patch_bytes": len(patch),
                "baseline_commit": args.baseline,
                "base_raw_snapshot_sha256": metadata["base"]["raw_snapshot_sha256"],
                "base_lf_snapshot_sha256": metadata["base"]["lf_snapshot_sha256"],
                "candidate_raw_snapshot_sha256": metadata[name]["raw_snapshot_sha256"],
                "candidate_lf_snapshot_sha256": metadata[name]["lf_snapshot_sha256"],
                "snapshot_manifest": f"snapshots/{name}.json",
                "git_apply_check": "PASS", "reconstructed_complete_lf_tree_matches": True,
                "changed_files": [{
                    "path": "minivectordb/" + path,
                    "action": "added" if path not in trees["base"] else
                              "deleted" if path not in trees[name] else "modified",
                    "base_raw_sha256": sha256(raw_trees["base"][path]) if path in trees["base"] else None,
                    "base_lf_sha256": sha256(trees["base"][path]) if path in trees["base"] else None,
                    "candidate_raw_sha256": sha256(raw_trees[name][path]) if path in trees[name] else None,
                    "candidate_lf_sha256": sha256(trees[name][path]) if path in trees[name] else None,
                } for path in changed],
            }
            write_json(staged / f"{name}.metadata.json", candidates[name])

        # Refuse to publish if sources or their recorded manifests changed
        # while patches were being checked. No source checkout is modified.
        for name in ["base", *names]:
            now_raw, _ = source_snapshot(root / name / "minivectordb")
            if hashes(now_raw) != hashes(raw_trees[name]):
                raise ValueError(f"{name} source changed during archive verification")
            if read_small(root / "results" / f"{name}-source-sha256.json") != build_manifests[name][0]:
                raise ValueError(f"{name} recorded manifest changed during archive verification")
        write_json(staged / "index.json", {
            "schema": "seekdb-round7-source-candidates-v1",
            "created_utc": datetime.now(timezone.utc).isoformat(),
            "baseline_commit": args.baseline,
            "base_matches_commit_after_lf_normalization": True,
            "selection": {"status": "pending" if args.selected is None else "decided",
                          "selected": args.selected},
            "git": {"executable": args.git, "version": git_version, "host": "Windows"},
            "snapshot_hash_definition": "SHA256 of UTF-8 JSON path-to-SHA256 map; sorted keys, compact separators",
            "source_snapshots": metadata, "candidates": candidates,
            "scope": "Complete source-content diffs only; no builds, tests, binaries, fixtures, result logs, push or formal submission.",
            "validation_limit": "Patch reconstruction proves source reproducibility, not test success or a performance score.",
        })
        selected = "Pending" if args.selected is None else args.selected
        readme = (
            f"ROUND7 source candidates relative to {args.baseline}.\n\n"
            f"Final selection: {selected}. Statuses come only from explicit command-line arguments.\n\n"
            "Each patch is a complete independent diff from the baseline, including changed tests. "
            "Apply exactly one patch to the baseline repository. Windows Git apply --check and "
            "complete LF source-tree reconstruction have both passed.\n\n"
            "snapshots/*.json record every original raw and normalized LF file hash; build-manifests/ "
            "preserves the matching recorded source manifests, normalized to LF. Per-candidate "
            "metadata also records the raw/LF baseline and candidate hashes for every changed file.\n\n"
            "Failed or rejected candidates remain reproducible when explicitly listed. "
            "This source archive does not claim test success or establish a formal score. "
            "It contains no binaries, fixture directories or measurement logs.\n"
        )
        (staged / "README.md").write_bytes(readme.encode("utf-8"))
        # Exclusive mkdir rejects any concurrent writer or preexisting archive.
        # If publication fails partway, the partial directory is never replaced.
        output.mkdir()
        for path in staged.iterdir():
            path.rename(output / path.name)
    print(json.dumps({"output": str(output), "baseline": args.baseline,
                      "candidates": names, "selected": args.selected,
                      "patch_checks": "PASS"}, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, default=REPO)
    parser.add_argument("--source-root", type=Path,
                        default=Path(os.environ.get("SEEKDB_ROUND7_ROOT",
                                                   r"\\wsl.localhost\ArchBench\tmp\seekdb-round7")))
    parser.add_argument("--baseline", default=BASELINE)
    parser.add_argument("--output", type=Path,
                        default=REPO / "performance/exact-tbm/results/round7/candidates")
    parser.add_argument("--git", default="git", help="Windows Git executable")
    parser.add_argument("--candidates", nargs="+", required=True)
    parser.add_argument("--selected", help="Candidate name, or base; omit while the decision is pending")
    parser.add_argument("--status", action="append", default=[],
                        help="Explicit NAME=pending|failed|rejected; may be repeated")
    archive(parser.parse_args())
