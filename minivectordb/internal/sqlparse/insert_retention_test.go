package sqlparse

import (
	"strings"
	"testing"
)

func TestRowParserReleasesSources(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		wantError    bool
	}{
		{"shorter batch", "(3,'third')", false},
		{"parse error", "(3,'third') unexpected", true},
		{"lex error", "(3,'third'),(4,'unterminated", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rp RowParser
			payload := strings.Repeat("retained-source-", 4096)
			first, err := rp.ParseRows("(1,'" + payload + "'),(2,'second')")
			if err != nil {
				t.Fatal(err)
			}
			_, err = rp.ParseRows(tc.source)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, want error=%v", err, tc.wantError)
			}
			for i, tok := range rp.toks[:cap(rp.toks)] {
				if tok.Text != "" {
					t.Fatalf("completed parser retains source at token %d (%d bytes)", i, len(tok.Text))
				}
			}
			// Returned ASTs own their values even after the scratch is released
			// and reused by a later statement.
			if first[0][1].(*StrLit).V != payload || first[1][1].(*StrLit).V != "second" {
				t.Fatal("scratch reuse changed a previously returned AST")
			}
		})
	}
}
