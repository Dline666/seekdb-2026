package engine

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// TestDotInt8KernelVsScalar checks the AVX2 dot-product kernel against the
// portable scalar loop. The two need not agree bit for bit (different
// summation orders); the tolerance is the float32 rounding scale, far below
// the prefilter bound's eps term.
func TestDotInt8KernelVsScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, dim := range []int{16, 17, 31, 32, 33, 63, 64, 100, 128, 255, 256, 257, 1024} {
		for _, rows := range []int{1, 3, 128} {
			q := make([]float32, dim)
			for i := range q {
				q[i] = float32(rng.NormFloat64())
			}
			codes := make([]byte, rows*dim)
			for i := range codes {
				codes[i] = byte(rng.Intn(256))
			}
			offs := make([]int64, rows)
			for r := range offs {
				offs[r] = int64(r * dim)
			}
			want := make([]float32, rows)
			dotInt8GatherScalar(q, codes, offs, want)
			got := make([]float32, rows)
			dotInt8Gather(q, codes, offs, got)
			for r := range want {
				tol := 1e-4 * (1 + math.Abs(float64(want[r])))
				if math.Abs(float64(got[r]-want[r])) > tol {
					t.Fatalf("dim %d rows %d row %d: kernel %v scalar %v", dim, rows, r, got[r], want[r])
				}
			}
		}
	}
	// The scalar fallback path (no SIMD) must also match.
	defer func() { useSIMD = hasAVX2FMA() }()
	useSIMD = false
	q := make([]float32, 100)
	codes := make([]byte, 3*100)
	for i := range q {
		q[i] = float32(rng.NormFloat64())
	}
	for i := range codes {
		codes[i] = byte(rng.Intn(256))
	}
	offs := []int64{0, 100, 200}
	want := make([]float32, 3)
	dotInt8GatherScalar(q, codes, offs, want)
	got := make([]float32, 3)
	dotInt8Gather(q, codes, offs, got)
	for r := range want {
		if math.Abs(float64(got[r]-want[r])) > 1e-4*(1+math.Abs(float64(want[r]))) {
			t.Fatalf("no-SIMD row %d: %v vs %v", r, got[r], want[r])
		}
	}
}

// TestDotInt8KernelIntVsScalar checks the integer dot-product kernel
// against the portable scalar integer loop (exact agreement: integer
// arithmetic has no rounding), and checks the dequantized integer dot
// against the float kernel's dot within the query-quantization error
// (qscale/2 per element, times the code sum). Dims include non-multiples
// of 32 to cover the scalar tail.
func TestDotInt8KernelIntVsScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, dim := range []int{16, 17, 31, 32, 33, 63, 64, 100, 128, 255, 256, 257, 1024} {
		for _, rows := range []int{1, 3, 128} {
			q := make([]float32, dim)
			for i := range q {
				q[i] = float32(rng.NormFloat64())
			}
			qmin, qscale, qc, _, _, ok := quantizeQuery(q)
			if !ok {
				t.Fatalf("dim %d: quantizeQuery rejected a finite query", dim)
			}
			codes := make([]byte, rows*dim)
			for i := range codes {
				codes[i] = byte(rng.Intn(256))
			}
			offs := make([]int64, rows)
			for r := range offs {
				offs[r] = int64(r * dim)
			}
			want := make([]int32, rows)
			dotInt8GatherIntScalar(qc, codes, offs, want)
			got := make([]int32, rows)
			dotInt8GatherInt(qc, codes, offs, got)
			for r := range want {
				if got[r] != want[r] {
					t.Fatalf("dim %d rows %d row %d: int kernel %v scalar %v", dim, rows, r, got[r], want[r])
				}
			}
			// Dequantized integer dot vs the float kernel's dot.
			fdot := make([]float32, rows)
			dotInt8Gather(q, codes, offs, fdot)
			for r := range want {
				var sumC float64
				for i := 0; i < dim; i++ {
					sumC += float64(codes[int64(r*dim)+int64(i)])
				}
				deq := float64(qmin)*sumC + float64(qscale)*float64(got[r])
				tol := float64(qscale)/2*sumC + 1e-3*(1+math.Abs(float64(fdot[r])))
				if math.Abs(deq-float64(fdot[r])) > tol {
					t.Fatalf("dim %d rows %d row %d: dequantized %v float %v tol %v", dim, rows, r, deq, fdot[r], tol)
				}
			}
		}
	}
	// The scalar fallback path (no SIMD) must also match exactly.
	defer func() { useSIMD = hasAVX2FMA() }()
	useSIMD = false
	_, _, qc, _, _, _ := quantizeQuery([]float32{0.5, -1.2, 3.3, 0.1, 2.2})
	codes := []byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	offs := []int64{0, 5}
	want := make([]int32, 2)
	dotInt8GatherIntScalar(qc, codes, offs, want)
	got := make([]int32, 2)
	dotInt8GatherInt(qc, codes, offs, got)
	for r := range want {
		if got[r] != want[r] {
			t.Fatalf("no-SIMD row %d: %v vs %v", r, got[r], want[r])
		}
	}
	// Non-finite queries are rejected.
	for _, bad := range [][]float32{
		{0, float32(math.NaN()), 1},
		{0, float32(math.Inf(1)), 1},
		{0, float32(math.Inf(-1)), 1},
	} {
		if _, _, _, _, _, ok := quantizeQuery(bad); ok {
			t.Fatalf("quantizeQuery accepted a non-finite query %v", bad)
		}
	}
}

