package engine

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"minivectordb/internal/storage"
)

type partialTruthRow struct {
	cat string
	vec []float32
}

// The oracle ranks a separate copy of the input by scalar float64 squared
// distance and primary key. It never calls a distance or quantization helper.
func checkPartialTruth(t *testing.T, db *DB, table string, truth map[int64]partialTruthRow, q []float32, k int, cat string, minID int64) {
	t.Helper()
	type answer struct {
		id int64
		sq float64
	}
	want := make([]answer, 0, len(truth))
	for id, row := range truth {
		if cat != "" && row.cat != cat || minID >= 0 && id <= minID {
			continue
		}
		var sq float64
		for j, f := range q {
			d := float64(f) - float64(row.vec[j])
			sq += d * d
		}
		want = append(want, answer{id, sq})
	}
	sort.Slice(want, func(i, j int) bool {
		return want[i].sq < want[j].sq || want[i].sq == want[j].sq && want[i].id < want[j].id
	})
	where := ""
	if cat != "" {
		where = " WHERE cat='" + cat + "'"
	}
	if minID >= 0 {
		if where == "" {
			where = " WHERE "
		} else {
			where += " AND "
		}
		where += fmt.Sprintf("id>%d", minID)
	}
	sql := fmt.Sprintf("SELECT id,L2_DISTANCE(e,'%s') AS d FROM %s%s ORDER BY d LIMIT %d", vectorText(q), table, where, k)
	got := mustExec(t, db, sql).Rows
	if len(got) != min(k, len(want)) {
		t.Fatalf("%s: got %d rows, want %d", sql, len(got), min(k, len(want)))
	}
	for i, row := range got {
		d := math.Sqrt(want[i].sq)
		if row[0].I != want[i].id || math.IsNaN(row[1].F) || math.Abs(row[1].F-d) > 1e-12*math.Max(1, d) {
			t.Fatalf("%s: rank %d got id=%d d=%g, want id=%d d=%g", sql, i, row[0].I, row[1].F, want[i].id, d)
		}
	}
}

func partialQuantTestModes(t *testing.T) bool {
	t.Helper()
	pre, six, cat, fused := prefilterEnabled, prefilter6Bit, catBlocksEnabled, fusedCandidatesEnabled
	simd, batch, parallel, workers := useSIMD, batchScan, parallelMinSlots, maxScanWorkers
	procs := runtime.GOMAXPROCS(2)
	t.Cleanup(func() {
		prefilterEnabled, prefilter6Bit, catBlocksEnabled, fusedCandidatesEnabled = pre, six, cat, fused
		useSIMD, batchScan, parallelMinSlots, maxScanWorkers = simd, batch, parallel, workers
		runtime.GOMAXPROCS(procs)
	})
	prefilterEnabled, prefilter6Bit, catBlocksEnabled, fusedCandidatesEnabled = true, false, true, true
	batchScan, parallelMinSlots, maxScanWorkers = true, 1, 2
	return simd
}

