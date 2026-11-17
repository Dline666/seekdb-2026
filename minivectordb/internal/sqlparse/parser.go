package sqlparse

import "strings"

type parser struct {
	src  string
	toks []Token
	p    int
}

// Parse parses one SQL statement (without the trailing semicolon handling of the caller).
func Parse(src string) (Stmt, error) {
	var p Parser
	return p.Parse(src)
}

// Parser owns bounded token scratch. Returned statements own their token text
// values and remain valid after the next Parse; no AST node points at a token.
// Like the database request loop, one Parser is used by one caller at a time.
type Parser struct{ tokens []Token }

func (p *Parser) Parse(src string) (Stmt, error) {
	old := p.tokens
	toks, err := LexInto(src, old)
	if err != nil {
		// LexInto may return nil after filling part of the reused array.
		// Clear the entire retained capacity even on this error path.
		clear(old[:cap(old)])
		p.tokens = old[:0]
		return nil, err
	}
	defer func() {
		clear(toks)
		if cap(toks) <= 256 {
			p.tokens = toks[:0]
		} else {
			p.tokens = nil
		}
	}()
	ps := &parser{src: src, toks: toks}
	st, err := ps.statement()
	if err != nil {
		return nil, err
	}
	for ps.isOp(";") {
		ps.p++
	}
	if ps.cur().Kind != EOF {
		return nil, ps.errNear()
	}
	return st, nil
}

func (ps *parser) cur() Token  { return ps.toks[ps.p] }
func (ps *parser) next() Token { t := ps.toks[ps.p]; ps.p++; return t }
func (ps *parser) peek(k int) Token {
	if ps.p+k < len(ps.toks) {
		return ps.toks[ps.p+k]
	}
	return ps.toks[len(ps.toks)-1]
}

func (ps *parser) isKw(kw string) bool {
	t := ps.cur()
	return t.Kind == Ident && strings.EqualFold(t.Text, kw)
}
func (ps *parser) isOp(op string) bool { t := ps.cur(); return t.Kind == Op && t.Text == op }

func (ps *parser) acceptKw(kw string) bool {
	if ps.isKw(kw) {
		ps.p++
		return true
	}
	return false
}

func (ps *parser) acceptOp(op string) bool {
	if ps.isOp(op) {
		ps.p++
		return true
	}
	return false
}

func (ps *parser) errNear() *Error {
	t := ps.cur()
	if t.Kind == EOF {
		return syntaxErr("syntax error at end of input")
	}
	return syntaxErr("syntax error near '%s'", snippet(ps.src, t.Pos))
}

func (ps *parser) expectKw(kw string) error {
	if !ps.acceptKw(kw) {
		return ps.errNear()
	}
	return nil
}

func (ps *parser) expectOp(op string) error {
	if !ps.acceptOp(op) {
		return ps.errNear()
	}
	return nil
}

var reserved = map[string]bool{
	"select": true, "from": true, "where": true, "order": true, "by": true, "limit": true,
	"and": true, "or": true, "not": true, "insert": true, "into": true, "values": true,
	"update": true, "set": true, "delete": true, "create": true, "table": true, "as": true,
	"asc": true, "desc": true, "primary": true, "key": true, "on": true,
	"index": true, "group": true, "having": true, "join": true, "union": true, "is": true,
	"in": true, "like": true, "between": true, "null": true, "drop": true,
}

func (ps *parser) ident() (string, error) {
	t := ps.cur()
	if t.Kind == QIdent || (t.Kind == Ident && !reserved[strings.ToLower(t.Text)]) {
		ps.p++
		return t.Text, nil
	}
	return "", ps.errNear()
}

// tableName accepts an optional "db." qualifier and returns the bare table name.
func (ps *parser) tableName() (string, error) {
	name, err := ps.ident()
	if err != nil {
		return "", err
	}
	if ps.acceptOp(".") {
		return ps.ident()
	}
	return name, nil
}

func (ps *parser) statement() (Stmt, error) {
	t := ps.cur()
	if t.Kind != Ident {
		if t.Kind == EOF {
			return nil, syntaxErr("empty statement")
		}
		return nil, ps.errNear()
	}
	switch strings.ToLower(t.Text) {
	case "create":
		return ps.create()
	case "drop":
		return ps.drop()
	case "insert":
		return ps.insert()
	case "select":
		return ps.selectStmt()
	case "update":
		return ps.update()
	case "delete":
		return ps.delete()
	case "begin":
		ps.p++
		ps.acceptKw("work")
		return &Begin{}, nil
	case "start":
		ps.p++
		if err := ps.expectKw("transaction"); err != nil {
			return nil, err
		}
		return &Begin{}, nil
	case "commit":
		ps.p++
		ps.acceptKw("work")
		return &Commit{}, nil
	case "rollback":
		ps.p++
		ps.acceptKw("work")
		if ps.isKw("to") {
			return nil, unsupported("savepoints are not supported")
		}
		return &Rollback{}, nil
	case "set":
		return ps.set()
	case "quit", "exit":
		ps.p++
		return &Quit{}, nil
	case "savepoint", "release", "alter", "truncate", "replace", "show", "describe", "desc",
		"explain", "use", "grant", "revoke", "rename", "lock", "unlock", "with", "analyze":
		return nil, unsupported("statement '%s' is not supported", strings.ToUpper(t.Text))
	}
	return nil, ps.errNear()
}
