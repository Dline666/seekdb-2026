package engine

import (
	"math"
	"os"
)

// prefilterEnabled gates the int8 prefilter; a variable so tests can toggle
// it. MVDB_NO_PREFILTER=1 disables it entirely: the kill-switch for A/B
// runs and for safety. When it is off the query path is exactly the old
// batched exact scan.
var prefilterEnabled = os.Getenv("MVDB_NO_PREFILTER") == ""

// prefilter6Bit selects the 6-bit query quantization (VPMADDUBSW kernel)
// instead of the 8-bit one (VPMOVZXBW+VPMADDWD kernel). MVDB_PREFILTER_6BIT=1
// enables it. It is an experiment toggle for A/B runs: the 6-bit kernel
// needs fewer instructions per 32 codes but its qscale is ~4x larger, so
// the prefilter bound is looser and fewer rows are skipped.
var prefilter6Bit = os.Getenv("MVDB_PREFILTER_6BIT") != ""

// qparam holds the per-row affine quantization parameters of one stored
// vector, plus the two sums the approximate distance needs. Codes are
// uint8 in [0,255]: code[i] = round((v[i]-min)/scale), so the dequantized
// value min+scale*code[i] is within scale/2 of v[i]. Codes are derived
// state, like the rest of VecIndex: they are built at insert/commit time
// and rebuilt from the log on recovery, never persisted separately.
//
// cRow folds the per-row constants of the approximate-distance expansion
// into one value, so the per-row threshold loop does not recompute them:
//
//	d̂² = qNorm2 - 2*min*qSum - 2*scale*dot + cRow,
//	cRow = dim*min² + 2*scale*min*sumC + scale²*sumC2.
//
// cRow is computed once at INSERT time in float64. It is exact up to the
// float64 rounding of that one sum, which differs from the per-query
// expansion by a few ulps — far below the prefilterEps slack in the
// bound (see approxDist2).
type qparam struct {
	min   float32
	scale float32
	sumC  float32 // sum of the codes
	cRow  float64 // folded per-row constant of the dhat2 expansion
}

// quantizeRow writes the int8 codes of v into codes (len(v) bytes) and
// returns the quantization parameters. v must be non-empty.
func quantizeRow(v []float32, codes []byte) qparam {
	mn, mx := v[0], v[0]
	for _, f := range v[1:] {
		if f < mn {
			mn = f
		}
		if f > mx {
			mx = f
		}
	}
	var sc float32
	if mx > mn {
		sc = (mx - mn) / 255
	}
	var sumC, sumC2 float64
	for i, f := range v {
		c := 0
		if sc > 0 {
			c = int(math.Round((float64(f) - float64(mn)) / float64(sc)))
			if c < 0 {
				c = 0
			} else if c > 255 {
				c = 255
			}
		}
		codes[i] = byte(c)
		sumC += float64(c)
		sumC2 += float64(c) * float64(c)
	}
	mnf, scf := float64(mn), float64(sc)
	cRow := float64(len(v))*mnf*mnf + 2*scf*mnf*sumC + scf*scf*sumC2
	return qparam{min: mn, scale: sc, sumC: float32(sumC), cRow: cRow}
}

// quantizeQuery6 is the 6-bit variant of quantizeQuery: q[i] ~=
// qmin + qscale*qc[i] with qc[i] in [0,63]. The coarser query
// quantization lets the dot kernel use VPMADDUBSW (unsigned row codes
// times signed query codes, pairwise-summed into int16) instead of
// VPMOVZXBW+VPMADDWD, at the cost of a ~4x larger qscale, which the
// prefilter bound absorbs (the combined scale becomes maxScale+qscale).
// It returns ok=false for a non-finite element, like quantizeQuery.
func quantizeQuery6(q []float32) (qmin, qscale float32, qc []int8, qSum, qNorm2 float64, ok bool) {
	if len(q) == 0 {
		return 0, 0, nil, 0, 0, true
	}
	mn, mx := q[0], q[0]
	if math.IsNaN(float64(mn)) || math.IsInf(float64(mn), 0) {
		return 0, 0, nil, 0, 0, false
	}
	for _, f := range q[1:] {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return 0, 0, nil, 0, 0, false
		}
		if f < mn {
			mn = f
		}
		if f > mx {
			mx = f
		}
	}
	var sc float32
	if mx > mn {
		sc = (mx - mn) / 63
	}
	qc = make([]int8, len(q))
	for i, f := range q {
		c := 0
		if sc > 0 {
			c = int(math.Round((float64(f) - float64(mn)) / float64(sc)))
			if c < 0 {
				c = 0
			} else if c > 63 {
				c = 63
			}
		}
		qc[i] = int8(c)
		qh := float64(mn) + float64(sc)*float64(c)
		qSum += qh
		qNorm2 += qh * qh
	}
	return mn, sc, qc, qSum, qNorm2, true
}

