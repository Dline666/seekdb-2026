package engine

import (
	"fmt"
	"strings"
	"testing"

	"minivectordb/internal/storage"
)

// The pending transaction starts with rows still in Log.buf. Each streamed
// UPDATE then appends more than the 4 MiB flush threshold, so subsequent row
// reads must work across buffer reuse and freshly flushed/mapped records.
func TestStreamingUpdateFlushesTransactionRows(t *testing.T) {
	forceStreamingUpdate(t)
	const dim, rows = 2048, 530
	const changedID, changedA, changedB = 7, 90001, 90002
	const addedID, addedA, addedB = rows + 1, 91001, 91002
	if (rows+1)*(dim*4+24) <= 4<<20 {
		t.Fatal("fixture must exceed the log flush threshold in each update")
	}

	replacement := make([]float32, dim)
	for i := range replacement {
		replacement[i] = float32(i%7 - 3)
	}
	baseVector := func(id int) []float32 {
		v := make([]float32, dim)
		v[0], v[dim-1] = float32(id%101), -float32(id%97)
		return v
	}

	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			db, err := Open(dir, Options{Sync: storage.SyncData})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			mustExec(t, db, fmt.Sprintf("CREATE TABLE docs(id BIGINT PRIMARY KEY,a BIGINT,b BIGINT,e VECTOR(%d))", dim))
			for first := 1; first <= rows; first += 32 {
				var sql strings.Builder
				sql.WriteString("INSERT INTO docs VALUES ")
				for id := first; id <= min(first+31, rows); id++ {
					if id > first {
						sql.WriteByte(',')
					}
					fmt.Fprintf(&sql, "(%d,%d,%d,'%s')", id, id*3+1, id*3+2, vectorText(baseVector(id)))
				}
				mustExec(t, db, sql.String())
			}

			// Expected values are derived from the fixture, independently of
			// engine row decoding, encoding, update planning or a snapshot.
			check := func(tag string, pending bool, swapped bool) {
				t.Helper()
				r := mustExec(t, db, "SELECT id,a,b,e FROM docs ORDER BY id")
				wantRows := rows
				if pending {
					wantRows++
				}
				if len(r.Rows) != wantRows {
					t.Fatalf("%s: rows=%d want=%d", tag, len(r.Rows), wantRows)
				}
				for i, row := range r.Rows {
					id := i + 1
					a, b := int64(id*3+1), int64(id*3+2)
					if pending && id == changedID {
						a, b = changedA, changedB
					}
					if pending && id == addedID {
						a, b = addedA, addedB
					}
					if swapped {
						a, b = b, a
					}
					if row[0].I != int64(id) || row[1].I != a || row[2].I != b {
						t.Fatalf("%s row %d: got id,a,b=%d,%d,%d want=%d,%d,%d", tag, i, row[0].I, row[1].I, row[2].I, id, a, b)
					}
					if len(row[3].V) != dim {
						t.Fatalf("%s id=%d vector dimension=%d", tag, id, len(row[3].V))
					}
					for j, got := range row[3].V {
						var want float32
						if pending {
							want = float32(j%7 - 3)
						} else if j == 0 {
							want = float32(id % 101)
						} else if j == dim-1 {
							want = -float32(id % 97)
						}
						if got != want {
							t.Fatalf("%s id=%d coordinate=%d got=%g want=%g", tag, id, j, got, want)
						}
					}
				}
			}

			mustExec(t, db, "BEGIN")
			before := db.log.Stats()
			mustExec(t, db, fmt.Sprintf("UPDATE docs SET a=%d,b=%d WHERE id=%d", changedA, changedB, changedID))
			mustExec(t, db, fmt.Sprintf("INSERT INTO docs VALUES(%d,%d,%d,'%s')", addedID, addedA, addedB, vectorText(baseVector(addedID))))
			if now := db.log.Stats(); now.WriteCalls != before.WriteCalls || now.SyncCalls != before.SyncCalls {
				t.Fatal("prior transaction writes must still be buffered")
			}
			if len(db.txn.overlay[db.tables["docs"].ID]) != 2 {
				t.Fatal("fixture must contain both a changed and a newly inserted pending row")
			}
			queries := []string{
				fmt.Sprintf("UPDATE docs SET a=b,b=a,e='%s'", vectorText(replacement)),
				"UPDATE docs SET a=b,b=a,e=e",
			}
			for i, sql := range queries {
				writes := db.log.Stats().WriteCalls
				r := mustExec(t, db, sql)
				if r.Affected != rows+1 || db.log.Stats().WriteCalls <= writes {
					t.Fatalf("update %d: must match %d rows and cross a log flush", i+1, rows+1)
				}
				if db.log.Stats().SyncCalls != before.SyncCalls {
					t.Fatal("streamed statement synced before explicit COMMIT")
				}
				check(fmt.Sprintf("pending update %d", i+1), true, i == 0)
			}
			if commit {
				mustExec(t, db, "COMMIT")
				if db.log.Stats().SyncCalls != before.SyncCalls+1 {
					t.Fatal("explicit COMMIT must perform one durability sync")
				}
			} else {
				mustExec(t, db, "ROLLBACK")
			}
			check("transaction finished", commit, false)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, err = Open(dir, Options{Sync: storage.SyncData})
			if err != nil {
				t.Fatal(err)
			}
			check("recovered", commit, false)
		})
	}
}