// TestDotInt8KernelInt6VsScalar checks the 6-bit integer dot-product
// kernel (VPMADDUBSW) against the portable scalar loop. The dot is exact
// int32 arithmetic, so the two must agree bit for bit — including the
// unsigned/signed operand order of VPMADDUBSW, which a swapped order
// would get visibly wrong (row codes up to 255 read as signed bytes).
func TestDotInt8KernelInt6VsScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, dim := range []int{16, 17, 31, 32, 33, 63, 64, 100, 128, 255, 256, 257, 1024} {
		for _, rows := range []int{1, 3, 128} {
			q := make([]float32, dim)
			for i := range q {
				q[i] = float32(rng.NormFloat64())
			}
			qmin, qscale, qc, _, _, ok := quantizeQuery6(q)
			if !ok {
				t.Fatalf("dim %d: quantizeQuery6 rejected a finite query", dim)
			}
			codes := make([]byte, rows*dim)
			for i := range codes {
				codes[i] = byte(rng.Intn(256))
			}
			offs := make([]int64, rows)
			for r := range offs {
				offs[r] = int64(r * dim)
			}
			want := make([]int32, rows)
			dotInt8GatherInt6Scalar(qc, codes, offs, want)
			got := make([]int32, rows)
			dotInt8GatherInt6(qc, codes, offs, got)
			for r := range want {
				if got[r] != want[r] {
					t.Fatalf("dim %d rows %d row %d: 6-bit kernel %v scalar %v", dim, rows, r, got[r], want[r])
				}
			}
			// Dequantized 6-bit dot vs the float kernel's dot.
			fdot := make([]float32, rows)
			dotInt8Gather(q, codes, offs, fdot)
			for r := range want {
				var sumC float64
				for i := 0; i < dim; i++ {
					sumC += float64(codes[int64(r*dim)+int64(i)])
				}
				deq := float64(qmin)*sumC + float64(qscale)*float64(got[r])
				tol := float64(qscale)/2*sumC + 1e-3*(1+math.Abs(float64(fdot[r])))
				if math.Abs(deq-float64(fdot[r])) > tol {
					t.Fatalf("dim %d rows %d row %d: dequantized 6-bit %v float %v tol %v", dim, rows, r, deq, fdot[r], tol)
				}
			}
		}
	}
	// The scalar fallback path (no SIMD) must also match exactly.
	defer func() { useSIMD = hasAVX2FMA() }()
	useSIMD = false
	_, _, qc, _, _, _ := quantizeQuery6([]float32{0.5, -1.2, 3.3, 0.1, 2.2})
	codes := []byte{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	offs := []int64{0, 5}
	want := make([]int32, 2)
	dotInt8GatherInt6Scalar(qc, codes, offs, want)
	got := make([]int32, 2)
	dotInt8GatherInt6(qc, codes, offs, got)
	for r := range want {
		if got[r] != want[r] {
			t.Fatalf("no-SIMD row %d: %v vs %v", r, got[r], want[r])
		}
	}
	// Non-finite queries are rejected by quantizeQuery6 too.
	for _, bad := range [][]float32{
		{0, float32(math.NaN()), 1},
		{0, float32(math.Inf(1)), 1},
		{0, float32(math.Inf(-1)), 1},
	} {
		if _, _, _, _, _, ok := quantizeQuery6(bad); ok {
			t.Fatalf("quantizeQuery6 accepted a non-finite query %v", bad)
		}
	}
}