// quantizeQuery quantizes the query to 8-bit codes for the integer
// prefilter kernel: q[i] ~= qmin + qscale*qc[i] with qc[i] in [0,255].
// It also returns the element sum and squared norm of the DEQUANTIZED
// query (qmin+qscale*qc), which approxDist2 needs. It returns ok=false
// when the query has a non-finite element: the integer kernel cannot
// represent those, so the caller must fall back to the exact scan.
func quantizeQuery(q []float32) (qmin, qscale float32, qc []int16, qSum, qNorm2 float64, ok bool) {
	if len(q) == 0 {
		return 0, 0, nil, 0, 0, true
	}
	mn, mx := q[0], q[0]
	if math.IsNaN(float64(mn)) || math.IsInf(float64(mn), 0) {
		return 0, 0, nil, 0, 0, false
	}
	for _, f := range q[1:] {
		if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
			return 0, 0, nil, 0, 0, false
		}
		if f < mn {
			mn = f
		}
		if f > mx {
			mx = f
		}
	}
	var sc float32
	if mx > mn {
		sc = (mx - mn) / 255
	}
	qc = make([]int16, len(q))
	for i, f := range q {
		c := 0
		if sc > 0 {
			c = int(math.Round((float64(f) - float64(mn)) / float64(sc)))
			if c < 0 {
				c = 0
			} else if c > 255 {
				c = 255
			}
		}
		qc[i] = int16(c)
		qh := float64(mn) + float64(sc)*float64(c)
		qSum += qh
		qNorm2 += qh * qh
	}
	return mn, sc, qc, qSum, qNorm2, true
}

// dotInt8GatherScalar is the portable reference for the AVX2 kernel:
// out[r] = sum_i q[i]*float32(codes[offs[r]+i]).
func dotInt8GatherScalar(q []float32, codes []byte, offs []int64, out []float32) {
	for r, o := range offs {
		var s float32
		for i, f := range q {
			s += f * float32(codes[o+int64(i)])
		}
		out[r] = s
	}
}

// dotInt8GatherInt6Scalar is the portable reference for the 6-bit
// integer kernel: out[r] = sum_i qc[i]*codes[offs[r]+i], exact int32
// arithmetic. qc[i] is in [0,63], so each product is at most 63*255 and
// the sum over dim <= 1024 fits in int32.
func dotInt8GatherInt6Scalar(qc []int8, codes []byte, offs []int64, out []int32) {
	for r, o := range offs {
		var s int32
		for i, qi := range qc {
			s += int32(qi) * int32(codes[o+int64(i)])
		}
		out[r] = s
	}
}

// dotInt8GatherIntScalar is the portable reference for the integer kernel:
// out[r] = sum_i qc[i]*codes[offs[r]+i], exact int32 arithmetic.
func dotInt8GatherIntScalar(qc []int16, codes []byte, offs []int64, out []int32) {
	for r, o := range offs {
		var s int32
		for i, qi := range qc {
			s += int32(qi) * int32(codes[o+int64(i)])
		}
		out[r] = s
	}
}

// prefilterEps is the relative slack in the error bound covering float
// rounding in the kernel dot product, the dhat2 expansion and the
// quantization itself. It is validated empirically by TestPrefilterBound.
const prefilterEps = 1e-4