func openPartialDB(t *testing.T, dir string, budget int64) *DB {
	t.Helper()
	db, err := Open(dir, Options{Sync: storage.SyncData, IndexMemBytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func partialFixture(t *testing.T, db *DB, table string, n, dim int, unique bool) map[int64]partialTruthRow {
	t.Helper()
	mustExec(t, db, fmt.Sprintf("CREATE TABLE %s(id BIGINT PRIMARY KEY,cat VARCHAR(64),e VECTOR(%d))", table, dim))
	truth := make(map[int64]partialTruthRow, n)
	for start := 0; start < n; start += 64 {
		var sql strings.Builder
		fmt.Fprintf(&sql, "INSERT INTO %s VALUES ", table)
		for i := start; i < min(start+64, n); i++ {
			v := make([]float32, dim)
			for j := range v {
				// Binary-exact coordinates make the scalar oracle independent
				// of accumulation order for ordinary rows.
				v[j] = float32((i*19+j*7)%97-48) / 16
			}
			if i < 24 || i == n-1 {
				clear(v)
			}
			if i == 129 || i == n-2 {
				v[0] = 1e20
			}
			id := int64(n - i) // exact ties arrive in reverse PK order
			cat := fmt.Sprintf("c%d", id%3)
			if unique {
				cat = fmt.Sprintf("unique-%04d", id)
			}
			truth[id] = partialTruthRow{cat, v}
			if i > start {
				sql.WriteByte(',')
			}
			fmt.Fprintf(&sql, "(%d,'%s','%s')", id, cat, vectorText(v))
		}
		mustExec(t, db, sql.String())
	}
	mustExec(t, db, fmt.Sprintf("CREATE VECTOR INDEX vi_%s ON %s(e)", table, table))
	return truth
}

func checkPartialBudget(t *testing.T, db *DB) {
	t.Helper()
	var retained int64
	for _, table := range db.tables {
		if table.Index != nil {
			retained += table.Index.derivedBytes()
		}
	}
	if retained > db.opts.IndexMemBytes {
		t.Fatalf("aggregate derived bytes %d exceed budget %d", retained, db.opts.IndexMemBytes)
	}
}

func checkPartialPrefix(t *testing.T, db *DB, table string) *VecIndex {
	t.Helper()
	x := db.tables[table].Index
	if x == nil || x.Disabled || x.quantRows < quantBlockRows || x.quantRows >= len(x.pks) || x.quantRows%quantBlockRows != 0 {
		t.Fatalf("expected a nonempty partial encoded prefix in table %s", table)
	}
	// No initialized tail storage exists. Any tail qf/code access must fail
	// instead of accidentally reading zero-filled capacity as valid codes.
	if len(x.codes) != x.quantRows*x.dim || len(x.qf) != 4*x.quantRows || len(x.qbounds) != x.quantRows/quantBlockRows {
		t.Fatalf("encoded storage does not end at Q=%d: codes=%d qf=%d bounds=%d", x.quantRows, len(x.codes), len(x.qf), len(x.qbounds))
	}
	checkPartialBudget(t, db)
	return x
}

func partialUpdate(t *testing.T, db *DB, truth map[int64]partialTruthRow, id int64, cat string, v []float32) {
	t.Helper()
	mustExec(t, db, fmt.Sprintf("UPDATE docs SET cat='%s',e='%s' WHERE id=%d", cat, vectorText(v), id))
	truth[id] = partialTruthRow{cat, append([]float32(nil), v...)}
}

func partialInsert(t *testing.T, db *DB, table string, truth map[int64]partialTruthRow, id int64, cat string, v []float32) {
	t.Helper()
	mustExec(t, db, fmt.Sprintf("INSERT INTO %s VALUES(%d,'%s','%s')", table, id, cat, vectorText(v)))
	truth[id] = partialTruthRow{cat, append([]float32(nil), v...)}
}

func clonePartialTruth(src map[int64]partialTruthRow) map[int64]partialTruthRow {
	dst := make(map[int64]partialTruthRow, len(src))
	for id, row := range src {
		dst[id] = partialTruthRow{row.cat, append([]float32(nil), row.vec...)}
	}
	return dst
}

func TestPartialQuantExactMutationAndRecovery(t *testing.T) {
	hostSIMD := partialQuantTestModes(t)
	const n, dim, budget = 1539, 33, 180 << 10
	dir := t.TempDir()
	db := openPartialDB(t, dir, budget)
	defer func() { db.Close() }()
	truth := partialFixture(t, db, "docs", n, dim, false)
	x := checkPartialPrefix(t, db, "docs")
	qn := x.quantRows
	if qn <= n/2 || (n/2)%scanBatch == 0 {
		t.Fatalf("fixture must split a coded/tail batch in a nonaligned worker: Q=%d split=%d", qn, n/2)
	}
	q := make([]float32, dim)
	batches, coded, uncoded := partialPrefilterBatches.Load(), partialPrefilterRows.Load(), partialUncodedRows.Load()
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "c1", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "c1", n/3)
	if hostSIMD && (partialPrefilterBatches.Load() <= batches || partialPrefilterRows.Load() <= coded || partialUncodedRows.Load() <= uncoded) {
		t.Fatal("fixture did not execute coded and uncoded portions of the partial prefilter")
	}
	if x.catBlockFor(db, db.tables["docs"], 1, "c1") != nil {
		t.Fatal("partial quantization must not build a category code copy")
	}
	slots, ok := x.eqSlots(db, db.tables["docs"], 1, truth[1].cat)
	foundTail := false
	for _, slot := range slots {
		foundTail = foundTail || x.pks[slot] == 1 && int(slot) >= qn
	}
	if !ok || !foundTail {
		t.Fatal("full equality set omitted the nearest uncoded tail row")
	}

	// Query every switch independently against the scalar oracle, including
	// the 6-bit path and both exact fallbacks.
	for _, mode := range []struct {
		name                  string
		six, simd, batch, pre bool
	}{
		{"normal", false, hostSIMD, true, true},
		{"six-bit", true, hostSIMD, true, true},
		{"scalar", false, false, true, true},
		{"no-gather", false, hostSIMD, false, true},
		{"no-prefilter", false, hostSIMD, true, false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			prefilter6Bit, useSIMD, batchScan, prefilterEnabled = mode.six, mode.simd, mode.batch, mode.pre
			checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
			checkPartialTruth(t, db, "docs", truth, q, 17, "c1", n/3)
			randomQ := make([]float32, dim)
			for j := range randomQ {
				randomQ[j] = float32((j*11)%23-11) / 8
			}
			checkPartialTruth(t, db, "docs", truth, randomQ, 31, "", -1)
		})
	}
	prefilter6Bit, useSIMD, batchScan, prefilterEnabled = false, hostSIMD, true, true
	covered, firstTail, nextTail := x.pks[qn-1], x.pks[qn], x.pks[qn+1]
	for _, id := range []int64{covered, firstTail, nextTail} {
		partialUpdate(t, db, truth, id, "boundary", q)
	}
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "boundary", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "boundary", firstTail)

	large := make([]float32, dim)
	for j := range large {
		large[j] = 1 << 20
	}
	partialUpdate(t, db, truth, covered, "large", large)
	partialUpdate(t, db, truth, firstTail, "large", large)
	checkPartialTruth(t, db, "docs", truth, large, 2, "", -1)

	// Deletion and LIFO slot reuse touch both sides of Q.
	mapHighWater := x.posRows
	mustExec(t, db, fmt.Sprintf("DELETE FROM docs WHERE id=%d", covered))
	delete(truth, covered)
	mustExec(t, db, fmt.Sprintf("DELETE FROM docs WHERE id=%d", firstTail))
	delete(truth, firstTail)
	if x.posRows != mapHighWater {
		t.Fatal("deletion released the retained primary-key map charge")
	}
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	tailReplacement, codedReplacement := int64(n+100), int64(n+101)
	partialInsert(t, db, "docs", truth, tailReplacement, "reused", q)
	partialInsert(t, db, "docs", truth, codedReplacement, "reused", q)
	if x.pos[tailReplacement] != int32(qn) || x.pos[codedReplacement] != int32(qn-1) || x.quantRows != qn {
		t.Fatal("reuse failed to preserve covered and uncoded slots")
	}
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 31, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "reused", -1)
	checkPartialPrefix(t, db, "docs")

	before := clonePartialTruth(truth)
	mustExec(t, db, "BEGIN")
	overlayVector := make([]float32, dim)
	overlayVector[0] = 0.125
	partialUpdate(t, db, truth, codedReplacement, "txn", overlayVector)
	partialUpdate(t, db, truth, tailReplacement, "txn", overlayVector)
	mustExec(t, db, "DELETE FROM docs WHERE id=1")
	delete(truth, 1)
	partialInsert(t, db, "docs", truth, int64(n+200), "txn", q)
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "txn", n)
	mustExec(t, db, "ROLLBACK")
	truth = before
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "txn", -1)

	mustExec(t, db, "BEGIN")
	partialUpdate(t, db, truth, codedReplacement, "committed", q)
	partialUpdate(t, db, truth, tailReplacement, "committed", q)
	mustExec(t, db, "DELETE FROM docs WHERE id=1")
	delete(truth, 1)
	partialInsert(t, db, "docs", truth, int64(n+201), "committed", q)
	mustExec(t, db, "COMMIT")
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "committed", n)
	checkPartialBudget(t, db)

	// Capture an unfinished transaction before Close can roll it back.
	// Replaying both the intact and torn uncommitted tail must preserve the
	// independently tracked committed state and rebuild partial coverage.
	mustExec(t, db, "BEGIN")
	mustExec(t, db, fmt.Sprintf("UPDATE docs SET cat='uncommitted',e='%s' WHERE id=%d", vectorText(large), codedReplacement))
	mustExec(t, db, fmt.Sprintf("DELETE FROM docs WHERE id=%d", tailReplacement))
	if err := db.log.Flush(); err != nil {
		t.Fatal(err)
	}
	logBytes, err := os.ReadFile(filepath.Join(dir, logName))
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{len(logBytes), len(logBytes) - 7} {
		crashDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(crashDir, logName), logBytes[:cut], 0o644); err != nil {
			t.Fatal(err)
		}
		recovered := openPartialDB(t, crashDir, budget)
		checkPartialPrefix(t, recovered, "docs")
		checkPartialTruth(t, recovered, "docs", truth, q, 17, "", -1)
		checkPartialTruth(t, recovered, "docs", truth, q, 17, "committed", n)
		checkPartialTruth(t, recovered, "docs", truth, q, 17, "uncommitted", -1)
		if err := recovered.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mustExec(t, db, "ROLLBACK")
	for round := 0; round < 2; round++ {
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		db = openPartialDB(t, dir, budget)
		checkPartialPrefix(t, db, "docs")
		checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
		checkPartialTruth(t, db, "docs", truth, q, 17, "committed", n)
	}
}