// TestDotInt8KernelIntAVX512VsAVX2 checks the AVX-512 integer dot kernel
// against the AVX2 kernel and the scalar loop. All three must agree bit
// for bit: integer addition is associative and the totals fit int32, so
// the different lane grouping of the 512-bit kernel cannot change the
// result. Dims include odd 64-block counts (65, 127, 129, 191, 255, 257)
// to cover the single-block tail of the unrolled AVX-512 loop, and
// non-multiples of 32 for the AVX2 tail. It also checks the dispatch
// fallback: with useAVX512 forced off, dotInt8GatherInt must produce the
// same result via the AVX2 kernel, and with both flags off via the scalar
// loop. Skipped on CPUs without AVX-512.
func TestDotInt8KernelIntAVX512VsAVX2(t *testing.T) {
	if !useAVX512 {
		t.Skip("CPU/OS lacks AVX-512, AVX-512 kernel not in use")
	}
	rng := rand.New(rand.NewSource(2))
	for _, dim := range []int{16, 17, 31, 32, 33, 63, 64, 65, 100, 127, 128, 129, 191, 192, 255, 256, 257, 1024} {
		for _, rows := range []int{1, 3, 128} {
			q := make([]float32, dim)
			for i := range q {
				q[i] = float32(rng.NormFloat64())
			}
			_, _, qc, _, _, ok := quantizeQuery(q)
			if !ok {
				t.Fatalf("dim %d: quantizeQuery rejected a finite query", dim)
			}
			codes := make([]byte, rows*dim)
			for i := range codes {
				codes[i] = byte(rng.Intn(256))
			}
			offs := make([]int64, rows)
			for r := range offs {
				offs[r] = int64(r * dim)
			}
			want := make([]int32, rows)
			dotInt8GatherIntScalar(qc, codes, offs, want)
			// Direct AVX-512 entry point.
			got512 := make([]int32, rows)
			if b := dim / 64; b > 0 {
				dotInt8GatherIntAVX512(&qc[0], &codes[0], &offs[0], b, rows, &got512[0])
				for i := b * 64; i < dim; i++ {
					for r, o := range offs {
						got512[r] += int32(qc[i]) * int32(codes[o+int64(i)])
					}
				}
			} else {
				dotInt8GatherIntScalar(qc, codes, offs, got512)
			}
			for r := range want {
				if got512[r] != want[r] {
					t.Fatalf("dim %d rows %d row %d: AVX-512 %v scalar %v", dim, rows, r, got512[r], want[r])
				}
			}
			// Direct AVX2 entry point (same result, different grouping).
			if useSIMD {
				got2 := make([]int32, rows)
				if b := dim / 32; b > 0 {
					dotInt8GatherIntAVX2(&qc[0], &codes[0], &offs[0], b, rows, &got2[0])
					for i := b * 32; i < dim; i++ {
						for r, o := range offs {
							got2[r] += int32(qc[i]) * int32(codes[o+int64(i)])
						}
					}
				} else {
					dotInt8GatherIntScalar(qc, codes, offs, got2)
				}
				for r := range want {
					if got2[r] != want[r] {
						t.Fatalf("dim %d rows %d row %d: AVX2 %v scalar %v", dim, rows, r, got2[r], want[r])
					}
				}
			}
		}
	}
	// Dispatch fallback: forcing useAVX512 off must give the identical
	// result through the AVX2 kernel, and forcing both off through the
	// scalar loop. Extreme values (all 255) exercise the largest totals.
	defer func() { useAVX512 = hasAVX512(); useSIMD = hasAVX2FMA() }()
	dim := 256
	qc := make([]int16, dim)
	for i := range qc {
		qc[i] = 255
	}
	codes := make([]byte, 3*dim)
	for i := range codes {
		codes[i] = 255
	}
	offs := []int64{0, int64(dim), 2 * int64(dim)}
	want := make([]int32, 3)
	dotInt8GatherInt(qc, codes, offs, want)
	useAVX512 = false
	got2 := make([]int32, 3)
	dotInt8GatherInt(qc, codes, offs, got2)
	for r := range want {
		if got2[r] != want[r] {
			t.Fatalf("AVX2 fallback row %d: %v vs AVX-512 %v", r, got2[r], want[r])
		}
	}
	useSIMD = false
	got0 := make([]int32, 3)
	dotInt8GatherInt(qc, codes, offs, got0)
	for r := range want {
		if got0[r] != want[r] {
			t.Fatalf("scalar fallback row %d: %v vs AVX-512 %v", r, got0[r], want[r])
		}
	}
}

// TestHasAVX512EnvOverride checks that MVDB_NO_AVX512=1 disables the
// AVX-512 dispatch (the fallback-testing switch) and that, with it unset,
// the probe agrees with the package-level flag on this machine.
func TestHasAVX512EnvOverride(t *testing.T) {
	t.Setenv("MVDB_NO_AVX512", "1")
	if hasAVX512() {
		t.Fatalf("hasAVX512 true with MVDB_NO_AVX512=1")
	}
	t.Setenv("MVDB_NO_AVX512", "")
	if hasAVX512() != useAVX512 {
		t.Fatalf("hasAVX512() %v != useAVX512 %v with env unset", hasAVX512(), useAVX512)
	}
}

