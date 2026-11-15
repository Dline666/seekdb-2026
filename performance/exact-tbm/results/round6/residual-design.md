ROUND6 residual-norm candidate, baseline 33485b53c039a6cc3d2c4fa5b40d064365cf6eee.

No Go tests or benchmarks have been run by this agent. gofmt parsed all five changed Go files successfully. Root owns serialized validation and timing.

Change:
- Quantization keeps identical final integer codes and folded distance constants. While emitting each code, accumulate r_i^2 for r_i = fl(float64(v_i)-fl(min+scale*code_i)).
- qparam carries a transient residual norm upper bound. VecIndex retains only the maximum for each existing 128-slot block, replacing the old float32 maxScale member with float64 maxResidual. Together with maxAbs this remains 16 bytes per block. No per-row retained array is added.
- Query quantization computes its residual in the same pass. Existing test/helper API wrappers remain; production calls the residual-returning variants.
- Continuous, category, and arbitrary candidate batches all use the residual threshold. Category blocks conservatively inherit each source slot block's residual maximum; maxAbs remains reconstructed per row as before. This avoids rereading original vectors or retaining a per-row residual array, at the cost of possibly looser category residual bounds near an outlier in a different category.
- Index mutation only increases block maxima until rebuild, preserving conservative stale-bound behavior. No changes to durability, exact distance kernels, or distance/PK heap comparison.

Real-arithmetic proof:
Let xhat and qhat denote real affine reconstructions from the final clamped integer codes. Let R >= ||x-xhat||_2 and Q >= ||q-qhat||_2. By the triangle inequality,
||xhat-qhat||_2 <= ||x-q||_2 + R + Q.
Therefore, if a candidate can enter the exact heap, its approximate real squared distance is at most (D+R+Q)^2, with D an upper bound for its real exact distance. The kernel's approximate computation may differ from real affine distance by the existing prefilterRoundSlack; that allowance remains unchanged and is added after the threshold.

Residual numerical proof:
- Input float32 values, min, and scale are exactly representable as float64. For finite scale and code<=255, scale*code has at most 32 significant bits and is exactly representable in float64.
- Put A=|min|+256*scale and V=max(|min|,|max|). The rounded reconstruction addition plus residual subtraction has absolute error <=4*u*(V+A), using u=2^-52 (twice IEEE unit roundoff). This is a deliberately generous bound and also covers the rounding used to calculate A.
- Hence the residual-vector norm differs from the computed residual-vector norm by at most dim*4*u*(V+A). Using dim in place of sqrt(dim) is conservative and avoids a second square root per row.
- Squaring and accumulating the computed residuals has relative error bounded by gamma_(dim+1). The code multiplies sqrt(sumSq) by 1+8*(dim+1)*u, adds the arithmetic allowance, multiplies by 1+8*u, and steps upward with Nextafter. For dim<=maxInt8Dim=33025 these factors dominate the sum/sqrt/final arithmetic errors.
- Every nonzero exact residual is a multiple of 2^-149. Float32 subnormal input and residual squares are thus normal float64 numbers (smallest relevant square 2^-298); no float64 underflow term is needed.
- Scale rounded to zero, rounding of scale before code generation, endpoint clamping and scalar division rounding are automatically covered because the residual uses the actual final code. There is no scale/2 assumption.
- A float32 max-min overflow makes scale infinite. Rows keep an infinite residual bound and hence cannot be pruned using it. Queries explicitly decline quantization and take the exact path. Other non-finite residual arithmetic also returns an infinite bound.

Heap/threshold numerical proof:
The exact kernels form positive squared terms from finite float32 coordinates. Float64 subtraction, squaring/FMA, and nonnegative accumulation over <=33025 terms have relative error far below prefilterEps=1e-4 (a conservative gamma_(dim+4) is <4e-12). There is no float64 overflow or underflow in these terms. Thus D^2=(bd+eps)/(1-eps) safely upper-bounds the real distance of any row whose computed exact distance is <=bd. The threshold inflates (sqrt(D^2)+R+Q) by 1+eps before squaring and Nextafter, covering threshold arithmetic. Equality survives: filtering still drops only computed approximate distance strictly greater than this threshold. The original absolute prefilterRoundSlack remains added to cover affine expansion and VNNI bias arithmetic. Exact recomputation and PK tie breaking are unchanged.

Targeted test source (not executed here):
- TestResidualNormRationalOracle uses math/big.Rat to reconstruct codes and sum residual squares exactly, independently of all production bound/distance routines. It checks row, 8-bit query, and 6-bit query bounds at ordinary, extreme, constant, subnormal and maximum supported dimensions; an overflowed range must fall back safely.
- TestResidualThresholdIndependentDistance uses independent float64 sums across random/extreme/near-constant vectors, intentionally nudges bd eight ulps down, and asserts no false prune for 8/6-bit queries.
- TestResidualBoundsRemainBlockLocalAndTighter verifies the retained 16-byte bound size, tighter uniform-data threshold, conservative arbitrary-candidate merges, unaffected blocks near an outlier and infinite-bound behavior.
- Existing TestBlockBoundsExactAfterMutation, TestBlockBoundsIsolateOutlier, TestAbsoluteRoundingAllowance, TestCancellationSafeTopK, integer/AVX-VNNI reference and full exact suites remain needed.

Expected costs and validation:
Extra residual arithmetic and one sqrt per quantized row increase index build/maintenance CPU. Queries add residual arithmetic in their existing quantization pass; retained M and file format/B do not change. Whether reduced exact survivors repay those costs requires complete T/B/M comparisons. Build/index first-use cost must remain charged through the established B/M and lifecycle accounting; no score or speedup is asserted.

Recommended independent experiment:
Build candidate alone over 33485b5. Run targeted and full correctness suites, then five interleaved rounds at 10k*128 and 100k*256 with identical oracle/query order; compare complete write/query T/B/M/E and paired timings. Include mixed uniform, outlier, constant-tie, 127/257 tails, 1024 dimensions, 6-bit, forced AVX2, mutation/recovery, and parallel partitions. Keep any rejected result; do not aggregate per-candidate improvements.
