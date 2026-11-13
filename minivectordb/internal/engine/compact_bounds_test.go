package engine

import (
	"fmt"
	"strings"
	"testing"

	"minivectordb/internal/storage"
)

// A clean startup must not fault the row mapping merely to decide that a
// compaction is unnecessary. Recovery already validates every log record.
func TestCompactionCleanLogDoesNotMapRows(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	mustExec(t, db, "CREATE TABLE docs (id BIGINT PRIMARY KEY, a VARCHAR(8), b VARCHAR(8), e VECTOR(16))")
	var sql strings.Builder
	sql.WriteString("INSERT INTO docs VALUES ")
	for i := 0; i < 64; i++ {
		if i > 0 {
			sql.WriteByte(',')
		}
		fmt.Fprintf(&sql, "(%d,'','汉字','[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15]')", i)
	}
	mustExec(t, db, sql.String())
	want := snapshot(t, db)
	size := db.log.Size()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	defer db.Close()
	if db.log.Map() != nil {
		t.Fatal("clean startup read row pages to check compaction")
	}
	if db.log.Size() != size || snapshot(t, db) != want {
		t.Fatal("clean startup changed persisted rows")
	}
}

// Fixed-width rows let the live size be known independently. Exercise both
// sides and equality of both thresholds so a conservative shortcut cannot
// accidentally defer a required rewrite, including at the exact boundary.
func TestCompactionThresholdBoundaries(t *testing.T) {
	old := compactMinGarbage
	defer func() { compactMinGarbage = old }()
	const live = int64(64 * (storage.HdrSize + 4 + 8))
	for _, tc := range []struct {
		name      string
		garbage   int64
		threshold int64
		compact   bool
	}{
		{"below_live", live - 1, 1, false},
		{"equal_live", live, 1, true},
		{"below_minimum", live, live + 1, false},
		{"equal_minimum", live, live, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			compactMinGarbage = tc.threshold
			dir := t.TempDir()
			db := open(t, dir)
			mustExec(t, db, "CREATE TABLE docs (id BIGINT PRIMARY KEY)")
			var sql strings.Builder
			sql.WriteString("INSERT INTO docs VALUES ")
			for i := 0; i < 64; i++ {
				if i > 0 {
					sql.WriteByte(',')
				}
				fmt.Fprintf(&sql, "(%d)", i)
			}
			mustExec(t, db, sql.String())
			want := snapshot(t, db)
			// ABORT payload bytes are ignored by recovery but are dead log
			// space. Pad the valid log to the exact threshold under test.
			padding := live + tc.garbage - db.log.Size() - storage.HdrSize
			if padding < 0 {
				t.Fatal("fixture exceeds target log size")
			}
			if _, err := db.log.Append(storage.RecAbort, 999, make([]byte, padding)); err != nil {
				t.Fatal(err)
			}
			size := db.log.Size()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = open(t, dir)
			defer db.Close()
			if got := db.log.Size() < size; got != tc.compact {
				t.Fatalf("compacted=%v want %v; before=%d after=%d", got, tc.compact, size, db.log.Size())
			}
			if got := snapshot(t, db); got != want {
				t.Fatalf("rows changed: %s want %s", got, want)
			}
		})
	}
}