// TestPrefilterBound validates the error bound empirically: for ~200k
// (stored vector, query) pairs across distributions and magnitudes, the
// exact squared distance and the approximate one must satisfy
// |exact - approx| <= bound, with margin. Cases include the cancellation
// regime (query almost equal to a stored vector, where the true distance is
// near zero and the bound must still hold) and extreme magnitudes.
func TestPrefilterBound(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	type dist struct {
		name string
		gen  func() float32
	}
	dists := []dist{
		{"uniform01", func() float32 { return float32(rng.Float64()) }},
		{"normal1", func() float32 { return float32(rng.NormFloat64()) }},
		{"normal1e3", func() float32 { return float32(rng.NormFloat64() * 1e3) }},
		{"normal1e-3", func() float32 { return float32(rng.NormFloat64() * 1e-3) }},
		{"mixed", func() float32 {
			return float32(rng.NormFloat64() * math.Pow(10, float64(rng.Intn(7)-3)))
		}},
	}
	var worstRatio float64
	var worstDesc string
	cases := 0
	check := func(tag string, dim int, v, q []float32) {
		codes := make([]byte, dim)
		qp := quantizeRow(v, codes)
		qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
		if !ok {
			t.Fatalf("%s dim %d: quantizeQuery rejected a finite query", tag, dim)
		}
		// Exact squared distance in float64.
		var exact float64
		for i := range q {
			d := float64(q[i]) - float64(v[i])
			exact += d * d
		}
		// Approximate distance through the real code path (integer
		// kernel dot + dequantization + Go-side expansion).
		dot := make([]int32, 1)
		dotInt8GatherInt(qc, codes, []int64{0}, dot)
		deq := float64(qmin)*float64(qp.sumC) + float64(qscale)*float64(dot[0])
		dhat2, b := approxBound(qSum, qNorm2, math.Sqrt(float64(dim)), dim, deq, qp, qscale)
		if math.Abs(exact-dhat2) > b {
			t.Fatalf("%s dim %d: |exact-approx|=%g > bound=%g (exact=%g approx=%g)",
				tag, dim, math.Abs(exact-dhat2), b, exact, dhat2)
		}
		if b > 0 {
			if r := math.Abs(exact-dhat2) / b; r > worstRatio {
				worstRatio, worstDesc = r, fmt.Sprintf("%s dim %d", tag, dim)
			}
		}
		cases++
	}
	for _, dim := range []int{16, 31, 128, 256} {
		for _, dd := range dists {
			for i := 0; i < 5000; i++ {
				v := make([]float32, dim)
				q := make([]float32, dim)
				for j := range v {
					v[j] = dd.gen()
					q[j] = dd.gen()
				}
				check(dd.name, dim, v, q)
			}
		}
		// Cancellation: query almost equal to the stored vector.
		for i := 0; i < 2000; i++ {
			v := make([]float32, dim)
			for j := range v {
				v[j] = float32(rng.Float64())
			}
			q := make([]float32, dim)
			for j := range q {
				q[j] = v[j] + float32(rng.NormFloat64()*1e-3)
			}
			check("near-dup", dim, v, q)
			copy(q, v)
			check("exact-dup", dim, v, q)
		}
		// Constant stored vector (scale 0) and a large-magnitude query.
		for i := 0; i < 500; i++ {
			c := float32(rng.NormFloat64() * 100)
			v := make([]float32, dim)
			for j := range v {
				v[j] = c
			}
			q := make([]float32, dim)
			for j := range q {
				q[j] = float32(rng.NormFloat64() * 1000)
			}
			check("constant", dim, v, q)
		}
	}
	// Extreme magnitudes.
	for _, dim := range []int{128, 256} {
		for i := 0; i < 2000; i++ {
			v := make([]float32, dim)
			q := make([]float32, dim)
			for j := range v {
				v[j] = float32(rng.NormFloat64() * 1e6)
				q[j] = float32(rng.NormFloat64() * 1e6)
			}
			check("huge", dim, v, q)
			for j := range v {
				v[j] = float32(rng.NormFloat64() * 1e-6)
				q[j] = float32(rng.NormFloat64() * 1e-6)
			}
			check("tiny", dim, v, q)
		}
	}
	t.Logf("%d cases, worst |exact-approx|/bound = %.3f (%s)", cases, worstRatio, worstDesc)
	if worstRatio > 0.5 {
		t.Fatalf("bound margin too thin: worst ratio %.3f", worstRatio)
	}
	// Tightness: on uniform [0,1) dim 256 data the bound must actually
	// filter. With 1000 rows, the 10th distance is ~31 and the mean ~42.7;
	// rows whose approx distance minus the bound exceeds the 10th distance
	// are skipped, which should be the large majority.
	{
		const dim, n = 256, 1000
		rows := make([][]float32, n)
		codes := make([]byte, n*dim)
		qps := make([]qparam, n)
		for i := range rows {
			rows[i] = make([]float32, dim)
			for j := range rows[i] {
				rows[i][j] = float32(rng.Float64())
			}
			qps[i] = quantizeRow(rows[i], codes[i*dim:(i+1)*dim])
		}
		q := make([]float32, dim)
		for j := range q {
			q[j] = float32(rng.Float64())
		}
		qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
		if !ok {
			t.Fatal("quantizeQuery rejected a finite query")
		}
		exact := make([]float64, n)
		for i, v := range rows {
			for j := range v {
				d := float64(q[j]) - float64(v[j])
				exact[i] += d * d
			}
		}
		sorted := append([]float64(nil), exact...)
		sort.Float64s(sorted)
		th := sorted[9]
		dots := make([]int32, n)
		offs := make([]int64, n)
		for i := range offs {
			offs[i] = int64(i * dim)
		}
		dotInt8GatherInt(qc, codes, offs, dots)
		skipped := 0
		for i := range rows {
			deq := float64(qmin)*float64(qps[i].sumC) + float64(qscale)*float64(dots[i])
			dhat2, b := approxBound(qSum, qNorm2, math.Sqrt(float64(dim)), dim, deq, qps[i], qscale)
			if dhat2-b > th {
				skipped++
			}
		}
		t.Logf("uniform dim 256: 10th distance %.2f, prefilter skips %d/%d rows", th, skipped, n)
		if skipped < 900 {
			t.Fatalf("prefilter too loose: skipped only %d/%d rows", skipped, n)
		}
	}
}

