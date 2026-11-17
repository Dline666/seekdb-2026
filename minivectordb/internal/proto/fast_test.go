package proto

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
	"unsafe"

	"minivectordb/internal/engine"
	"minivectordb/internal/storage"
)

// bads holds malformed byte sequences used by the differential tests:
// overlong encodings, a UTF-16 surrogate, out-of-range and invalid lead
// bytes, stray continuations and truncations.
var bads = [][]byte{
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
		`{"id":1,"op":"execute","sql":"a\tb"}`,
		`{"id":1,"op":"execute","sql":"a\\b"}`,
		`{"id":1,"op":"execute","sql":"a\/b"}`,
		`{"id":1,"op":"execute","sql":"a\r\bb\fc"}`,
		`{"id":1,"op":"execute","sql":"\u4f60\u597d"}`,
		`{"id":1,"op":"execute","sql":"\uD83D\uDE00"}`,
		`{"id":1,"op":"execute","sql":"\ud800"}`,
		`{"id":1,"op":"execute","sql":"a\\"}`,
		`{"id":1,"op":"execute","sql":"a\""}`,
		`{"id":1,"op":"exe\"cute","sql":"x"}`,
		`{"id":"a\nb","op":"x","sql":"y"}`,
		`{"id":1,"op":"execute","sql":"a\'b"}`,
		`{"id":1,"op":"execute","sql":"a\xb"}`,
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
		fr, _, ok := parseFast(bytes.TrimSpace([]byte(c)), nil)
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
		req, _, ok := parseFast(line, nil)
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
		if r, _, ok := parseFast(line, nil); !ok || r.SQL == nil {
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
			if got, _, ok := parseFast(line, nil); ok {
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
		got, _, ok := parseFast(line, nil)
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

// TestUnescapeMatchesEncodingJSON checks the escape-aware decode of the sql
// value: whatever the fast path accepts must equal encoding/json, and for
// bodies made only of pieces it provably handles it must not fall back.
func TestUnescapeMatchesEncodingJSON(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	runes := []rune{'中', '文', '事', '务', 0x80, 0x7FF, 0x800, 0xFFFF, 0x10000, 0x10FFFF}
	uEscapes := []string{
		`\u0000`, `\u001f`, `\u0022`, `\u005c`, `\u007f`, `\u0080`,
		`\u07ff`, `\u0800`, `\ud7ff`, `\ue000`, `\ufffd`, `\uffff`,
		`\uD7FF`, `\uE000`, `\uABCD`,
	}
	// piece appends JSON string-body bytes; ok=true means the fast path is
	// required to accept them (everything else may legitimately fall back).
	piece := func(out []byte) ([]byte, bool) {
		switch rng.Intn(20) {
		case 0, 1, 2, 3, 4: // printable ASCII run, no quote/backslash
			n := 1 + rng.Intn(12)
			for i := 0; i < n; i++ {
				out = append(out, byte(0x20+rng.Intn(0x5F)))
			}
			for i := len(out) - n; i < len(out); i++ {
				if out[i] == '"' || out[i] == '\\' {
					out[i] = 'x'
				}
			}
			return out, true
		case 5, 6: // valid multi-byte rune
			return utf8.AppendRune(out, runes[rng.Intn(len(runes))]), true
		case 7, 8: // simple escape
			return append(out, []byte{'\\', "bfnrt\"\\/"[rng.Intn(8)]}...), true
		case 9, 10: // non-surrogate \u escape, sometimes upper hex
			return append(out, uEscapes[rng.Intn(len(uEscapes))]...), true
		case 11: // surrogate \u, lone or paired
			switch rng.Intn(4) {
			case 0:
				return append(out, `\ud800`...), false
			case 1:
				return append(out, `\udc00`...), false
			case 2:
				return append(out, `\ud83d\ude00`...), false
			default:
				return append(out, `\ude00\ud83d`...), false
			}
		case 12: // invalid escape
			return append(out, []byte{'\\', `'xU ?`[rng.Intn(5)]}...), false
		case 13: // short or bad \u
			switch rng.Intn(3) {
			case 0:
				return append(out, `\u12`...), false
			case 1:
				return append(out, `\u12G4`...), false
			default:
				return append(out, '\\'), false
			}
		case 14: // raw control byte
			return append(out, byte(rng.Intn(0x20))), false
		case 15: // raw invalid UTF-8
			return append(out, bads[rng.Intn(len(bads))]...), false
		case 16: // escape/quote chains near the terminator
			switch rng.Intn(3) {
			case 0:
				// \\ is a complete escape, so the " after it terminates the
				// JSON string early and the appended closer makes the line
				// invalid: consistent rejection, not guaranteed acceptance.
				return append(out, `\\"`...), false
			case 1:
				return append(out, `\\\"`...), true
			default:
				return append(out, `\\`...), true
			}
		default: // arbitrary non-quote, non-backslash byte
			c := byte(rng.Intn(256))
			for c == '"' || c == '\\' {
				c = byte(rng.Intn(256))
			}
			return append(out, c), false // may be a control byte or bad UTF-8
		}
	}
	var line []byte
	check := func(body []byte, mustAccept bool, iter int) {
		for _, form := range []int{0, 1, 2} {
			line = line[:0]
			switch form {
			case 0:
				line = append(line, `{"id":1,"op":"execute","sql":"`...)
				line = append(line, body...)
				line = append(line, `","search_mode":"exact"}`...)
			case 1:
				line = append(line, `{"op":"execute","id":7,"search_mode":"ann","sql":"`...)
				line = append(line, body...)
				line = append(line, `"}`...)
			default:
				line = append(line, `{ "id" : -3 , "sql" : "`...)
				line = append(line, body...)
				line = append(line, `" , "op" : "execute" }`...)
			}
			fr, sc, ok := parseFast(line, nil)
			_ = sc
			sr, err := slowParse(line)
			if ok {
				if err != nil {
					t.Fatalf("iter %d form %d: fast accepted, json rejected: %v\n%q", iter, form, err, line)
				}
				if !same(fr, sr) {
					t.Fatalf("iter %d form %d: fast %+v != slow %+v\n%q", iter, form, fr, sr, line)
				}
			} else if mustAccept {
				t.Fatalf("iter %d form %d: fast path fell back on a body it must decode\n%q", iter, form, line)
			}
		}
	}
	for i := 0; i < 1000000; i++ {
		var body []byte
		must := true
		n := rng.Intn(24)
		for j := 0; j < n; j++ {
			var ok bool
			body, ok = piece(body)
			must = must && ok
		}
		check(body, must, i)
	}

	// Scratch reuse: one parser, a long escaped string, a short one, then an
	// escape-free one that must alias the line.
	rp := new(RequestParser)
	long := bytes.Repeat([]byte("ab\\ncd"), 2000)
	short := []byte("x\\ty")
	for _, body := range [][]byte{long, short} {
		l := append(append([]byte(`{"id":1,"op":"execute","sql":"`), body...), []byte(`"}`)...)
		req, err := rp.Parse(l)
		if err != nil || req.SQL == nil {
			t.Fatalf("reused parser rejected %q", body[:16])
		}
		sr, err := slowParse(l)
		if err != nil || *req.SQL != *sr.SQL {
			t.Fatalf("reused parser decoded %q != %q", *req.SQL, *sr.SQL)
		}
	}
	l := []byte(`{"id":2,"op":"execute","sql":"plain text"}`)
	req, err := rp.Parse(l)
	if err != nil || req.SQL == nil || *req.SQL != "plain text" {
		t.Fatalf("escape-free sql through reused parser: %v %v", req.SQL, err)
	}
	if p := unsafe.StringData(*req.SQL); p != &l[strings.Index(string(l), "plain")] {
		t.Fatal("escape-free sql should alias the request line")
	}
}

// TestRequestParserScratchLifetime checks the documented aliasing rule for
// the scratch buffer: each Parse may reuse it, so a previously returned SQL
// is only guaranteed until the next Parse.
func TestRequestParserScratchLifetime(t *testing.T) {
	rp := new(RequestParser)
	parse := func(sql string) *Request {
		t.Helper()
		line, err := json.Marshal(struct {
			ID  int    `json:"id"`
			Op  string `json:"op"`
			SQL string `json:"sql"`
		}{1, "execute", sql})
		if err != nil {
			t.Fatal(err)
		}
		r, err := rp.Parse(line)
		if err != nil || r.SQL == nil {
			t.Fatalf("Parse(%q): %v", sql, err)
		}
		return r
	}
	// With escapes in the SQL, the decoded string lives in the scratch.
	a := parse("SELECT 'a\nb'\t\"q\"")
	aSQL := *a.SQL
	if aSQL != "SELECT 'a\nb'\t\"q\"" {
		t.Fatalf("decoded SQL %q", aSQL)
	}
	if len(rp.scratch) == 0 {
		t.Fatal("escaped SQL did not use the scratch")
	}
	if got := *parse("SELECT 'short\t'").SQL; got != "SELECT 'short\t'" {
		t.Fatalf("shorter reused scratch decoded %q", got)
	}
	long := "SELECT '" + strings.Repeat("x\n", 5000) + "'"
	if got := *parse(long).SQL; got != long {
		t.Fatal("longer reused scratch decoded wrong")
	}
	// The rule documents that aSQL need not survive these parses, so there
	// is nothing to assert about it here.
	_ = aSQL

	// One giant escaped request must not pin a scratch above 8 MiB.
	huge := strings.Repeat("x\\n", 5<<20) // ~10 MiB of escapes -> ~10 MiB out
	big := make([]byte, 0, 15<<20)
	big = append(big, `{"id":1,"op":"execute","sql":"`...)
	big = append(big, huge...)
	big = append(big, `"}`...)
	if _, err := rp.Parse(big); err != nil {
		t.Fatal(err)
	}
	if _, err := rp.Parse([]byte(`{"id":1,"op":"execute","sql":"ok"}`)); err != nil {
		t.Fatal(err)
	}
	if cap(rp.scratch) > 8<<20 {
		t.Fatalf("scratch retained %d bytes after a huge request", cap(rp.scratch))
	}
}

// TestFastParsePersistentStringsEscaped mirrors the lifetime test above but
// through a reused RequestParser with escaped SQL: engine-persistent strings
// must not alias the parser's scratch either.
func TestFastParsePersistentStringsEscaped(t *testing.T) {
	db, err := engine.Open(t.TempDir(), engine.Options{Sync: storage.SyncNone})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rp := new(RequestParser)

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
		req, err := rp.Parse(line)
		if err != nil || req.SQL == nil {
			t.Fatalf("parse rejected %q", sql)
		}
		if _, err := db.Exec(*req.SQL); err != nil {
			t.Fatal(err)
		}
		// Corrupt both possible alias targets: the request line and the
		// scratch buffer the decoded SQL may live in.
		for i := range line {
			line[i] = 'x'
		}
		for i := range rp.scratch {
			rp.scratch[i] = 'x'
		}
	}
	// The newlines force escape decoding on the fast path.
	execute("CREATE TABLE docs (\n id BIGINT PRIMARY KEY,\n category VARCHAR(8),\n embedding VECTOR(2))")
	execute("CREATE VECTOR INDEX vi ON docs(embedding)")
	execute("SET search_mode = 'ann'")

	execute("INSERT INTO docs VALUES (1, 'keep', '[1,\n2]')")
	execute("SELECT id FROM docs WHERE category = 'keep' ORDER BY id LIMIT 1")
	if got := db.SearchMode(); got != "ann" {
		t.Fatalf("saved search mode = %q, want ann", got)
	}
}

// BenchmarkParseLargeRequestEscaped is the 250 KB INSERT shape with rows
// separated by ",\n": the escape decode either runs on the fast path or the
// whole request goes through encoding/json.
func BenchmarkParseLargeRequestEscaped(b *testing.B) {
	var sql strings.Builder
	sql.WriteString("INSERT INTO docs VALUES ")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&sql, "(%d,'%s'),\n", i, strings.Repeat("1,", 1200))
	}
	line, err := json.Marshal(map[string]any{"id": 1, "op": "execute", "sql": sql.String(), "search_mode": "exact"})
	if err != nil {
		b.Fatal(err)
	}
	if len(line) < 200<<10 {
		b.Fatalf("request only %d bytes", len(line))
	}
	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	rp := new(RequestParser)
	for i := 0; i < b.N; i++ {
		r, err := rp.Parse(line)
		if err != nil || r.SQL == nil {
			b.Fatal("parse rejected benchmark request")
		}
	}
}

// BenchmarkParseQueryEscaped is a ~2.4 KB multi-line SELECT request.
func BenchmarkParseQueryEscaped(b *testing.B) {
	sql := "SELECT id, category,\n       embedding\nFROM docs\nWHERE category = 'q'\n" +
		strings.Repeat("  AND id < 1000\n", 100) + "ORDER BY id\nLIMIT 10"
	line, err := json.Marshal(map[string]any{"id": 9, "op": "execute", "sql": sql})
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(line)))
	b.ReportAllocs()
	rp := new(RequestParser)
	for i := 0; i < b.N; i++ {
		r, err := rp.Parse(line)
		if err != nil || r.SQL == nil {
			b.Fatal("parse rejected benchmark request")
		}
	}
}
