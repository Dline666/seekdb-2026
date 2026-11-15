package engine

import "testing"

// Deleting rows cannot release the backing map or slot arrays. The newly
// allocated free-slot list therefore needs its own budget check, even when
// the live row count falls and the row payload is no longer indexed.
func TestPartialQuantDeleteEnforcesBudget(t *testing.T) {
	partialQuantTestModes(t)
	const n, dim = 40, 32
	fullBudget := int64(n)*(indexBytesPerRow+dim+32) + int64(dim)*4 + quantBlockBytes(n)
	for _, tc := range []struct {
		name   string
		budget int64
		full   bool
	}{
		{"full-quantization", fullBudget, true},
		{"slots-only", int64(n) * indexBytesPerRow, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			db := openPartialDB(t, dir, tc.budget)
			defer func() { db.Close() }()
			truth := partialFixture(t, db, "docs", n, dim, false)
			x := db.tables["docs"].Index
			if x.Disabled || x.derivedBytes() != tc.budget {
				t.Fatalf("fixture must exactly fill its budget: disabled=%t derived=%d budget=%d", x.Disabled, x.derivedBytes(), tc.budget)
			}
			if tc.full && x.quantRows != n || !tc.full && x.quantRows != 0 {
				t.Fatalf("wrong initial coverage: full=%t Q=%d", tc.full, x.quantRows)
			}
			if len(x.free) != 0 || cap(x.free) != 0 {
				t.Fatal("fixture must allocate its first free slot on DELETE")
			}

			// The final inserted row ties at distance zero and has the lowest
			// PK, so removing it must change the independent expected TopK.
			q := make([]float32, dim)
			checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
			mustExec(t, db, "DELETE FROM docs WHERE id=1")
			delete(truth, 1)

			// Check before any query can alter derived state. On the broken
			// implementation free grows while the other retained charges stay
			// fixed, exceeding the originally exact-fit budget.
			checkPartialBudget(t, db)
			if tc.full {
				if x.Disabled || x.quantRows != 0 || x.codes != nil || x.qf != nil || x.qbounds != nil {
					t.Fatal("DELETE should release optional quantization and retain the exact slot index")
				}
			} else if !x.Disabled || x.derivedBytes() != 0 || x.posRows != 0 {
				t.Fatal("DELETE must disable an over-budget slot index and release its retained charges")
			}
			checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
			checkPartialTruth(t, db, "docs", truth, q, 17, "c1", -1)
			checkPartialTruth(t, db, "docs", truth, q, 17, "c1", 20)
			checkPartialBudget(t, db)

			// The fallback is derived state; the committed deletion must
			// survive rebuilding the explicit index with the same allowance.
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = openPartialDB(t, dir, tc.budget)
			checkPartialBudget(t, db)
			checkPartialTruth(t, db, "docs", truth, q, 17, "", -1)
			checkPartialTruth(t, db, "docs", truth, q, 17, "c1", -1)
		})
	}
}
