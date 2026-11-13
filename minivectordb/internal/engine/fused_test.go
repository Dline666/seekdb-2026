package engine

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// TestFusedScanVsScalar checks the fused AVX2 kernel against the portable
// scalar reference: identical survivor sets (log offset + primary key,
// slot order) across dimensions (including tails: dim%32 != 0), row counts
// and thresholds. The two must agree bit for bit — dhat2 uses the same
// association order as foldDist2 in both paths.
func TestFusedScanVsScalar(t *testing.T) {
	old := useSIMD
	useSIMD = true
	defer func() { useSIMD = old }()
	if !hasAVX2FMA() {
		t.Skip("no AVX2+FMA on this CPU; asm path cannot run")
	}
	rng := rand.New(rand.NewSource(31))
	for _, dim := range []int{16, 17, 31, 32, 33, 64, 100, 128, 255, 256, 257, 1024} {
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
			for _, thresh := range []float64{5, 35, 1e6, math.Inf(1)} {
				survASM := make([]int64, 2*rows)
				survRef := make([]int64, 2*rows)
				nASM := fusedScanInt8(qc, x, 0, rows, qSum, qNorm2, qmin, qscale, thresh, survASM)
				nRef := fusedScanInt8Scalar(qc, x, 0, rows, qSum, qNorm2, qmin, qscale, thresh, survRef)
				if nASM != nRef {
					t.Fatalf("dim %d rows %d thresh %g: asm kept %d, scalar kept %d",
						dim, rows, thresh, nASM, nRef)
				}
				for i := 0; i < 2*nASM; i++ {
					if survASM[i] != survRef[i] {
						t.Fatalf("dim %d rows %d thresh %g: survivor %d differs: asm %d, scalar %d",
							dim, rows, thresh, i, survASM[i], survRef[i])
					}
				}
			}
			// Non-zero base slot: the asm cursors must start at baseSlot.
			if rows > 3 {
				survASM := make([]int64, 2*(rows-3))
				survRef := make([]int64, 2*(rows-3))
				nASM := fusedScanInt8(qc, x, 3, rows-3, qSum, qNorm2, qmin, qscale, 35, survASM)
				nRef := fusedScanInt8Scalar(qc, x, 3, rows-3, qSum, qNorm2, qmin, qscale, 35, survRef)
				if nASM != nRef {
					t.Fatalf("dim %d rows %d baseSlot 3: asm kept %d, scalar kept %d",
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

// TestFusedScanRawAsm calls the raw asm entry point directly (forced SIMD),
// including the all-255-codes / all-255-query-codes corner, where the dot
// is exactly 255*255*dim per row and the threshold decision is exact.
func TestFusedScanRawAsm(t *testing.T) {
	if !hasAVX2FMA() {
		t.Skip("no AVX2+FMA on this CPU")
	}
	const dim, rows = 256, 4
	x := &VecIndex{dim: dim}
	x.pks = []int64{11, 22, 33, 44}
	x.off = []int64{100, 200, 300, 400}
	x.codes = make([]byte, rows*dim)
	x.qf = make([]float64, rows*4)
	for s := 0; s < rows; s++ {
		for j := range x.codes[s*dim : (s+1)*dim] {
			x.codes[s*dim+j] = 255
		}
		// m2=t2=s2=0, cRow=0: dhat2 = qNorm2 exactly.
		x.qf[4*s], x.qf[4*s+1], x.qf[4*s+2], x.qf[4*s+3] = 0, 0, 0, 0
	}
	qc := make([]int16, dim)
	for i := range qc {
		qc[i] = 255
	}
	// qNorm2 = dim (dequantized query all ones: qmin=0,qscale=1/255? No:
	// pass qSum/qNorm2 directly: with qmin=0, qscale=0 the dot is unused.
	// Set qNorm2 = 10, thresh2 = 10: dhat2 = 10 <= 10 keeps every row.
	surv := make([]int64, 2*rows)
	n := fusedScanInt8AVX2(&qc[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
		0, 10, 0, 0, 10, 0, rows, dim/32, 0, &surv[0])
	if n != rows {
		t.Fatalf("thresh == dhat2: expected all %d rows kept, got %d", rows, n)
	}
	// thresh2 = 9.999: dhat2 = 10 > thresh2 drops every row.
	n = fusedScanInt8AVX2(&qc[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
		0, 10, 0, 0, 9.999, 0, rows, dim/32, 0, &surv[0])
	if n != 0 {
		t.Fatalf("thresh < dhat2: expected 0 rows kept, got %d", n)
	}
	// rows == 0 exits immediately.
	n = fusedScanInt8AVX2(&qc[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
		0, 10, 0, 0, 10, 0, 0, dim/32, 0, &surv[0])
	if n != 0 {
		t.Fatalf("rows == 0: expected 0, got %d", n)
	}
}

// TestFusedScanEndToEnd runs TopK through the fused single-pass scan and
// compares it bit for bit with the prefilter-off exact scan: ids and
// distances must match exactly. It also proves the gates work: the fused
// branch must run on a clean table (fusedBatches advances), and must NOT
// run once tombstones exist (after deletes) or when a WHERE filter applies.
func TestFusedScanEndToEnd(t *testing.T) {
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
	check := func(tag string) {
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
				t.Fatalf("%s q%d: %d rows fused, %d exact", tag, qn, len(got), len(want))
			}
			for i := range want {
				if got[i][0].I != want[i][0].I || math.Float64bits(got[i][1].F) != math.Float64bits(want[i][1].F) {
					t.Fatalf("%s q%d rank %d: fused (%d, %v) exact (%d, %v)", tag, qn, i,
						got[i][0].I, got[i][1].F, want[i][0].I, want[i][1].F)
				}
			}
		}
	}
	before := fusedBatches.Load()
	check("clean")
	if fusedBatches.Load() == before {
		t.Fatalf("fused branch never ran on a clean table (no tombstones, no WHERE)")
	}
	// Deletes create tombstones: the fused gate must exclude the branch,
	// and results must still match the exact scan bit for bit.
	for i := 1; i <= 100; i++ {
		mustExec(t, db, fmt.Sprintf("DELETE FROM d WHERE id = %d", i*37))
	}
	before = fusedBatches.Load()
	check("tombstones")
	if fusedBatches.Load() != before {
		t.Fatalf("fused branch ran with tombstones present (gate failed)")
	}
	// A WHERE equality filter must also exclude the fused branch.
	q := make([]float32, dim)
	for j := range q {
		q[j] = rng.Float32()
	}
	sql := fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS dist FROM d WHERE cat = 'c3' ORDER BY dist LIMIT 10", lit(q))
	before = fusedBatches.Load()
	prefilterEnabled = false
	want := mustExec(t, db, sql).Rows
	prefilterEnabled = true
	got := mustExec(t, db, sql).Rows
	if len(got) != len(want) {
		t.Fatalf("filtered: %d rows fused, %d exact", len(got), len(want))
	}
	for i := range want {
		if got[i][0].I != want[i][0].I || math.Float64bits(got[i][1].F) != math.Float64bits(want[i][1].F) {
			t.Fatalf("filtered rank %d: fused (%d, %v) exact (%d, %v)", i,
				got[i][0].I, got[i][1].F, want[i][0].I, want[i][1].F)
		}
	}
	if fusedBatches.Load() != before {
		t.Fatalf("fused branch ran on a filtered query (gate failed)")
	}
}
