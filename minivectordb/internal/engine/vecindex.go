package engine

import (
	"math"
	"math/rand"
	"sort"
	"strings"

	"minivectordb/internal/types"
)

// VecIndex keeps the slot layout of the committed vectors of one table:
// the primary key and the log offset of every row, plus the derived sets the
// query path needs. The vectors themselves are not copied: they are read
// straight from the log, which is mapped read-only, so the peak memory of the
// process does not hold a second copy of the table. Exact search scans the
// mapping; ANN search uses an IVF partition over the same slots. It is
// derived state: recovery rebuilds it from the log, so it is never persisted.
type VecIndex struct {
	Name     string
	Explicit bool // created by CREATE VECTOR INDEX
	// Disabled is set when the slots do not fit in the memory budget; the
	// index then holds nothing and queries scan the log instead.
	Disabled bool
	dim      int
	pks      []int64
	off      []int64 // row offset of each slot
	pos      map[int64]int32
	free     []int32
	live     int

	// IVF state. list[c] holds slot ids; slotList[s] is the list of slot s.
	cents    []float32
	nlist    int
	lists    [][]int32
	slotList []int32
	ivfBuilt int // live count when the IVF was trained

	// Int8 prefilter state. codes holds one uint8 code per dimension per
	// slot (slot-major: codes[s*dim+i]); qp holds the per-slot affine
	// parameters. Codes are derived state, rebuilt with the slots, and are
	// only used to filter candidates: exact distances are always
	// recomputed for survivors. codes is nil when the prefilter is off
	// (small dimension, memory budget, or a disabled index).
	// qf holds the folded per-slot threshold constants (see foldQparam),
	// four float64s per slot in slot-major order: qf[s*4+0]=m2,
	// qf[s*4+1]=t2, qf[s*4+2]=s2, qf[s*4+3]=cRow. The fused scan's kernel
	// reads all four with one cursor. It is built and reset exactly with
	// qp, so it is never stale relative to the codes.
	codes       []byte
	qf          []float64
	qbounds     []quantBlockBound // conservative bounds for each quantBlockRows consecutive slots
	maxScale    float32           // largest per-row quantization scale in the index; a stale larger value is safe (it only loosens the prefilter threshold)
	maxAbs      float64           // upper bound on absolute dequantized coordinates; stale larger bounds are safe
	qscratch    []float32         // reused decode buffer for quantizeSlot
	noPrefilter bool

	// gen changes whenever a slot changes; derived sets compare against it.
	gen      uint64
	eq       map[int]*eqSet
	cat      map[int]map[string]*catBlock
	catBytes int64
	eqBytes  int64
	eqFull   bool
}

// indexBytesPerRow is the in-memory cost of one indexed row once the vectors
// are read from the log on demand: the primary key, the row offset and the
// primary key map entry.
const indexBytesPerRow = 64

func newVecIndex(name string) *VecIndex {
	return &VecIndex{Name: strings.Clone(name), pos: map[int64]int32{}}
}

