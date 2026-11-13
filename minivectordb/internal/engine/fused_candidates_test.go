package engine

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
)

func TestFusedCandidatesMatchScalar(t *testing.T) {
	rng := rand.New(rand.NewSource(612))
	for _, dim := range []int{1, 16, 17, 31, 32, 33, 64, 127, 128, 129, 255, 256, 257, 1024} {
		x := &VecIndex{dim: dim, pks: make([]int64, 257), off: make([]int64, 257), codes: make([]byte, 257*dim), qf: make([]float64, 257*4)}
		for s := range x.pks {
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32()*4 - 2
			}
			qp := quantizeRow(v, x.codes[s*dim:(s+1)*dim])
			x.qf[s*4], x.qf[s*4+1], x.qf[s*4+2], x.qf[s*4+3] = foldQparam(qp, dim)
			x.pks[s], x.off[s] = int64(999-s), int64(s*4096)
		}
		// Includes exceptional intermediate values and both ends of the slot
		// arrays. Shuffled slots catch accidental contiguous-row assumptions.
		x.qf[3] = math.NaN()
		x.qf[7] = math.Inf(1)
		q := make([]float32, dim)
		for j := range q {
			q[j] = rng.Float32()
		}
		qmin, qscale, qc, qsum, qnorm, _ := quantizeQuery(q)
		perm := rng.Perm(257)
		slots := make([]int32, len(perm))
		for i, s := range perm {
			slots[i] = int32(s)
		}
		for _, n := range []int{0, 1, 17, 128, 257} {
			for _, threshold := range []float64{0, 5, 35, 200, math.Inf(1), math.NaN()} {
				got, want := make([]int64, 2*n), make([]int64, 2*n)
				gn := fusedScanCandidates(qc, x, slots[:n], qsum, qnorm, qmin, qscale, threshold, got)
				wn := fusedScanCandidatesScalar(qc, x, slots[:n], qsum, qnorm, qmin, qscale, threshold, want)
				if gn != wn || !reflect.DeepEqual(got[:2*gn], want[:2*wn]) {
					t.Fatalf("dim=%d rows=%d threshold=%g: candidate kernel differs", dim, n, threshold)
				}
			}
		}
	}
}

func TestFusedCandidatesExactAfterMutation(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY, category VARCHAR(8), embedding VECTOR(33))")
	rng := rand.New(rand.NewSource(54))
	for i := 0; i < 150; i++ {
		v := make([]float32, 33)
		for j := range v {
			v[j] = rng.Float32()
		}
		mustExec(t, db, fmt.Sprintf("INSERT INTO docs VALUES(%d,'c%d','%s')", i, i%3, vectorText(v)))
	}
	old := prefilterEnabled
	defer func() { prefilterEnabled = old }()
	before := fusedCandidateBatches.Load()
	for _, change := range []string{"", "DELETE FROM docs WHERE id=4", "UPDATE docs SET category='c1' WHERE id=0", "BEGIN", "INSERT INTO docs VALUES(150,'c1','" + vectorText(make([]float32, 33)) + "')", "ROLLBACK"} {
		if change != "" {
			mustExec(t, db, change)
		}
		for cat := 0; cat < 4; cat++ {
			q := make([]float32, 33)
			for j := range q {
				q[j] = rng.Float32()
			}
			sql := fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS d FROM docs WHERE category='c%d' ORDER BY d LIMIT 10", vectorText(q), cat)
			prefilterEnabled = true
			got := mustExec(t, db, sql).Rows
			prefilterEnabled = false
			want := mustExec(t, db, sql).Rows
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("after %q: exact results differ", change)
			}
		}
	}
	if useSIMD && fusedCandidateBatches.Load() == before {
		t.Fatal("candidate kernel was not exercised")
	}
}