func TestPartialQuantDimensionTail(t *testing.T) {
	hostSIMD := partialQuantTestModes(t)
	const n, dim = 515, 257
	db := openPartialDB(t, t.TempDir(), 160<<10)
	defer db.Close()
	truth := partialFixture(t, db, "docs", n, dim, false)
	x := checkPartialPrefix(t, db, "docs")
	q := make([]float32, dim)
	for j := range q {
		q[j] = float32((j*13)%31-15) / 8
	}
	for _, id := range []int64{x.pks[x.quantRows-1], x.pks[x.quantRows], x.pks[x.quantRows+1]} {
		partialUpdate(t, db, truth, id, "boundary", q)
	}
	for mode := 0; mode < 4; mode++ {
		prefilter6Bit, useSIMD, batchScan = mode == 1, hostSIMD && mode != 2, mode != 3
		checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
		checkPartialTruth(t, db, "docs", truth, q, 17, "boundary", -1)
		checkPartialTruth(t, db, "docs", truth, q, 17, "c1", n/3)
	}
}

func TestPartialQuantEqualityBudgetFallback(t *testing.T) {
	partialQuantTestModes(t)
	db := openPartialDB(t, t.TempDir(), 58<<10)
	defer db.Close()
	truth := partialFixture(t, db, "docs", 513, 33, true)
	x := checkPartialPrefix(t, db, "docs")
	q := make([]float32, 33)
	tailCat := truth[1].cat
	checkPartialTruth(t, db, "docs", truth, q, 17, tailCat, -1)
	if !x.eqFull || x.eqBytes != 0 || len(x.eq) != 0 {
		t.Fatal("high-cardinality equality allocation did not use the budget fallback")
	}
	checkPartialBudget(t, db)
	mustExec(t, db, "UPDATE docs SET cat='shared'")
	for id, row := range truth {
		row.cat = "shared"
		truth[id] = row
	}
	if x.eqFull {
		t.Fatal("mutation must allow equality grouping to be retried")
	}
	slots, ok := x.eqSlots(db, db.tables["docs"], 1, "shared")
	if !ok || len(slots) != len(truth) {
		t.Fatal("shared equality group must include both coded and uncoded rows")
	}
	checkPartialTruth(t, db, "docs", truth, q, 17, "shared", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "shared", 200)
	checkPartialBudget(t, db)
}

