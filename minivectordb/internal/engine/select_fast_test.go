package engine

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"testing"

	sp "minivectordb/internal/sqlparse"
	"minivectordb/internal/types"
)

func selectIDs(t *testing.T, db *DB, sql string, col int, want []int64) {
	t.Helper()
	r := mustExec(t, db, sql)
	if len(r.Rows) != len(want) {
		t.Fatalf("%s: rows=%d want=%d", sql, len(r.Rows), len(want))
	}
	for i, id := range want {
		if got := r.Rows[i][col]; got.K != types.KInt || got.I != id {
			t.Fatalf("%s: row %d column %d got=%v want=%d", sql, i, col, got, id)
		}
	}
}

func TestSelectPrimaryAliasesAndLimits(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY,label VARCHAR(8),n BIGINT,e VECTOR(2))")
	mustExec(t, db, "INSERT INTO docs VALUES(9223372036854775807,'a',1,'[3,4]'),(4,'b',3,'[0,0]'),(-9223372036854775808,'b',2,'[1,0]'),(-1,'a',2,'[0,2]'),(0,'c',1,'[0,0]'),(2,'a',3,'[1,1]')")
	asc := []int64{math.MinInt64, -1, 0, 2, 4, math.MaxInt64}
	desc := []int64{math.MaxInt64, 4, 2, 0, -1, math.MinInt64}
	for _, tc := range []struct {
		sql  string
		col  int
		want []int64
	}{
		{"SELECT id FROM docs", 0, asc},
		{"SELECT id FROM docs ORDER BY id", 0, asc},
		{"SELECT id FROM docs ORDER BY id DESC", 0, desc},
		{"SELECT id AS pk FROM docs ORDER BY pk DESC", 0, desc},
		{"SELECT label,id FROM docs ORDER BY 2", 1, asc},
		{"SELECT * FROM docs ORDER BY 1 DESC", 0, desc},
		{"SELECT id FROM docs ORDER BY id DESC,label ASC,n DESC", 0, desc},
		{"SELECT d.id FROM docs AS d ORDER BY d.id", 0, asc},
		{"SELECT n AS id,docs.id AS real FROM docs ORDER BY docs.id DESC", 1, desc},
		// Alias precedence must not mistake n for the primary key.
		{"SELECT n AS id,docs.id AS real FROM docs ORDER BY id,real DESC", 1, []int64{math.MaxInt64, 0, -1, math.MinInt64, 4, 2}},
		{"SELECT id FROM docs ORDER BY label,id DESC", 0, []int64{math.MaxInt64, 2, -1, 4, math.MinInt64, 0}},
		{"SELECT id FROM docs WHERE label='a' ORDER BY id DESC LIMIT 2", 0, []int64{math.MaxInt64, 2}},
		{"SELECT id FROM docs ORDER BY id LIMIT 2 OFFSET 2", 0, []int64{0, 2}},
		{"SELECT id FROM docs ORDER BY id DESC LIMIT 2,3", 0, []int64{2, 0, -1}},
		{"SELECT id FROM docs ORDER BY id LIMIT 0 OFFSET 2", 0, nil},
		{"SELECT id FROM docs ORDER BY id LIMIT 99 OFFSET 6", 0, nil},
		{"SELECT id FROM docs ORDER BY id LIMIT 9223372036854775807 OFFSET 2", 0, []int64{0, 2, 4, math.MaxInt64}},
		{"SELECT id FROM docs ORDER BY id LIMIT 18446744073709551616", 0, asc},
		{"SELECT id FROM docs ORDER BY id LIMIT 2 OFFSET 18446744073709551616", 0, nil},
		{"SELECT id FROM docs ORDER BY id LIMIT 0 OFFSET 18446744073709551616", 0, nil},
		// Non-integer equality literals keep the general numeric predicate.
		{"SELECT id FROM docs WHERE id=2.0 ORDER BY id", 0, []int64{2}},
		{"SELECT id FROM docs WHERE id='2' ORDER BY id", 0, []int64{2}},
		{"SELECT id FROM docs WHERE id=1e100 ORDER BY id", 0, nil},
	} {
		selectIDs(t, db, tc.sql, tc.col, tc.want)
	}
	// The pressure driver's full census projects a category and norm while
	// ordering by PK. Projection must still evaluate every returned row.
	census := mustExec(t, db, "SELECT id,label,L2_DISTANCE(e,'[0,0]') AS norm FROM docs ORDER BY id")
	labels := []string{"b", "a", "c", "a", "b", "a"}
	norms := []float64{1, 2, 0, math.Sqrt(2), 0, 5}
	if len(census.Rows) != len(asc) {
		t.Fatal("full census row count changed")
	}
	for i, row := range census.Rows {
		if row[0].I != asc[i] || row[1].S != labels[i] || row[2].F != norms[i] {
			t.Fatalf("census row %d=%v, want=(%d,%s,%g)", i, row, asc[i], labels[i], norms[i])
		}
	}
	for _, tc := range []struct{ sql, want string }{
		{"SELECT id FROM docs ORDER BY id,e", "type_mismatch"},
		{"SELECT id FROM docs WHERE id=123 ORDER BY missing", "column_not_found"},
		{"SELECT id FROM docs WHERE id=123 AND missing=1 LIMIT 0", "column_not_found"},
		{"SELECT missing FROM docs WHERE id=123", "column_not_found"},
		{"SELECT id FROM docs WHERE id=123 ORDER BY 2", "column_not_found"},
		{"SELECT id FROM docs WHERE id=123 ORDER BY L2_DISTANCE(e,'[0]')", "dimension_mismatch"},
		{"SELECT id FROM docs WHERE id=NULL", "unsupported_sql"},
		{"SELECT id FROM docs ORDER BY NULL", "unsupported_sql"},
		{"SELECT NULL FROM docs WHERE id=123 LIMIT 0", "unsupported_sql"},
	} {
		if _, err := db.Exec(tc.sql); errName(err) != tc.want {
			t.Fatalf("%s: error=%v want=%s", tc.sql, err, tc.want)
		}
	}
}

