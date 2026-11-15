package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"unicode/utf8"
	"unsafe"
)

// parseFast decodes the common request shape without reflection: a flat
// object whose values are strings without escapes or plain numbers, for
// example {"id":7,"op":"execute","sql":"SELECT ...","search_mode":"exact"}.
// Anything else (escapes, nesting, duplicate or unknown structure) returns
// ok=false and the caller falls back to encoding/json, so the accepted
// language is a strict subset of what encoding/json accepts with identical
// results.
func parseFast(b []byte) (*Request, bool) {
	i, n := 0, len(b)
	ws := func() {
		for i < n && (b[i] == ' ' || b[i] == '\t' || b[i] == '\r' || b[i] == '\n') {
			i++
		}
	}
	// str reads a string with no escapes and no control characters. The
	// returned string aliases b so the large SQL field does not need a copy;
	// callers must keep b unchanged until the request is fully executed.
	str := func() (string, bool) {
		if i >= n || b[i] != '"' {
			return "", false
		}
		rest := b[i+1:]
		j := bytes.IndexByte(rest, '"')
		if j < 0 || bytes.IndexByte(rest[:j], '\\') >= 0 || !validStringBytes(rest[:j]) {
			return "", false
		}
		s := unsafe.String(&b[i+1], j)
		i += j + 2
		return s, true
	}
	ws()
	if i >= n || b[i] != '{' {
		return nil, false
	}
	i++
	var r Request
	// The only keys the fast path accepts are op, sql, search_mode and id,
	// so four flags replace a map that had to be allocated per request.
	var seenOp, seenSQL, seenMode, seenID bool
	for {
		ws()
		if i < n && b[i] == '}' && !seenOp && !seenSQL && !seenMode && !seenID {
			i++
			break
		}
		k, ok := str()
		if !ok {
			return nil, false
		}
		ws()
		if i >= n || b[i] != ':' {
			return nil, false
		}
		i++
		ws()
		switch k {
		case "op":
			if seenOp {
				return nil, false
			}
			seenOp = true
			v, ok := str()
			if !ok {
				return nil, false
			}
			r.Op = v
		case "sql":
			if seenSQL {
				return nil, false
			}
			seenSQL = true
			v, ok := str()
			if !ok {
				return nil, false
			}
			r.SQL = &v
		case "search_mode":
			if seenMode {
				return nil, false
			}
			seenMode = true
			v, ok := str()
			if !ok {
				return nil, false
			}
			r.SearchMode = v
		case "id":
			if seenID {
				return nil, false
			}
			seenID = true
			start := i
			if i < n && b[i] == '"' {
				if _, ok := str(); !ok {
					return nil, false
				}
			} else {
				if i < n && b[i] == '-' {
					i++
				}
				d := 0
				for i < n && b[i] >= '0' && b[i] <= '9' {
					i++
					d++
				}
				if d == 0 || (d > 1 && b[start] == '0') || (d > 1 && b[start] == '-' && b[start+1] == '0') {
					return nil, false
				}
			}
			r.ID = json.RawMessage(append([]byte(nil), b[start:i]...))
		default:
			return nil, false
		}
		ws()
		if i < n && b[i] == ',' {
			i++
			continue
		}
		if i < n && b[i] == '}' {
			i++
			break
		}
		return nil, false
	}
	ws()
	if i != n || r.ID == nil {
		return nil, false
	}
	return &r, true
}

// validStringBytes skips eight printable ASCII bytes at a time and resumes
// the skip after each non-ASCII rune, so a few Chinese characters do not
// turn the check byte-at-a-time for the rest of the string. The SWAR test
// also trips on 0x7F..0xFF and on bytes below 0x20; those fall to the
// per-byte branch, which enforces encoding/json's requirement that the fast
// path reject invalid UTF-8 and control bytes for its slow fallback.
func validStringBytes(b []byte) bool {
	for {
		for len(b) >= 8 {
			w := binary.LittleEndian.Uint64(b)
			if ((w-0x2020202020202020)|w)&0x8080808080808080 != 0 {
				break
			}
			b = b[8:]
		}
		if len(b) == 0 {
			return true
		}
		c := b[0]
		if c < 0x20 {
			return false
		}
		if c < utf8.RuneSelf {
			b = b[1:]
			continue
		}
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		b = b[size:]
	}
}
