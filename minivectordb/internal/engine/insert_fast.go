package engine

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	sp "minivectordb/internal/sqlparse"
	"minivectordb/internal/types"
)

// The fast path is optimistic: it either completes a whole INSERT statement
// or reports "not handled" with no side effects, after which Exec runs the
// untouched sequential path (sp.Parse -> execInsert) on the original SQL.
// Any lex/parse/type/duplicate/shape problem therefore still surfaces through
// the sequential path with its own error object. The only error the fast
// path returns itself is a storage write failure.
var (
	fastInsertEnabled  = os.Getenv("MVDB_NO_FAST_INSERT") == ""
	fastInsertMinBytes = envInt("MVDB_FAST_INSERT_MIN", 16<<10)
	fastInsertWorkers  = envInt("MVDB_FAST_INSERT_WORKERS", 0)

	fastInsertHandled  atomic.Int64
	fastInsertFallback atomic.Int64 // statements that reached the head parse but fell back
)

func envInt(name string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return n
	}
	return def
}

type fastSpan struct {
	pk         int64
	start, end int
}

// fastInsertWorker owns the buffers one worker fills while parsing and
// encoding its half of the row region. Workers only read shared state.
type fastInsertWorker struct {
	rp      sp.RowParser
	enc     []byte
	spans   []fastSpan
	row     []types.Value
	scratch []float32
	failed  bool
}

type fastInsertBufs struct {
	workers [2]fastInsertWorker
	chs     []change
	seen    map[int64]struct{}
}

func hasInsertPrefix(sql string) bool {
	i := 0
	for i < len(sql) {
		switch sql[i] {
		case ' ', '\t', '\n', '\r', '\v', '\f':
			i++
		default:
			goto kw
		}
	}
kw:
	for k := 0; k < len("insert"); k++ {
		if i+k >= len(sql) || sql[i+k]|0x20 != "insert"[k] {
			return false
		}
	}
	return true
}

func (db *DB) tryFastInsert(sql string) (res *Result, err error, handled bool) {
	if !fastInsertEnabled || len(sql) < fastInsertMinBytes || !hasInsertPrefix(sql) {
		return nil, nil, false
	}
	fallback := func() (*Result, error, bool) {
		fastInsertFallback.Add(1)
		return nil, nil, false
	}
	ins, vend, ok := sp.ParseInsertHead(sql)
	if !ok {
		return fallback()
	}
	t := db.tables[strings.ToLower(ins.Table)]
	if t == nil {
		return fallback()
	}
	target, terr := insertTarget(t, ins.Cols)
	if terr != nil {
		return fallback()
	}
	region := sql[vend:]
	workers := fastInsertWorkers
	if workers <= 0 {
		workers = min(runtime.GOMAXPROCS(0), 2)
	}
	parts := []string{region}
	if workers >= 2 {
		if a, b, ok := splitInsertRows(region); ok {
			parts = []string{a, b}
		}
	}
	n := 0
	defer func() { db.resetFastInsert(n) }()
	fi := &db.fi
	run := func(w *fastInsertWorker, part string) {
		w.failed = false
		rows, perr := w.rp.ParseRows(part)
		if perr != nil {
			w.failed = true
			return
		}
		if len(w.row) != len(t.Cols) {
			w.row = make([]types.Value, len(t.Cols))
		}
		w.enc = w.enc[:0]
		w.spans = w.spans[:0]
		for _, exprs := range rows {
			if len(exprs) != len(target) {
				w.failed = true
				return
			}
			for j, e := range exprs {
				var value types.Value
				switch e := e.(type) {
				case *sp.IntLit:
					value = types.Int(e.V)
				case *sp.FloatLit:
					value = types.Float(e.V)
				case *sp.StrLit:
					value = types.Str(e.V)
				default:
					w.failed = true
					return
				}
				col := target[j]
				v, cerr := coerceVectorInto(t.Cols[col], value, w.scratch)
				if cerr != nil {
					w.failed = true
					return
				}
				// As in execInsert: the parsed vector becomes the scratch
				// buffer; encode copies it before the next row reuses it.
				if value.K == types.KStr && v.K == types.KVec {
					w.scratch = v.V
				}
				w.row[col] = v
			}
			start := len(w.enc)
			w.enc = t.encode(w.enc, w.row)
			w.spans = append(w.spans, fastSpan{pk: w.row[t.PK].I, start: start, end: len(w.enc)})
		}
	}
	if len(parts) == 2 {
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			run(&fi.workers[1], parts[1])
		}()
		run(&fi.workers[0], parts[0])
		wg.Wait()
	} else {
		run(&fi.workers[0], parts[0])
	}
	for i := range parts {
		if fi.workers[i].failed {
			return fallback()
		}
		n += len(fi.workers[i].spans)
	}
	if n == 0 {
		return fallback()
	}
	// Duplicate checks run in row order on the calling goroutine, after all
	// workers finished: in-statement dups and conflicts with visible rows
	// both send the statement back to the sequential path.
	seen := fi.seen
	if seen == nil {
		seen = make(map[int64]struct{}, n)
	} else {
		clear(seen)
	}
	fi.seen = seen
	chs := fi.chs[:0]
	for i := range parts {
		w := &fi.workers[i]
		for _, s := range w.spans {
			if _, dup := seen[s.pk]; dup {
				return fallback()
			}
			if _, exists := db.lookup(t, s.pk); exists {
				return fallback()
			}
			seen[s.pk] = struct{}{}
			chs = append(chs, change{t: t, pk: s.pk, row: w.enc[s.start:s.end:s.end]})
		}
	}
	fi.chs = chs
	if err := db.write(chs); err != nil {
		return nil, ioErr(err), true
	}
	fastInsertHandled.Add(1)
	return &Result{Affected: int64(n), HasAffected: true}, nil, true
}