func (x *VecIndex) rebuild(db *DB, t *Table) {
	x.invalidateSets()
	x.qbounds = nil
	x.maxAbs = 0
	x.qscratch = nil
	x.dim = t.Dim
	budget := db.indexBudget(x)
	x.Disabled = int64(len(t.rows))*indexBytesPerRow > budget
	if x.Disabled {
		x.pks, x.off, x.free, x.pos, x.live = nil, nil, nil, map[int64]int32{}, 0
		x.codes, x.qf, x.qscratch, x.noPrefilter, x.maxScale = nil, nil, nil, false, 0
		x.resetIVF()
		return
	}
	// The prefilter needs dim+32 bytes per row (codes + folded threshold
	// constants) on top of the index itself, and only
	// pays off for the batched scan (dim>=16).
	// When it does not fit, the index still works and queries just run the
	// exact scan.
	x.noPrefilter = x.dim < 16 || x.dim > maxInt8Dim || int64(len(t.rows))*(indexBytesPerRow+int64(x.dim)+32)+int64(x.dim)*4+quantBlockBytes(len(t.rows)) > budget
	x.codes, x.qf, x.maxScale = nil, nil, 0
	x.pks, x.off, x.free = nil, nil, nil
	x.pos = make(map[int64]int32, len(t.rows))
	x.live = 0
	x.resetIVF()
	x.pks = make([]int64, 0, len(t.rows))
	x.off = make([]int64, 0, len(t.rows))
	// Rebuild knows the final size. Repeated append growth otherwise retains
	// several generations of large code/parameter arrays until the next GC.
	if !x.noPrefilter {
		x.codes = make([]byte, 0, len(t.rows)*x.dim)
		x.qf = make([]float64, 0, len(t.rows)*4)
		x.qbounds = make([]quantBlockBound, (len(t.rows)+quantBlockRows-1)/quantBlockRows)
	}
	// The slots follow the log order. The scan then walks the log from the
	// oldest row to the newest and reads the mapped file sequentially; with
	// an arbitrary order every row would be a separate random access.
	type rowRef struct {
		pk  int64
		off int64
	}
	rows := make([]rowRef, 0, len(t.rows))
	for pk, off := range t.rows {
		rows = append(rows, rowRef{pk, off})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].off < rows[j].off })
	for _, r := range rows {
		x.upsert(db, t, r.pk, r.off)
	}
}

func (x *VecIndex) resetIVF() {
	x.cents, x.lists, x.slotList, x.nlist, x.ivfBuilt = nil, nil, nil, 0, 0
}

func (x *VecIndex) upsert(db *DB, t *Table, pk, off int64) {
	if x.Disabled {
		return
	}
	x.invalidateSets()
	budget := db.indexBudget(x)
	if !x.has(pk) && int64(x.live+1)*indexBytesPerRow > budget {
		x.Disabled = true
		x.pks, x.off, x.free, x.pos, x.live = nil, nil, nil, map[int64]int32{}, 0
		x.codes, x.qf, x.qscratch, x.noPrefilter, x.maxScale = nil, nil, nil, false, 0
		x.qbounds = nil
		x.resetIVF()
		return
	}
	// Backstop for the codes budget: rows added after the last rebuild can
	// push the codes past the budget. Dropping the codes only turns the
	// prefilter off; the index itself keeps working.
	if !x.has(pk) && !x.noPrefilter && x.dim >= 16 &&
		int64(max(len(x.pks), x.live+1))*(indexBytesPerRow+int64(x.dim)+32)+int64(x.dim)*4+quantBlockBytes(max(len(x.pks), x.live+1)) > budget {
		x.codes, x.qf, x.qscratch, x.noPrefilter, x.maxScale = nil, nil, nil, true, 0
		x.qbounds = nil
	}
	s, ok := x.pos[pk]
	if !ok {
		if n := len(x.free); n > 0 {
			s = x.free[n-1]
			x.free = x.free[:n-1]
		} else {
			s = int32(len(x.pks))
			x.pks = append(x.pks, 0)
			x.off = append(x.off, 0)
			if x.slotList != nil {
				x.slotList = append(x.slotList, -1)
			}
			if !x.noPrefilter && x.dim >= 16 {
				x.codes = append(x.codes, make([]byte, x.dim)...)
				x.qf = append(x.qf, 0, 0, 0, 0)
			}
		}
		x.pos[pk] = s
		x.live++
	} else if x.slotList != nil {
		x.unlist(s)
	}
	x.pks[s] = pk
	x.off[s] = off
	x.quantizeSlot(db, t, s)
	if x.slotList != nil {
		c := x.nearestCentroidBytes(db, t, off)
		x.lists[c] = append(x.lists[c], s)
		x.slotList[s] = int32(c)
	}
	// append capacity and map overhead may exceed the row-count estimate.
	// Drop optional quantization first; if slots alone do not fit, use the
	// generic scan over the authoritative table map.
	if x.derivedBytes() > budget {
		x.resetIVF()
		x.codes, x.qf, x.qscratch, x.noPrefilter, x.maxScale, x.maxAbs = nil, nil, nil, true, 0, 0
		x.qbounds = nil
		if x.derivedBytes() > budget {
			x.Disabled = true
			x.pks, x.off, x.free, x.pos, x.live = nil, nil, nil, nil, 0
			x.resetIVF()
			return
		}
	}
}