func TestSelectPrimaryBoundedOrder(t *testing.T) {
	db := open(t, t.TempDir())
	defer db.Close()
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY)")
	ids := []int64{math.MinInt64, math.MaxInt64}
	for i := -127; i <= 127; i++ {
		ids = append(ids, int64(i)*3)
	}
	rng := rand.New(rand.NewSource(937))
	var sql strings.Builder
	sql.WriteString("INSERT INTO docs VALUES ")
	for i, pos := range rng.Perm(len(ids)) {
		if i != 0 {
			sql.WriteByte(',')
		}
		fmt.Fprintf(&sql, "(%d)", ids[pos])
	}
	mustExec(t, db, sql.String())
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, desc := range []bool{false, true} {
		ordered := append([]int64(nil), ids...)
		direction := "ASC"
		if desc {
			direction = "DESC"
			for i, j := 0, len(ordered)-1; i < j; i, j = i+1, j-1 {
				ordered[i], ordered[j] = ordered[j], ordered[i]
			}
		}
		for _, limit := range []int{0, 1, 3, 8, 65, 130, 257, 300} {
			for _, offset := range []int{0, 7, 130, 300} {
				lo := min(offset, len(ordered))
				hi := min(offset+limit, len(ordered))
				sql := fmt.Sprintf("SELECT id FROM docs ORDER BY id %s LIMIT %d OFFSET %d", direction, limit, offset)
				selectIDs(t, db, sql, 0, ordered[lo:hi])
			}
		}
	}
}

