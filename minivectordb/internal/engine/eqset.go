package engine

import (
	"os"
	"strings"
	"sync/atomic"

	sp "minivectordb/internal/sqlparse"
	"minivectordb/internal/types"
)

// catBlocksEnabled gates the per-category compacted blocks (worker h-b);
// a variable so tests can toggle it. MVDB_NO_CATBLOCK=1 disables them
// entirely: the filtered path falls back to the candidate-set multi-pass
// scan, bit-identical.
var catBlocksEnabled = os.Getenv("MVDB_NO_CATBLOCK") == ""

// fusedBlockBatches counts the batches the fused scan kernel has run over a
// compacted category block (diagnostic for tests: it proves the block
// branch was taken).
var fusedBlockBatches atomic.Int64

// eqSet groups the index slots of committed rows by the value of one VARCHAR
// column. It is derived state, built on first use and dropped whenever the
// index changes (gen), so it needs no maintenance on the write path.
type eqSet struct {
	gen uint64
	m   map[string][]int32
}

// eqSlots returns the slots whose column col equals v. The second result is
// false when no candidate set can be used.
func (x *VecIndex) eqSlots(db *DB, t *Table, col int, v string) ([]int32, bool) {
	if x.Disabled || col < 0 {
		return nil, false
	}
	if x.eq == nil {
		x.eq = map[int]*eqSet{}
	}
	e := x.eq[col]
	if e == nil || e.gen != x.gen {
		e = &eqSet{gen: x.gen, m: map[string][]int32{}}
		for s, pk := range x.pks {
			if pk == tombstone {
				continue
			}
			k := string(t.varBytes(db.rowBytes(x.off[s]), col))
			e.m[k] = append(e.m[k], int32(s))
		}
		x.eq[col] = e
	}
	return e.m[v], true
}

// catBlock is a compacted, category-major copy of one equality value's
// rows: the int8 codes, folded threshold constants, log offsets and primary
// keys of the value's candidate slots, laid out sequentially in slot order
// within the category. It exists so a covering equality filter can run the
// fused single-pass scan (fusedScanInt8Block) over sequential rows instead
// of the multi-pass scan over scattered candidate slots: the multi-pass
// path pays a ~2x scattered-dot penalty (measured: dotInt8GatherInt over
// 10k rows, 70µs sequential vs 147µs scattered, serial) plus a separate
// gather loop and threshold loop over the same rows. The block is derived
// state, built lazily on the first covering filtered query for the value
// and dropped whenever the index changes (gen), exactly like eqSet, so it
// needs no maintenance on the write path. Rows are copied from live slots
// only (eqSlots already skips tombstones); the block never holds a
// tombstone, and a transaction overlay disables the block path (the
// overlay rows are not in the block).
type catBlock struct {
	gen   uint64
	n     int
	codes []byte    // n*dim uint8 codes, row-major
	qf    []float64 // n*4 folded threshold constants (see foldQparam)
	off   []int64   // log offset per row
	pks   []int64   // primary key per row
}

// catBlockFor returns the compacted block for column col = val, building it
// from the candidate set on first use. It returns nil when blocks are
// disabled, the prefilter state is absent (no codes/qf to compact), or no
// candidate set can be used. The build copies one category's rows
// (~10k rows at the bench scale, ~3 MB); it runs once per value per index
// generation.
func (x *VecIndex) catBlockFor(db *DB, t *Table, col int, val string) *catBlock {
	if !catBlocksEnabled || x.Disabled || col < 0 || x.codes == nil || x.qf == nil {
		return nil
	}
	// The map key must own its bytes: val comes from the SQL parser and,
	// through the stdio server, aliases the request reader's ring buffer
	// (proto.parseFast builds the SQL with unsafe.String, and StrLit
	// slices it), so the bytes are overwritten by later requests. Storing
	// val itself as the key lets a later query's WHERE literal land on
	// the same buffer offset and mutate the key, so a lookup for the new
	// value returns the old value's block (measured: c8's query returned
	// c3's top-10 when both literals sat at offset 5018, and c1's
	// returned c6's at 5020). Clone once; the lookup below is transient
	// and needs no copy.
	val = strings.Clone(val)
	if x.cat == nil {
		x.cat = map[int]map[string]*catBlock{}
	}
	m := x.cat[col]
	if m == nil {
		m = map[string]*catBlock{}
		x.cat[col] = m
	}
	if b := m[val]; b != nil && b.gen == x.gen {
		return b
	}
	cand, ok := x.eqSlots(db, t, col, val)
	if !ok {
		return nil
	}
	dim := t.Dim
	b := &catBlock{gen: x.gen, n: len(cand)}
	b.codes = make([]byte, len(cand)*dim)
	b.qf = make([]float64, len(cand)*4)
	b.off = make([]int64, len(cand))
	b.pks = make([]int64, len(cand))
	for i, s := range cand {
		copy(b.codes[i*dim:(i+1)*dim], x.codes[int(s)*dim:int(s)*dim+dim])
		copy(b.qf[i*4:(i+1)*4], x.qf[int(s)*4:int(s)*4+4])
		b.off[i] = x.off[s]
		b.pks[i] = x.pks[s]
	}
	m[val] = b
	return b
}

// eqCovers reports whether the candidate set alone decides WHERE: the whole
// condition is the single equality on the set's column.
func eqCovers(e sp.Expr) bool {
	x, ok := e.(*sp.Cmp)
	return ok && x.Op == "="
}

// eqFilter finds a conjunct "varchar_column = 'constant'" in WHERE. The full
// WHERE is still evaluated on every candidate, so this only narrows the scan.
func eqFilter(sc *scope, e sp.Expr) (col int, val string, ok bool) {
	switch x := e.(type) {
	case *sp.And:
		if col, val, ok = eqFilter(sc, x.L); ok {
			return
		}
		return eqFilter(sc, x.R)
	case *sp.Cmp:
		if x.Op != "=" {
			return -1, "", false
		}
		c, l := x.L.(*sp.ColRef)
		s, r := x.R.(*sp.StrLit)
		if !l || !r {
			c, l = x.R.(*sp.ColRef)
			s, r = x.L.(*sp.StrLit)
		}
		if !l || !r || sc.t == nil {
			return -1, "", false
		}
		i, err := sc.resolve(c)
		if err != nil || sc.t.Cols[i].Type != types.TVarchar {
			return -1, "", false
		}
		return i, s.V, true
	}
	return -1, "", false
}