// TestPrefilterMatchesExactScan runs the same TopK queries with the int8
// prefilter on and off and requires identical results, down to the bits of
// every distance, plus agreement with a brute-force reference. It covers
// dimensions with and without a scalar tail, deleted and updated rows,
// WHERE filters with and without the equality candidate set, an open
// transaction, ties, and both the serial and the parallel scan.
// TestPrefilterThreshold validates the per-batch threshold that replaces
// the per-row error bound in the scan: for random rows and queries across
// distributions and magnitudes, a row the threshold drops must satisfy
// exact > bd (the true safety condition), and the threshold must never
// drop a row the per-row approxBound test would keep. The threshold is
// looser by construction (it uses the largest scale over the whole index
// instead of the row's own), so it drops a subset of what approxBound
// drops; the skip rates are logged for the report.
// TestPrefilterBound6Bit is the 6-bit-query variant of TestPrefilterBound:
// the same empirical bound check, but the query is quantized to 6-bit
// codes (quantizeQuery6) and the dot comes from the VPMADDUBSW kernel, so
// the bound uses the larger 6-bit qscale. The bound must still hold with
// margin on every case.
func TestPrefilterBound6Bit(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	type dist struct {
		name string
		gen  func() float32
	}
	dists := []dist{
		{"uniform01", func() float32 { return float32(rng.Float64()) }},
		{"normal1", func() float32 { return float32(rng.NormFloat64()) }},
		{"normal1e3", func() float32 { return float32(rng.NormFloat64() * 1e3) }},
		{"normal1e-3", func() float32 { return float32(rng.NormFloat64() * 1e-3) }},
		{"mixed", func() float32 {
			return float32(rng.NormFloat64() * math.Pow(10, float64(rng.Intn(7)-3)))
		}},
	}
	var worstRatio float64
	var worstDesc string
	cases := 0
	check := func(tag string, dim int, v, q []float32) {
		codes := make([]byte, dim)
		qp := quantizeRow(v, codes)
		qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery6(q)
		if !ok {
			t.Fatalf("%s dim %d: quantizeQuery6 rejected a finite query", tag, dim)
		}
		var exact float64
		for i := range q {
			d := float64(q[i]) - float64(v[i])
			exact += d * d
		}
		dot := make([]int32, 1)
		dotInt8GatherInt6(qc, codes, []int64{0}, dot)
		deq := float64(qmin)*float64(qp.sumC) + float64(qscale)*float64(dot[0])
		dhat2, b := approxBound(qSum, qNorm2, math.Sqrt(float64(dim)), dim, deq, qp, qscale)
		if math.Abs(exact-dhat2) > b {
			t.Fatalf("%s dim %d: |exact-approx|=%g > bound=%g (exact=%g approx=%g)",
				tag, dim, math.Abs(exact-dhat2), b, exact, dhat2)
		}
		if b > 0 {
			if r := math.Abs(exact-dhat2) / b; r > worstRatio {
				worstRatio, worstDesc = r, fmt.Sprintf("%s dim %d", tag, dim)
			}
		}
		cases++
	}
	for _, dim := range []int{16, 31, 128, 256} {
		for _, dd := range dists {
			for i := 0; i < 2000; i++ {
				v := make([]float32, dim)
				q := make([]float32, dim)
				for j := range v {
					v[j] = dd.gen()
					q[j] = dd.gen()
				}
				check(dd.name, dim, v, q)
			}
		}
		for i := 0; i < 1000; i++ {
			v := make([]float32, dim)
			for j := range v {
				v[j] = float32(rng.Float64())
			}
			q := make([]float32, dim)
			for j := range q {
				q[j] = v[j] + float32(rng.NormFloat64()*1e-3)
			}
			check("near-dup", dim, v, q)
			copy(q, v)
			check("exact-dup", dim, v, q)
		}
	}
	t.Logf("6-bit bound validation: %d cases, worst |exact-approx|/bound = %.3f (%s)", cases, worstRatio, worstDesc)
	if worstRatio >= 1 {
		t.Fatalf("worst ratio %.3f >= 1", worstRatio)
	}
}

func TestPrefilterThreshold(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	type dist struct {
		name string
		gen  func() float32
	}
	dists := []dist{
		{"uniform01", func() float32 { return float32(rng.Float64()) }},
		{"normal1", func() float32 { return float32(rng.NormFloat64()) }},
		{"normal1e3", func() float32 { return float32(rng.NormFloat64() * 1e3) }},
		{"mixed", func() float32 {
			return float32(rng.NormFloat64() * math.Pow(10, float64(rng.Intn(7)-3)))
		}},
	}
	for _, dim := range []int{16, 31, 128, 256} {
		for _, dd := range dists {
			const rows = 2000
			vs := make([][]float32, rows)
			q := make([]float32, dim)
			for j := range q {
				q[j] = dd.gen()
			}
			var qSum, qNorm2 float64
			qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
			if !ok {
				t.Fatalf("%s dim %d: quantizeQuery rejected a finite query", dd.name, dim)
			}
			codes := make([]byte, rows*dim)
			qps := make([]qparam, rows)
			var maxScale float32
			exact := make([]float64, rows)
			for i := range vs {
				vs[i] = make([]float32, dim)
				for j := range vs[i] {
					vs[i][j] = dd.gen()
				}
				qps[i] = quantizeRow(vs[i], codes[i*dim:(i+1)*dim])
				if qps[i].scale > maxScale {
					maxScale = qps[i].scale
				}
				for j := range q {
					d := float64(q[j]) - float64(vs[i][j])
					exact[i] += d * d
				}
			}
			// Dots through the real kernel, one batch.
			coffs := make([]int64, rows)
			for i := range coffs {
				coffs[i] = int64(i * dim)
			}
			dots := make([]int32, rows)
			dotInt8GatherInt(qc, codes, coffs, dots)
			// Batch bound: the 10th smallest exact distance, as a TopK
			// heap bound would be once full; plus the median for the
			// loose regime.
			sorted := append([]float64(nil), exact...)
			sort.Float64s(sorted)
			for _, bd := range []float64{sorted[9], sorted[rows/2]} {
				thresh2 := prefilterThreshold(maxScale+qscale, dim, bd)
				var thrDrop, boundDrop int
				for i := 0; i < rows; i++ {
					deq := float64(qmin)*float64(qps[i].sumC) + float64(qscale)*float64(dots[i])
					dhat2 := approxDist2(qSum, qNorm2, deq, qps[i])
					_, b := approxBound(qSum, qNorm2, math.Sqrt(float64(dim)), dim, deq, qps[i], qscale)
					boundDrops := dhat2-b > bd
					if boundDrops {
						boundDrop++
					}
					if dhat2 > thresh2 {
						thrDrop++
						if exact[i] <= bd {
							t.Fatalf("%s dim %d bd %g: threshold dropped row %d with exact %g <= bd (dhat2 %g thresh2 %g)",
								dd.name, dim, bd, i, exact[i], dhat2, thresh2)
						}
						if !boundDrops {
							t.Fatalf("%s dim %d bd %g: threshold dropped row %d that approxBound keeps (dhat2 %g bound %g)",
								dd.name, dim, bd, i, dhat2, b)
						}
					}
				}
				t.Logf("%s dim %d bd %.3g: threshold drops %d/%d, approxBound drops %d/%d",
					dd.name, dim, bd, thrDrop, rows, boundDrop, rows)
			}
		}
	}
}

