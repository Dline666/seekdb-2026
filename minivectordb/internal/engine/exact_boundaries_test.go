package engine

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func TestMinimumPrimaryKeyTopK(t *testing.T) {
	for _, dim := range []int{2, 128} {
		t.Run(fmt.Sprint(dim), func(t *testing.T) {
			dir := t.TempDir()
			db := open(t, dir)
			defer func() { db.Close() }()
			zero, one := make([]float32, dim), make([]float32, dim)
			for i := range one {
				one[i] = 1
			}
			mustExec(t, db, fmt.Sprintf("CREATE TABLE d(id BIGINT PRIMARY KEY,c VARCHAR(8),e VECTOR(%d))", dim))
			mustExec(t, db, fmt.Sprintf("INSERT INTO d VALUES(%d,'a','%s'),(1,'a','%s')", int64(math.MinInt64), vectorText(zero), vectorText(one)))
			check := func(want int64) {
				t.Helper()
				for _, where := range []string{"", " WHERE c='a'", " WHERE id <= 1"} {
					r := mustExec(t, db, fmt.Sprintf("SELECT id,L2_DISTANCE(e,'%s') AS d FROM d%s ORDER BY d LIMIT 1", vectorText(zero), where))
					if len(r.Rows) != 1 || r.Rows[0][0].I != want {
						t.Fatalf("%s: got %v, want PK=%d", where, r.Rows, want)
					}
				}
			}
			check(math.MinInt64)
			mustExec(t, db, "BEGIN")
			mustExec(t, db, fmt.Sprintf("DELETE FROM d WHERE id=%d", int64(math.MinInt64)))
			check(1)
			mustExec(t, db, "ROLLBACK")
			check(math.MinInt64)
			mustExec(t, db, fmt.Sprintf("DELETE FROM d WHERE id=%d", int64(math.MinInt64)))
			check(1)
			mustExec(t, db, fmt.Sprintf("INSERT INTO d VALUES(%d,'a','%s')", int64(math.MinInt64), vectorText(zero)))
			check(math.MinInt64)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = open(t, dir)
			check(math.MinInt64)
		})
	}
}

func TestCancellationSafeTopK(t *testing.T) {
	oldPF, old6, oldCandidates, oldSIMD := prefilterEnabled, prefilter6Bit, fusedCandidatesEnabled, useSIMD
	defer func() {
		prefilterEnabled, prefilter6Bit, fusedCandidatesEnabled, useSIMD = oldPF, old6, oldCandidates, oldSIMD
	}()
	for _, dim := range []int{128, 512, 1024} {
		db := open(t, t.TempDir())
		mustExec(t, db, fmt.Sprintf("CREATE TABLE d(id BIGINT PRIMARY KEY,c VARCHAR(8),e VECTOR(%d))", dim))
		v := make([]float32, dim)
		for j := range v {
			v[j] = 7722174976
		}
		lit := vectorText(v)
		var b strings.Builder
		b.WriteString("INSERT INTO d VALUES ")
		for i := 300; i >= 1; i-- {
			if i < 300 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "(%d,'a','%s')", i, lit)
		}
		mustExec(t, db, b.String())
		for _, mode := range []string{"fused", "multipass", "sixbit", "scalar"} {
			useSIMD = oldSIMD && mode != "scalar"
			fusedCandidatesEnabled = mode != "multipass"
			prefilter6Bit = mode == "sixbit"
			for _, where := range []string{"", " WHERE c='a'", " WHERE id>0"} {
				prefilterEnabled = true
				sql := fmt.Sprintf("SELECT id,L2_DISTANCE(e,'%s') AS d FROM d%s ORDER BY d LIMIT 10", lit, where)
				got := mustExec(t, db, sql).Rows
				if len(got) != 10 {
					t.Fatalf("dim=%d mode=%s: %d rows", dim, mode, len(got))
				}
				for i, r := range got {
					if r[0].I != int64(i+1) || r[1].F != 0 {
						t.Fatalf("dim=%d mode=%s where=%q: %v", dim, mode, where, got)
					}
				}
			}
		}
		// Nearby vectors and updates exercise the maximum's maintenance, not
		// only identical vectors with a zero quantization scale.
		useSIMD, prefilter6Bit, fusedCandidatesEnabled = oldSIMD, false, true
		for _, pk := range []int{1, 130, 299} {
			near := append([]float32(nil), v...)
			near[pk%dim] = math.Nextafter32(near[pk%dim], float32(math.Inf(1)))
			mustExec(t, db, fmt.Sprintf("UPDATE d SET e='%s' WHERE id=%d", vectorText(near), pk))
		}
		for i := 0; i < 4; i++ {
			q := append([]float32(nil), v...)
			q[i] = math.Nextafter32(q[i], float32(math.Inf(-1)))
			sql := fmt.Sprintf("SELECT id,L2_DISTANCE(e,'%s') AS d FROM d WHERE c='a' ORDER BY d LIMIT 10", vectorText(q))
			prefilterEnabled = false
			want := mustExec(t, db, sql).Rows
			prefilterEnabled = true
			got := mustExec(t, db, sql).Rows
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("nearby vectors dim=%d: exact mismatch", dim)
			}
		}
		db.Close()
	}
}

