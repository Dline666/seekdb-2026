package engine

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	sp "minivectordb/internal/sqlparse"
	"minivectordb/internal/storage"
)

// fastKnobs saves and restores the package-level fast insert configuration so
// tests can force or disable the path regardless of the environment.
func fastKnobs(t *testing.T, enabled bool, minBytes, workers int) {
	t.Helper()
	oldE, oldM, oldW := fastInsertEnabled, fastInsertMinBytes, fastInsertWorkers
	fastInsertEnabled, fastInsertMinBytes, fastInsertWorkers = enabled, minBytes, workers
	t.Cleanup(func() {
		fastInsertEnabled, fastInsertMinBytes, fastInsertWorkers = oldE, oldM, oldW
	})
}

// TestFastInsertCounters: a benchmark-shaped statement must be handled, a
// duplicate inside it must fall back to the sequential duplicate_key error.
func TestFastInsertCounters(t *testing.T) {
	fastKnobs(t, true, 0, 0)
	db := open(t, t.TempDir())
	defer db.Close()
	mustExec(t, db, "CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), embedding VECTOR(128))")
	var sb strings.Builder
	sb.WriteString("INSERT INTO docs VALUES ")
	for i := 1; i <= 100; i++ {
		if i > 1 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "(%d,'c%d','[", i, i%7)
		for j := 0; j < 128; j++ {
			if j > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, "0.%017d", i*1000+j)
		}
		sb.WriteString("]')")
	}
	stmt := sb.String()
	h0 := fastInsertHandled.Load()
	f0 := fastInsertFallback.Load()
	r := mustExec(t, db, stmt)
	if !r.HasAffected || r.Affected != 100 {
		t.Fatalf("got %+v", r)
	}
	if got := fastInsertHandled.Load() - h0; got != 1 {
		t.Fatalf("fastInsertHandled moved by %d, want 1", got)
	}
	if got := fastInsertFallback.Load() - f0; got != 0 {
		t.Fatalf("fastInsertFallback moved by %d, want 0", got)
	}
	n := len(mustExec(t, db, "SELECT * FROM docs ORDER BY id").Rows)
	if n != 100 {
		t.Fatalf("table has %d rows, want 100", n)
	}
	_, err := db.Exec(stmt)
	if errName(err) != "duplicate_key" {
		t.Fatalf("duplicate insert gave %v, want duplicate_key", err)
	}
	if got := fastInsertFallback.Load() - f0; got != 1 {
		t.Fatalf("fastInsertFallback moved by %d, want 1", got)
	}
	if got := fastInsertHandled.Load() - h0; got != 1 {
		t.Fatalf("fastInsertHandled moved by %d, want 1 total", got)
	}
	if n := len(mustExec(t, db, "SELECT * FROM docs ORDER BY id").Rows); n != 100 {
		t.Fatalf("rejected insert changed the table to %d rows", n)
	}
}

// TestSplitInsertRows checks the splitter invariant: when it returns ok, the
// halves either fail to parse (safe fallback) or reassemble into exactly the
// rows the whole region parses to.
func TestSplitInsertRows(t *testing.T) {
	regions := []string{
		"(1,'a'),(2,'b')",
		"(1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e')",
		"(1,'a') , (2,'b')\n,\t(3,'c')",
		" (1,'a') ",
		"(1,'a'),(2,'b');",
		"(1,'x),(y'),(2,'b'),(3,'c')",  // ),( inside a string near the midpoint
		"(0,'x') -- ),(1,2)\n,(3,'y')", // ),( inside a -- comment: must refuse
		"(0,'x') # ),(1,2)\n,(3,'y')",  // ),( inside a # comment: must refuse
		"(1,'a') /* ),(2,'x') */ ,(3,'b')",
		"(1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e'),(6,'f'),(7,'g'),(8,'h')",
	}
	var rp, whole sp.RowParser
	for _, region := range regions {
		a, b, ok := splitInsertRows(region)
		if !ok {
			continue
		}
		if !strings.HasPrefix(b, "(") {
			t.Fatalf("%q: part B %q does not start with (", region, b)
		}
		want, werr := whole.ParseRows(region)
		if werr != nil {
			t.Fatalf("%q: whole region does not parse: %v", region, werr)
		}
		ra, ea := rp.ParseRows(a)
		rb, eb := rp.ParseRows(b)
		if ea != nil || eb != nil {
			continue // a bad split is allowed to fail; it falls back
		}
		if got := append(ra, rb...); !reflect.DeepEqual(got, want) {
			t.Fatalf("%q: split produced %v rows, want %v", region, got, want)
		}
	}
	// Comment traps: the only boundary is inside a line comment, so the
	// splitter must give up rather than produce a divergent split.
	for _, region := range []string{
		"(0,'x') -- ),(1,2)\n,(3,'y')",
		"(0,'x') # ),(1,2)\n,(3,'y')",
	} {
		if _, _, ok := splitInsertRows(region); ok {
			t.Fatalf("%q: split inside a line comment was accepted", region)
		}
	}
	// A single row region has no boundary.
	if _, _, ok := splitInsertRows(" (1,'a') "); ok {
		t.Fatal("single row region split")
	}
}

