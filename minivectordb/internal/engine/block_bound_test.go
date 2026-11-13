package engine

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

// This oracle uses only the input float32 vectors and a separate float64
// sum. It does not read index state or call any engine distance kernel.
func checkBlockTruth(t *testing.T, db *DB, vectors map[int64][]float32, q []float32, filter int, minID int64) {
	t.Helper()
	type answer struct {
		id int64
		d  float64
	}
	var want []answer
	for id, v := range vectors {
		if id <= minID || filter >= 0 && id%2 != int64(filter) {
			continue
		}
		var sq float64
		for i, f := range q {
			d := float64(f) - float64(v[i])
			sq += d * d
		}
		want = append(want, answer{id, math.Sqrt(sq)})
	}
	sort.Slice(want, func(i, j int) bool {
		return want[i].d < want[j].d || want[i].d == want[j].d && want[i].id < want[j].id
	})
	where := ""
	if filter >= 0 {
		where = fmt.Sprintf(" WHERE cat='c%d'", filter)
		if minID >= 0 {
			where += fmt.Sprintf(" AND id>%d", minID)
		}
	}
	sql := fmt.Sprintf("SELECT id,L2_DISTANCE(e,'%s') AS d FROM docs%s ORDER BY d LIMIT 10", vectorText(q), where)
	got := mustExec(t, db, sql).Rows
	if len(got) != min(10, len(want)) {
		t.Fatalf("row count: got %d want %d", len(got), min(10, len(want)))
	}
	for i, row := range got {
		if row[0].I != want[i].id || math.Abs(row[1].F-want[i].d) > 1e-12*math.Max(1, want[i].d) {
			t.Fatalf("rank %d: got id=%d d=%g, want id=%d d=%g", i, row[0].I, row[1].F, want[i].id, want[i].d)
		}
	}
}

func TestBlockBoundsExactAfterMutation(t *testing.T) {
	const dim, rows = 257, 2309 // tail dimensions and non-aligned worker partitions
	oldPre, oldSix, oldCat, oldCand, oldParallel := prefilterEnabled, prefilter6Bit, catBlocksEnabled, fusedCandidatesEnabled, parallelMinSlots
	defer func() {
		prefilterEnabled, prefilter6Bit, catBlocksEnabled, fusedCandidatesEnabled, parallelMinSlots = oldPre, oldSix, oldCat, oldCand, oldParallel
	}()
	prefilterEnabled, catBlocksEnabled, parallelMinSlots = true, true, 1
	dir := t.TempDir()
	db := open(t, dir)
	defer func() { db.Close() }()
	mustExec(t, db, fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,cat VARCHAR(8),e VECTOR(%d))", dim))
	rng := rand.New(rand.NewSource(3981))
	vectors := make(map[int64][]float32, rows)
	for start := 0; start < rows; start += 100 {
		var sql strings.Builder
		sql.WriteString("INSERT INTO docs VALUES ")
		for i := start; i < min(start+100, rows); i++ {
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32()
			}
			if i == 128 {
				v[0] = 1e20 // both quantization and absolute-rounding outlier
			}
			// First rows tie exactly, with reverse insertion order.
			if i < 20 {
				clear(v)
			}
			id := int64(rows - i)
			vectors[id] = v
			if i > start {
				sql.WriteByte(',')
			}
			fmt.Fprintf(&sql, "(%d,'c%d','%s')", id, id%2, vectorText(v))
		}
		mustExec(t, db, sql.String())
	}
	q := make([]float32, dim)
	check := func(tag string) {
		t.Helper()
		for mode := 0; mode < 3; mode++ {
			// Candidate fusion, category fusion, and the 6-bit gather
			// path must all use bounds covering their actual rows.
			fusedCandidatesEnabled = mode == 0
			prefilter6Bit = mode == 2
			for filter := -1; filter < 2; filter++ {
				checkBlockTruth(t, db, vectors, q, filter, -1)
			}
			checkBlockTruth(t, db, vectors, q, 1, 10)
		}
		t.Log(tag)
	}
	check("initial")
	// Mutate an existing slot outside the original outlier's block. Its
	// block must immediately acquire the new, larger bound.
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(7722174976)
	}
	mustExec(t, db, fmt.Sprintf("UPDATE docs SET e='%s' WHERE id=999", vectorText(v)))
	vectors[999] = v
	copy(q, v) // cancellation near the new large constant must keep id=999
	check("updated large constant")
	clear(q)
	mustExec(t, db, "DELETE FROM docs WHERE id=999")
	delete(vectors, 999)
	check("deleted")
	v = make([]float32, dim)
	mustExec(t, db, fmt.Sprintf("INSERT INTO docs VALUES(999,'c1','%s')", vectorText(v)))
	vectors[999] = v
	check("reused slot")
	mustExec(t, db, "BEGIN")
	v = append([]float32(nil), v...)
	v[0] = 0.25
	mustExec(t, db, fmt.Sprintf("UPDATE docs SET e='%s' WHERE id=999", vectorText(v)))
	vectors[999] = v
	check("transaction overlay")
	mustExec(t, db, "ROLLBACK")
	vectors[999] = make([]float32, dim)
	check("rollback")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	check("recovered")
	for i := range q {
		q[i] = rng.Float32()
	}
	check("independent random query")
}

