package engine

import (
	"container/heap"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
)

// fusedBatches counts the batches the fused single-pass scan kernel has
// run (diagnostic for tests: it proves the fused branch was taken).
var fusedBatches atomic.Int64
var fusedCandidateBatches atomic.Int64

// dbgScanGatherRows / dbgScanExactRows / dbgPrefilterRuns are diagnostics
// for the multi-pass scan (worker h-b): rows gathered into batches, rows
// that survive the prefilter to the exact kernel, and batches where the
// prefilter ran. Read/reset only from tests.
var dbgScanGatherRows, dbgScanExactRows, dbgPrefilterRuns atomic.Int64

const ivfMinRows = 200000

// parallelMinSlots is the minimum scan work for multiple goroutines. Full
// distance rows count once; the cheaper fused path scales by code width
// below. A variable so tests can force either path.
var parallelMinSlots = 4096

// scanBatch is the number of rows whose distances one kernel call computes.
const scanBatch = 128

// batchScan enables the batched exact scan; a variable so tests can compare
// it with the one-row path.
var batchScan = true

type hit struct {
	d   float64
	pk  int64
	off int64
}

// worse reports whether a ranks after b (distance, then primary key).
func worse(a, b hit) bool {
	if a.d != b.d {
		return a.d > b.d
	}
	return a.pk > b.pk
}

type hitHeap []hit // max-heap: the worst hit on top

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return worse(h[i], h[j]) }
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(x any)        { *h = append(*h, x.(hit)) }
func (h *hitHeap) Pop() any {
	o := *h
	x := o[len(o)-1]
	*h = o[:len(o)-1]
	return x
}

type topK struct {
	k    int
	h    hitHeap
	pred func([]byte) bool
	db   *DB
}

// bound is the distance a candidate must not exceed to possibly enter.
func (t *topK) bound() float64 {
	if len(t.h) < t.k {
		return math.Inf(1)
	}
	return t.h[0].d
}

// offer adds a candidate. Filtering must happen before heap admission: a row
// that does not satisfy WHERE must not consume one of the TopK slots or make
// a valid filtered row look too far away to enter.
func (t *topK) offer(d float64, pk, off int64) {
	if t.pred != nil && !t.pred(t.db.rowBytes(off)) {
		return
	}
	t.admit(d, pk, off)
}

// admit adds a candidate that is already known to satisfy WHERE.
func (t *topK) admit(d float64, pk, off int64) {
	c := hit{d, pk, off}
	full := len(t.h) >= t.k
	if full && !worse(t.h[0], c) {
		return
	}
	if full {
		t.h[0] = c
		heap.Fix(&t.h, 0)
	} else {
		heap.Push(&t.h, c)
	}
}

func (t *topK) result() ([]int64, []float64) {
	hs := []hit(t.h)
	sort.Slice(hs, func(i, j int) bool { return worse(hs[j], hs[i]) })
	out := make([]int64, len(hs))
	dists := make([]float64, len(hs))
	for i, h := range hs {
		out[i] = h.off
		dists[i] = h.d
	}
	return out, dists
}