func TestPartialQuantSharedBudgetAndGrowth(t *testing.T) {
	partialQuantTestModes(t)
	db := openPartialDB(t, t.TempDir(), 58<<10)
	defer db.Close()
	aTruth := partialFixture(t, db, "a", 513, 33, false)
	a := checkPartialPrefix(t, db, "a")
	db.opts.IndexMemBytes = a.derivedBytes() + (58 << 10)
	bTruth := partialFixture(t, db, "b", 513, 33, false)
	b := checkPartialPrefix(t, db, "b")
	cTruth := partialFixture(t, db, "c", 513, 33, false)
	if !db.tables["c"].Index.Disabled {
		t.Fatal("third table slots should exceed the remaining shared budget")
	}
	q := make([]float32, 33)
	for _, entry := range []struct {
		name string
		rows map[int64]partialTruthRow
	}{{"a", aTruth}, {"b", bTruth}, {"c", cTruth}} {
		checkPartialTruth(t, db, entry.name, entry.rows, q, 17, "", -1)
	}
	if a.quantRows == 0 || b.quantRows == 0 {
		t.Fatal("both active indexes must initially retain a partial prefix")
	}
	// Grow through backing-array expansion and then the slot-only budget.
	// The optional prefix may be dropped once; every state remains exact.
	for id := int64(514); id <= 1113; id++ {
		v := make([]float32, 33)
		v[0] = float32(id%7) / 8
		partialInsert(t, db, "a", aTruth, id, "grown", v)
		checkPartialBudget(t, db)
		if id == 600 || id == 900 || id == 1113 {
			checkPartialTruth(t, db, "a", aTruth, q, 17, "", -1)
			checkPartialTruth(t, db, "a", aTruth, q, 17, "grown", 550)
		}
	}
	if !a.Disabled {
		t.Fatal("slot-only growth must eventually fall back within the shared budget")
	}
	checkPartialTruth(t, db, "b", bTruth, q, 17, "", -1)
	checkPartialTruth(t, db, "c", cTruth, q, 17, "", -1)
}

func TestPartialQuantTooSmallForOneBlock(t *testing.T) {
	partialQuantTestModes(t)
	const n, dim = 129, 33
	budget := int64(n*indexBytesPerRow + 127*(dim+32) + 4*dim)
	db := openPartialDB(t, t.TempDir(), budget)
	defer db.Close()
	truth := partialFixture(t, db, "docs", n, dim, false)
	x := db.tables["docs"].Index
	if x.Disabled || x.quantRows != 0 || x.codes != nil || x.qf != nil || x.qbounds != nil {
		t.Fatal("sub-block budget must retain only the exact slot index")
	}
	checkPartialTruth(t, db, "docs", truth, make([]float32, dim), 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, make([]float32, dim), 17, "c1", -1)
	checkPartialBudget(t, db)
}