func TestSelectPointAndPrimaryTransactionVisibility(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer func() { db.Close() }()
	mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY,name VARCHAR(8),n BIGINT,e VECTOR(2))")
	mustExec(t, db, "INSERT INTO docs VALUES(-9223372036854775808,'min',1,'[3,4]'),(2,'two',2,'[1,2]'),(9223372036854775807,'max',3,'[0,1]')")
	for _, sql := range []string{
		"SELECT id FROM docs WHERE 2=id",
		"SELECT id FROM docs WHERE name='two' AND id=2",
		"SELECT d.id FROM docs AS d WHERE d.id=2 AND d.n=2 ORDER BY d.id DESC,n",
		"SELECT id FROM docs WHERE id=2 ORDER BY L2_DISTANCE(e,'[0,0]') LIMIT 1",
	} {
		selectIDs(t, db, sql, 0, []int64{2})
	}
	if db.tables["docs"].Index != nil {
		t.Fatal("a unique point result must not build a table-wide vector index")
	}
	selectIDs(t, db, "SELECT id FROM docs WHERE id=-9223372036854775808", 0, []int64{math.MinInt64})
	selectIDs(t, db, "SELECT id FROM docs WHERE id=9223372036854775807", 0, []int64{math.MaxInt64})
	for _, sql := range []string{
		"SELECT id FROM docs WHERE id=2 AND name='missing'",
		"SELECT id FROM docs WHERE id=2 AND id=3",
		"SELECT id FROM docs WHERE id=123",
		"SELECT id FROM docs WHERE id=2 LIMIT 0 OFFSET 1",
		"SELECT id FROM docs WHERE id=2 LIMIT 1 OFFSET 1",
	} {
		selectIDs(t, db, sql, 0, nil)
	}
	checkRow := func(name string, n int64, distance float64) {
		t.Helper()
		r := mustExec(t, db, "SELECT id,name,n,L2_DISTANCE(e,'[0,0]') AS d FROM docs WHERE 2=id ORDER BY d DESC LIMIT 1")
		if len(r.Rows) != 1 || r.Rows[0][0].I != 2 || r.Rows[0][1].S != name || r.Rows[0][2].I != n || r.Rows[0][3].F != distance {
			t.Fatalf("point row got=%v want=(2,%s,%d,%g)", r.Rows, name, n, distance)
		}
	}
	mustExec(t, db, "UPDATE docs SET n=9,e='[5,12]' WHERE id=2")
	checkRow("two", 9, 13)
	mustExec(t, db, "BEGIN")
	mustExec(t, db, "UPDATE docs SET name='tx',n=10,e='[8,15]' WHERE id=2")
	mustExec(t, db, "DELETE FROM docs WHERE id=-9223372036854775808")
	mustExec(t, db, "INSERT INTO docs VALUES(7,'new',7,'[1,0]')")
	checkRow("tx", 10, 17)
	selectIDs(t, db, "SELECT id FROM docs WHERE id=-9223372036854775808", 0, nil)
	selectIDs(t, db, "SELECT id FROM docs WHERE id=7 AND name='new'", 0, []int64{7})
	selectIDs(t, db, "SELECT id FROM docs ORDER BY id", 0, []int64{2, 7, math.MaxInt64})
	selectIDs(t, db, "SELECT id FROM docs ORDER BY id DESC LIMIT 2 OFFSET 1", 0, []int64{7, 2})
	mustExec(t, db, "ROLLBACK")
	checkRow("two", 9, 13)
	selectIDs(t, db, "SELECT id FROM docs WHERE id=7", 0, nil)
	selectIDs(t, db, "SELECT id FROM docs ORDER BY id", 0, []int64{math.MinInt64, 2, math.MaxInt64})
	mustExec(t, db, "BEGIN")
	mustExec(t, db, "UPDATE docs SET name='kept',n=11,e='[7,24]' WHERE id=2")
	mustExec(t, db, "DELETE FROM docs WHERE id=9223372036854775807")
	mustExec(t, db, "INSERT INTO docs VALUES(7,'new',7,'[1,0]')")
	mustExec(t, db, "COMMIT")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	checkRow("kept", 11, 25)
	selectIDs(t, db, "SELECT id FROM docs WHERE id=9223372036854775807", 0, nil)
	selectIDs(t, db, "SELECT id FROM docs WHERE id=7 AND n=7", 0, []int64{7})
	selectIDs(t, db, "SELECT id FROM docs ORDER BY id DESC", 0, []int64{7, 2, math.MinInt64})
}

func TestSelectPrimaryPlanningDoesNotReadLog(t *testing.T) {
	// Deliberately omit storage: choosing primary-key order only needs the
	// authoritative offsets, even with an overlay. Any row read here panics.
	tbl := newTable(1, "docs", []types.Column{{Name: "id", Type: types.TBigint}}, 0)
	tbl.rows = map[int64]int64{math.MinInt64: 101, -1: 202, 0: 303, math.MaxInt64: 404}
	db := &DB{tables: map[string]*Table{"docs": tbl}}
	sc := &scope{t: tbl}
	pk, err := sc.compile(&sp.ColRef{Name: "id"})
	if err != nil {
		t.Fatal(err)
	}
	checkOffsets := func(keys []sortKey, want int64, expected []int64) {
		t.Helper()
		got := db.scanSorted(tbl, nil, keys, want)
		if len(got) != len(expected) {
			t.Fatalf("offset count=%d want=%d", len(got), len(expected))
		}
		for i := range got {
			if got[i] != expected[i] {
				t.Fatalf("offset %d=%d want=%d", i, got[i], expected[i])
			}
		}
	}
	checkOffsets(nil, math.MaxInt64, []int64{101, 202, 303, 404})
	checkOffsets([]sortKey{{c: pk, desc: true}}, 2, []int64{404, 303})
	db.txn = &Txn{overlay: map[uint32]map[int64]int64{1: {math.MinInt64: deleted, 2: 505, math.MaxInt64: 606}}}
	checkOffsets([]sortKey{{c: pk}}, math.MaxInt64, []int64{202, 303, 505, 606})
	checkOffsets([]sortKey{{c: pk, desc: true}}, 3, []int64{606, 505, 303})
	selectIDs(t, db, "SELECT id FROM docs WHERE id=123", 0, nil)
	selectIDs(t, db, "SELECT id FROM docs WHERE id=2 LIMIT 0 OFFSET 3", 0, nil)
	selectIDs(t, db, "SELECT id FROM docs ORDER BY id LIMIT 0 OFFSET 3", 0, nil)
}