func TestPrefilterMatchesExactScan(t *testing.T) {
	orig := prefilterEnabled
	defer func() { prefilterEnabled, batchScan, parallelMinSlots = orig, true, 4096 }()
	for _, dim := range []int{16, 17, 31, 48, 128, 256} {
		for _, par := range []int{1 << 30, 1} {
			parallelMinSlots = par
			testPrefilterScan(t, dim)
		}
	}
}

func testPrefilterScan(t *testing.T, dim int) {
	db := open(t, t.TempDir())
	rng := rand.New(rand.NewSource(int64(dim) + 1000))
	const n = 400
	cats := []string{"a", "bb", "ccccccc", "你好", ""}
	type row struct {
		cat string
		v   []float32
	}
	rows := map[int64]row{}
	lit := func(v []float32) string {
		p := make([]string, len(v))
		for i, f := range v {
			p[i] = fmt.Sprint(f)
		}
		return "[" + strings.Join(p, ",") + "]"
	}
	vec := func() []float32 {
		v := make([]float32, dim)
		coarse := rng.Intn(3) == 0 // many exact ties
		for i := range v {
			if coarse {
				v[i] = float32(rng.Intn(3))
			} else {
				v[i] = float32(rng.NormFloat64() * math.Pow(10, float64(rng.Intn(5)-2)))
			}
		}
		return v
	}
	mustExec(t, db, fmt.Sprintf("CREATE TABLE d (id BIGINT PRIMARY KEY, cat VARCHAR(8), n BIGINT, e VECTOR(%d))", dim))
	if dim%2 == 0 {
		mustExec(t, db, "CREATE VECTOR INDEX vi ON d(e)")
	}
	ins := func(id int64) {
		r := row{cats[rng.Intn(len(cats))], vec()}
		rows[id] = r
		mustExec(t, db, fmt.Sprintf("INSERT INTO d VALUES (%d,'%s',%d,'%s')", id, r.cat, id%7, lit(r.v)))
	}
	for i := int64(1); i <= n; i++ {
		ins(i)
	}
	// A first query materializes the implicit index on tables without an
	// explicit one (odd dims); the prefilter must be armed after it.
	mustExec(t, db, fmt.Sprintf("SELECT id FROM d ORDER BY L2_DISTANCE(e, '%s') LIMIT 1", lit(make([]float32, dim))))
	// The prefilter must actually be armed: codes exist for every slot.
	idx := db.tables["d"].Index
	if idx == nil || idx.codes == nil {
		t.Fatalf("dim %d: prefilter codes not built", dim)
	}
	if len(idx.codes) != len(idx.pks)*dim || len(idx.qf) != len(idx.pks)*4 {
		t.Fatalf("dim %d: codes %d qf %d for %d slots", dim, len(idx.codes), len(idx.qf), len(idx.pks))
	}
	for i := 0; i < 60; i++ {
		id := int64(rng.Intn(n) + 1)
		if _, ok := rows[id]; !ok {
			continue
		}
		if i%3 == 0 {
			mustExec(t, db, fmt.Sprintf("DELETE FROM d WHERE id = %d", id))
			delete(rows, id)
		} else {
			r := row{cats[rng.Intn(len(cats))], vec()}
			rows[id] = r
			mustExec(t, db, fmt.Sprintf("UPDATE d SET cat = '%s', e = '%s' WHERE id = %d", r.cat, lit(r.v), id))
		}
	}
	wheres := []string{"", "", " WHERE cat = 'a'", " WHERE cat = '你好'", " WHERE cat = 'none'",
		" WHERE cat = 'bb' AND n > 2", " WHERE n < 3", " WHERE cat <> 'a'"}
	check := func(tag string) {
		for qn := 0; qn < 20; qn++ {
			q := vec()
			if qn%5 == 0 {
				for _, r := range rows { // a query equal to a stored vector
					q = append([]float32(nil), r.v...)
					break
				}
			}
			k := []int{1, 10, 10, 37, 2000}[qn%5]
			sql := fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS dist FROM d%s ORDER BY dist LIMIT %d",
				lit(q), wheres[qn%len(wheres)], k)
			prefilterEnabled = false
			want := mustExec(t, db, sql).Rows
			prefilterEnabled = true
			got := mustExec(t, db, sql).Rows
			if len(got) != len(want) {
				t.Fatalf("dim %d %s: %s: %d rows prefiltered, %d exact", dim, tag, sql[:60], len(got), len(want))
			}
			for i := range want {
				if got[i][0].I != want[i][0].I || math.Float64bits(got[i][1].F) != math.Float64bits(want[i][1].F) {
					t.Fatalf("dim %d %s: rank %d prefiltered (%d, %v) exact (%d, %v)", dim, tag, i,
						got[i][0].I, got[i][1].F, want[i][0].I, want[i][1].F)
				}
			}
			// Brute force: ids in (distance, id) order.
			type c struct {
				d  float64
				id int64
			}
			var ref []c
			for id, r := range rows {
				if !whereKeeps(wheres[qn%len(wheres)], id, r.cat) {
					continue
				}
				var s float64
				for i := range q {
					x := float64(q[i]) - float64(r.v[i])
					s += x * x
				}
				ref = append(ref, c{s, id})
			}
			sort.Slice(ref, func(i, j int) bool {
				if ref[i].d != ref[j].d {
					return ref[i].d < ref[j].d
				}
				return ref[i].id < ref[j].id
			})
			if len(ref) > k {
				ref = ref[:k]
			}
			if len(ref) != len(got) {
				t.Fatalf("dim %d %s: %d rows, brute force %d", dim, tag, len(got), len(ref))
			}
			for i := range ref {
				if got[i][0].I != ref[i].id || math.Abs(got[i][1].F-math.Sqrt(ref[i].d)) > 1e-9*math.Max(1, math.Sqrt(ref[i].d)) {
					t.Fatalf("dim %d %s: rank %d got (%d, %v) brute force (%d, %v)", dim, tag, i,
						got[i][0].I, got[i][1].F, ref[i].id, math.Sqrt(ref[i].d))
				}
			}
		}
	}
	check("committed")
	mustExec(t, db, "BEGIN")
	for i := 0; i < 30; i++ {
		id := int64(rng.Intn(n+40) + 1)
		if _, ok := rows[id]; ok && i%3 == 0 {
			mustExec(t, db, fmt.Sprintf("DELETE FROM d WHERE id = %d", id))
			delete(rows, id)
		} else if ok {
			r := row{cats[rng.Intn(len(cats))], vec()}
			rows[id] = r
			mustExec(t, db, fmt.Sprintf("UPDATE d SET cat = '%s', e = '%s' WHERE id = %d", r.cat, lit(r.v), id))
		} else {
			ins(id)
		}
	}
	check("in transaction")
	mustExec(t, db, "ROLLBACK")
}