// quantizeSlot (re)builds the int8 codes of the row in slot s from the
// vector stored at x.off[s]. It reuses x.qscratch so bulk inserts do not
// allocate once per row. It is a no-op when the prefilter is off. The
// defensive growth below keeps the invariant the scan relies on (every
// slot owns dim code bytes and one parameter set); upsert extends the
// arrays on append, so it should never trigger, but a slot without codes
// would let the scan filter on stale data, which must not happen silently.
func (x *VecIndex) quantizeSlot(db *DB, t *Table, s int32) {
	if x.noPrefilter || x.dim < 16 {
		return
	}
	if want := (int(s) + 1) * x.dim; len(x.codes) < want {
		x.codes = append(x.codes, make([]byte, want-len(x.codes))...)
	}
	for len(x.qf) < (int(s)+1)*4 {
		x.qf = append(x.qf, 0, 0, 0, 0)
	}
	if len(x.qscratch) < x.dim {
		x.qscratch = make([]float32, x.dim)
	}
	v := x.qscratch[:x.dim]
	x.decodeInto(v, db, t, x.off[s])
	base := int(s) * x.dim
	qp := quantizeRow(v, x.codes[base:base+x.dim])
	// min + code*scale is bounded by |min| + 256*scale. Keep a
	// conservative maximum for the absolute rounding error in the expanded
	// squared distance; its magnitude can dwarf the final distance.
	bound := math.Abs(float64(qp.min)) + 256*float64(qp.scale)
	if bound > x.maxAbs {
		x.maxAbs = bound
	}
	block := int(s) / quantBlockRows
	for len(x.qbounds) <= block {
		x.qbounds = append(x.qbounds, quantBlockBound{})
	}
	x.qbounds[block].add(qp.scale, bound)
	x.qf[4*int(s)], x.qf[4*int(s)+1], x.qf[4*int(s)+2], x.qf[4*int(s)+3] = foldQparam(qp, x.dim)
	if qp.scale > x.maxScale {
		x.maxScale = qp.scale
	}
}

// decodeInto reads the vector stored at the given log offset into dst.
func (x *VecIndex) decodeInto(dst []float32, db *DB, t *Table, off int64) {
	types.DecodeVecInto(dst, db.rowBytes(off)[t.vecOff:])
}

// decode returns the vector stored at the given log offset.
func (x *VecIndex) decode(db *DB, t *Table, off int64) []float32 {
	v := make([]float32, x.dim)
	x.decodeInto(v, db, t, off)
	return v
}

func (x *VecIndex) has(pk int64) bool { _, ok := x.pos[pk]; return ok }

// Drop derived sets at the mutation, not when each individual value is next
// queried. Otherwise stale categories keep their code copies alive forever.
func (x *VecIndex) invalidateSets() {
	x.gen++
	x.eq, x.cat, x.catBytes, x.eqBytes, x.eqFull = nil, nil, 0, 0, false
}

// derivedBytes includes slice capacity, since append growth can retain more
// storage than the number of live rows. Map cost remains an estimate, as in
// indexBytesPerRow; all tables and category blocks share one index budget.
func (x *VecIndex) derivedBytes() int64 {
	bytes := int64(cap(x.pks)+cap(x.off)+cap(x.qf))*8 +
		int64(cap(x.free)+cap(x.qscratch))*4 + int64(len(x.pos))*48 +
		int64(cap(x.codes)) + int64(cap(x.qbounds))*16 + x.catBytes + x.eqBytes
	bytes += int64(cap(x.cents)+cap(x.slotList))*4 + int64(cap(x.lists))*24
	for _, list := range x.lists {
		bytes += int64(cap(list)) * 4
	}
	return bytes
}

