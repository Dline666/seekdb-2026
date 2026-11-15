ROUND6 first-batch approximate bootstrap candidate.
Baseline: 33485b53c039a6cc3d2c4fa5b40d064365cf6eee.
Source: /tmp/seekdb-round6-bootstrap/minivectordb.
No Go tests, builds or benchmarks were executed by this agent. gofmt parsed the changed files.

Candidate scope:
- Only the first batch of each worker's existing fused contiguous, fused equality-candidate or fused category-block scan.
- Existing caller gates still require valid quantization, SIMD/mapping, no overlay, no residual predicate, no six-bit query mode, and valid live slots.
- Additional gates: empty heap, 1<=K<=16, batch size>=2K and <=128. All other cases use the unchanged existing path. MVDB_NO_BOOTSTRAP disables the candidate.
- The input batch respects existing 128-slot block boundaries. Scattered equality candidates use their existing merged block bound.

Algorithm:
1. Run the existing unshifted int16-query integer-dot gather kernel on all first-batch rows.
2. Compute each folded approximate squared distance and select the smallest K indices with bounded insertion selection.
3. Read original vectors for those K rows, compute exact distances with the unchanged gather kernel, and admit their exact distance/PK/offset into the normal heap.
4. Compute the existing conservative threshold from that heap bound.
5. Visit every other row in the same batch exactly once. Only approximate distances greater than that conservative threshold are skipped; all remaining rows receive the unchanged exact distance and normal heap admission.
6. Resume the existing fused loop at the next batch. No row from the first batch is visited again.

Correctness:
- Approximate ranking only changes visit order. It never supplies an answer or excludes a row by rank.
- A fixed 128-entry chosen bitmap excludes seed indices only from the second pass, preventing duplicate heap entries.
- NaN approximations normalize to -Inf, making them attractive seeds and always survivors if unseeded. Infinite or NaN thresholds remain conservative through the strict greater-than filtering comparison.
- A seed producing an invalid exact distance is not admitted, matching existing d<=bound behavior; the heap remains unfilled and the infinite threshold keeps unseeded rows.
- Every nonseed row remains reachable, and safe skipped rows cannot enter the current heap. Later heap bounds only shrink. Each worker computes its exact local TopK; the existing merge remains unchanged.
- Original exact reduction order and PK tie comparison remain unchanged, including zero-distance and maximum-magnitude constant ties.
- No assembly, persistence, storage ordering or transaction semantics change.

Expected cost, not a measurement:
The old first batch already performs integer dots, but the new first batch uses the multipass gather path plus 128 folded-distance evaluations, a fixed mapping pass, and O(128*K) scalar seed selection (K<=16), rather than the AVX-VNNI fused kernel. It also makes two exact-kernel calls rather than one. Fixed scratch is several KiB per participating worker, all local arrays bounded by 128; no per-row table cache or O(N) allocation is added.
The possible saving is reducing the first batch's usual 128 original-vector exact distances to K plus conservative survivors. If almost all first-batch rows survive, especially constants/outliers or cheap low-dimensional vectors, the overhead can dominate. Cost/benefit is unknown until full write/query T/B/M interleaved comparison; do not infer a score.

Changed files:
- bootstrap.go: fixed-buffer helper, optional disable flag, once-per-bootstrap diagnostic counter.
- topk.go: calls only in existing fused branches, then advances past fully handled first batch.
- bootstrap_test.go: independent float64 brute-force truth for shuffled/contiguous first batches, bad/NaN approximations, duplicate prevention and gates; end-to-end first/later-batch truth with equality filters, coarse ties, huge constants, overflowing float32 quantization range, serial/parallel partitions and overlay/predicate fallback.
- fused_candidates_test.go: explicitly disables bootstrap only within the pre-existing test whose purpose is proving the fused candidate assembly kernel ran. Its 50-row categories could otherwise be fully handled by bootstrap, making the old counter assertion invalid. New independent tests cover bootstrap itself.

Root should run TestBootstrap, then the complete existing suite and exact protocol truth, and inspect compiler escape/stack information if relevant. For performance, compare this candidate alone against baseline in alternating rounds with identical query order/oracle, retaining M and all write/index-build cost. If combined later with residual bounds, adapt the helper's threshold call to include queryResidual and rerun combined truth; this patch deliberately uses baseline bounds for an independent experiment.