// TestPrefilterLargeLine is the >1 MiB single-line regression test: a dim
// 1024, 128-row INSERT is one ~1.3 MB statement, exercising the ReadSlice
// long-line fix, and the following TopK query runs with the prefilter on.
func TestPrefilterLargeLine(t *testing.T) {
	orig := prefilterEnabled
	defer func() { prefilterEnabled = orig }()
	db := open(t, t.TempDir())
	const dim, n = 1024, 128
	rng := rand.New(rand.NewSource(99))
	mustExec(t, db, fmt.Sprintf("CREATE TABLE d (id BIGINT PRIMARY KEY, e VECTOR(%d))", dim))
	mustExec(t, db, "CREATE VECTOR INDEX vi ON d(e)")
	rows := make([][]float32, n)
	var sb strings.Builder
	sb.WriteString("INSERT INTO d VALUES ")
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		v := make([]float32, dim)
		for j := range v {
			v[j] = float32(rng.NormFloat64())
		}
		rows[i] = v
		sb.WriteString(fmt.Sprintf("(%d,'[", i+1))
		for j, f := range v {
			if j > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "%.9f", f)
		}
		sb.WriteString("]')")
	}
	sql := sb.String()
	if len(sql) <= 1<<20 {
		t.Fatalf("test setup: single-line INSERT is only %d bytes, want >1 MiB", len(sql))
	}
	mustExec(t, db, sql)
	idx := db.tables["d"].Index
	if idx == nil || idx.codes == nil || len(idx.codes) != n*dim {
		t.Fatalf("prefilter codes not built for the large-line table")
	}
	q := make([]float32, dim)
	for j := range q {
		q[j] = float32(rng.NormFloat64())
	}
	lit := func(v []float32) string {
		p := make([]string, len(v))
		for i, f := range v {
			p[i] = fmt.Sprint(f)
		}
		return "[" + strings.Join(p, ",") + "]"
	}
	prefilterEnabled = true
	got := mustExec(t, db, fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS dist FROM d ORDER BY dist LIMIT 10", lit(q))).Rows
	prefilterEnabled = false
	want := mustExec(t, db, fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS dist FROM d ORDER BY dist LIMIT 10", lit(q))).Rows
	if len(got) != len(want) {
		t.Fatalf("%d rows prefiltered, %d exact", len(got), len(want))
	}
	for i := range want {
		if got[i][0].I != want[i][0].I || math.Float64bits(got[i][1].F) != math.Float64bits(want[i][1].F) {
			t.Fatalf("rank %d prefiltered (%d, %v) exact (%d, %v)", i, got[i][0].I, got[i][1].F, want[i][0].I, want[i][1].F)
		}
	}
}

// BenchmarkDotInt8Gather measures the kernel on the benchmark shape.
func BenchmarkDotInt8Gather(b *testing.B) {
	if !useSIMD {
		b.Skip("no SIMD kernel")
	}
	const n, dim = 10000, 256
	rng := rand.New(rand.NewSource(1))
	q := make([]float32, dim)
	for i := range q {
		q[i] = float32(rng.Float64())
	}
	codes := make([]byte, n*dim)
	for i := range codes {
		codes[i] = byte(rng.Intn(256))
	}
	offs := make([]int64, n)
	for i := range offs {
		offs[i] = int64(i * dim)
	}
	out := make([]float32, n)
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		dotInt8Gather(q, codes, offs, out)
	}
}

