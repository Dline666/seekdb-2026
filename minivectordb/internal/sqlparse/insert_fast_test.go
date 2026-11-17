package sqlparse

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TestInsertHeadAndRowsMatchParse checks that splitting an INSERT into a head
// parse plus a row-region parse reproduces Parse exactly: for statements
// Parse accepts, ParseInsertHead + RowParser must produce a deep-equal
// *Insert; for statements Parse rejects, one of the two must fail.
func TestInsertHeadAndRowsMatchParse(t *testing.T) {
	longHead := "INSERT INTO t VALUES ('" + strings.Repeat("x", 5000) + "')"
	cases := []string{
		"INSERT INTO t VALUES (1,'a'),(2,'b')",
		"INSERT INTO t (b, a) VALUES (1, 'x')",
		"INSERT INTO db.t VALUES (1,'a')",
		"INSERT INTO t VALUE (1,'a')",
		"INSERT INTO `my table` VALUES (1,'a'),(2,'b'),(3,'c')",
		"INSERT INTO t /* note */ VALUES (1,'a')",
		"INSERT INTO t VALUES /* note */ (1,'a'),(2,'b')",
		"INSERT INTO t VALUES (1,'a');",
		"INSERT INTO t VALUES (1,'a');;",
		"insert into t values (1,'a'),(2,'b')",
		"Insert Into t Values (1,'a'),(2,'b')",
		"INSERT INTO t VALUES ('a),(b'),(2,'c'),(3,'d')",
		"INSERT INTO t VALUES ('a\\'b'),('x''y'),('z\"w')",
		"INSERT INTO t VALUES (\"a\",\"b\"),(\"c\",\"d\")",
		"INSERT INTO t VALUES (1,'[0.1,0.2,0.3]'),(2,'[0.4, 0.5]')",
		"INSERT INTO t VALUES (1,'a') -- trailing\n,(2,'b')",
		"INSERT INTO t VALUES (1,'a'),(2,'b') -- done",
		"INSERT INTO t VALUES (1,'a'),(2,'b') # done\n",
		"INSERT INTO t VALUES (1, -5, 'x'),(2, +3, 'y')",
		"INSERT INTO t (a,b) VALUES (1,'x') /* tail */",
		"  \t\nINSERT  INTO  t  VALUES\n (1,'a') , (2,'b')\t",
		// Errors and unsupported forms.
		"INSERT INTO t VALUES (1,'a') ON DUPLICATE KEY UPDATE a=1",
		"INSERT INTO t VALUES (1,'a') ON",
		"INSERT IGNORE INTO t VALUES (1,'a')",
		"INSERT INTO t SELECT * FROM s",
		"INSERT INTO t SET a=1",
		"INSERT INTO VALUES (1,'a')",
		"INSERT INTO t",
		"INSERT INTO t VALUES",
		"INSERT INTO t VALUES (1,'a'",
		"INSERT INTO t VALUES (1,'a),(2,'b')",
		"INSERT INTO t VALUES (1+1,'a')",
		"INSERT INTO t VALUES (NULL,'a')",
		"INSERT INTO t (a,) VALUES (1,'a')",
		"INSERT INTO t () VALUES (1,'a')",
		"INSERT INTO t /* 'quoted' */ VALUES (1,'a')",
		longHead,
		"not an insert",
		"SELECT * FROM t",
	}
	var rp RowParser
	valid, fast := 0, 0
	for _, src := range cases {
		want, werr := Parse(src)
		ins, vend, ok := ParseInsertHead(src)
		var rows [][]Expr
		var rerr error
		if ok {
			if vend <= 0 || vend > len(src) {
				t.Fatalf("%q: valuesEnd %d out of range", src, vend)
			}
			rows, rerr = rp.ParseRows(src[vend:])
		}
		if werr != nil {
			if ok && rerr == nil {
				t.Fatalf("%q: Parse failed with %v but head+rows both succeeded", src, werr)
			}
			continue
		}
		valid++
		// A conservative miss is fine: ok=false or a ParseRows error just
		// means the statement falls back to the sequential path.
		if !ok || rerr != nil {
			continue
		}
		fast++
		ins.Rows = rows
		if !reflect.DeepEqual(ins, want) {
			t.Fatalf("%q: head+rows gave %+v, want %+v", src, ins, want)
		}
	}
	if fast < valid*2/3 {
		t.Fatalf("head+rows covered only %d of %d valid statements", fast, valid)
	}
}

// TestLexIntoMatchesLex feeds a dirty, reused buffer to LexInto and requires
// the same tokens and errors as Lex.
func TestLexIntoMatchesLex(t *testing.T) {
	inputs := []string{
		"SELECT * FROM t WHERE a = 1 AND b <= 2 AND c >= 3 AND d <> 4 AND e != 5",
		"SELECT a<=>b, a<->b FROM t WHERE a && b OR c || d",
		"INSERT INTO t VALUES (1,'a'),(2,'b');",
		"a ( ) , ; * = + - / % . [ ]",
		"& | ! ~ @ ? : < >",
		"a - -1",
		"a -- comment\nb",
		"a --comment\nb",
		"# hash comment\nSELECT 1",
		"/* block , ; comment */ SELECT 2",
		"a /* unterminated",
		"'str' \"dstr'' `qid` 'esc\\'q'",
		"0.5 .5 5. 1e3 1E-3 0x1p3 1_000 nan inf",
		"UPDATE t SET a='x,y(z' WHERE b='),('",
		"CREATE TABLE t (id BIGINT PRIMARY KEY, v VECTOR(128))",
		"INSERT INTO t VALUES ('[0.1,0.2]','it''s','a\"b'),(2,'\\'','x')",
		"",
		"   \t\n  ",
		"VALUES (1,2),(3,4)",
	}
	// A few hundred synthetic statements of varied shapes.
	for i := 0; i < 300; i++ {
		inputs = append(inputs,
			"INSERT INTO t"+strconv.Itoa(i)+" VALUES ("+strconv.Itoa(i)+",'v"+strconv.Itoa(i%7)+"'),("+strconv.Itoa(i+1)+",'x,y'); -- t"+strconv.Itoa(i))
	}
	var buf []Token
	for i := 11; i < len(inputs); i += 97 {
		// Prime the buffer with garbage capacity from a big lex.
		if toks, err := LexInto(strings.Repeat("(1,'x'),", 200)+"(1,'x')", buf); err == nil {
			buf = toks
		}
	}
	for _, src := range inputs {
		want, werr := Lex(src)
		got, gerr := LexInto(src, buf)
		if (werr == nil) != (gerr == nil) {
			t.Fatalf("%q: Lex err %v, LexInto err %v", src, werr, gerr)
		}
		if werr != nil {
			if werr.Error() != gerr.Error() {
				t.Fatalf("%q: errors differ: %v vs %v", src, werr, gerr)
			}
			continue
		}
		if len(want) != len(got) {
			t.Fatalf("%q: %d tokens vs %d", src, len(want), len(got))
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("%q: token %d: %+v vs %+v", src, i, want[i], got[i])
			}
		}
		buf = got // dirty reuse: the buffer may be longer than needed
	}
}
