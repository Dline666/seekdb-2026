package engine

import (
	"testing"

	"minivectordb/internal/storage"
)

func forceStreamingUpdate(t *testing.T) {
	t.Helper()
	old := updateStreamBytes
	updateStreamBytes = 1
	t.Cleanup(func() { updateStreamBytes = old })
}

// The last row in log order cannot be coerced. A failed statement must keep
// earlier writes in the explicit transaction while applying none of its own.
func TestStreamingUpdateLateErrorPreservesTransaction(t *testing.T) {
	forceStreamingUpdate(t)
	dir := t.TempDir()
	db := open(t, dir)
	mustExec(t, db, "CREATE TABLE docs (id BIGINT PRIMARY KEY, a VARCHAR(4), b VARCHAR(8), e VECTOR(2))")
	mustExec(t, db, "INSERT INTO docs VALUES (1,'a','one','[1,0]'),(2,'b','two','[2,0]')")
	mustExec(t, db, "BEGIN")
	mustExec(t, db, "UPDATE docs SET a='tx' WHERE id=1")
	mustExec(t, db, "INSERT INTO docs VALUES (3,'c','toolong','[3,0]')")
	want := snapshot(t, db)
	size := db.log.Size()
	if _, err := db.Exec("UPDATE docs SET a=b"); errName(err) != "data_too_long" {
		t.Fatalf("unexpected update error: %v", err)
	}
	if db.log.Size() != size || snapshot(t, db) != want {
		t.Fatal("failed statement changed its transaction or wrote to the log")
	}
	mustExec(t, db, "COMMIT")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	defer db.Close()
	if got := snapshot(t, db); got != want {
		t.Fatalf("earlier transaction changes lost: %s want %s", got, want)
	}
}

func TestStreamingUpdateDurabilityAndOldValues(t *testing.T) {
	forceStreamingUpdate(t)
	dir := t.TempDir()
	db, err := Open(dir, Options{Sync: storage.SyncData})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, "CREATE TABLE docs (id BIGINT PRIMARY KEY, a BIGINT, b BIGINT, e VECTOR(2))")
	mustExec(t, db, "INSERT INTO docs VALUES (1,10,20,'[1,0]'),(2,30,40,'[2,0]')")
	before := db.log.Stats().SyncCalls
	r := mustExec(t, db, "UPDATE docs SET a=b,b=a,e='[5,6]'")
	if r.Affected != 2 || db.log.Stats().SyncCalls != before+1 {
		t.Fatal("autocommit must sync once and report every matched row")
	}
	r = mustExec(t, db, "SELECT a,b FROM docs ORDER BY id")
	if r.Rows[0][0].I != 20 || r.Rows[0][1].I != 10 || r.Rows[1][0].I != 40 || r.Rows[1][1].I != 30 {
		t.Fatal("SET assignments did not use the pre-statement rows")
	}
	size := db.log.Size()
	r = mustExec(t, db, "UPDATE docs SET a=a,b=b,e=e")
	if r.Affected != 2 || db.log.Size() != size || db.log.Stats().SyncCalls != before+1 {
		t.Fatal("no-op update wrote records or lost its matched row count")
	}
	want := snapshot(t, db)
	mustExec(t, db, "BEGIN")
	mustExec(t, db, "UPDATE docs SET a=99")
	mustExec(t, db, "UPDATE docs SET b=a")
	r = mustExec(t, db, "SELECT b FROM docs ORDER BY id")
	if r.Rows[0][0].I != 99 || r.Rows[1][0].I != 99 {
		t.Fatal("streamed update did not see earlier transaction writes")
	}
	mustExec(t, db, "ROLLBACK")
	if snapshot(t, db) != want {
		t.Fatal("rollback did not restore rows")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	defer db.Close()
	if got := snapshot(t, db); got != want {
		t.Fatalf("restart changed durable rows: %s want %s", got, want)
	}
}