type diffTable struct {
	name string
	ddl  string
	cols []diffCol
}

type diffCol struct {
	name  string
	typ   string // "bigint" | "varchar" | "vector"
	param int
	pk    bool
}

var diffTables = []diffTable{
	{"t1", "CREATE TABLE t1 (id BIGINT PRIMARY KEY, c VARCHAR(32), v VECTOR(128))",
		[]diffCol{{"id", "bigint", 0, true}, {"c", "varchar", 32, false}, {"v", "vector", 128, false}}},
	{"t2", "CREATE TABLE t2 (v VECTOR(2), id BIGINT PRIMARY KEY, a BIGINT, d BIGINT, c VARCHAR(8))",
		[]diffCol{{"v", "vector", 2, false}, {"id", "bigint", 0, true}, {"a", "bigint", 0, false}, {"d", "bigint", 0, false}, {"c", "varchar", 8, false}}},
	{"t3", "CREATE TABLE t3 (c VARCHAR(64), b BIGINT, id BIGINT PRIMARY KEY, v VECTOR(16))",
		[]diffCol{{"c", "varchar", 64, false}, {"b", "bigint", 0, false}, {"id", "bigint", 0, true}, {"v", "vector", 16, false}}},
	{"t4", "CREATE TABLE t4 (id BIGINT PRIMARY KEY, v VECTOR(1))",
		[]diffCol{{"id", "bigint", 0, true}, {"v", "vector", 1, false}}},
}

type diffGen struct {
	r         *rand.Rand
	nextPK    map[string]int64
	committed map[string][]int64
}

func newDiffGen(seed int64) *diffGen {
	return &diffGen{
		r:         rand.New(rand.NewSource(seed)),
		nextPK:    map[string]int64{},
		committed: map[string][]int64{},
	}
}

func (g *diffGen) pick(ss ...string) string { return ss[g.r.Intn(len(ss))] }

func (g *diffGen) floatLit() string {
	switch g.r.Intn(10) {
	case 0, 1:
		return strconv.FormatFloat(g.r.Float64(), 'g', 17, 64) // workload shape
	case 2:
		return g.pick(".5", "5.", "0.", "-.25", "1e2", "-1.5e-3", "3.14", "-0", "0")
	default:
		return strconv.FormatFloat(g.r.Float64(), 'g', -1, 64)
	}
}

func (g *diffGen) vecLit(dim int) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for j := 0; j < dim; j++ {
		if j > 0 {
			sb.WriteString(g.pick(",", ", ", ",  "))
		}
		sb.WriteString(g.floatLit())
	}
	sb.WriteByte(']')
	if g.r.Intn(12) == 0 {
		return sb.String() // unquoted bracket literal
	}
	return "'" + sb.String() + "'"
}

