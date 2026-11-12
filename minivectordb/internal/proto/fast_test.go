package proto

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"

	"minivectordb/internal/engine"
	"minivectordb/internal/storage"
)

// slowParse is the reference: the encoding/json path of ParseRequest.
func slowParse(line []byte) (*Request, error) {
	line = bytes.TrimSpace(line)
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		return nil, err
	}
	if len(r.ID) == 0 || bytes.Equal(r.ID, []byte("null")) {
		return nil, errMissingID
	}
	return &r, nil
}

func same(a, b *Request) bool {
	if !bytes.Equal(a.ID, b.ID) || a.Op != b.Op || a.SearchMode != b.SearchMode {
		return false
	}
	if (a.SQL == nil) != (b.SQL == nil) {
		return false
	}
	return a.SQL == nil || *a.SQL == *b.SQL
}

// TestFastParseMatchesEncodingJSON checks that whenever the fast path accepts
// a line, encoding/json accepts it with the same result.
func TestFastParseMatchesEncodingJSON(t *testing.T) {
	cases := []string{
		`{"id":1,"op":"execute","sql":"SELECT 1;","search_mode":"exact"}`,
		` { "id" : "abc" , "op" : "shutdown" } `,
		`{"op":"execute","id":-0,"sql":"INSERT INTO t VALUES (1,'a;b','[1,2]')"}`,
		`{"id":12345678901234567890,"op":"execute","sql":"你好"}`,
		`{"id":1,"op":"execute","sql":"a\"b"}`,
		`{"id":1,"op":"execute","sql":"a\nb"}`,
		`{"id":01,"op":"x"}`,
		`{"id":1.5,"op":"x"}`,
		`{"id":null,"op":"x"}`,
		`{"id":1,"id":2,"op":"x"}`,
		`{"id":1,"op":"x","extra":true}`,
		`{"id":1,"op":"x",}`,
		`{"id":1,"op":"x"} trailing`,
		`{}`,
		`{"op":"x"}`,
		"{\"id\":1,\"op\":\"\xff\"}",
		`{"id":1,"op":"execute","sql":"x","search_mode":"ann"}`,
	}
	rng := rand.New(rand.NewSource(1))
	alphabet := []byte(`{}":,\ abc01-9.enul` + "\t\xff")
	for i := 0; i < 20000; i++ {
		b := []byte(cases[rng.Intn(len(cases))])
		for m := rng.Intn(3); m > 0; m-- {
			p := rng.Intn(len(b) + 1)
			switch rng.Intn(3) {
			case 0:
				b = append(b[:p], append([]byte{alphabet[rng.Intn(len(alphabet))]}, b[p:]...)...)
			case 1:
				if p < len(b) {
					b = append(b[:p], b[p+1:]...)
				}
			default:
				if p < len(b) {
					b[p] = alphabet[rng.Intn(len(alphabet))]
				}
			}
		}
		cases = append(cases, string(b))
	}
	accepted := 0
	for _, c := range cases {
		fr, ok := parseFast(bytes.TrimSpace([]byte(c)))
		if !ok {
			continue
		}
		accepted++
		sr, err := slowParse([]byte(c))
		if err != nil {
			t.Fatalf("%q: fast accepted, encoding/json rejected: %v", c, err)
		}
		if !same(fr, sr) {
			t.Fatalf("%q: fast %+v != slow %+v", c, fr, sr)
		}
	}
	if accepted < 100 {
		t.Fatalf("fast path accepted only %d cases", accepted)
	}
}

// TestFastParsePersistentStringsOwnStorage guards the lifetime contract:
// SQL may alias the request line for the duration of one Exec, but catalog
// names and saved settings copied from it must survive the next read.
func TestFastParsePersistentStringsOwnStorage(t *testing.T) {
	db, err := engine.Open(t.TempDir(), engine.Options{Sync: storage.SyncNone})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	execute := func(sql string) {
		t.Helper()
		line, err := json.Marshal(struct {
			ID   int    `json:"id"`
			Op   string `json:"op"`
			SQL  string `json:"sql"`
			Mode string `json:"search_mode"`
		}{1, "execute", sql, "exact"})
		if err != nil {
			t.Fatal(err)
		}
		req, ok := parseFast(line)
		if !ok || req.SQL == nil {
			t.Fatalf("fast parse rejected %q", sql)
		}
		if _, err := db.Exec(*req.SQL); err != nil {
			t.Fatal(err)
		}
		for i := range line {
			line[i] = 'x'
		}
	}
	execute("CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(8), embedding VECTOR(2))")
	execute("CREATE VECTOR INDEX vi ON docs(embedding)")
	execute("SET search_mode = 'ann'")

	execute("INSERT INTO docs VALUES (1, 'keep', '[1,2]')")
	execute("SELECT id FROM docs WHERE category = 'keep' ORDER BY id LIMIT 1")
	if got := db.SearchMode(); got != "ann" {
		t.Fatalf("saved search mode = %q, want ann", got)
	}
}

// BenchmarkParseLargeRequest covers the request shape used by the 100-row
// INSERT workload: almost all of the line is one ASCII string.
func BenchmarkParseLargeRequest(b *testing.B) {
	sql := "INSERT INTO docs VALUES ('" + string(bytes.Repeat([]byte("1,"), 125<<10)) + "')"
	line := []byte(`{"id":1,"op":"execute","sql":"` + sql + `","search_mode":"exact"}`)
	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if r, ok := parseFast(line); !ok || r.SQL == nil {
			b.Fatal("fast parse rejected benchmark request")
		}
	}
}
