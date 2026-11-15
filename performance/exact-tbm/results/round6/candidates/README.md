ROUND6 independent source candidates relative to `33485b53c039a6cc3d2c4fa5b40d064365cf6eee`.

Each patch includes all changed source and tests from the unified snapshot, uses `a/minivectordb/...` and `b/minivectordb/...` paths, and has been checked with `git apply --check` and full reconstruction in a temporary source tree. Apply exactly one patch to the baseline; combination patches are complete independent diffs.

All candidates except the explicitly rejected residual/reader experiments have status `pending`. The experimental name `final` does not imply it was retained; the final retention decision belongs in the completed results metadata.

`index.json`, per-candidate metadata and `snapshots/*.json` record patch, changed-file, baseline and full snapshot SHA256 values. Patches use LF; snapshot manifests record both original bytes and CRLF-normalized LF hashes.

Residual and reader are rejected performance candidates, not omitted experiments. Their available raw measurements, summaries and test logs remain in `evidence/`. The original bootstrap overlay-test failure and corrected passing run are also preserved; archived bootstrap/final source uses the corrected c2 UPDATE. `designs/` preserves the original residual and bootstrap proposals, whose historical validation status must not replace the later test evidence.

This archive does not establish a formal score. It contains no binaries or fixtures.
