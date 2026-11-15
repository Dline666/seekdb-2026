package proto

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"unicode/utf8"
	"unsafe"
)

// parseFast decodes the common request shape without reflection: a flat
// object whose values are strings or plain numbers, for example
// {"id":7,"op":"execute","sql":"SELECT ...","search_mode":"exact"}. Only the
// sql string may contain escapes; it is decoded into scratch, whose possibly
// grown replacement is returned as the second value. Anything else
// (unsupported escapes, nesting, duplicate or unknown structure) returns
// ok=false and the caller falls back to encoding/json, so the accepted
// language is a strict subset of what encoding/json accepts with identical
// results.
func parseFast(b []byte, scratch []byte) (*Request, []byte, bool) {
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
		return nil, scratch, false
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
			return nil, scratch, false
		}
		ws()
		if i >= n || b[i] != ':' {
			return nil, scratch, false
		}
		i++
		ws()
		switch k {
		case "op":
			if seenOp {
				return nil, scratch, false
			}
			seenOp = true
			v, ok := str()
			if !ok {
				return nil, scratch, false
			}
			r.Op = v
		case "sql":
			if seenSQL {
				return nil, scratch, false
			}
			seenSQL = true
			if i >= n || b[i] != '"' {
				return nil, scratch, false
			}
			rest := b[i+1:]
			j := bytes.IndexByte(rest, '"')
			if j < 0 {
				return nil, scratch, false
			}
			if bytes.IndexByte(rest[:j], '\\') < 0 {
				// No escapes: alias the line exactly like str().
				if !validStringBytes(rest[:j]) {
					return nil, scratch, false
				}
				v := unsafe.String(&b[i+1], j)
				i += j + 2
				r.SQL = &v
				break
			}
			// Escapes: decode into the scratch. A backslash before the
			// first quote means the naive closing quote is inside an
			// escape, so the escape-aware scan decides.
			out, end, ok := unescapeInto(scratch[:0], rest)
			scratch = out
			if !ok {
				return nil, scratch, false
			}
			var v string
			if len(out) > 0 {
				v = unsafe.String(&out[0], len(out))
			}
			i += end + 1
			r.SQL = &v
		case "search_mode":
			if seenMode {
				return nil, scratch, false
			}
			seenMode = true
			v, ok := str()
			if !ok {
				return nil, scratch, false
			}
			r.SearchMode = v
		case "id":
			if seenID {
				return nil, scratch, false
			}
			seenID = true
			start := i
			if i < n && b[i] == '"' {
				if _, ok := str(); !ok {
					return nil, scratch, false
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
					return nil, scratch, false
				}
			}
			r.ID = json.RawMessage(append([]byte(nil), b[start:i]...))
		default:
			return nil, scratch, false
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
		return nil, scratch, false
	}
	ws()
	if i != n || r.ID == nil {
		return nil, scratch, false
	}
	return &r, scratch, true
}

// unescapeInto decodes one JSON string body. src starts just after the
// opening quote; end is the offset just past the closing quote. Only the
// escapes whose meaning is unambiguous are decoded on the fast path: \" \\
// \/ \b \f \n \r \t and \uXXXX for a non-surrogate code unit. Anything else
// (a surrogate, an unknown escape, a truncated escape or string, a raw
// control byte or invalid UTF-8) fails so the whole line can go to
// encoding/json, whose surrogate pairing and error rules stay authoritative.
func unescapeInto(dst, src []byte) (out []byte, end int, ok bool) {
	i, n := 0, len(src)
	for i < n {
		j := bytes.IndexAny(src[i:], "\"\\")
		if j < 0 {
			// No closing quote and no escape: unterminated string.
			return dst, 0, false
		}
		run := src[i : i+j]
		if !validStringBytes(run) {
			return dst, 0, false
		}
		dst = append(dst, run...)
		i += j
		switch src[i] {
		case '"':
			return dst, i + 1, true
		}
		// src[i] == '\\'
		i++
		if i >= n {
			return dst, 0, false
		}
		switch c := src[i]; c {
		case '"', '\\', '/':
			dst = append(dst, c)
			i++
		case 'b':
			dst = append(dst, 0x08)
			i++
		case 'f':
			dst = append(dst, 0x0C)
			i++
		case 'n':
			dst = append(dst, 0x0A)
			i++
		case 'r':
			dst = append(dst, 0x0D)
			i++
		case 't':
			dst = append(dst, 0x09)
			i++
		case 'u':
			if i+5 > n {
				return dst, 0, false
			}
			h, ok := hex4(src[i+1 : i+5])
			if !ok || h >= 0xD800 && h <= 0xDFFF {
				return dst, 0, false
			}
			dst = utf8.AppendRune(dst, rune(h))
			i += 5
		default:
			return dst, 0, false
		}
	}
	return dst, 0, false
}

// hex4 parses exactly four hexadecimal digits.
func hex4(b []byte) (uint32, bool) {
	var v uint32
	for _, c := range b {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint32(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint32(c - 'a' + 10)
		case c >= 'A' && c <= 'F':
			v |= uint32(c - 'A' + 10)
		default:
			return 0, false
		}
	}
	return v, true
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
