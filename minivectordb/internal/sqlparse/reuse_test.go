package sqlparse

import (
	"reflect"
	"strings"
	"testing"
)

func TestParserScratchLifetime(t *testing.T) {
	var p Parser
	texts := []string{"SELECT id, L2_DISTANCE(v, '[1,2,3]') AS d FROM docs WHERE tag='alpha' ORDER BY d LIMIT 10", "SELECT 'a\\nβ', 9223372036854775807 FROM docs", "CREATE TABLE docs(id BIGINT PRIMARY KEY,tag VARCHAR(20),v VECTOR(3))", "INSERT INTO docs VALUES (1,'long source','[1,2,3]')"}
	for _, sql := range texts {
		want, err := Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Parse(sql)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = p.Parse("SELECT 'unclosed")
		_, _ = p.Parse("SELECT x FROM docs")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("statement changed after reuse: %s", sql)
		}
		for _, tok := range p.tokens[:cap(p.tokens)] {
			if tok != (Token{}) {
				t.Fatal("retained token text")
			}
		}
	}
	huge := "SELECT " + strings.Repeat("id,", 2000) + "id FROM docs"
	if _, err := p.Parse(huge); err != nil {
		t.Fatal(err)
	}
	if cap(p.tokens) != 0 {
		t.Fatal("unbounded retained token capacity")
	}
	if _, err := p.Parse("SELECT id FROM docs"); err != nil {
		t.Fatal(err)
	}
	_, _ = p.Parse("SELECT " + strings.Repeat("id,", 1000) + "'unterminated")
	for _, tok := range p.tokens[:cap(p.tokens)] {
		if tok != (Token{}) {
			t.Fatal("lex error retained source")
		}
	}
}