// foldQparam precomputes the per-row constants the fused scan's threshold
// pass needs, in SoA layout (four parallel slices on VecIndex, one entry
// per slot), so the pass can load 4 rows' worth of one constant with a
// single vector load and does no per-row constant arithmetic at query time:
//
//	dhat2 = qNorm2 + m2*qSum + cRow + qmin*t2 + qscale*s2*intDot
//
// with m2 = -2*min, t2 = -2*scale*sumC, s2 = -2*scale and
// cRow = dim*min^2 + 2*scale*min*sumC + scale^2*sumC2.
// Algebra: expand approxDist2's -2*s*(dot - mn*sumC) with the dequantized
// dot = qmin*sumC + qscale*intDot. All four constants are float64: m2 and
// s2 are exact (a multiply by -2 is exact), t2 and cRow differ from the
// unfolded expansion by a few float64 ulps — the same argument as the C_row
// fold — far below the prefilterEps slack (1e-4 relative). (Storing them
// as float32 would NOT do: the rounding of t2, amplified by |qmin|, reaches
// ~1e-5 relative on mixed-magnitude data — still inside the bound's
// empirical headroom, but no longer a few ulps, so float64 it is.)
// TestFoldMatchesApproxDist2 validates the agreement empirically,
// including extreme magnitudes.
func foldQparam(qp qparam, dim int) (m2, t2, s2, cRow float64) {
	mn, s := float64(qp.min), float64(qp.scale)
	sumC := float64(qp.sumC)
	m2 = -2 * mn
	s2 = -2 * s
	t2 = -2 * s * sumC
	// cRow is the INSERT-time fold (qp.cRow), computed by quantizeRow with
	// the identical formula (dim*min² + 2*scale*min*sumC + scale²*sumC2).
	// The qparam no longer carries sumC2 (C_row fold), so cRow cannot be
	// recomputed here; taking qp.cRow is also the more accurate value,
	// since it was formed from the full float64 sumC/sumC2 rather than
	// the float32-rounded qp.sumC. dim is retained for signature
	// compatibility and is no longer used for cRow.
	cRow = float64(qp.cRow)
	return m2, t2, s2, cRow
}

// foldDist2 evaluates the folded dhat2 expansion above. It is the scalar
// reference for the fused scan's threshold pass: the pass must agree with
// approxDist2 to within a few ulps (see foldQparam).
func foldDist2(qSum, qNorm2 float64, qmin, qscale float32, intDot int32, m2, t2, s2, cRow float64) float64 {
	d := qNorm2 + m2*qSum + cRow + float64(qmin)*t2 + float64(qscale)*s2*float64(intDot)
	if d < 0 {
		d = 0
	}
	return d
}

// approxDist2 returns the approximate squared distance between the query
// (whose element sum and squared norm are qSum and qNorm2, both in float64
// from the widened query) and the row described by qp, expanded exactly
// from the kernel's dot product:
//
//	d̂² = Σ(q-min-scale*code)²
//	    = qNorm2 - 2*min*qSum + dim*min²
//	      - 2*scale*(dot - min*sumC) + scale²*sumC2
//	    = qNorm2 - 2*min*qSum - 2*scale*dot + cRow
//
// where cRow = dim*min² + 2*scale*min*sumC + scale²*sumC2 is folded once
// at INSERT time (see qparam). The folded form differs from the unfolded
// expansion only by float64 rounding of that one sum — a few ulps, far
// below the prefilterEps slack in the bound — so the threshold proof and
// the empirical bound validation (TestPrefilterBound) are unaffected.
func approxDist2(qSum, qNorm2 float64, dot float64, qp qparam) float64 {
	mn := float64(qp.min)
	s := float64(qp.scale)
	dhat2 := qNorm2 - 2*mn*qSum - 2*s*dot + qp.cRow
	if dhat2 < 0 {
		dhat2 = 0
	}
	return dhat2
}