func (x *VecIndex) remove(pk int64) {
	s, ok := x.pos[pk]
	if !ok {
		return
	}
	x.invalidateSets()
	delete(x.pos, pk)
	if x.slotList != nil {
		x.unlist(s)
	}
	// Committed row offsets are non-negative. Keep deletion state here so
	// every int64 value, including math.MinInt64, remains a valid key.
	x.off[s] = deleted
	x.free = append(x.free, s)
	x.live--
}

func (x *VecIndex) unlist(s int32) {
	c := x.slotList[s]
	if c < 0 {
		return
	}
	l := x.lists[c]
	for i, v := range l {
		if v == s {
			l[i] = l[len(l)-1]
			x.lists[c] = l[:len(l)-1]
			break
		}
	}
	x.slotList[s] = -1
}

// All distance kernels share one summation order, so a TopK ordering key and
// the L2_DISTANCE value printed for the same row are bit-identical:
// differences are taken in float64; element i of each 16-element block goes
// to accumulator i%8; elements after the last full block go to accumulator 0
// in order; the result is ((s0+s1)+(s2+s3))+((s4+s5)+(s6+s7)).

// widen converts a query vector to float64 once per query.
func widen(q []float32) []float64 {
	w := make([]float64, len(q))
	for i, f := range q {
		w[i] = float64(f)
	}
	return w
}

// l2sq is the squared Euclidean distance in float64.
func l2sq(a, b []float32) float64 { return l2sqBound(widen(a), b, math.Inf(1)) }

// l2sqBound computes the squared distance between the widened query a and
// the stored vector b, but stops after a 16-element block once the partial
// sum exceeds bound, returning a value > bound. Every term is non-negative,
// so the partial sum only grows and the early exit never drops a true
// candidate. When the full sum is computed it equals l2sq exactly.
func l2sqBound(a []float64, b []float32, bound float64) float64 {
	b = b[:len(a)]
	if useSIMD {
		// The vector kernel has no early exit; it is faster than the
		// scalar loop even when the scalar loop stops early.
		return l2sqSIMD(a, b)
	}
	var s0, s1, s2, s3, s4, s5, s6, s7 float64
	i := 0
	for ; i+16 <= len(a); i += 16 {
		x := (*[16]float64)(a[i:])
		y := (*[16]float32)(b[i:])
		d0 := x[0] - float64(y[0])
		d1 := x[1] - float64(y[1])
		d2 := x[2] - float64(y[2])
		d3 := x[3] - float64(y[3])
		d4 := x[4] - float64(y[4])
		d5 := x[5] - float64(y[5])
		d6 := x[6] - float64(y[6])
		d7 := x[7] - float64(y[7])
		s0 += d0 * d0
		s1 += d1 * d1
		s2 += d2 * d2
		s3 += d3 * d3
		s4 += d4 * d4
		s5 += d5 * d5
		s6 += d6 * d6
		s7 += d7 * d7
		d0 = x[8] - float64(y[8])
		d1 = x[9] - float64(y[9])
		d2 = x[10] - float64(y[10])
		d3 = x[11] - float64(y[11])
		d4 = x[12] - float64(y[12])
		d5 = x[13] - float64(y[13])
		d6 = x[14] - float64(y[14])
		d7 = x[15] - float64(y[15])
		s0 += d0 * d0
		s1 += d1 * d1
		s2 += d2 * d2
		s3 += d3 * d3
		s4 += d4 * d4
		s5 += d5 * d5
		s6 += d6 * d6
		s7 += d7 * d7
		if p := ((s0 + s1) + (s2 + s3)) + ((s4 + s5) + (s6 + s7)); p > bound {
			return p
		}
	}
	for ; i < len(a); i++ {
		d := a[i] - float64(b[i])
		s0 += d * d
	}
	return ((s0 + s1) + (s2 + s3)) + ((s4 + s5) + (s6 + s7))
}

func (x *VecIndex) nearestCentroid(v []float32) int {
	best, bd := 0, math.Inf(1)
	for c := 0; c < x.nlist; c++ {
		if d := l2sq(v, x.cents[c*x.dim:(c+1)*x.dim]); d < bd {
			best, bd = c, d
		}
	}
	return best
}

