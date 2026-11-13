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
	db.opts.IndexMemBytes = 10*(indexBytesPerRow+32+56) - 1
	tbl := db.tables["docs"]
	offs, _, ok := db.vectorTopK(tbl, q, nil, 10, false, nil)
	if !ok || len(offs) != 10 || tbl.Index.Disabled || tbl.Index.codes != nil {
		t.Fatal("expected an exact slot scan without quantized copies")
	}
}