// prefilterThreshold returns the squared-distance threshold for one scan
// batch: a row whose approximate squared distance exceeds it cannot enter
// TopK. It replaces the per-row error bound with a single square root per
// batch.
//
// Rigor: the caller passes the COMBINED scale S = maxScale + qscale,
// where maxScale is the largest per-row scale over the whole index
// (maintained at INSERT time; a stale larger value is safe because it
// only loosens the bound) and qscale is the query's quantization scale.
// The per-element error is (scale+qscale)/2: the row contributes
// |x-x̂| ≤ scale/2 and the query contributes |q-q̂| ≤ qscale/2, so with
// e = scale+qscale the Cauchy–Schwarz part of the bound satisfies
// b0 = e·√dim·√dhat2 + dim·e²/4 ≤ S·√dim·s + dim·S²/4 =: B0(s),
// where s = √dhat2 ≥ 0. The full bound is b0 + eps·(dhat2+b0+1), so
// exact d² ≥ dhat2 − b0 − eps·(dhat2+b0+1) ≥ g(s) with
// g(s) = (1−eps)·s² − (1+eps)·B0(s) − eps.
// g is a quadratic opening upward with g(0) < 0, so it has exactly one
// positive root s* (for the batch bound bd), and g(s) > bd ⟺ s > s*.
// Dropping rows with dhat2 > s*² is therefore exactly as safe as the
// per-row approxBound test: it can only ever admit extra rows, never drop
// a true TopK candidate (ties included: a row whose exact distance equals
// the bound has g(s) ≤ bd and is kept).
func prefilterThreshold(maxScale float32, dim int, bd float64) float64 {
	const eps = prefilterEps
	s := float64(maxScale)
	sqrtDim := math.Sqrt(float64(dim))
	a := (1 + eps) * s * sqrtDim
	// g(s) = bd ⟺ (1−eps)s² − a·s − [(1+eps)·dim·S²/4 + eps + bd] = 0.
	c := (1+eps)*float64(dim)*s*s/4 + eps + bd
	disc := a*a + 4*(1-eps)*c
	star := (a + math.Sqrt(disc)) / (2 * (1 - eps))
	return star * star
}

// approxBound returns the approximate squared distance (see approxDist2)
// together with a rigorous bound on |exact - approx|.
//
// Proof of the bound. Write x for the stored vector, x̂ = min+scale*code
// for its dequantization, q for the query, q̂ = qmin+qscale*qcode for its
// dequantization, e_x = x - x̂, e_q = q - q̂. Rounding gives
// |e_x,i| ≤ scale/2 and |e_q,i| ≤ qscale/2, so with e = scale+qscale:
//
//	d² - d̂² = Σ((e_q,i-e_x,i)·(2(q̂_i-x̂_i) + e_q,i-e_x,i))
//	|d² - d̂²| ≤ Σ(e·|q̂_i-x̂_i| + e²/4)
//	          ≤ e·√dim·d̂ + dim·e²/4     (Cauchy-Schwarz)
//
// (With qscale = 0 this is the row-only bound of the original float
// kernel.) The eps term (prefilterEps, relative) covers the float64
// rounding of the dhat2 expansion and the sub-ulp rounding of the
// quantization itself; the integer dot product is exact, so it contributes
// no rounding. The bound is validated empirically by TestPrefilterBound,
// including extreme magnitudes.
//
// A row is skipped only when d̂² - bound > the current TopK threshold,
// which the inequality shows implies exact > threshold. When any input is
// non-finite the bound is non-finite too, the comparison is false, and the
// row falls through to the exact computation, so the prefilter can only
// ever admit extra rows, never drop a true candidate.
func approxBound(qSum, qNorm2, sqrtDim float64, dim int, dot float64, qp qparam, qscale float32) (dhat2, bound float64) {
	dhat2 = approxDist2(qSum, qNorm2, dot, qp)
	e := float64(qp.scale) + float64(qscale)
	d := float64(dim)
	b0 := e*sqrtDim*math.Sqrt(dhat2) + d*e*e/4
	bound = b0 + prefilterEps*(dhat2+b0+1)
	return dhat2, bound
}