// resetFastInsert returns oversized per-statement buffers so one huge INSERT
// cannot pin memory, and clears chs so it cannot keep a dropped enc alive.
func (db *DB) resetFastInsert(rows int) {
	fi := &db.fi
	for i := range fi.workers {
		w := &fi.workers[i]
		// Encoded rows own their bytes. A string value in the scratch row
		// can otherwise keep the whole completed request alive, including
		// when this worker failed or is unused by the next INSERT.
		clear(w.row)
		if cap(w.enc) > 4<<20 {
			w.enc = nil
		}
		if cap(w.spans) > 65536 {
			w.spans = nil
		}
	}
	// Appends on a truncated chs can leave row references past its length;
	// clear the whole backing array so a dropped enc is not pinned.
	clear(fi.chs[:cap(fi.chs)])
	fi.chs = fi.chs[:0]
	if rows > 65536 {
		fi.seen = nil
	}
}

// splitInsertRows splits region at a row boundary near its midpoint. It is
// deliberately naive: it does not track quotes or comments, so it can pick a
// `),(` that lies inside a string or comment. That is still safe: a part A
// ending inside a string or /* */ comment is unterminated and fails to lex,
// while a -- or # line comment would eat the boundary `)` without a lex error
// and let part B start mid-comment, inventing rows — so a candidate is only
// accepted when A contains no -- or # at all. When both halves then lex and
// parse as complete row lists, A ends on a real `)` token (no lookahead
// dependence), tokens(A) + `,` + tokens(B) is exactly the token stream of the
// original region and the rows are rows(A) ++ rows(B).
func splitInsertRows(region string) (a, b string, ok bool) {
	for i := len(region) / 2; i < len(region); i++ {
		if region[i] != ')' {
			continue
		}
		j := i + 1
		for j < len(region) && isByteSpace(region[j]) {
			j++
		}
		if j >= len(region) || region[j] != ',' {
			continue
		}
		k := j + 1
		for k < len(region) && isByteSpace(region[k]) {
			k++
		}
		if k >= len(region) || region[k] != '(' {
			continue
		}
		a = region[:i+1]
		if strings.IndexByte(a, '#') >= 0 || strings.Contains(a, "--") {
			continue
		}
		return a, region[k:], true
	}
	return "", "", false
}

func isByteSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
