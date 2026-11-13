package engine

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"minivectordb/internal/storage"
	"os"
	"path/filepath"
	"runtime/pprof"
)

var decompTOnce sync.Once

// decompSetupT is decompSetup for *testing.T (same data, seed 7, dim 256).
func decompSetupT(t *testing.T) {
	t.Helper()
	decompTOnce.Do(func() {
		if decompDB != nil {
			return // already built by decompSetup (benchmark path)
		}
		db, err := Open(t.TempDir(), Options{Sync: storage.SyncNone})
		if err != nil {
			t.Fatal(err)
		}
		decompDB = db
		if _, err := db.Exec("CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), embedding VECTOR(256))"); err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(7))
		var sb strings.Builder
		for s := 0; s < 100000; s += 1000 {
			sb.Reset()
			sb.WriteString("INSERT INTO docs VALUES ")
			for i := s; i < s+1000; i++ {
				if i > s {
					sb.WriteByte(',')
				}
				fmt.Fprintf(&sb, "(%d,'c%d','[", i+1, rng.Intn(10))
				for j := 0; j < 256; j++ {
					if j > 0 {
						sb.WriteByte(',')
					}
					fmt.Fprintf(&sb, "%v", float64(float32(rng.Float64())))
				}
				sb.WriteString("]')")
			}
			if _, err := db.Exec(sb.String()); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec("CREATE VECTOR INDEX vi ON docs(embedding)"); err != nil {
			t.Fatal(err)
		}
		qrng := rand.New(rand.NewSource(11))
		for i := 0; i < 10; i++ {
			q := make([]float32, 256)
			for j := range q {
				q[j] = qrng.Float32()
			}
			decompQueries = append(decompQueries, q)
		}
	})
}

