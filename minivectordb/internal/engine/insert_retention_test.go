package engine

import "testing"

func TestFastInsertReleasesRowReferences(t *testing.T) {
	fastKnobs(t, true, 0, 2)
	dir := t.TempDir()
	db := open(t, dir)
	defer func() { db.Close() }()
	mustExec(t, db, "CREATE TABLE t (id BIGINT PRIMARY KEY, payload VARCHAR(100), v VECTOR(1))")
	for _, tc := range []struct {
		sql     string
		wantErr bool
	}{
		{"INSERT INTO t VALUES (1,'first','[0.25]'),(2,'second','[0.5]')", false},
		{"BEGIN", false},
		{"INSERT INTO t VALUES (3,'pending','[0.75]')", false},
		{"INSERT INTO t VALUES (4,'rejected','[1]'),(2,'duplicate','[1]')", true},
		{"INSERT INTO t VALUES (4,'bad vector','[1,2]')", true},
		{"COMMIT", false},
	} {
		_, err := db.Exec(tc.sql)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: error=%v, want error=%v", tc.sql, err, tc.wantErr)
		}
		for worker := range db.fi.workers {
			for column, value := range db.fi.workers[worker].row {
				if value.S != "" {
					t.Fatalf("worker %d column %d retains %q after %s", worker, column, value.S, tc.sql)
				}
			}
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = open(t, dir)
	rows := mustExec(t, db, "SELECT id,payload FROM t ORDER BY id").Rows
	if len(rows) != 3 || rows[0][0].I != 1 || rows[0][1].S != "first" ||
		rows[1][0].I != 2 || rows[1][1].S != "second" ||
		rows[2][0].I != 3 || rows[2][1].S != "pending" {
		t.Fatalf("recovery after scratch release: %+v", rows)
	}
}
