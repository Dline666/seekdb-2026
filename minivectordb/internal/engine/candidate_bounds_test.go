package engine

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCandidateBoundCacheLifetimeAndBudget(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY, category VARCHAR(8), embedding VECTOR(32))")
	q := make([]float32, 32)
	var rows []string
	for i := 0; i < 513; i++ {
		v := make([]float32, 32)
		v[i%32] = float32(i%17) - 8
		rows = append(rows, fmt.Sprintf("(%d,'c%d','%s')", i, i%2, vectorText(v)))
	}
	mustExec(t, db, "INSERT INTO docs VALUES "+strings.Join(rows, ","))
	tbl := db.tables["docs"]
	db.vectorTopK(tbl, q, nil, 10, false, nil)
	x := tbl.Index
	cand, ok := x.eqSlots(db, tbl, 1, "c0")
	if !ok {
		t.Fatal("missing candidates")
	}
	before := x.derivedBytes()
	db.opts.IndexMemBytes = before + quantBlockBytes(len(cand)) - 1
	if x.candidateBounds(db, 1, "c0") != nil || x.derivedBytes() != before {
		t.Fatal("cache exceeded budget")
	}
	db.opts.IndexMemBytes = before + quantBlockBytes(len(cand))
	bounds := x.candidateBounds(db, 1, "c0")
	if len(bounds) != 3 || x.derivedBytes() != db.opts.IndexMemBytes {
		t.Fatal("budget charge or block count")
	}
	for i, b := range bounds {
		lo := i * quantBlockRows
		hi := min(lo+quantBlockRows, len(cand))
		if b != x.boundForSlots(cand[lo:hi]) {
			t.Fatal("cached merge differs")
		}
		for j := lo; j < hi; j++ {
			sub := x.boundForSlots(cand[j:hi])
			if sub.maxScale > b.maxScale || sub.maxAbs > b.maxAbs {
				t.Fatal("subset not bounded")
			}
		}
	}
	query := fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS d FROM docs WHERE category='c0' ORDER BY d LIMIT 10", vectorText(q))
	got := mustExec(t, db, query).Rows
	old := candidateBoundsEnabled
	candidateBoundsEnabled = false
	want := mustExec(t, db, query).Rows
	candidateBoundsEnabled = old
	if !reflect.DeepEqual(got, want) {
		t.Fatal("cached path changed exact output")
	}
	mustExec(t, db, "UPDATE docs SET category='c1' WHERE id=0")
	if x.eq != nil || x.eqBytes != 0 {
		t.Fatal("stale cached bounds after mutation")
	}
}
