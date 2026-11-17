package proto

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Request is one JSONL request from the judge, e.g.
// {"id":1,"op":"execute","sql":"SELECT 1;","search_mode":"exact"}.
type Request struct {
	// ID is echoed verbatim so its JSON type (number, string) is preserved.
	ID json.RawMessage `json:"id"`
	Op string          `json:"op"`
	// SQL may alias either the request line or the parser's scratch buffer;
	// it is valid until the next Parse on the same parser, which the main
	// request loop obeys by finishing each request before the next read.
	SQL        *string `json:"sql"`
	SearchMode string  `json:"search_mode"`
}

// RequestParser decodes request lines and may reuse one scratch buffer to
// hold decoded SQL text, avoiding a fresh allocation per request.
type RequestParser struct {
	scratch []byte
}

// ParseRequest decodes one request line. On error the returned request still
// carries the id when it could be recovered, so the reply can echo it.
func ParseRequest(line []byte) (*Request, error) {
	return new(RequestParser).Parse(line)
}

// Parse decodes one request line. On error the returned request still
// carries the id when it could be recovered, so the reply can echo it.
func (p *RequestParser) Parse(line []byte) (*Request, error) {
	line = bytes.TrimSpace(line)
	if cap(p.scratch) > 8<<20 {
		// One giant escaped request must not pin the buffer forever.
		p.scratch = nil
	}
	fr, sc, ok := parseFast(line, p.scratch)
	p.scratch = sc
	if ok {
		return fr, nil
	}
	var r Request
	if err := json.Unmarshal(line, &r); err != nil {
		// Recover the id from an otherwise malformed object, if possible.
		var probe struct {
			ID json.RawMessage `json:"id"`
		}
		if json.Unmarshal(line, &probe) == nil {
			r.ID = probe.ID
		}
		return &r, err
	}
	if len(r.ID) == 0 || bytes.Equal(r.ID, []byte("null")) {
		return &r, errMissingID
	}
	return &r, nil
}

var errMissingID = errors.New("missing request id")