// TestFilteredScanDirect times vectorTopK directly (no SQL parse, no
// result formatting) for filtered vs unfiltered, and checks the direct
// filtered call returns the same row count as the SQL filtered query.
func TestFilteredScanDirect(t *testing.T) {
	decompSetupT(t)
	prefilterEnabled = true
	tbl := decompDB.tables["docs"]
	if tbl == nil {
		t.Fatal("docs table not found")
	}
	catCol := -1
	for i, c := range tbl.Cols {
		if c.Name == "category" {
			catCol = i
		}
	}
	if catCol < 0 {
		t.Fatal("category column not found")
	}
	eq := &eqFilterSpec{col: catCol, val: "c3", covers: true}
	sqlQ := "SELECT id, L2_DISTANCE(embedding, '" + vectorText(decompQueries[0]) + "') AS distance FROM docs WHERE category = 'c3' ORDER BY distance LIMIT 10"
	res, err := decompDB.Exec(sqlQ)
	if err != nil {
		t.Fatal(err)
	}
	offs, _, ok := decompDB.vectorTopK(tbl, decompQueries[0], nil, 10, false, eq)
	if !ok {
		t.Fatal("vectorTopK refused")
	}
	if len(offs) != len(res.Rows) {
		t.Fatalf("row count mismatch: direct %d vs sql %d", len(offs), len(res.Rows))
	}
	t.Logf("direct filtered returns %d rows, sql filtered returns %d rows", len(offs), len(res.Rows))

	const n = 200
	var filt, unfilt []time.Duration
	for i := 0; i < n; i++ {
		q := decompQueries[i%len(decompQueries)]
		cat := fmt.Sprintf("c%d", i%10)
		eqi := &eqFilterSpec{col: catCol, val: cat, covers: true}
		t0 := time.Now()
		if _, _, ok := decompDB.vectorTopK(tbl, q, nil, 10, false, eqi); !ok {
			t.Fatal("filtered vectorTopK refused")
		}
		filt = append(filt, time.Since(t0))
		t0 = time.Now()
		if _, _, ok := decompDB.vectorTopK(tbl, q, nil, 10, false, nil); !ok {
			t.Fatal("unfiltered vectorTopK refused")
		}
		unfilt = append(unfilt, time.Since(t0))
	}
	stats := func(ds []time.Duration) (med, mean, p90 time.Duration) {
		s := append([]time.Duration(nil), ds...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		med = s[len(s)/2]
		p90 = s[int(float64(len(s))*0.9)]
		var tot time.Duration
		for _, d := range s {
			tot += d
		}
		return med, tot / time.Duration(len(s)), p90
	}
	fm, fa, fp := stats(filt)
	um, ua, up := stats(unfilt)
	t.Logf("DIRECT vectorTopK filtered   (n=%d): median %v mean %v p90 %v", n, fm, fa, fp)
	t.Logf("DIRECT vectorTopK unfiltered (n=%d): median %v mean %v p90 %v", n, um, ua, up)
	t.Logf("DIRECT ratio filtered/unfiltered median: %.3f", float64(fm)/float64(um))
	t.Logf("DIRECT projected bench total 500+500: %.3f s", float64(fa+ua)*500/float64(time.Second))
}

// Filtered-vs-unfiltered split timing (worker h-b): measures per-query wall
// time for the two halves of the bench workload (100k rows / dim 256,
// K=10, exact mode) through the same SQL path the bench uses, alternating
// so both halves see the same machine conditions.
func TestFilteredVsUnfilteredSplit(t *testing.T) {
	decompSetupT(t)
	prefilterEnabled = true

	sqlFor := func(q []float32, cat string) string {
		where := ""
		if cat != "" {
			where = " WHERE category = '" + cat + "'"
		}
		return "SELECT id, L2_DISTANCE(embedding, '" + vectorText(q) + "') AS distance FROM docs" + where + " ORDER BY distance LIMIT 10"
	}

	// Warm up the unfiltered path (also warms codes / mappings).
	for i := 0; i < 5; i++ {
		if _, err := decompDB.Exec(sqlFor(decompQueries[i%len(decompQueries)], "")); err != nil {
			t.Fatal(err)
		}
	}
	// First filtered query: includes the one-time eqSet build (lazy, per gen).
	t0 := time.Now()
	if _, err := decompDB.Exec(sqlFor(decompQueries[0], "c3")); err != nil {
		t.Fatal(err)
	}
	firstFiltered := time.Since(t0)

	const n = 100
	var filt, unfilt []time.Duration
	for i := 0; i < n; i++ {
		q := decompQueries[i%len(decompQueries)]
		cat := fmt.Sprintf("c%d", i%10)
		t0 = time.Now()
		if _, err := decompDB.Exec(sqlFor(q, cat)); err != nil {
			t.Fatal(err)
		}
		filt = append(filt, time.Since(t0))
		t0 = time.Now()
		if _, err := decompDB.Exec(sqlFor(q, "")); err != nil {
			t.Fatal(err)
		}
		unfilt = append(unfilt, time.Since(t0))
	}
	stats := func(ds []time.Duration) (med, mean, p90 time.Duration) {
		s := append([]time.Duration(nil), ds...)
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
		med = s[len(s)/2]
		p90 = s[int(float64(len(s))*0.9)]
		var tot time.Duration
		for _, d := range s {
			tot += d
		}
		return med, tot / time.Duration(len(s)), p90
	}
	fm, fa, fp := stats(filt)
	um, ua, up := stats(unfilt)
	t.Logf("first filtered query (incl one-time eqSet build): %v", firstFiltered)
	t.Logf("filtered   (n=%d): median %v mean %v p90 %v", n, fm, fa, fp)
	t.Logf("unfiltered (n=%d): median %v mean %v p90 %v", n, um, ua, up)
	t.Logf("ratio filtered/unfiltered median: %.3f", float64(fm)/float64(um))
	t.Logf("projected bench total for 500+500: filtered %.3f s + unfiltered %.3f s = %.3f s",
		float64(fa)*500/float64(time.Second), float64(ua)*500/float64(time.Second),
		float64(fa+ua)*500/float64(time.Second))
}

// TestFilteredProfile runs many filtered vectorTopK calls under a CPU
// profile so the filtered path's hot spots can be attributed.
func TestFilteredProfile(t *testing.T) {
	decompSetupT(t)
	prefilterEnabled = true
	tbl := decompDB.tables["docs"]
	catCol := -1
	for i, c := range tbl.Cols {
		if c.Name == "category" {
			catCol = i
		}
	}
	f, err := os.Create(filepath.Join(t.TempDir(), "filtered.prof"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3000; i++ {
		q := decompQueries[i%len(decompQueries)]
		eqi := &eqFilterSpec{col: catCol, val: fmt.Sprintf("c%d", i%10), covers: true}
		if _, _, ok := decompDB.vectorTopK(tbl, q, nil, 10, false, eqi); !ok {
			t.Fatal("refused")
		}
	}
	pprof.StopCPUProfile()
	t.Logf("profile written (3000 filtered vectorTopK calls)")
}

// TestUnfilteredProfile is the unfiltered counterpart for comparison.
func TestUnfilteredProfile(t *testing.T) {
	decompSetupT(t)
	prefilterEnabled = true
	tbl := decompDB.tables["docs"]
	f, err := os.Create(filepath.Join(t.TempDir(), "unfiltered.prof"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3000; i++ {
		q := decompQueries[i%len(decompQueries)]
		if _, _, ok := decompDB.vectorTopK(tbl, q, nil, 10, false, nil); !ok {
			t.Fatal("refused")
		}
	}
	pprof.StopCPUProfile()
	t.Logf("profile written (3000 unfiltered vectorTopK calls)")
}

// TestFilteredSurvivors counts gathered vs exact-kernel rows per query for
// filtered vs unfiltered, to explain the exact kernel's profile share.
func TestFilteredSurvivors(t *testing.T) {
	decompSetupT(t)
	prefilterEnabled = true
	tbl := decompDB.tables["docs"]
	catCol := -1
	for i, c := range tbl.Cols {
		if c.Name == "category" {
			catCol = i
		}
	}
	run := func(eq *eqFilterSpec, n int) (gather, exact, runs int64) {
		dbgScanGatherRows.Store(0)
		dbgScanExactRows.Store(0)
		dbgPrefilterRuns.Store(0)
		for i := 0; i < n; i++ {
			q := decompQueries[i%len(decompQueries)]
			if _, _, ok := decompDB.vectorTopK(tbl, q, nil, 10, false, eq); !ok {
				t.Fatal("refused")
			}
		}
		return dbgScanGatherRows.Load(), dbgScanExactRows.Load(), dbgPrefilterRuns.Load()
	}
	const n = 100
	g, e, r := run(&eqFilterSpec{col: catCol, val: "c3", covers: true}, n)
	t.Logf("filtered: gathered %d/query, exact %d/query (%.1f%% survive), prefilter batches %d/query",
		g/n, e/n, 100*float64(e)/float64(g), r/n)
	g, e, r = run(nil, n)
	t.Logf("unfiltered (multi-pass only if fused off; fused normally takes this path): gathered %d/query, exact %d/query (%.1f%% survive), prefilter batches %d/query",
		g/n, e/n, 100*float64(e)/float64(g), r/n)
	t.Logf("fusedBatches total: %d", fusedBatches.Load())
}

// BenchmarkDotScatter10k vs BenchmarkDotSequential10k: the int8 dot kernel
// over 10k rows, codes reached via scattered offsets (every 10th slot, as
// a filtered candidate set does) vs sequential offsets (first 10k slots).
// Same row count and bytes per row; only the stride differs.
func BenchmarkDotSequential10k(b *testing.B) {
	_, _, _, q, codes, coffs, _ := decompKernelData()
	_, _, qc, _, _, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantize rejected")
	}
	coffs = coffs[:10000]
	out := make([]int32, len(coffs))
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(coffs); s += scanBatch {
			e := s + scanBatch
			if e > len(coffs) {
				e = len(coffs)
			}
			dotInt8GatherInt(qc, codes, coffs[s:e], out[s:e])
		}
	}
}

func BenchmarkDotScatter10k(b *testing.B) {
	_, _, _, q, codes, coffs, _ := decompKernelData()
	_, _, qc, _, _, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantize rejected")
	}
	scat := make([]int64, 10000)
	for i := range scat {
		scat[i] = coffs[i*10]
	}
	out := make([]int32, len(scat))
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(scat); s += scanBatch {
			e := s + scanBatch
			if e > len(scat) {
				e = len(scat)
			}
			dotInt8GatherInt(qc, codes, scat[s:e], out[s:e])
		}
	}
}

// BenchmarkExactScatter415 vs BenchmarkExactSequential415: the exact kernel
// over 415 survivor rows, scattered vs sequential offsets.
func BenchmarkExactSequential415(b *testing.B) {
	qw, base, offs, _, _, _, _ := decompKernelData()
	offs = offs[:415]
	out := make([]float64, len(offs))
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(offs); s += scanBatch {
			e := s + scanBatch
			if e > len(offs) {
				e = len(offs)
			}
			l2sqGatherBytes(qw, base, offs[s:e], out[s:e])
		}
	}
}

