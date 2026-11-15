package engine

import (
	"math"
	"testing"
)

// A frozen full index can end inside a 128-row block. Rebuild-only partial
// fixtures cannot exercise this state because their prefixes are aligned.
func TestPartialQuantFreezeAtUnalignedBoundary(t *testing.T) {
	hostSIMD := partialQuantTestModes(t)
	parallelMinSlots = math.MaxInt
	const n, dim = 129, 3072
	fullBytes := int64(n)*(indexBytesPerRow+dim+32) + int64(dim)*4 + quantBlockBytes(n)
	// One more fully encoded row costs 3168 bytes. The slot/map capacity
	// growth costs less than 3000 bytes, so the existing prefix can survive
	// freezing without allocating another row of codes.
	budget := fullBytes + 3000
	dir := t.TempDir()
	db := openPartialDB(t, dir, budget)
	defer func() { db.Close() }()
	truth := partialFixture(t, db, "docs", n, dim, false)
	x := db.tables["docs"].Index
	if x.Disabled || x.quantRows != n || x.quantLimit != -1 || x.derivedBytes() != fullBytes {
		t.Fatalf("expected initial full index: disabled=%t Q=%d limit=%d bytes=%d want=%d", x.Disabled, x.quantRows, x.quantLimit, x.derivedBytes(), fullBytes)
	}
	q := make([]float32, dim)
	far := make([]float32, dim)
	for j := range far {
		far[j] = 2
	}
	// The final coded row will later become nearest. Its previous constants
	// would reject it against a zero-distance heap unless UPDATE reencodes.
	partialUpdate(t, db, truth, 1, "boundary", far)
	partialInsert(t, db, "docs", truth, 0, "tail", q)
	checkBoundary := func() {
		t.Helper()
		if x.Disabled || x.quantRows != n || x.quantLimit != n || len(x.pks) != n+1 || x.quantRows%quantBlockRows == 0 {
			t.Fatalf("expected retained nonaligned frozen prefix: disabled=%t Q=%d limit=%d slots=%d", x.Disabled, x.quantRows, x.quantLimit, len(x.pks))
		}
		if len(x.codes) != n*dim || len(x.qf) != n*4 || len(x.qbounds) != 2 {
			t.Fatal("frozen growth appended or removed encoded storage")
		}
		checkPartialBudget(t, db)
	}
	checkBoundary()
	batches, coded, uncoded := partialPrefilterBatches.Load(), partialPrefilterRows.Load(), partialUncodedRows.Load()
	// The first full batch establishes a zero-distance heap; batch two has
	// slot 128 (coded) and slot 129 (uncoded), whose PK=0 must win the tie.
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	if hostSIMD && (partialPrefilterBatches.Load() <= batches || partialPrefilterRows.Load() <= coded || partialUncodedRows.Load() <= uncoded) {
		t.Fatal("nonaligned freeze fixture did not exercise a mixed coded/tail batch")
	}
	checkPartialTruth(t, db, "docs", truth, q, 17, "tail", -1)

	partialUpdate(t, db, truth, 1, "boundary", q)
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	tailQuery := make([]float32, dim)
	tailQuery[dim-1] = 0.5
	partialUpdate(t, db, truth, 0, "tail", tailQuery)
	checkPartialTruth(t, db, "docs", truth, tailQuery, 1, "", -1)
	checkPartialTruth(t, db, "docs", truth, tailQuery, 17, "tail", -1)
	checkBoundary()

	// Leave a rejecting code in the covered slot, then delete both boundary
	// rows and reuse their slots in reverse order. The new lower PK ties
	// arrive last and must still rank first.
	partialUpdate(t, db, truth, 1, "boundary", far)
	mustExec(t, db, "DELETE FROM docs WHERE id=1")
	delete(truth, 1)
	mustExec(t, db, "DELETE FROM docs WHERE id=0")
	delete(truth, 0)
	checkBoundary()
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	partialInsert(t, db, "docs", truth, -1, "reused", q)
	partialInsert(t, db, "docs", truth, -2, "reused", q)
	if x.pos[-1] != n || x.pos[-2] != n-1 {
		t.Fatal("fixture did not reuse the uncoded and coded boundary slots")
	}
	checkBoundary()
	for _, six := range []bool{false, true} {
		prefilter6Bit = six
		checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
		checkPartialTruth(t, db, "docs", truth, q, 17, "reused", -1)
	}
	prefilter6Bit = false
	parallelMinSlots = 1
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	useSIMD = false
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	useSIMD = hostSIMD
	checkPartialBudget(t, db)

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openPartialDB(t, dir, budget)
	// Recovery may choose a different safe coverage under its headroom rule.
	checkPartialBudget(t, db)
	checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
	checkPartialTruth(t, db, "docs", truth, q, 17, "reused", -1)
}