// vectorTopK answers ORDER BY L2_DISTANCE(col, q) LIMIT k from the vector
// index. It returns false when no index can be used, in which case the
// caller falls back to a generic sorted scan with identical semantics.
// Alongside the offsets it returns the squared L2 distances (hit.d), in
// the same order: the caller reuses them for the L2_DISTANCE output
// column instead of recomputing the distance per result row. The values
// are bit-identical to what the output column's compiled function would
// compute — all distance kernels share one summation order (see
// vecindex.go) — so the printed distances do not change, only the
// recomputation is skipped.
// cand, when non-nil, lists the only slots that can satisfy WHERE (from an
// equality candidate set); pred is still evaluated on each of them.
func (db *DB) vectorTopK(t *Table, q []float32, pred func([]byte) bool, k int, approx bool, eq *eqFilterSpec) ([]int64, []float64, bool) {
	if t.Index == nil {
		// Without CREATE VECTOR INDEX, keep an implicit in-memory copy so
		// repeated TopK queries do not re-read rows from the log.
		t.Index = newVecIndex("")
		t.Index.rebuild(db, t)
	}
	x := t.Index
	if x.Disabled {
		return nil, nil, false
	}
	ann := approx || db.opts.SearchMode == "ann"
	// IVF is only worth its recall risk on large tables. Measured locally on
	// uniform random 128-dim data, nprobe=nlist/4 gave Recall@10 of 0.61 at
	// 10k rows while saving only ~30% query time, so below ivfMinRows the
	// "ann" mode answers with the exact scan.
	ann = ann && x.live >= ivfMinRows
	if ann && x.Explicit && x.live >= 2*x.ivfBuilt {
		x.trainIVF(db, t) // first use, or the table has doubled since training
	}
	qw := widen(q)
	// Per-query constants for the int8 prefilter. The query is quantized
	// once per query (quantizeQuery) for the integer dot kernel; qSum and
	// qNorm2 are the element sum and squared norm of the DEQUANTIZED
	// query, used to expand the approximate squared distance from the
	// kernel's integer dot product. A non-finite query cannot be
	// quantized, so the prefilter is off for this query (the exact scan
	// still answers it). With prefilter6Bit the query is quantized to
	// 6-bit codes instead (quantizeQuery6 + VPMADDUBSW kernel); qscale is
	// then the 6-bit scale and the threshold uses maxScale+qscale.
	var qmin, qscale float32
	var qSum, qNorm2 float64
	var qok bool
	var qc []int16
	var qc6 []int8
	if prefilterEnabled && x.codes != nil && t.Dim <= maxInt8Dim {
		if prefilter6Bit {
			qmin, qscale, qc6, qSum, qNorm2, qok = quantizeQuery6(q)
		} else {
			qmin, qscale, qc, qSum, qNorm2, qok = quantizeQuery(q)
		}
	}
	var ov map[int64]int64
	if db.txn != nil {
		ov = db.txn.overlay[t.ID]
	}
	// When the candidate set comes from the only WHERE condition, every
	// candidate already satisfies WHERE and the per-row check is skipped.
	rowPred := pred
	if eq != nil && eq.covers {
		rowPred = nil
	}
	// The scan reads the vectors straight out of the log. Push the buffered
	// bytes to the kernel first, so the whole table is in the read-only
	// mapping, then take that mapping once: every row is a slice of it and
	// the scan needs no call into the log at all.
	if err := db.log.Flush(); err != nil {
		panic(err)
	}
	db.log.Prepare()
	base := db.log.Map()
	vecLen := int64(4 * t.Dim)
	vecOff := int64(t.vecOff)
	visit := func(tk *topK, s int32) {
		pk := x.pks[s]
		if x.off[s] == deleted {
			return
		}
		if ov != nil {
			if _, shadowed := ov[pk]; shadowed {
				return
			}
		}
		// Evaluate WHERE before the distance: a filtered-out row costs one
		// row lookup instead of a full distance computation. The row bytes
		// are read once and used for both.
		off := x.off[s]
		if rowPred != nil && !rowPred(db.rowBytes(off)) {
			return
		}
		at := off + vecOff
		var vb []byte
		if base != nil {
			vb = base[at : at+vecLen]
		} else {
			vb = db.log.View(at, int(vecLen))
		}
		bd := tk.bound()
		d := l2sqBytesBound(qw, vb, bd)
		if d > bd {
			return
		}
		tk.admit(d, pk, off)
	}
	// ANN only applies to an explicit, trained index. Filtered queries and
	// queries inside a transaction with pending changes stay exact.
	useIVF := ann && x.nlist > 0 && pred == nil &&
		len(ov) == 0 && k <= x.live/16
	var cand []int32
	useCand := false
	if eq != nil && !useIVF {
		cand, useCand = x.eqSlots(db, t, eq.col, eq.val)
	}
	n := len(x.pks)
	if useCand {
		n = len(cand)
	} else {
		rowPred = pred
	}
	// A covering equality filter can run the fused scan over the value's
	// compacted category block (sequential rows) instead of the multi-pass
	// scan over scattered candidate slots. The block holds exactly the
	// candidate rows, in the same order, so n and the multi-pass fallback
	// are unchanged when the block path is unavailable.
	var blk *catBlock
	// Small candidate sets already fit in cache. Compacting another copy
	// there adds to peak memory with little query benefit. Also avoid
	// building copies for modes that cannot use the fused block kernel.
	if useCand && eq != nil && eq.covers && ov == nil &&
		prefilterEnabled && useSIMD && !prefilter6Bit && n*t.Dim >= 256<<10 &&
		(!fusedCandidatesEnabled || useVNNI) {
		blk = x.catBlockFor(db, t, eq.col, eq.val)
	}
	if blk != nil {
		n = blk.n
	}
	tk := &topK{k: k, pred: pred, db: db, h: make(hitHeap, 0, k+1)}
	// scan visits slots lo..hi-1 in order. With the SIMD kernel and the log
	// mapping it gathers the rows that pass the cheap checks into batches
	// and computes their distances with one kernel call per batch; the
	// distances are bit-identical to the one-row kernel and are admitted in
	// slot order against the same bound, so the result equals the per-row
	// visit exactly.
	gather := batchScan && useSIMD && base != nil && t.Dim >= 16
	// prefilterOK gates the int8 prefilter. The length checks are the
	// memory-safety net for the kernel: every live slot must own dim code
	// bytes and one parameter set, so codes[slot*dim:slot*dim+dim] is in
	// range for every slot the scan can visit.
	prefilterOK := gather && prefilterEnabled && qok && t.Dim <= maxInt8Dim && x.codes != nil &&
		len(x.codes) >= len(x.pks)*t.Dim && len(x.qf) >= len(x.pks)*4 &&
		len(x.qbounds) >= (len(x.pks)+quantBlockRows-1)/quantBlockRows
	// fusedOK gates the fused single-pass scan kernel (fusedScanInt8),
	// which folds the gather loop, the coffs multiply, the integer dot
	// kernel, the dequantization and the threshold loop into one pass over
	// sequential slots. It needs everything the multi-pass scan needs,
	// plus: AVX2 (the kernel is asm), no candidate set (slots must be
	// sequential), no transaction overlay and no WHERE (the kernel cannot
	// evaluate either), no tombstones (live == len(pks) means the free
	// list is empty, so no slot holds a tombstone — the kernel takes pks
	// at face value), and the folded constants for every slot. The 6-bit
	// query-quantization experiment is also excluded (the fused kernel
	// expands an 8-bit dot; with the ~4x larger 6-bit qscale the
	// prefilter would be neutered and the experiment silently bypassed).
	// Any query that fails a gate runs the multi-pass scan unchanged.
	fusedOK := prefilterOK && useSIMD && !useCand && ov == nil && rowPred == nil &&
		x.live == len(x.pks) && len(x.qf) >= len(x.pks)*4 && !prefilter6Bit
	// fusedBlockOK gates the same fused kernel over a compacted category
	// block (fusedScanInt8Block). It needs everything the multi-pass scan
	// needs, plus: a block for this value, no transaction overlay (overlay
	// rows are not in the block), and no WHERE beyond the covering equality
	// (rowPred == nil; the kernel cannot evaluate a predicate). Unlike
	// fusedOK it does not need sequential index slots or live == len(pks):
	// the block is built from live slots only and is sequential by
	// construction. Any query that fails a gate runs the multi-pass scan
	// over the candidate set unchanged.
	fusedBlockOK := blk != nil && prefilterOK && useSIMD && ov == nil && rowPred == nil &&
		len(blk.qf) >= blk.n*4 && len(blk.codes) >= blk.n*t.Dim && !prefilter6Bit &&
		len(blk.qbounds) >= (blk.n+quantBlockRows-1)/quantBlockRows
	fusedCandidateOK := fusedCandidatesEnabled && prefilterOK && useCand &&
		blk == nil && ov == nil && rowPred == nil && !prefilter6Bit &&
		len(x.qf) >= len(x.pks)*4
	// The fused integer scan does much less work per row. Estimate its
	// parallel work in 512-byte units, keeping small scans on the caller
	// while retaining parallelism for larger row counts or dimensions.
	scanWork := n
	if fusedOK || fusedBlockOK || fusedCandidateOK {
		scanWork = n * t.Dim / 512
	}
	scan := func(tk *topK, lo, hi int) {
		if !gather {
			for i := lo; i < hi; i++ {
				if useCand {
					visit(tk, cand[i])
				} else {
					visit(tk, int32(i))
				}
			}
			return
		}
		// With AVX512_VNNI the VPDPBUSD kernel runs instead of the
		// AVX2 one, in both the unfiltered fused branch and the
		// block branch below: the biased query codes (qc8) and the
		// bias-absorbing qmin (qminAdj) are built once per query
		// here, not per batch. See fusedScanInt8VNNI.
		var qc8 []int8
		var qminAdj float64
		// Built only when a fused branch will actually use them; the
		// multi-pass path below never touches qc8/qminAdj.
		if useVNNI && (fusedBlockOK || fusedOK) {
			qc8 = make([]int8, len(qc))
			for i, c := range qc {
				qc8[i] = int8(c - 128)
			}
			qminAdj = float64(qmin) + 128*float64(qscale)
		}
		if fusedBlockOK {
			// Fused scan over the compacted category block: same
			// single-pass kernel as the unfiltered fused branch, but
			// over the block's sequential rows (baseSlot is the block
			// offset). The threshold is taken once, before the batch,
			// from the current TopK bound, exactly as in the fused
			// branch; survivors get exact distances in block order.
			var surv [2 * scanBatch]int64
			var soff, spk [scanBatch]int64
			var out [scanBatch]float64
			for i := lo; i < hi; {
				n := hi - i
				if n > scanBatch {
					n = scanBatch
				}
				n = min(n, quantBlockRows-i%quantBlockRows)
				bd := tk.bound()
				thresh2 := blk.qbounds[i/quantBlockRows].threshold(qmin, qscale, t.Dim, bd)
				var m int
				if useVNNI {
					m = fusedScanInt8BlockVNNI(qc8, blk.codes, blk.pks, blk.off, blk.qf, i, n, qSum, qNorm2, qminAdj, float64(qscale), thresh2, surv[:])
				} else {
					m = fusedScanInt8Block(qc, blk.codes, blk.pks, blk.off, blk.qf, i, n, qSum, qNorm2, qmin, qscale, thresh2, surv[:])
				}
				fusedBlockBatches.Add(1)
				for j := 0; j < m; j++ {
					soff[j] = surv[2*j] + vecOff
					spk[j] = surv[2*j+1]
				}
				l2sqGatherBytes(qw, base, soff[:m], out[:m])
				for j := 0; j < m; j++ {
					if d := out[j]; d <= tk.bound() {
						tk.admit(d, spk[j], surv[2*j])
					}
				}
				i += n
			}
			return
		}
		if fusedOK || fusedCandidateOK {
			// Fused single-pass scan: one kernel call per batch returns
			// the survivors' (row offset, primary key) pairs directly;
			// the exact kernel runs on the survivors and admits them in
			// slot order, exactly as the multi-pass scan does. The
			// threshold is taken once, before the batch, from the
			// current TopK bound; it can only shrink afterwards, so a
			// row dropped here stays dropped. When the heap is not full
			// yet the bound is +Inf, the threshold is +Inf, and the
			// kernel keeps every row (the dot work for that first batch
			// is the only cost of fusing the prefilter in).
			//
			// qc8/qminAdj (VNNI) are built once per query above and
			// shared with the block branch.
			var surv [2 * scanBatch]int64
			var soff, spk [scanBatch]int64
			var out [scanBatch]float64
			for i := lo; i < hi; {
				n := hi - i
				if n > scanBatch {
					n = scanBatch
				}
				var bound quantBlockBound
				if fusedCandidateOK {
					bound = x.boundForSlots(cand[i : i+n])
				} else {
					// Worker partitions can start inside a block. Do not
					// apply its bound to rows from the following block.
					n = min(n, quantBlockRows-i%quantBlockRows)
					bound = x.qbounds[i/quantBlockRows]
				}
				bd := tk.bound()
				thresh2 := bound.threshold(qmin, qscale, t.Dim, bd)
				var m int
				if fusedCandidateOK {
					m = fusedScanCandidates(qc, x, cand[i:i+n], qSum, qNorm2, qmin, qscale, thresh2, surv[:])
					fusedCandidateBatches.Add(1)
				} else if useVNNI {
					m = fusedScanVNNI(qc8, x, int32(i), n, qSum, qNorm2, qminAdj, float64(qscale), thresh2, surv[:])
				} else {
					m = fusedScanInt8(qc, x, int32(i), n, qSum, qNorm2, qmin, qscale, thresh2, surv[:])
				}
				if fusedOK {
					fusedBatches.Add(1)
				}
				for j := 0; j < m; j++ {
					soff[j] = surv[2*j] + vecOff
					spk[j] = surv[2*j+1]
				}
				l2sqGatherBytes(qw, base, soff[:m], out[:m])
				for j := 0; j < m; j++ {
					if d := out[j]; d <= tk.bound() {
						tk.admit(d, spk[j], surv[2*j])
					}
				}
				i += n
			}
			return
		}
		var offs, pks, rows [scanBatch]int64
		var slots [scanBatch]int32
		var coffs [scanBatch]int64
		var approxInt [scanBatch]int32
		var out [scanBatch]float64
		for i := lo; i < hi; {
			n := 0
			for ; i < hi && n < scanBatch; i++ {
				// Inline the slot mapping: a branch on the
				// loop-invariant useCand instead of a closure call
				// per row.
				var s int32
				if useCand {
					s = cand[i]
				} else {
					s = int32(i)
				}
				pk := x.pks[s]
				if x.off[s] == deleted {
					continue
				}
				if ov != nil {
					if _, shadowed := ov[pk]; shadowed {
						continue
					}
				}
				off := x.off[s]
				if rowPred != nil && !rowPred(db.rowBytes(off)) {
					continue
				}
				at := off + vecOff
				_ = base[at : at+vecLen] // same bounds check as the one-row path
				offs[n], pks[n], rows[n] = at, pk, off
				slots[n] = s
				n++
			}
			m := n
			dbgScanGatherRows.Add(int64(n))
			if prefilterOK && len(tk.h) >= tk.k {
				dbgPrefilterRuns.Add(1)
				// Int8 prefilter: approximate the squared distance of
				// every row in the batch from its codes, and drop the
				// rows the error bound proves cannot enter TopK. The
				// integer dot kernel returns exact int32 dot products
				// of the quantized query against the codes; each is
				// dequantized (dot = qmin*sumC + qscale*intDot) and
				// expanded to an approximate squared distance. The
				// per-row bound is replaced by a single threshold per
				// batch (prefilterThreshold): one square root per batch
				// instead of one per row, and no per-row bound
				// arithmetic. The threshold takes the combined scale
				// (maxScale + qscale): the per-element error is
				// (scale+qscale)/2, row quantization plus query
				// quantization. It is exactly as safe as the per-row
				// test (see prefilterThreshold's proof): it can only
				// admit extra rows, never drop a true candidate.
				// The bound is taken once, before any admission from
				// this batch; it can only shrink afterwards, so a row
				// dropped here stays dropped. Survivors are compacted
				// in place (slot order preserved) and get exact
				// distances below.
				for j := 0; j < n; j++ {
					coffs[j] = int64(slots[j]) * int64(t.Dim)
				}
				if prefilter6Bit {
					dotInt8GatherInt6(qc6, x.codes, coffs[:n], approxInt[:n])
				} else {
					dotInt8GatherInt(qc, x.codes, coffs[:n], approxInt[:n])
				}
				bd := tk.bound()
				thresh2 := x.boundForSlots(slots[:n]).threshold(qmin, qscale, t.Dim, bd)
				m = 0
				for j := 0; j < n; j++ {
					f := x.qf[int(slots[j])*4 : int(slots[j])*4+4]
					if foldDist2(qSum, qNorm2, qmin, qscale, approxInt[j], f[0], f[1], f[2], f[3]) > thresh2 {
						continue
					}
					offs[m], pks[m], rows[m] = offs[j], pks[j], rows[j]
					m++
				}
			}
			dbgScanExactRows.Add(int64(m))
			l2sqGatherBytes(qw, base, offs[:m], out[:m])
			for j := 0; j < m; j++ {
				if d := out[j]; d <= tk.bound() {
					tk.admit(d, pks[j], rows[j])
				}
			}
		}
	}
	if useIVF {
		for _, c := range x.probeOrder(q)[:x.nprobe()] {
			for _, s := range x.lists[c] {
				visit(tk, s)
			}
		}
	} else if workers := scanWorkers(scanWork); workers > 1 {
		parts := make([]*topK, workers)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			lo, hi := n*w/workers, n*(w+1)/workers
			p := &topK{k: k, pred: pred, db: db, h: make(hitHeap, 0, k+1)}
			parts[w] = p
			wg.Add(1)
			go func() {
				defer wg.Done()
				scan(p, lo, hi)
			}()
		}
		wg.Wait()
		// Merging the per-part heaps with the same (distance, pk) order
		// yields exactly the serial TopK.
		for _, p := range parts {
			for _, h := range p.h {
				tk.admit(h.d, h.pk, h.off)
			}
		}
	} else {
		scan(tk, 0, n)
	}
	for pk, off := range ov {
		if off != deleted {
			tk.offer(l2sqBytes(q, db.rowBytes(off)[t.vecOff:]), pk, off)
		}
	}
	offs, dists := tk.result()
	return offs, dists, true
}

// eqFilterSpec is a WHERE conjunct "varchar_column = 'constant'".
type eqFilterSpec struct {
	col    int
	val    string
	covers bool // the equality is the whole WHERE
}

// maxScanWorkers caps the goroutines used by one exact scan. 0 means
// "use GOMAXPROCS"; MVDB_SCAN_WORKERS overrides it for experiments.
var maxScanWorkers = envWorkers()

func envWorkers() int {
	if v, err := strconv.Atoi(os.Getenv("MVDB_SCAN_WORKERS")); err == nil && v > 0 {
		return v
	}
	return 2
}

func scanWorkers(slots int) int {
	if slots < parallelMinSlots {
		return 1
	}
	w := runtime.GOMAXPROCS(0)
	if maxScanWorkers > 0 && w > maxScanWorkers {
		w = maxScanWorkers
	}
	return w
}
