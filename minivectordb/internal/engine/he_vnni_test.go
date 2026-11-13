package engine

// H-E: VPDPBUSD (AVX512_VNNI) fused scan kernel — correctness tests.
// The VNNI kernel must agree with the portable scalar reference
// (fusedScanInt8Scalar) on keep/drop decisions: its integer dot is
// bit-identical (exact arithmetic, bias folded into qminAdj), and only
// the dhat2 re-association differs, by a few float64 ulps — the same
// class of difference the existing AVX2 fused kernel already has vs
// the scalar reference.

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// heBiasQuery builds the VNNI inputs for a query: biased int8 codes
// (qc[i]-128) and the bias-absorbing qmin (qmin + 128*qscale).
func heBiasQuery(qc []int16, qmin, qscale float32) ([]int8, float64) {
	qc8 := make([]int8, len(qc))
	for i, c := range qc {
		qc8[i] = int8(c - 128)
	}
	return qc8, float64(qmin) + 128*float64(qscale)
}

func TestFusedScanVNNIVsScalar(t *testing.T) {
	if !hasAVX512VNNI() {
		t.Skip("no AVX512_VNNI on this CPU; VNNI kernel cannot run")
	}
	rng := rand.New(rand.NewSource(31))
	for _, dim := range []int{16, 17, 31, 32, 33, 64, 100, 128, 191, 192, 255, 256, 257, 512, 1024} {
		for _, rows := range []int{1, 3, 128, 129} {
			x := &VecIndex{dim: dim}
			x.pks = make([]int64, rows)
			x.off = make([]int64, rows)
			x.codes = make([]byte, rows*dim)
			x.qf = make([]float64, rows*4)
			for s := 0; s < rows; s++ {
				v := make([]float32, dim)
				for j := range v {
					v[j] = rng.Float32()
				}
				qp := quantizeRow(v, x.codes[s*dim:(s+1)*dim])
				x.qf[4*s], x.qf[4*s+1], x.qf[4*s+2], x.qf[4*s+3] = foldQparam(qp, dim)
				x.pks[s] = int64(1000 + s)
				x.off[s] = int64(s * 4096)
			}
			q := make([]float32, dim)
			for j := range q {
				q[j] = rng.Float32()
			}
			qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
			if !ok {
				t.Fatal("quantizeQuery rejected a finite query")
			}
			qc8, qminAdj := heBiasQuery(qc, qmin, qscale)
			for _, thresh := range []float64{5, 35, 1e6, math.Inf(1)} {
				survASM := make([]int64, 2*rows)
				survRef := make([]int64, 2*rows)
				nASM := fusedScanVNNI(qc8, x, 0, rows, qSum, qNorm2, qminAdj, float64(qscale), thresh, survASM)
				nRef := fusedScanInt8Scalar(qc, x, 0, rows, qSum, qNorm2, qmin, qscale, thresh, survRef)
				if nASM != nRef {
					t.Fatalf("dim %d rows %d thresh %g: VNNI kept %d, scalar kept %d",
						dim, rows, thresh, nASM, nRef)
				}
				for i := 0; i < 2*nASM; i++ {
					if survASM[i] != survRef[i] {
						t.Fatalf("dim %d rows %d thresh %g: survivor %d differs: VNNI %d, scalar %d",
							dim, rows, thresh, i, survASM[i], survRef[i])
					}
				}
			}
			// Non-zero base slot.
			if rows > 3 {
				survASM := make([]int64, 2*(rows-3))
				survRef := make([]int64, 2*(rows-3))
				nASM := fusedScanVNNI(qc8, x, 3, rows-3, qSum, qNorm2, qminAdj, float64(qscale), 35, survASM)
				nRef := fusedScanInt8Scalar(qc, x, 3, rows-3, qSum, qNorm2, qmin, qscale, 35, survRef)
				if nASM != nRef {
					t.Fatalf("dim %d rows %d baseSlot 3: VNNI kept %d, scalar kept %d",
						dim, rows, nASM, nRef)
				}
				for i := 0; i < 2*nASM; i++ {
					if survASM[i] != survRef[i] {
						t.Fatalf("dim %d rows %d baseSlot 3: survivor %d differs", dim, rows, i)
					}
				}
			}
		}
	}
}