func (g *diffGen) strLit(param int) string {
	var s string
	switch g.r.Intn(16) {
	case 0:
		s = "a),(b"
	case 1:
		s = "x') , ('y"
	case 2:
		s = "it''s"
	case 3:
		s = "say \"hi\""
	case 4:
		s = "你好，世界"
	case 5:
		s = "semi;colon"
	case 6:
		s = "with -- dash"
	case 7:
		s = "hash # tag"
	case 8:
		s = "/* fake */"
	case 9:
		s = ""
	default:
		s = "v" + strconv.Itoa(g.r.Intn(100000))
	}
	if g.r.Intn(40) == 0 {
		s += strings.Repeat("q", param) // usually too long
	}
	var sb strings.Builder
	sb.WriteByte('\'')
	for _, c := range s {
		if c == '\'' {
			sb.WriteString("''")
		} else {
			sb.WriteRune(c)
		}
	}
	sb.WriteByte('\'')
	if g.r.Intn(10) == 0 {
		// Same literal in double quotes (no '' escaping needed there, but a
		// doubled '' inside "" is literal text in MySQL mode anyway).
		if !strings.Contains(s, "\"") {
			return "\"" + s + "\""
		}
	}
	return sb.String()
}

func (g *diffGen) intLit() string {
	switch g.r.Intn(12) {
	case 0:
		return "-5"
	case 1:
		return "9223372036854775807"
	case 2:
		return "-9223372036854775808"
	case 3:
		return "1e3"
	case 4:
		return "+42"
	default:
		return strconv.Itoa(g.r.Intn(1000000))
	}
}

// allocPK draws a fresh primary key for the table.
func (g *diffGen) allocPK(t *diffTable) int64 {
	g.nextPK[t.name]++
	return g.nextPK[t.name]
}

// colVals produces one value per supplied column (in supplied order) for a
// clean row; pks are drawn from the table's pk stream.
func (g *diffGen) colVals(t *diffTable, order []int) []string {
	vals := make([]string, len(order))
	for i, ci := range order {
		c := t.cols[ci]
		switch c.typ {
		case "vector":
			vals[i] = g.vecLit(c.param)
		case "varchar":
			vals[i] = g.strLit(c.param)
		default:
			if c.pk {
				vals[i] = strconv.FormatInt(g.allocPK(t), 10)
			} else {
				vals[i] = g.intLit()
			}
		}
	}
	return vals
}

func (g *diffGen) insert(t *diffTable, commit bool) string {
	var sb strings.Builder
	ignore := g.r.Intn(30) == 0
	sb.WriteString(g.pick("INSERT", "insert", "Insert"))
	if ignore {
		sb.WriteString(" IGNORE")
	}
	sb.WriteString(" INTO ")
	if g.r.Intn(25) == 0 {
		sb.WriteString("mydb.")
	}
	sb.WriteString(t.name)
	order := make([]int, len(t.cols))
	for i := range order {
		order[i] = i
	}
	badCols := false
	if g.r.Intn(4) == 0 {
		g.r.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		sb.WriteString(" /*c*/ (")
		for i, ci := range order {
			if i > 0 {
				sb.WriteByte(',')
			}
			name := t.cols[ci].name
			switch g.r.Intn(30) {
			case 0:
				name, badCols = "nonexistent", true
			case 1:
				if i > 0 {
					name, badCols = t.cols[order[0]].name, true // duplicated column
				}
			}
			sb.WriteString(name)
		}
		sb.WriteByte(')')
	}
	sb.WriteByte(' ')
	sb.WriteString(g.pick("VALUES", "VALUE"))
	sb.WriteByte(' ')
	nrows := 1 + g.r.Intn(8)
	switch g.r.Intn(10) {
	case 0:
		nrows = 20 + g.r.Intn(60)
	case 1:
		nrows = 60 + g.r.Intn(140)
	}
	pkBase := g.nextPK[t.name]
	rows := make([]string, nrows)
	for i := range rows {
		vals := g.colVals(t, order)
		rows[i] = "(" + strings.Join(vals, ",") + ")"
	}
	corrupted := g.r.Intn(3) == 0
	if corrupted {
		g.corruptRows(t, rows, order)
	}
	sep := g.pick(",", ", ", ",\n", " , ")
	var body strings.Builder
	for i, row := range rows {
		if i > 0 {
			body.WriteString(sep)
			if g.r.Intn(20) == 0 {
				body.WriteString(" /* between */ ")
			}
		}
		body.WriteString(row)
	}
	sb.WriteString(body.String())
	switch g.r.Intn(12) {
	case 0:
		sb.WriteString(" ON DUPLICATE KEY UPDATE c='x'")
	case 1:
		sb.WriteString(";")
	case 2:
		sb.WriteString(";;")
	}
	if commit && !corrupted && !ignore && !badCols {
		// Best-effort pk tracking for "duplicate with existing" injections:
		// clean statements in committing contexts feed the pool.
		for pk := pkBase + 1; pk <= pkBase+int64(nrows); pk++ {
			g.committed[t.name] = append(g.committed[t.name], pk)
		}
	}
	return sb.String()
}