func TestAbsoluteRoundingAllowance(t *testing.T) {
	rng := rand.New(rand.NewSource(717))
	for _, dim := range []int{16, 128, 512, 1024} {
		for trial := 0; trial < 50; trial++ {
			v, q := make([]float32, dim), make([]float32, dim)
			base := float32(math.Ldexp(rng.Float64()+1, rng.Intn(240)-120))
			for i := range v {
				v[i] = base
				q[i] = base
				if rng.Intn(3) == 0 {
					v[i] = math.Nextafter32(base, float32(math.Inf(1)))
				}
			}
			codes := make([]byte, dim)
			qp := quantizeRow(v, codes)
			qmin, qscale, qc, sum, norm, _ := quantizeQuery(q)
			var dot int32
			var exact float64
			for i, c := range codes {
				dot += int32(c) * int32(qc[i])
				d := float64(v[i]) - float64(q[i])
				exact += d * d
			}
			m2, t2, s2, cr := foldQparam(qp, dim)
			computed := foldDist2(sum, norm, qmin, qscale, dot, m2, t2, s2, cr)
			rowAbs := math.Abs(float64(qp.min)) + 256*float64(qp.scale)
			threshold := prefilterThreshold(qp.scale+qscale, dim, exact) + prefilterRoundSlack(rowAbs, qmin, qscale, dim)
			if computed > threshold {
				t.Fatalf("unsafe prune dim=%d base=%g computed=%g exact=%g threshold=%g", dim, base, computed, exact, threshold)
			}
		}
	}
}

func TestLargeDimensionExactFallback(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	dim := maxInt8Dim + 2
	v := make([]float32, dim)
	for i := 1; i < dim; i++ {
		v[i] = 1
	}
	lit := vectorText(v)
	mustExec(t, db, fmt.Sprintf("CREATE TABLE d(id BIGINT PRIMARY KEY,e VECTOR(%d))", dim))
	for i := 130; i >= 1; i-- {
		mustExec(t, db, fmt.Sprintf("INSERT INTO d VALUES(%d,'%s')", i, lit))
	}
	r := mustExec(t, db, fmt.Sprintf("SELECT id,L2_DISTANCE(e,'%s') AS d FROM d ORDER BY d LIMIT 1", lit))
	if len(r.Rows) != 1 || r.Rows[0][0].I != 1 || r.Rows[0][1].F != 0 {
		t.Fatalf("overflow-safe exact result: %v", r.Rows)
	}
	if db.tables["d"].Index.codes != nil {
		t.Fatal("unsafe integer prefilter allocated for a large dimension")
	}
}