// TestFoldMatchesApproxDist2 validates the folded threshold expansion
// (foldQparam/foldDist2, the fused scan's operand format) against the
// unfolded approxDist2: the two must agree to a few ulps, and the folded
// value must still satisfy the approxBound error bound on every case, so
// the threshold test built on it drops exactly the rows approxBound would
// drop (up to ulps). Covers constant rows (scale 0) and extreme magnitudes.
func TestFoldMatchesApproxDist2(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	type dist struct {
		name string
		gen  func() float32
	}
	dists := []dist{
		{"uniform01", func() float32 { return float32(rng.Float64()) }},
		{"normal1", func() float32 { return float32(rng.NormFloat64()) }},
		{"normal1e3", func() float32 { return float32(rng.NormFloat64() * 1e3) }},
		{"normal1e-3", func() float32 { return float32(rng.NormFloat64() * 1e-3) }},
		{"mixed", func() float32 {
			return float32(rng.NormFloat64() * math.Pow(10, float64(rng.Intn(7)-3)))
		}},
	}
	var worstRel float64
	var worstDesc string
	cases := 0
	check := func(tag string, dim int, v, q []float32) {
		codes := make([]byte, dim)
		qp := quantizeRow(v, codes)
		m2, t2, s2, cRow := foldQparam(qp, dim)
		qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
		if !ok {
			t.Fatalf("%s dim %d: quantizeQuery rejected a finite query", tag, dim)
		}
		var exact float64
		for i := range q {
			d := float64(q[i]) - float64(v[i])
			exact += d * d
		}
		dot := make([]int32, 1)
		dotInt8GatherInt(qc, codes, []int64{0}, dot)
		deq := float64(qmin)*float64(qp.sumC) + float64(qscale)*float64(dot[0])
		unfolded := approxDist2(qSum, qNorm2, deq, qp)
		folded := foldDist2(qSum, qNorm2, qmin, qscale, dot[0], m2, t2, s2, cRow)
		scale := math.Max(math.Abs(unfolded), 1)
		if r := math.Abs(folded-unfolded) / scale; r > worstRel {
			worstRel, worstDesc = r, fmt.Sprintf("%s dim %d", tag, dim)
		}
		// The folded value must satisfy the same error bound: the
		// threshold test built on it is then as safe as approxBound.
		_, b := approxBound(qSum, qNorm2, math.Sqrt(float64(dim)), dim, deq, qp, qscale)
		if math.Abs(exact-folded) > b {
			t.Fatalf("%s dim %d: |exact-folded|=%g > bound=%g (exact=%g folded=%g unfolded=%g)",
				tag, dim, math.Abs(exact-folded), b, exact, folded, unfolded)
		}
		cases++
	}
	for _, dim := range []int{16, 31, 128, 256} {
		for _, dd := range dists {
			for i := 0; i < 3000; i++ {
				v := make([]float32, dim)
				q := make([]float32, dim)
				for j := range v {
					v[j] = dd.gen()
					q[j] = dd.gen()
				}
				check(dd.name, dim, v, q)
			}
		}
		for i := 0; i < 500; i++ {
			c := float32(rng.NormFloat64() * 100)
			v := make([]float32, dim)
			for j := range v {
				v[j] = c
			}
			q := make([]float32, dim)
			for j := range q {
				q[j] = float32(rng.NormFloat64() * 1000)
			}
			check("constant", dim, v, q)
		}
	}
	for _, dim := range []int{128, 256} {
		for i := 0; i < 1000; i++ {
			v := make([]float32, dim)
			q := make([]float32, dim)
			for j := range v {
				v[j] = float32(rng.NormFloat64() * 1e6)
				q[j] = float32(rng.NormFloat64() * 1e6)
			}
			check("huge", dim, v, q)
			for j := range v {
				v[j] = float32(rng.NormFloat64() * 1e-6)
				q[j] = float32(rng.NormFloat64() * 1e-6)
			}
			check("tiny", dim, v, q)
		}
	}
	t.Logf("%d cases, worst |folded-unfolded|/scale = %.3g (%s)", cases, worstRel, worstDesc)
	if worstRel > 1e-12 {
		t.Fatalf("fold disagrees with unfolded expansion: worst relative diff %.3g", worstRel)
	}
}
