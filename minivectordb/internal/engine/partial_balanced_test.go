package engine

import (
	"math"
	"testing"
)

func TestPartialBalancedIndependentTruth(t *testing.T) {
	if !partialQuantTestModes(t) {
		t.Skip("balanced partial scans require SIMD")
	}
	const n, dim, budget = 1539, 33, 180 << 10
	db := openPartialDB(t, t.TempDir(), budget)
	defer db.Close()
	truth := partialFixture(t, db, "docs", n, dim, false)
	x := checkPartialPrefix(t, db, "docs")
	qn := x.quantRows
	if scanWorkers(n) != 2 || qn < 2 || n-qn < 2 || (qn+(n-qn)/2)%scanBatch == 0 {
		t.Fatal("fixture must give both workers mixed ranges and an unaligned tail split")
	}
	zero := make([]float32, dim)
	q := make([]float32, dim)
	q[0], q[dim-1] = 0.125, -0.25
	// Put exact ties at both sides of every new range boundary. Missing a
	// range, revisiting a row, or losing PK tie order changes the oracle result.
	for _, slot := range []int{qn/2 - 1, qn / 2, qn - 1, qn, qn + (n-qn)/2 - 1, qn + (n-qn)/2, n - 1} {
		partialUpdate(t, db, truth, x.pks[slot], "c1", q)
	}
	check := func(query []float32, k int, cat string, minID int64) {
		t.Helper()
		before := partialBalancedWorkers.Load()
		checkPartialTruth(t, db, "docs", truth, query, k, cat, minID)
		if delta := partialBalancedWorkers.Load() - before; delta != 2 {
			t.Fatalf("query must use two workers with coded and uncoded ranges: got %d", delta)
		}
	}
	check(q, 17, "", -1)
	check(q, 17, "c1", -1)
	check(q, 17, "c1", 100)
	check(zero, 31, "", -1)
	prefilter6Bit = true
	check(q, 17, "c1", 100)
	prefilter6Bit = false

	// The normal equality set is ordered. Deliberately reorder its backing
	// slice to prove the production guard chooses the original partitioning.
	slots, ok := x.eqSlots(db, db.tables["docs"], 1, "c1")
	if !ok || len(slots) < 2 {
		t.Fatal("missing equality candidates")
	}
	func() {
		slots[0], slots[len(slots)-1] = slots[len(slots)-1], slots[0]
		defer func() { slots[0], slots[len(slots)-1] = slots[len(slots)-1], slots[0] }()
		before := partialBalancedWorkers.Load()
		checkPartialTruth(t, db, "docs", truth, q, 17, "c1", -1)
		if partialBalancedWorkers.Load() != before {
			t.Fatal("unordered candidate slots used the balanced partition")
		}
	}()

	// Shadow a row from each region and add a lower-PK overlay tie. The
	// overlay is merged once, after the two workers' disjoint base ranges.
	committed := clonePartialTruth(truth)
	mustExec(t, db, "BEGIN")
	partialUpdate(t, db, truth, x.pks[qn/2], "c1", zero)
	partialUpdate(t, db, truth, x.pks[qn+(n-qn)/2], "c1", zero)
	partialInsert(t, db, "docs", truth, -100, "c1", zero)
	check(zero, 17, "", -1)
	check(zero, 17, "c1", -1)
	check(zero, 17, "c1", 100)
	mustExec(t, db, "ROLLBACK")
	truth = committed
	check(q, 17, "", -1)
	checkPartialBudget(t, db)

	// Serial scans and unavailable quantization retain the existing route.
	parallelMinSlots = math.MaxInt
	before := partialBalancedWorkers.Load()
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	parallelMinSlots, prefilterEnabled = 1, false
	checkPartialTruth(t, db, "docs", truth, q, 17, "c1", -1)
	if partialBalancedWorkers.Load() != before {
		t.Fatal("fallback query used a balanced partial partition")
	}
}

func TestPartialBalancedCandidateOrder(t *testing.T) {
	for _, tc := range []struct {
		name   string
		slots  []int32
		q, cut int
		ok     bool
	}{
		{"empty", nil, 128, 0, true},
		{"covered", []int32{0, 63, 127}, 128, 3, true},
		{"uncoded", []int32{128, 129, 300}, 128, 0, true},
		{"boundary", []int32{0, 127, 128, 300}, 128, 2, true},
		{"unaligned", []int32{1, 128, 129, 300}, 129, 2, true},
		{"unordered", []int32{300, 0, 127, 128}, 128, 0, false},
		{"duplicate", []int32{0, 0, 128}, 128, 0, false},
	} {
		cut, ok := partialCandidateCut(tc.slots, tc.q)
		if cut != tc.cut || ok != tc.ok {
			t.Fatalf("%s: cut=%d ok=%t want cut=%d ok=%t", tc.name, cut, ok, tc.cut, tc.ok)
		}
	}
}