// nearestCentroidBytes returns the index of the centroid closest to the
// vector stored at the given log offset.
func (x *VecIndex) nearestCentroidBytes(db *DB, t *Table, off int64) int {
	return x.nearestCentroid(x.decode(db, t, off))
}

// trainIVF runs a few k-means iterations on a sample of live vectors.
func (x *VecIndex) trainIVF(db *DB, t *Table) {
	x.resetIVF()
	if x.live < 2048 {
		return
	}
	nlist := int(math.Sqrt(float64(x.live)))
	if nlist > 4096 {
		nlist = 4096
	}
	// Include training scratch as well as the retained IVF arrays. If the
	// optional partition does not fit, ANN can still use the exact scan.
	need := int64(nlist)*int64(x.dim)*12 + int64(nlist)*40 +
		int64(len(x.pks))*32 + int64(x.dim)*4
	if need > db.indexBudget(x)-x.derivedBytes() {
		return
	}
	slots := make([]int32, 0, x.live)
	for s := range x.pks {
		if x.off[s] != deleted {
			slots = append(slots, int32(s))
		}
	}
	rng := rand.New(rand.NewSource(42))
	sample := slots
	if len(sample) > nlist*64 {
		sample = make([]int32, nlist*64)
		for i, j := range rng.Perm(len(slots))[:len(sample)] {
			sample[i] = slots[j]
		}
	}
	d := x.dim
	cents := make([]float32, nlist*d)
	for c, j := range rng.Perm(len(sample))[:nlist] {
		copy(cents[c*d:], x.decode(db, t, x.off[sample[j]]))
	}
	x.cents, x.nlist = cents, nlist
	assign := make([]int, len(sample))
	sums := make([]float64, nlist*d)
	counts := make([]int, nlist)
	scratch := make([]float32, d)
	for iter := 0; iter < 8; iter++ {
		for i, s := range sample {
			assign[i] = x.nearestCentroidBytes(db, t, x.off[s])
		}
		for i := range sums {
			sums[i] = 0
		}
		for i := range counts {
			counts[i] = 0
		}
		for i, s := range sample {
			c := assign[i]
			counts[c]++
			x.decodeInto(scratch, db, t, x.off[s])
			for k, f := range scratch {
				sums[c*d+k] += float64(f)
			}
		}
		for c := 0; c < nlist; c++ {
			if counts[c] == 0 {
				copy(cents[c*d:], x.decode(db, t, x.off[sample[rng.Intn(len(sample))]]))
				continue
			}
			for k := 0; k < d; k++ {
				cents[c*d+k] = float32(sums[c*d+k] / float64(counts[c]))
			}
		}
	}
	x.lists = make([][]int32, nlist)
	x.slotList = make([]int32, len(x.pks))
	for i := range x.slotList {
		x.slotList[i] = -1
	}
	for _, s := range slots {
		c := x.nearestCentroidBytes(db, t, x.off[s])
		x.lists[c] = append(x.lists[c], s)
		x.slotList[s] = int32(c)
	}
	x.ivfBuilt = x.live
	if x.derivedBytes() > db.indexBudget(x) {
		x.resetIVF()
	}
}

// nprobe is deliberately generous: recall must stay >= 0.95 even on
// unclustered data, where IVF partitions are least effective.
func (x *VecIndex) nprobe() int {
	n := x.nlist / 2
	if n < 16 {
		n = 16
	}
	if n > x.nlist {
		n = x.nlist
	}
	return n
}

// probeOrder returns list ids sorted by centroid distance to q.
func (x *VecIndex) probeOrder(q []float32) []int {
	type cd struct {
		c int
		d float64
	}
	ds := make([]cd, x.nlist)
	for c := 0; c < x.nlist; c++ {
		ds[c] = cd{c, l2sq(q, x.cents[c*x.dim:(c+1)*x.dim])}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].d < ds[j].d })
	out := make([]int, x.nlist)
	for i, e := range ds {
		out[i] = e.c
	}
	return out
}