// corruptRows injects one error class into the row list.
func (g *diffGen) corruptRows(t *diffTable, rows []string, order []int) {
	i := g.r.Intn(len(rows))
	vals := g.colVals(t, order)
	bad := func(vs []string) { rows[i] = "(" + strings.Join(vs, ",") + ")" }
	switch g.r.Intn(14) {
	case 0: // duplicate pk inside the statement (same or other half)
		if len(rows) > 1 {
			j := g.r.Intn(len(rows) - 1)
			if j >= i {
				j++
			}
			rows[i] = rows[j]
			return
		}
		bad(vals)
	case 1: // duplicate with an existing row (when the pool has one)
		if pks := g.committed[t.name]; len(pks) > 0 {
			for k, ci := range order {
				if t.cols[ci].pk {
					vals[k] = strconv.FormatInt(pks[g.r.Intn(len(pks))], 10)
				}
			}
		}
		bad(vals)
	case 2: // wrong value count
		if len(vals) > 1 {
			vals = vals[:len(vals)-1]
		}
		bad(vals)
	case 3: // extra value
		bad(append(vals, "1"))
	case 4: // wrong vector dimension
		for k, ci := range order {
			if t.cols[ci].typ == "vector" {
				vals[k] = g.vecLit(t.cols[ci].param + []int{-1, 1, 2}[g.r.Intn(3)])
			}
		}
		bad(vals)
	case 5: // nan / overflow vector element
		for k, ci := range order {
			if t.cols[ci].typ == "vector" {
				vals[k] = "'[" + g.pick("nan", "NaN", "1e39", "-1e39") + ",0]'"
				if t.cols[ci].param == 1 {
					vals[k] = "'[" + g.pick("nan", "1e39") + "]'"
				}
			}
		}
		bad(vals)
	case 6: // non-vector text in the vector column
		for k, ci := range order {
			if t.cols[ci].typ == "vector" {
				vals[k] = g.pick("'abc'", "'[1,2'", "'[]'", "12345")
			}
		}
		bad(vals)
	case 7: // VARCHAR too long
		for k, ci := range order {
			if t.cols[ci].typ == "varchar" {
				vals[k] = "'" + strings.Repeat("z", t.cols[ci].param+1) + "'"
			}
		}
		bad(vals)
	case 8: // BIGINT out of range
		for k, ci := range order {
			if t.cols[ci].typ == "bigint" && !t.cols[ci].pk {
				vals[k] = g.pick("99999999999999999999", "1e19", "-1e19", "'xyz'")
			}
		}
		bad(vals)
	case 9: // non-literal value
		vals[g.r.Intn(len(vals))] = g.pick("1+1", "L2_DISTANCE('[1]','[1]')", "zzz", "- 7")
		bad(vals)
	case 10: // unterminated string: whole statement fails to lex
		rows[i] = "(" + strings.Join(vals[:len(vals)-1], ",") + ",'abc"
	case 11: // missing close paren
		rows[i] = "(" + strings.Join(vals, ",")
	case 12: // NULL literal
		vals[g.r.Intn(len(vals))] = "NULL"
		bad(vals)
	default: // empty row
		rows[i] = "()"
	}
}

