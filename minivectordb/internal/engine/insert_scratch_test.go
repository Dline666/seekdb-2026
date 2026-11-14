package engine

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"minivectordb/internal/storage"
)

// Each coordinate is generated independently of the parser and row encoder.
// Distinct rows detect scratch aliasing that distance-only checks can miss.
func TestInsertScratchValuesAtomicityAndRecovery(t *testing.T) {
	for _, dim := range []int{1, 127, 128, 256, 1024} {
		t.Run(strconv.Itoa(dim), func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir, Options{Sync: storage.SyncData})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			mustExec(t, db, fmt.Sprintf("CREATE TABLE docs(c VARCHAR(8),e VECTOR(%d),id BIGINT PRIMARY KEY)", dim))
			want := map[int64][]float32{}
			vector := func(id int64) ([]float32, string) {
				v := make([]float32, dim)
				parts := make([]string, dim)
				for j := range v {
					v[j] = float32((id*257+int64(j)*17)%65537-32768) / 1024
					if j%31 == 0 {
						v[j] = math.Float32frombits(0x80000000) // signed zero
					}
					parts[j] = strconv.FormatFloat(float64(v[j]), 'g', -1, 64)
				}
				return v, "[" + strings.Join(parts, ",") + "]"
			}
			insert := func(first, count int64, keep bool) {
				t.Helper()
				var tuples []string
				for id := first; id < first+count; id++ {
					v, text := vector(id)
					expr := "'" + text + "'"
					if id%3 == 0 {
						expr = "VEC_FROM_TEXT(" + expr + ")"
					}
					tuples = append(tuples, fmt.Sprintf("(%d,%s,'row')", id, expr))
					if keep {
						want[id] = v
					}
				}
				mustExec(t, db, "INSERT INTO docs(id,e,c) VALUES "+strings.Join(tuples, ","))
			}
			check := func() {
				t.Helper()
				r := mustExec(t, db, "SELECT id,e,c FROM docs ORDER BY id")
				if len(r.Rows) != len(want) {
					t.Fatalf("got %d rows, want %d", len(r.Rows), len(want))
				}
				for i, row := range r.Rows {
					id := row[0].I
					v, exists := want[id]
					if !exists || len(row[1].V) != dim || row[2].S != "row" || (i > 0 && r.Rows[i-1][0].I >= id) {
						t.Fatalf("unexpected row %d", id)
					}
					for j, x := range v {
						if math.Float32bits(row[1].V[j]) != math.Float32bits(x) {
							t.Fatalf("row %d coordinate %d changed", id, j)
						}
					}
				}
			}
			beforeSync := db.log.Stats().SyncCalls
			insert(1, 100, true)
			if db.log.Stats().SyncCalls != beforeSync+1 {
				t.Fatal("autocommit did not synchronize exactly once")
			}
			check()
			for _, transaction := range []bool{false, true} {
				if transaction {
					mustExec(t, db, "BEGIN")
					insert(201, 17, true)
				}
				_, valid := vector(301)
				for _, tc := range []struct{ last, errorName string }{
					{"(302,'[]','row')", "dimension_mismatch"},
					{"(302,'[1,NaN]','row')", "invalid_vector"},
					{"(302,'[1e39]','row')", "invalid_vector"},
					{"(302,'[1,]','row')", "invalid_vector"},
					{fmt.Sprintf("(301,'%s','row')", valid), "duplicate_key"},
					{fmt.Sprintf("(1,'%s','row')", valid), "duplicate_key"},
				} {
					size, syncs := db.log.Size(), db.log.Stats().SyncCalls
					_, err := db.Exec(fmt.Sprintf("INSERT INTO docs(id,e,c) VALUES(301,'%s','row'),%s", valid, tc.last))
					if errName(err) != tc.errorName {
						t.Fatalf("got %v, want %s", err, tc.errorName)
					}
					if db.log.Size() != size || db.log.Stats().SyncCalls != syncs {
						t.Fatal("rejected statement changed the log")
					}
					check()
				}
				if transaction {
					syncs := db.log.Stats().SyncCalls
					mustExec(t, db, "COMMIT")
					if db.log.Stats().SyncCalls != syncs+1 {
						t.Fatal("COMMIT did not synchronize exactly once")
					}
				}
			}
			mustExec(t, db, "BEGIN")
			insert(401, 100, false)
			mustExec(t, db, "ROLLBACK")
			check()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(dir, Options{Sync: storage.SyncData})
			if err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}
