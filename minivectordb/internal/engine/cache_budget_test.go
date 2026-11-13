package engine

import (
	"fmt"
	"reflect"
	"testing"
)

func TestCategoryBudgetAndMutation(t *testing.T) {
	old := catBlocksEnabled
	catBlocksEnabled = true
	defer func() { catBlocksEnabled = old }()
	db := open(t, t.TempDir())
	defer db.Close()
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY, category VARCHAR(8), embedding VECTOR(32))")
	q := make([]float32, 32)
	for i := 0; i < 40; i++ {
		v := make([]float32, 32)
		v[0] = float32(i)
		mustExec(t, db, fmt.Sprintf("INSERT INTO docs VALUES (%d, 'c%d', '%s')", i+1, i%2, vectorText(v)))
	}
	query := fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS d FROM docs WHERE category='c1' ORDER BY d LIMIT 10", vectorText(q))
	// Build only the primary index; give category copies room for one value.
	tbl := db.tables["docs"]
	db.vectorTopK(tbl, q, nil, 10, false, nil)
	x := tbl.Index
	x.eqSlots(db, tbl, 1, "c0")
	db.opts.IndexMemBytes = x.derivedBytes() + 20*(32+48) + 258
	if x.catBlockFor(db, tbl, 1, "c0") == nil {
		t.Fatal("first category should fit")
	}
	if x.catBlockFor(db, tbl, 1, "c1") != nil {
		t.Fatal("second category exceeds the shared budget")
	}
	got := mustExec(t, db, query).Rows
	catBlocksEnabled = false
	want := mustExec(t, db, query).Rows
	catBlocksEnabled = true
	if !reflect.DeepEqual(got, want) {
		t.Fatal("budget fallback changed exact results")
	}
	// A mutation must release even categories that are never queried again.
	mustExec(t, db, "UPDATE docs SET category='c1' WHERE id=1")
	if x.cat != nil || x.eq != nil || x.catBytes != 0 {
		t.Fatal("mutation retained obsolete derived sets")
	}
	got = mustExec(t, db, query).Rows
	catBlocksEnabled = false
	want = mustExec(t, db, query).Rows
	if !reflect.DeepEqual(got, want) || len(got) == 0 || got[0][0].I != 1 {
		t.Fatalf("updated nearest row missing or stale: %v", got)
	}
}

func TestPrefilterSharesIndexBudget(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	q := make([]float32, 32)
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY, embedding VECTOR(32))")
	for i := 0; i < 10; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO docs VALUES(%d,'%s')", i, vectorText(q)))
	}
	// Slots alone and codes alone fit; together they exceed the budget.
	db.opts.IndexMemBytes = 10*(indexBytesPerRow+32+32) - 1
	tbl := db.tables["docs"]
	offs, _, ok := db.vectorTopK(tbl, q, nil, 10, false, nil)
	if !ok || len(offs) != 10 || tbl.Index.Disabled || tbl.Index.codes != nil {
		t.Fatal("expected an exact slot scan without quantized copies")
	}
}

func TestEqualityBudgetHighCardinality(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY, category VARCHAR(64), embedding VECTOR(32))")
	q := make([]float32, 32)
	for i := 0; i < 100; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO docs VALUES(%d,'unique-category-%d','%s')", i, i, vectorText(q)))
	}
	query := fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS d FROM docs WHERE category='unique-category-99' ORDER BY d LIMIT 10", vectorText(q))
	tbl := db.tables["docs"]
	db.vectorTopK(tbl, q, nil, 10, false, nil)
	x := tbl.Index
	// Candidate slots fit, but all owned keys and grouping maps do not.
	db.opts.IndexMemBytes = x.derivedBytes() + int64(x.live)*4 + 400
	got := mustExec(t, db, query).Rows
	if !x.eqFull || x.eqBytes != 0 || len(x.eq) != 0 || len(got) != 1 || got[0][0].I != 99 {
		t.Fatalf("high-cardinality fallback lost a matching row: %v", got)
	}
	mustExec(t, db, "UPDATE docs SET category='shared'")
	if x.eqFull {
		t.Fatal("mutation must allow a smaller grouping to be built")
	}
	slots, ok := x.eqSlots(db, tbl, 1, "shared")
	if !ok || len(slots) != 100 || x.derivedBytes() > db.opts.IndexMemBytes {
		t.Fatal("single-category grouping should fit after mutation")
	}
}

func TestAggregateIndexBudget(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	q := make([]float32, 32)
	for _, name := range []string{"a", "b"} {
		mustExec(t, db, fmt.Sprintf("CREATE TABLE %s(id BIGINT PRIMARY KEY, embedding VECTOR(32))", name))
		for i := 0; i < 40; i++ {
			mustExec(t, db, fmt.Sprintf("INSERT INTO %s VALUES(%d,'%s')", name, i, vectorText(q)))
		}
	}
	db.vectorTopK(db.tables["a"], q, nil, 10, false, nil)
	a := db.tables["a"].Index
	db.opts.IndexMemBytes = a.derivedBytes() + 40*indexBytesPerRow - 1
	query := fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS d FROM b ORDER BY d LIMIT 10", vectorText(q))
	got := mustExec(t, db, query).Rows
	b := db.tables["b"].Index
	if !b.Disabled || a.derivedBytes()+b.derivedBytes() > db.opts.IndexMemBytes || len(got) != 10 {
		t.Fatal("second table must use exact fallback when shared budget is full")
	}
	for i, row := range got {
		if row[0].I != int64(i) {
			t.Fatal("fallback tie ordering changed")
		}
	}
	// Incremental growth must charge slice capacity, not just live rows.
	for i := 40; i < 100; i++ {
		mustExec(t, db, fmt.Sprintf("INSERT INTO a VALUES(%d,'%s')", i, vectorText(q)))
		if a.derivedBytes()+b.derivedBytes() > db.opts.IndexMemBytes {
			t.Fatalf("retained capacity exceeded budget after row %d", i)
		}
	}
	query = fmt.Sprintf("SELECT id FROM a ORDER BY L2_DISTANCE(embedding,'%s') LIMIT 10", vectorText(q))
	got = mustExec(t, db, query).Rows
	for i, row := range got {
		if row[0].I != int64(i) {
			t.Fatal("incremental fallback lost exact ties")
		}
	}
}