// stream builds the deterministic statement stream shared by F and S.
func (g *diffGen) stream(n int) []string {
	stmts := make([]string, 0, n)
	tables := func(i int) *diffTable { return &diffTables[i] }
	for len(stmts) < n {
		switch x := g.r.Intn(100); {
		case x < 58: // plain insert
			t := tables(g.r.Intn(len(diffTables)))
			stmts = append(stmts, g.insert(t, true))
		case x < 68: // explicit transaction block
			t := tables(g.r.Intn(len(diffTables)))
			stmts = append(stmts, "BEGIN")
			willCommit := g.r.Intn(10) < 7
			for i, m := 0, 1+g.r.Intn(3); i < m; i++ {
				stmts = append(stmts, g.insert(t, willCommit))
			}
			if willCommit {
				stmts = append(stmts, "COMMIT")
			} else {
				stmts = append(stmts, "ROLLBACK")
			}
		case x < 79: // update
			t := tables(g.r.Intn(len(diffTables)))
			pk := int64(1 + g.r.Intn(200))
			col := "c"
			for _, c := range t.cols {
				if c.typ == "varchar" {
					col = c.name
					break
				}
			}
			if col == "c" && t.name == "t4" {
				stmts = append(stmts, fmt.Sprintf("UPDATE %s SET v='%s' WHERE id=%d", t.name, strings.Trim(g.vecLit(1), "'"), pk))
			} else {
				stmts = append(stmts, fmt.Sprintf("UPDATE %s SET %s=%s WHERE id=%d", t.name, col, g.strLit(16), pk))
			}
		case x < 88: // delete
			t := tables(g.r.Intn(len(diffTables)))
			stmts = append(stmts, fmt.Sprintf("DELETE FROM %s WHERE id=%d", t.name, int64(1+g.r.Intn(200))))
		case x < 93: // unsupported / odd insert shapes
			t := tables(g.r.Intn(len(diffTables)))
			stmts = append(stmts, g.pick(
				fmt.Sprintf("INSERT IGNORE INTO %s VALUES %s", t.name, g.row(t)),
				fmt.Sprintf("INSERT INTO %s SELECT * FROM %s", t.name, t.name),
				fmt.Sprintf("INSERT INTO %s VALUES %s,(1,2)", t.name, g.row(t)),
				fmt.Sprintf("INSERT INTO %s VALUES %s;", t.name, g.row(t)),
			))
		default: // insert-shaped statement on an unknown table
			s := g.insert(tables(0), false)
			s = strings.Replace(s, diffTables[0].name, "ghost", 1)
			stmts = append(stmts, s)
		}
	}
	return stmts[:n]
}

func (g *diffGen) row(t *diffTable) string {
	order := make([]int, len(t.cols))
	for i := range order {
		order[i] = i
	}
	return "(" + strings.Join(g.colVals(t, order), ",") + ")"
}

type diffRes struct {
	errS   string
	name   string
	aff    int64
	hasAff bool
	snaps  map[string]string
}

func runDiffStream(t *testing.T, db *DB, stmts []string) []diffRes {
	t.Helper()
	outs := make([]diffRes, len(stmts))
	for i, s := range stmts {
		res, err := db.Exec(s)
		o := &outs[i]
		if err != nil {
			o.errS = err.Error()
			o.name = errName(err)
		}
		if res != nil {
			o.aff = res.Affected
			o.hasAff = res.HasAffected
		}
		if i%50 == 0 || i == len(stmts)-1 {
			o.snaps = map[string]string{}
			for _, tb := range diffTables {
				r, e := db.Exec("SELECT * FROM " + tb.name + " ORDER BY id")
				if e != nil {
					t.Fatalf("snapshot %s after stmt %d: %v", tb.name, i, e)
				}
				bts, _ := json.Marshal(r.Rows)
				o.snaps[tb.name] = string(bts)
			}
		}
	}
	return outs
}

