package proto

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"
	"unicode/utf8"

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

// Exercise every byte at both word boundaries and interior positions. This
// catches a skipped control byte, escape or malformed UTF-8 in a long SQL
// field while comparing accepted requests with the standard JSON decoder.
func TestFastParseLongStringBytes(t *testing.T) {
	for pos := 0; pos < 32; pos++ {
		for c := 0; c < 256; c++ {
			sql := bytes.Repeat([]byte("a"), 80)
			sql[24+pos] = byte(c)
			line := append([]byte(`{"id":1,"op":"execute","sql":"`), sql...)
			line = append(line, []byte(`","search_mode":"exact"}`)...)
			if got, ok := parseFast(line); ok {
				want, err := slowParse(line)
				if err != nil || !same(got, want) {
					t.Fatalf("byte %x at %d: accepted incompatible JSON", c, pos)
				}
			}
		}
	}
	for pad := 0; pad < 16; pad++ {
		sql := string(bytes.Repeat([]byte("a"), pad)) + "中文🙂" + string(bytes.Repeat([]byte("z"), 80))
		line, _ := json.Marshal(map[string]any{"id": 1, "op": "execute", "sql": sql})
		got, ok := parseFast(line)
		want, err := slowParse(line)
		if !ok || err != nil || !same(got, want) {
			t.Fatalf("valid UTF-8 rejected at padding %d", pad)
		}
	}
}

// TestValidStringBytesMatchesReference checks the string-content predicate
// (no byte below 0x20, valid UTF-8) byte for byte against a plain reference
// on random mixes of ASCII, control bytes, valid multi-byte runes and every
// class of malformed sequence.
func TestValidStringBytesMatchesReference(t *testing.T) {
	ref := func(b []byte) bool {
		for _, c := range b {
			if c < 0x20 {
				return false
			}
		}
		return utf8.Valid(b)
	}
	check := func(b []byte, ctx string) {
		t.Helper()
		if got, want := validStringBytes(b), ref(b); got != want {
			t.Fatalf("%s: validStringBytes(%x) = %v, want %v", ctx, b, got, want)
		}
	}

	rng := rand.New(rand.NewSource(1))
	runes := []rune{0x80, 0x7FF, 0x800, 0xFFFF, 0x10000, 0x10FFFF, '中', '文', '事', '务'}
	bads := [][]byte{
		{0xC0, 0x80},             // overlong 2-byte
		{0xE0, 0x80, 0x80},       // overlong 3-byte
		{0xED, 0xA0, 0x80},       // UTF-16 surrogate
		{0xF4, 0x90, 0x80, 0x80}, // above U+10FFFF
		{0xF5, 0x80, 0x80, 0x80}, // invalid lead
		{0xFF}, {0xFE},           // never valid
		{0x80},             // stray continuation
		{0xC2},             // truncated 2-byte
		{0xE4, 0xB8},       // truncated 3-byte
		{0xF0, 0x9F, 0x92}, // truncated 4-byte
		{0xC2, 0x41},       // bad continuation
	}
	piece := func(out []byte) []byte {
		switch rng.Intn(10) {
		case 0, 1, 2, 3: // printable ASCII run
			n := 1 + rng.Intn(16)
			for i := 0; i < n; i++ {
				out = append(out, byte(0x20+rng.Intn(0x5F)))
			}
		case 4:
			out = append(out, 0x7F)
		case 5: // control byte
			out = append(out, byte(rng.Intn(0x20)))
		case 6, 7: // valid multi-byte rune
			out = utf8.AppendRune(out, runes[rng.Intn(len(runes))])
		case 8: // malformed sequence
			out = append(out, bads[rng.Intn(len(bads))]...)
		default: // arbitrary byte
			out = append(out, byte(rng.Intn(256)))
		}
		return out
	}

	for i := 0; i < 1000000; i++ {
		var s []byte
		n := rng.Intn(65)
		for len(s) < n {
			s = piece(s)
		}
		check(s[:n], "random")
	}

	// Every split position of a few long strings, so multi-byte sequences and
	// word boundaries straddle the 8-byte blocks in both directions.
	for _, s := range []string{
		"事务与恢复" + string(bytes.Repeat([]byte("a"), 40)) + "中文🙂" + string(bytes.Repeat([]byte("b"), 40)),
		string(bytes.Repeat([]byte("0123456789abcdef"), 8)),
		"plain" + "\xe4\xb8\xad\xff" + "tail", // invalid byte mid-string
	} {
		b := []byte(s)
		for k := 0; k <= len(b); k++ {
			check(b[:k], "prefix")
			check(b[k:], "suffix")
		}
	}

	// A bad byte at each early offset, inside and across word boundaries.
	for off := 0; off < 24; off++ {
		for _, bad := range [][]byte{{0x00}, {0x1F}, {0xFF}, {0xC0}, {0xE4, 0xB8}} {
			b := bytes.Repeat([]byte("a"), off)
			b = append(b, bad...)
			b = append(b, bytes.Repeat([]byte("z"), 32)...)
			check(b, "bad byte at offset")
		}
	}
}

// BenchmarkValidStringBytes covers the three SQL string shapes that matter:
// pure ASCII, the workload shape (a CJK rune every ~2.5 KB), and all CJK.
func BenchmarkValidStringBytes(b *testing.B) {
	ascii := bytes.Repeat([]byte("a"), 250<<10)
	mixed := bytes.Repeat([]byte("a"), 250<<10)
	for i := 2560; i+3 <= len(mixed); i += 2560 {
		copy(mixed[i:], "事")
	}
	var cjk []byte
	for len(cjk) < 250<<10 {
		cjk = utf8.AppendRune(cjk, '事')
	}
	cjk = cjk[:len(cjk)-len(cjk)%3] // keep runes whole
	for _, c := range []struct {
		name string
		in   []byte
	}{
		{"ascii", ascii},
		{"mixed", mixed},
		{"cjk", cjk},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.SetBytes(int64(len(c.in)))
			for i := 0; i < b.N; i++ {
				if !validStringBytes(c.in) {
					b.Fatal("rejected valid input")
				}
			}
		})
	}
}
