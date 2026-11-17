package sqlparse

import "strings"

// ParseInsertHead parses the head of an INSERT statement — everything up to
// and including the VALUES/VALUE keyword — without lexing the row region.
// valuesEnd is the byte offset in src where the row region begins (the End of
// the VALUES token). ok is false for anything that is not a plain INSERT
// head; callers then fall back to Parse.
//
// The head is bounded by the first quote in src: lexing src[:q] can never hit
// an unterminated string, and a long head (> 4 KiB before the first string
// literal) is not worth the speculative work.
func ParseInsertHead(src string) (ins *Insert, valuesEnd int, ok bool) {
	q := strings.IndexAny(src, "'\"")
	if q < 0 || q > 4096 {
		return nil, 0, false
	}
	toks, err := Lex(src[:q])
	if err != nil {
		return nil, 0, false
	}
	ps := &parser{src: src[:q], toks: toks}
	if !ps.isKw("insert") {
		return nil, 0, false
	}
	ins, err = ps.insertHead()
	if err != nil {
		return nil, 0, false
	}
	return ins, ps.toks[ps.p-1].End, true
}

// RowParser parses the row region of an INSERT statement (the text following
// the VALUES keyword) reusing one token buffer across calls.
type RowParser struct {
	toks []Token
}

// ParseRows lexes src and parses it as a complete row list: `(` expr `,` ...
// `)` pairs separated by commas, followed by optional semicolons and EOF.
// Errors are returned, never swallowed; the parser keeps its token buffer
// unless it grew past 65536 entries. Its tokens do not retain source strings
// after this call; the returned AST owns any values the caller still needs.
func (rp *RowParser) ParseRows(src string) ([][]Expr, error) {
	toks, err := LexInto(src, rp.toks[:0])
	if cap(toks) <= 65536 {
		rp.toks = toks[:0]
		// A smaller batch leaves old tokens beyond len(toks). Clear the
		// entire retained allocation on success and parse errors alike.
		defer clear(toks[:cap(toks)])
	} else {
		rp.toks = nil
	}
	if err != nil {
		return nil, err
	}
	ps := &parser{src: src, toks: toks}
	ins := &Insert{}
	if err := ps.insertRows(ins); err != nil {
		return nil, err
	}
	for ps.isOp(";") {
		ps.p++
	}
	if ps.cur().Kind != EOF {
		return nil, ps.errNear()
	}
	return ins.Rows, nil
}
