package engine

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// TestBatchedScanMatchesOneRowScan runs the same TopK queries with the
// batched scan and with the one-row scan and requires identical results,
// down to the bits of every distance, plus agreement with a brute-force
// reference. It covers dimensions with and without a scalar tail, rows of
// different lengths (VARCHAR), deleted and updated rows, WHERE filters with
// and without the equality candidate set, an open transaction, ties, and
// both the serial and the parallel scan.
func TestBatchedScanMatchesOneRowScan(t *testing.T) {
	defer func(v int) { batchScan, parallelMinSlots = true, v }(parallelMinSlots)
	for _, dim := range []int{16, 17, 31, 48, 128} {
		for _, par := range []int{1 << 30, 1} {
			parallelMinSlots = par
			testBatchedScan(t, dim)
		}
	}
}

func testBatchedScan(t *testing.T, dim int) {
	db := open(t, t.TempDir())
	rng := rand.New(rand.NewSource(int64(dim)))
	const n = 900
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
	for i := 0; i < 150; i++ {
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
		for qn := 0; qn < 40; qn++ {
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
			batchScan = false
			want := mustExec(t, db, sql).Rows
			batchScan = true
			got := mustExec(t, db, sql).Rows
			if len(got) != len(want) {
				t.Fatalf("dim %d %s: %s: %d rows batched, %d one-row", dim, tag, sql[:60], len(got), len(want))
			}
			for i := range want {
				if got[i][0].I != want[i][0].I || math.Float64bits(got[i][1].F) != math.Float64bits(want[i][1].F) {
					t.Fatalf("dim %d %s: rank %d batched (%d, %v) one-row (%d, %v)", dim, tag, i,
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
	for i := 0; i < 60; i++ {
		id := int64(rng.Intn(n+80) + 1)
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

func whereKeeps(w string, id int64, cat string) bool {
	switch w {
	case " WHERE cat = 'a'":
		return cat == "a"
	case " WHERE cat = '你好'":
		return cat == "你好"
	case " WHERE cat = 'none'":
		return false
	case " WHERE cat = 'bb' AND n > 2":
		return cat == "bb" && id%7 > 2
	case " WHERE n < 3":
		return id%7 < 3
	case " WHERE cat <> 'a'":
		return cat != "a"
	}
	return true
}

// BenchmarkGatherScan compares one kernel call per row with one per batch on
// the benchmark shape (10k rows x 128 dims, rows 544 bytes apart).
func BenchmarkGatherScan(b *testing.B) {
	if !useSIMD {
		b.Skip("no SIMD kernel")
	}
	const n, dim, stride = 10000, 128, 544
	rng := rand.New(rand.NewSource(1))
	base := make([]byte, n*stride)
	for i := range base {
		base[i] = byte(rng.Intn(64)) // small exponents: finite floats
	}
	q := make([]float64, dim)
	for i := range q {
		q[i] = rng.Float64()
	}
	offs := make([]int64, n)
	for i := range offs {
		offs[i] = int64(i*stride + 29)
	}
	out := make([]float64, scanBatch)
	b.Run("one-row", func(b *testing.B) {
		for it := 0; it < b.N; it++ {
			for _, o := range offs {
				_ = l2sqSIMDBytes(q, base[o:o+4*dim])
			}
		}
	})
	b.Run("batched", func(b *testing.B) {
		for it := 0; it < b.N; it++ {
			for lo := 0; lo < n; lo += scanBatch {
				hi := lo + scanBatch
				if hi > n {
					hi = n
				}
				l2sqGatherBytes(q, base, offs[lo:hi], out)
			}
		}
	})
}