// TestFusedScanVNNIAll255 pins the bias corner: query codes all 255
// (qc8 all 127) and row codes all 255. The biased dot per row is
// 127*255*dim and the bias term is 128*255*dim, so the true dot is
// exactly 255*255*dim; dhat2 must be finite and the threshold decision
// exact.
func TestFusedScanVNNIAll255(t *testing.T) {
	if !hasAVX512VNNI() {
		t.Skip("no AVX512_VNNI on this CPU")
	}
	const dim, rows = 256, 128
	x := &VecIndex{dim: dim}
	x.pks = make([]int64, rows)
	x.off = make([]int64, rows)
	x.codes = make([]byte, rows*dim)
	x.qf = make([]float64, rows*4)
	for s := 0; s < rows; s++ {
		for j := range x.codes[s*dim : (s+1)*dim] {
			x.codes[s*dim+j] = 255
		}
		// quantizeRow of an all-ones vector: min=1, scale=0, codes 0.
		// Overwrite codes with 255 and rebuild qf from a matching
		// qparam by hand: min=0, scale=1, sumC=255*dim, cRow=sumC2.
		sumC := float64(255 * dim)
		sumC2 := float64(255 * 255 * dim)
		qp := qparam{min: 0, scale: 1, sumC: float32(sumC), cRow: sumC2}
		x.qf[4*s], x.qf[4*s+1], x.qf[4*s+2], x.qf[4*s+3] = foldQparam(qp, dim)
		x.pks[s] = int64(1000 + s)
		x.off[s] = int64(s * 4096)
	}
	q := make([]float32, dim)
	for j := range q {
		q[j] = 1
	}
	qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
	if !ok {
		t.Fatal("quantizeQuery rejected the all-ones query")
	}
	// quantizeQuery of a constant query: qscale 0, qc all 0.
	if qscale != 0 {
		t.Fatalf("expected qscale 0 for a constant query, got %v", qscale)
	}
	qc8, qminAdj := heBiasQuery(qc, qmin, qscale)
	// With qscale 0 the bias term vanishes (qminAdj == qmin) and the
	// dot is 0 either way; force the interesting case by using qc all
	// 255 directly with qmin=0, qscale=1/255.
	for i := range qc {
		qc[i] = 255
	}
	qc8, qminAdj = heBiasQuery(qc, 0, 1.0/255.0)
	// Dequantized query is all ones: qSum=dim, qNorm2=dim.
	qSum, qNorm2 = float64(dim), float64(dim)
	survVNNI := make([]int64, 2*rows)
	survRef := make([]int64, 2*rows)
	// dhat2 here: row vector dequantizes to all 255, query to all 1,
	// so dhat2 = 256*(255-1)^2, huge; threshold 1e6 drops everything,
	// threshold +Inf keeps everything.
	for _, thresh := range []float64{1e6, math.Inf(1)} {
		nV := fusedScanVNNI(qc8, x, 0, rows, qSum, qNorm2, qminAdj, float64(1.0/255.0), thresh, survVNNI)
		nR := fusedScanInt8Scalar(qc, x, 0, rows, qSum, qNorm2, 0, 1.0/255.0, thresh, survRef)
		if nV != nR {
			t.Fatalf("thresh %g: VNNI kept %d, scalar kept %d", thresh, nV, nR)
		}
	}
}

// TestFusedScanVNNIEndToEnd runs TopK through vectorTopK (the VNNI
// dispatch in topk.go) and requires bit-identical results to the
// prefilter-off exact scan, mirroring TestFusedScanEndToEnd.
func TestFusedScanVNNIEndToEnd(t *testing.T) {
	if !hasAVX512VNNI() {
		t.Skip("no AVX512_VNNI on this CPU")
	}
	orig := prefilterEnabled
	defer func() { prefilterEnabled = orig }()
	db := open(t, t.TempDir())
	rng := rand.New(rand.NewSource(41))
	const dim, n = 256, 5000
	mustExec(t, db, fmt.Sprintf("CREATE TABLE d (id BIGINT PRIMARY KEY, cat VARCHAR(8), e VECTOR(%d))", dim))
	mustExec(t, db, "CREATE VECTOR INDEX vi ON d(e)")
	lit := func(v []float32) string {
		p := make([]string, len(v))
		for i, f := range v {
			p[i] = fmt.Sprint(f)
		}
		return "[" + strings.Join(p, ",") + "]"
	}
	var sb strings.Builder
	for s := 0; s < n; s += 1000 {
		sb.Reset()
		sb.WriteString("INSERT INTO d VALUES ")
		for i := s; i < s+1000; i++ {
			if i > s {
				sb.WriteByte(',')
			}
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32()
			}
			fmt.Fprintf(&sb, "(%d,'c%d','%s')", i+1, i%10, lit(v))
		}
		mustExec(t, db, sb.String())
	}
	before := fusedBatches.Load()
	for qn := 0; qn < 10; qn++ {
		q := make([]float32, dim)
		for j := range q {
			q[j] = rng.Float32()
		}
		k := []int{1, 10, 50}[qn%3]
		sql := fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS dist FROM d ORDER BY dist LIMIT %d", lit(q), k)
		prefilterEnabled = false
		want := mustExec(t, db, sql).Rows
		prefilterEnabled = true
		got := mustExec(t, db, sql).Rows
		if len(got) != len(want) {
			t.Fatalf("q%d: %d rows VNNI, %d exact", qn, len(got), len(want))
		}
		for i := range want {
			if got[i][0].I != want[i][0].I || math.Float64bits(got[i][1].F) != math.Float64bits(want[i][1].F) {
				t.Fatalf("q%d rank %d: VNNI (%d, %v) exact (%d, %v)", qn, i,
					got[i][0].I, got[i][1].F, want[i][0].I, want[i][1].F)
			}
		}
	}
	if fusedBatches.Load() == before {
		t.Fatalf("fused branch never ran on a clean table")
	}
}