func TestBlockBoundsIsolateOutlier(t *testing.T) {
	if !useSIMD {
		t.Skip("gather diagnostics require SIMD")
	}
	oldPre, oldSix, oldParallel := prefilterEnabled, prefilter6Bit, parallelMinSlots
	defer func() { prefilterEnabled, prefilter6Bit, parallelMinSlots = oldPre, oldSix, oldParallel }()
	// The 6-bit setting forces the multi-pass scan, whose counters let us
	// assert the pruning effect without a flaky timing expectation.
	prefilterEnabled, prefilter6Bit, parallelMinSlots = true, true, math.MaxInt
	db := open(t, t.TempDir())
	defer db.Close()
	const dim, rows = 33, 1024
	mustExec(t, db, fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,cat VARCHAR(8),e VECTOR(%d))", dim))
	vectors := make(map[int64][]float32, rows)
	var sql strings.Builder
	sql.WriteString("INSERT INTO docs VALUES ")
	for id := 0; id < rows; id++ {
		v := make([]float32, dim)
		if id >= 20 {
			for i := range v {
				v[i] = 1
			}
		}
		if id == 128 {
			v[0] = 1e20
		}
		vectors[int64(id)] = v
		if id > 0 {
			sql.WriteByte(',')
		}
		fmt.Fprintf(&sql, "(%d,'c%d','%s')", id, id%2, vectorText(v))
	}
	mustExec(t, db, sql.String())
	before := dbgScanExactRows.Load()
	checkBlockTruth(t, db, vectors, make([]float32, dim), -1, -1)
	exactRows := dbgScanExactRows.Load() - before
	if exactRows > 2*quantBlockRows {
		t.Fatalf("outlier leaked beyond its block: exact rows=%d of %d", exactRows, rows)
	}
	// Memory budgets must account for the small new array and release it
	// along with codes if quantization no longer fits.
	x := db.tables["docs"].Index
	if len(x.qbounds) != rows/quantBlockRows || x.qbounds[0].maxAbs != 1 || x.qbounds[1].maxAbs < 1e20 {
		t.Fatal("wrong block bounds")
	}
	db.opts.IndexMemBytes = rows * indexBytesPerRow
	mustExec(t, db, fmt.Sprintf("INSERT INTO docs VALUES(%d,'c0','%s')", rows, vectorText(make([]float32, dim))))
	if x.codes != nil || x.qbounds != nil {
		t.Fatal("budget fallback retained optional bounds")
	}
}