func BenchmarkExactScatter415(b *testing.B) {
	qw, base, offs, _, _, _, _ := decompKernelData()
	scat := make([]int64, 415)
	for i := range scat {
		scat[i] = offs[i*240]
	}
	out := make([]float64, len(scat))
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(scat); s += scanBatch {
			e := s + scanBatch
			if e > len(scat) {
				e = len(scat)
			}
			l2sqGatherBytes(qw, base, scat[s:e], out[s:e])
		}
	}
}

// TestFused10kSequential times the fused kernel over 10k sequential slots
// (the shape a per-category compacted block would give a filtered query),
// serial, with a realistic tight threshold and with +Inf.
func TestFused10kSequential(t *testing.T) {
	decompSetupT(t)
	tbl := decompDB.tables["docs"]
	x := tbl.Index
	q := decompQueries[0]
	qmin, qscale, qc, qSum, qNorm2, qok := quantizeQuery(q)
	if !qok {
		t.Fatal("quantize rejected")
	}
	// Realistic bound: run one unfiltered query and take its 10th distance.
	_, dists, ok := decompDB.vectorTopK(tbl, q, nil, 10, false, nil)
	if !ok || len(dists) != 10 {
		t.Fatal("no dists")
	}
	bd := dists[len(dists)-1]
	threshTight := prefilterThreshold(x.maxScale+qscale, tbl.Dim, bd)
	t.Logf("realistic bd=%.4f thresh2=%.4f", bd, threshTight)
	for _, tc := range []struct {
		name   string
		thresh float64
	}{
		{"tight", threshTight},
		{"inf", math.Inf(1)},
	} {
		surv := make([]int64, 2*10000)
		// Warm.
		m := fusedScanInt8(qc, x, 0, 10000, qSum, qNorm2, qmin, qscale, tc.thresh, surv)
		const n = 200
		var ds []time.Duration
		var tot int
		for i := 0; i < n; i++ {
			t0 := time.Now()
			m = fusedScanInt8(qc, x, 0, 10000, qSum, qNorm2, qmin, qscale, tc.thresh, surv)
			ds = append(ds, time.Since(t0))
			tot += m
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		t.Logf("fused 10k sequential %s: median %v mean %v survivors %d/query",
			tc.name, ds[len(ds)/2], time.Duration(int64(0)+sumDur(ds)/int64(len(ds))), tot/n)
	}
}

func sumDur(ds []time.Duration) int64 {
	var s int64
	for _, d := range ds {
		s += int64(d)
	}
	return s
}

// TestCatBlockBitIdentical runs the same covering filtered queries through
// the block path (catBlocksEnabled=true) and the candidate-set multi-pass
// path (false) and requires bit-identical offsets and distances.
func TestCatBlockBitIdentical(t *testing.T) {
	oldCandidates := fusedCandidatesEnabled
	fusedCandidatesEnabled = false
	defer func() { fusedCandidatesEnabled = oldCandidates }()
	decompSetupT(t)
	prefilterEnabled = true
	tbl := decompDB.tables["docs"]
	catCol := -1
	for i, c := range tbl.Cols {
		if c.Name == "category" {
			catCol = i
		}
	}
	old := catBlocksEnabled
	defer func() { catBlocksEnabled = old }()
	run := func() ([][]int64, [][]float64) {
		var offsList [][]int64
		var distsList [][]float64
		for i := 0; i < 20; i++ {
			q := decompQueries[i%len(decompQueries)]
			eqi := &eqFilterSpec{col: catCol, val: fmt.Sprintf("c%d", i%10), covers: true}
			offs, dists, ok := decompDB.vectorTopK(tbl, q, nil, 10, false, eqi)
			if !ok {
				t.Fatal("refused")
			}
			offsList = append(offsList, append([]int64(nil), offs...))
			distsList = append(distsList, append([]float64(nil), dists...))
		}
		return offsList, distsList
	}
	catBlocksEnabled = true
	bo, bd := run()
	if fusedBlockBatches.Load() == 0 {
		t.Fatal("block path never taken with catBlocksEnabled=true")
	}
	catBlocksEnabled = false
	co, cd := run()
	for i := range bo {
		if len(bo[i]) != len(co[i]) {
			t.Fatalf("query %d: block rows %d vs cand rows %d", i, len(bo[i]), len(co[i]))
		}
		for j := range bo[i] {
			if bo[i][j] != co[i][j] {
				t.Fatalf("query %d row %d: block off %d vs cand off %d", i, j, bo[i][j], co[i][j])
			}
			if bd[i][j] != cd[i][j] {
				t.Fatalf("query %d row %d: block dist %v vs cand dist %v", i, j, bd[i][j], cd[i][j])
			}
		}
	}
}

// TestCatBlockEmptyCategory checks a covering filter matching no rows.
func TestCatBlockEmptyCategory(t *testing.T) {
	decompSetupT(t)
	tbl := decompDB.tables["docs"]
	catCol := -1
	for i, c := range tbl.Cols {
		if c.Name == "category" {
			catCol = i
		}
	}
	eqi := &eqFilterSpec{col: catCol, val: "c99", covers: true}
	offs, dists, ok := decompDB.vectorTopK(tbl, decompQueries[0], nil, 10, false, eqi)
	if !ok {
		t.Fatal("refused")
	}
	if len(offs) != 0 || len(dists) != 0 {
		t.Fatalf("empty category returned %d rows", len(offs))
	}
}

// TestFusedBlockVsScalar checks the fused block kernel (asm) against the
// portable scalar reference: identical survivor sets across dimensions,
// row counts, base slots and thresholds.
func TestFusedBlockVsScalar(t *testing.T) {
	old := useSIMD
	useSIMD = true
	defer func() { useSIMD = old }()
	if !hasAVX2FMA() {
		t.Skip("no AVX2+FMA on this CPU; asm path cannot run")
	}
	rng := rand.New(rand.NewSource(31))
	for _, dim := range []int{16, 17, 31, 32, 33, 64, 100, 128, 255, 256, 257} {
		for _, rows := range []int{1, 3, 128, 129} {
			codes := make([]byte, rows*dim)
			qf := make([]float64, rows*4)
			pks := make([]int64, rows)
			offs := make([]int64, rows)
			for s := 0; s < rows; s++ {
				v := make([]float32, dim)
				for j := range v {
					v[j] = rng.Float32()
				}
				qp := quantizeRow(v, codes[s*dim:(s+1)*dim])
				qf[4*s], qf[4*s+1], qf[4*s+2], qf[4*s+3] = foldQparam(qp, dim)
				pks[s] = int64(1000 + s)
				offs[s] = int64(s * 4096)
			}
			q := make([]float32, dim)
			for j := range q {
				q[j] = rng.Float32()
			}
			qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
			if !ok {
				t.Fatal("quantizeQuery rejected a finite query")
			}
			for _, thresh := range []float64{5, 35, 1e6, math.Inf(1)} {
				survASM := make([]int64, 2*rows)
				survRef := make([]int64, 2*rows)
				nASM := fusedScanInt8Block(qc, codes, pks, offs, qf, 0, rows, qSum, qNorm2, qmin, qscale, thresh, survASM)
				nRef := fusedScanInt8BlockScalar(qc, codes, pks, offs, qf, 0, rows, qSum, qNorm2, qmin, qscale, thresh, survRef)
				if nASM != nRef {
					t.Fatalf("dim %d rows %d thresh %g: asm kept %d, scalar kept %d",
						dim, rows, thresh, nASM, nRef)
				}
				for i := 0; i < 2*nASM; i++ {
					if survASM[i] != survRef[i] {
						t.Fatalf("dim %d rows %d thresh %g: survivor %d differs: asm %d, scalar %d",
							dim, rows, thresh, i, survASM[i], survRef[i])
					}
				}
			}
			if rows > 3 {
				survASM := make([]int64, 2*(rows-3))
				survRef := make([]int64, 2*(rows-3))
				nASM := fusedScanInt8Block(qc, codes, pks, offs, qf, 3, rows-3, qSum, qNorm2, qmin, qscale, 35, survASM)
				nRef := fusedScanInt8BlockScalar(qc, codes, pks, offs, qf, 3, rows-3, qSum, qNorm2, qmin, qscale, 35, survRef)
				if nASM != nRef {
					t.Fatalf("dim %d rows %d baseSlot 3: asm kept %d, scalar kept %d",
						dim, rows, nASM, nRef)
				}
				for i := 0; i < 2*nASM; i++ {
					if survASM[i] != survRef[i] {
						t.Fatalf("dim %d rows %d baseSlot 3: survivor %d differs", dim, rows, i)
					}
				}
			}
		}
	}
}

// TestFusedBlockVNNIVsScalar is the VNNI counterpart of
// TestFusedBlockVsScalar: the VPDPBUSD block kernel (biased query codes
// + qminAdj) must make the same keep/drop decisions as the unbiased
// scalar reference, bit for bit. Skipped on CPUs without AVX512_VNNI.
func TestFusedBlockVNNIVsScalar(t *testing.T) {
	old := useSIMD
	useSIMD = true
	defer func() { useSIMD = old }()
	if !useVNNI {
		t.Skip("no AVX512_VNNI on this CPU; VNNI block path cannot run")
	}
	rng := rand.New(rand.NewSource(31))
	for _, dim := range []int{16, 17, 31, 32, 33, 64, 100, 128, 255, 256, 257} {
		for _, rows := range []int{1, 3, 128, 129} {
			codes := make([]byte, rows*dim)
			qf := make([]float64, rows*4)
			pks := make([]int64, rows)
			offs := make([]int64, rows)
			for s := 0; s < rows; s++ {
				v := make([]float32, dim)
				for j := range v {
					v[j] = rng.Float32()
				}
				qp := quantizeRow(v, codes[s*dim:(s+1)*dim])
				qf[4*s], qf[4*s+1], qf[4*s+2], qf[4*s+3] = foldQparam(qp, dim)
				pks[s] = int64(1000 + s)
				offs[s] = int64(s * 4096)
			}
			q := make([]float32, dim)
			for j := range q {
				q[j] = rng.Float32()
			}
			qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
			if !ok {
				t.Fatal("quantizeQuery rejected a finite query")
			}
			qc8 := make([]int8, len(qc))
			for i, c := range qc {
				qc8[i] = int8(c - 128)
			}
			qminAdj := float64(qmin) + 128*float64(qscale)
			for _, thresh := range []float64{5, 35, 1e6, math.Inf(1)} {
				survASM := make([]int64, 2*rows)
				survRef := make([]int64, 2*rows)
				nASM := fusedScanInt8BlockVNNI(qc8, codes, pks, offs, qf, 0, rows, qSum, qNorm2, qminAdj, float64(qscale), thresh, survASM)
				nRef := fusedScanInt8BlockScalar(qc, codes, pks, offs, qf, 0, rows, qSum, qNorm2, qmin, qscale, thresh, survRef)
				if nASM != nRef {
					t.Fatalf("dim %d rows %d thresh %g: VNNI kept %d, scalar kept %d",
						dim, rows, thresh, nASM, nRef)
				}
				for i := 0; i < 2*nASM; i++ {
					if survASM[i] != survRef[i] {
						t.Fatalf("dim %d rows %d thresh %g: survivor %d differs: VNNI %d, scalar %d",
							dim, rows, thresh, i, survASM[i], survRef[i])
					}
				}
			}
		}
	}
}

// TestCatBlockBuildCost measures the one-time per-value block build.
func TestCatBlockBuildCost(t *testing.T) {
	decompSetupT(t)
	tbl := decompDB.tables["docs"]
	x := tbl.Index
	catCol := -1
	for i, c := range tbl.Cols {
		if c.Name == "category" {
			catCol = i
		}
	}
	// Fresh index generation state is shared with other tests; force a
	// rebuild by dropping the cached blocks for this column.
	if x.cat != nil {
		delete(x.cat, catCol)
	}
	// eqSet build is timed separately (pre-existing cost, not this change).
	t0 := time.Now()
	if _, ok := x.eqSlots(decompDB, tbl, catCol, "c0"); !ok {
		t.Fatal("eqSlots refused")
	}
	eqBuild := time.Since(t0)
	t0 = time.Now()
	b := x.catBlockFor(decompDB, tbl, catCol, "c0")
	build := time.Since(t0)
	if b == nil || b.n == 0 {
		t.Fatal("no block built")
	}
	t.Logf("eqSet build (pre-existing): %v; catBlock build for %d rows: %v", eqBuild, b.n, build)
	// Second lookup must be cached (near-free).
	t0 = time.Now()
	b2 := x.catBlockFor(decompDB, tbl, catCol, "c0")
	if b2 != b {
		t.Fatal("cached block not returned")
	}
	t.Logf("cached catBlock lookup: %v", time.Since(t0))
}

// TestNonCoveringFilterFallsBack checks a filtered query whose WHERE is
// more than the single equality (covers=false) does NOT take the block
// path (the kernel cannot evaluate the extra predicate) and still returns
// the same rows as the covering query when the extra conjunct holds for
// every row.
func TestNonCoveringFilterFallsBack(t *testing.T) {
	decompSetupT(t)
	prefilterEnabled = true
	q := decompQueries[0]
	vt := vectorText(q)
	covering := "SELECT id, L2_DISTANCE(embedding, '" + vt + "') AS distance FROM docs WHERE category = 'c3' ORDER BY distance LIMIT 10"
	extra := "SELECT id, L2_DISTANCE(embedding, '" + vt + "') AS distance FROM docs WHERE category = 'c3' AND id > 0 ORDER BY distance LIMIT 10"
	r1, err := decompDB.Exec(covering)
	if err != nil {
		t.Fatal(err)
	}
	before := fusedBlockBatches.Load()
	r2, err := decompDB.Exec(extra)
	if err != nil {
		t.Fatal(err)
	}
	if fusedBlockBatches.Load() != before {
		t.Fatalf("non-covering query took the block path")
	}
	if len(r1.Rows) != len(r2.Rows) {
		t.Fatalf("row count: covering %d vs extra %d", len(r1.Rows), len(r2.Rows))
	}
	for i := range r1.Rows {
		if r1.Rows[i][0].I != r2.Rows[i][0].I || r1.Rows[i][1].F != r2.Rows[i][1].F {
			t.Fatalf("row %d differs: covering %v vs extra %v", i, r1.Rows[i], r2.Rows[i])
		}
	}
}

// TestCatBlockKeyOwnsBytes is the regression test for the wrong-category
// block bug (2026-11-13): through the stdio server the WHERE literal
// aliases the request reader's ring buffer (proto.parseFast uses
// unsafe.String), so the val string passed to catBlockFor is overwritten
// by later requests. catBlockFor must clone the key before storing it;
// otherwise a later query whose literal lands on the same buffer offset
// mutates the stored key and the lookup returns the wrong category's
// block (measured end-to-end: c8's query returned c3's top-10 when both
// literals sat at offset 5018, and c1's returned c6's at 5020).
// This test reproduces the aliasing in-process: build the c3 block with
// a val string aliasing mutable bytes, mutate those bytes to "c8", and
// verify the c3 block is still found by "c3" and "c8" builds its own.
func TestCatBlockKeyOwnsBytes(t *testing.T) {
	decompSetupT(t)
	prefilterEnabled = true
	tbl := decompDB.tables["docs"]
	catCol := -1
	for i, c := range tbl.Cols {
		if c.Name == "category" {
			catCol = i
		}
	}
	x := tbl.Index
	// Fresh cache state for this column so the test is hermetic.
	if x.cat != nil {
		delete(x.cat, catCol)
	}
	buf := []byte{'c', '3'}
	aliased := unsafe.String(&buf[0], len(buf))
	b3 := x.catBlockFor(decompDB, tbl, catCol, aliased)
	if b3 == nil || b3.n == 0 {
		t.Fatal("no block built for c3")
	}
	// A later request overwrites the buffer bytes the key aliased.
	buf[0], buf[1] = 'c', '8'
	again := x.catBlockFor(decompDB, tbl, catCol, "c3")
	if again != b3 {
		t.Fatal("c3 block lost after its key's bytes were overwritten (key not cloned)")
	}
	b8 := x.catBlockFor(decompDB, tbl, catCol, "c8")
	if b8 == nil || b8.n == 0 {
		t.Fatal("no block built for c8")
	}
	if b8 == b3 {
		t.Fatal("lookup for c8 returned the c3 block (aliased map key)")
	}
	if b8.n == b3.n {
		// Same size is possible for two categories; compare content.
		same := true
		for i := range b8.pks {
			if b8.pks[i] != b3.pks[i] {
				same = false
				break
			}
		}
		if same {
			t.Fatal("c8 block content is identical to the c3 block")
		}
	}
}
