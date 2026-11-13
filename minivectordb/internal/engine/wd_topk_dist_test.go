package engine

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
)

// TestThreadedTopKDistances verifies that the L2_DISTANCE values
// execSelect reuses from vectorTopK (sqrt of the scan's squared
// distance) are bit-identical to a fresh recomputation with l2sqBytes,
// the value the old code printed. It covers the gather/prefilter path,
// the one-row path (batchScan off), the eq-filter candidate path and
// OFFSET slicing, in both serial and parallel scans.
func TestThreadedTopKDistances(t *testing.T) {
	for _, workers := range []int{1, 2} {
		for _, batch := range []bool{true, false} {
			oldW, oldB := maxScanWorkers, batchScan
			maxScanWorkers, batchScan = workers, batch
			testThreadedTopKDistances(t, workers, batch)
			maxScanWorkers, batchScan = oldW, oldB
		}
	}
}

func testThreadedTopKDistances(t *testing.T, workers int, batch bool) {
	t.Helper()
	db := open(t, t.TempDir())
	rng := rand.New(rand.NewSource(23))
	const dim, n = 64, 2000
	vecs := make(map[int64][]float32, n)
	cats := make(map[int64]string, n)
	mustExec(t, db, fmt.Sprintf("CREATE TABLE docs (id BIGINT PRIMARY KEY, cat VARCHAR(8), e VECTOR(%d))", dim))
	var sb strings.Builder
	for s := int64(1); s <= n; s += 100 {
		sb.Reset()
		sb.WriteString("INSERT INTO docs VALUES ")
		for i := s; i < s+100 && i <= n; i++ {
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32()
			}
			vecs[i] = v
			cats[i] = []string{"a", "b", "c"}[rng.Intn(3)]
			if i > s {
				sb.WriteByte(',')
			}
			parts := make([]string, dim)
			for j, f := range v {
				parts[j] = fmt.Sprint(f)
			}
			fmt.Fprintf(&sb, "(%d,'%s','[%s]')", i, cats[i], strings.Join(parts, ","))
		}
		mustExec(t, db, sb.String())
	}
	mustExec(t, db, "CREATE VECTOR INDEX vi ON docs(e)")
	tbl, err := db.table("docs")
	if err != nil {
		t.Fatal(err)
	}
	check := func(sql string, q []float32, wantIDs map[int64]bool) {
		t.Helper()
		res, err := db.Exec(sql)
		if err != nil {
			t.Fatalf("workers=%d batch=%v sql=%s: %v", workers, batch, sql, err)
		}
		if len(res.Rows) == 0 {
			t.Fatalf("workers=%d batch=%v sql=%s: no rows", workers, batch, sql)
		}
		for _, row := range res.Rows {
			id := row[0].I
			if wantIDs != nil && !wantIDs[id] {
				t.Fatalf("workers=%d batch=%v: returned id %d not in filter set", workers, batch, id)
			}
			got := row[1].F
			off := tbl.Index.off[indexOfPK(tbl, id)]
			b := db.rowBytes(off)
			want := math.Sqrt(l2sqBytes(q, b[tbl.vecOff:]))
			if math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("workers=%d batch=%v id=%d: threaded distance %v (%x) != recomputed %v (%x)",
					workers, batch, id, got, math.Float64bits(got), want, math.Float64bits(want))
			}
		}
	}
	q := make([]float32, dim)
	for j := range q {
		q[j] = rng.Float32()
	}
	qlit := vecLit(q)
	check(fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS distance FROM docs ORDER BY distance LIMIT 10", qlit), q, nil)
	// Filtered check with a real id set.
	bIDs := map[int64]bool{}
	for id, c := range cats {
		if c == "b" {
			bIDs[id] = true
		}
	}
	check(fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS distance FROM docs WHERE cat = 'b' ORDER BY distance LIMIT 10", qlit), q, bIDs)
	// OFFSET: the threaded distances must stay aligned after slicing.
	check(fmt.Sprintf("SELECT id, L2_DISTANCE(e, '%s') AS distance FROM docs ORDER BY distance LIMIT 10 OFFSET 5", qlit), q, nil)
}

func vecLit(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprint(f)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// indexOfPK finds the slot holding pk in the index (linear scan; test only).
func indexOfPK(tbl *Table, pk int64) int32 {
	for s, p := range tbl.Index.pks {
		if p == pk {
			return int32(s)
		}
	}
	panic(fmt.Sprintf("pk %d not in index", pk))
}