// TestFastInsertMatchesSequential runs the same deterministic statement
// stream on a fast-path database and a sequential one, comparing every
// statement result, periodic full-table snapshots, and finally the raw logs.
func TestFastInsertMatchesSequential(t *testing.T) {
	fastKnobs(t, true, 0, 0)
	const n = 3000
	stmts := newDiffGen(42).stream(n)
	for _, workers := range []int{1, 2} {
		fastInsertWorkers = workers
		dirF, dirS := t.TempDir(), t.TempDir()
		dbF, dbS := open(t, dirF), open(t, dirS)
		for _, tb := range diffTables {
			mustExec(t, dbF, tb.ddl)
			mustExec(t, dbS, tb.ddl)
		}
		h0, f0 := fastInsertHandled.Load(), fastInsertFallback.Load()
		outF := runDiffStream(t, dbF, stmts)
		fastInsertEnabled = false
		outS := runDiffStream(t, dbS, stmts)
		fastInsertEnabled = true
		handled, fellBack := fastInsertHandled.Load()-h0, fastInsertFallback.Load()-f0
		for i := range stmts {
			f, s := outF[i], outS[i]
			if f.errS != s.errS || f.name != s.name || f.aff != s.aff || f.hasAff != s.hasAff {
				t.Fatalf("workers=%d stmt %d %q:\nF: err=%q name=%s aff=%d has=%v\nS: err=%q name=%s aff=%d has=%v",
					workers, i, stmts[i], f.errS, f.name, f.aff, f.hasAff, s.errS, s.name, s.aff, s.hasAff)
			}
			for tn, sf := range f.snaps {
				if sf != s.snaps[tn] {
					t.Fatalf("workers=%d stmt %d table %s snapshot diverged:\nF: %s\nS: %s", workers, i, tn, sf, s.snaps[tn])
				}
			}
		}
		if err := dbF.Close(); err != nil {
			t.Fatal(err)
		}
		if err := dbS.Close(); err != nil {
			t.Fatal(err)
		}
		lf, err := os.ReadFile(filepath.Join(dirF, "data.log"))
		if err != nil {
			t.Fatal(err)
		}
		ls, err := os.ReadFile(filepath.Join(dirS, "data.log"))
		if err != nil {
			t.Fatal(err)
		}
		if string(lf) != string(ls) {
			t.Fatalf("workers=%d: data.log files differ (%d vs %d bytes)", workers, len(lf), len(ls))
		}
		dbF, dbS = open(t, dirF), open(t, dirS)
		for _, tb := range diffTables {
			rf, _ := dbF.Exec("SELECT * FROM " + tb.name + " ORDER BY id")
			rs, _ := dbS.Exec("SELECT * FROM " + tb.name + " ORDER BY id")
			bf, _ := json.Marshal(rf.Rows)
			bs, _ := json.Marshal(rs.Rows)
			if string(bf) != string(bs) {
				t.Fatalf("workers=%d: table %s differs after reopen", workers, tb.name)
			}
		}
		dbF.Close()
		dbS.Close()
		t.Logf("workers=%d: %d handled, %d fell back of %d statements", workers, handled, fellBack, n)
	}
}

// BenchmarkInsertBatch measures the scored write shape: 100 rows x VECTOR(128)
// with 17-digit literals, sequential vs one vs two fast-path workers.
func BenchmarkInsertBatch(b *testing.B) {
	build := func(base int64) string {
		rng := rand.New(rand.NewSource(base))
		var sb strings.Builder
		sb.WriteString("INSERT INTO docs VALUES ")
		for i := int64(0); i < 100; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, "(%d,'c%d','[", base+i, rng.Intn(10))
			for j := 0; j < 128; j++ {
				if j > 0 {
					sb.WriteByte(',')
				}
				sb.WriteString(strconv.FormatFloat(float64(float32(rng.Float64())), 'g', 17, 64))
			}
			sb.WriteString("]')")
		}
		return sb.String()
	}
	const ddl = "CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), embedding VECTOR(128))"
	for _, tc := range []struct {
		name    string
		enabled bool
		workers int
	}{
		{"seq", false, 0},
		{"fast1", true, 1},
		{"fast2", true, 2},
	} {
		b.Run(tc.name, func(b *testing.B) {
			oldE, oldM, oldW := fastInsertEnabled, fastInsertMinBytes, fastInsertWorkers
			defer func() { fastInsertEnabled, fastInsertMinBytes, fastInsertWorkers = oldE, oldM, oldW }()
			fastInsertEnabled, fastInsertMinBytes, fastInsertWorkers = tc.enabled, 0, tc.workers
			db, err := Open(b.TempDir(), Options{Sync: storage.SyncNone})
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()
			mustExecB(b, db, ddl)
			const pool = 8
			sqls := make([]string, pool)
			var base int64 = 1
			fill := func() {
				for i := range sqls {
					sqls[i] = build(base)
					base += 100
				}
			}
			fill()
			b.ReportAllocs()
			b.ResetTimer()
			for it := 0; it < b.N; it++ {
				if it > 0 && it%pool == 0 {
					b.StopTimer()
					mustExecB(b, db, "DROP TABLE docs")
					mustExecB(b, db, ddl)
					base = 1
					fill()
					b.StartTimer()
				}
				if _, err := db.Exec(sqls[it%pool]); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
