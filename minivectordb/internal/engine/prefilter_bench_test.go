package engine

import (
	"fmt"
	"math"
	"math/rand"
	"strings"

	"minivectordb/internal/storage"
	"sync"
	"testing"
)

// Timing decomposition for the int8 prefilter at the benchmark scale
// (100k rows, dim 256, K=10, exact mode). The bench A/B showed only a
// ~28% query-time reduction, not the 60-70% the kernel throughput alone
// would suggest; these benchmarks say where the remaining time goes.
//
//   - BenchmarkScanExact100k / BenchmarkScanPrefilter100k: the whole
//     in-process SQL TopK (parse + scan + heap + result build), prefilter
//     off vs on. The difference to the bench's query_s is the MySQL
//     protocol + connection handling, which neither scan pays in-process.
//   - BenchmarkExactKernel100k / BenchmarkInt8Kernel100k: the two kernels
//     alone, over the same 100k rows in 128-row batches, serial.
//   - BenchmarkApproxBound100k: the per-row error-bound loop alone.
//   - BenchmarkScanTiny: a 100-row TopK, i.e. the fixed per-query cost
//     (parse, Flush/Prepare/Map, heap setup, result build) with a
//     negligible scan.

var decompOnce sync.Once
var decompDB *DB
var decompQueries [][]float32

func decompSetup(b *testing.B) {
	b.Helper()
	decompOnce.Do(func() {
		db, err := Open(b.TempDir(), Options{Sync: storage.SyncNone})
		if err != nil {
			b.Fatal(err)
		}
		decompDB = db
		if _, err := db.Exec("CREATE TABLE docs (id BIGINT PRIMARY KEY, category VARCHAR(16), embedding VECTOR(256))"); err != nil {
			b.Fatal(err)
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
				b.Fatal(err)
			}
		}
		if _, err := db.Exec("CREATE VECTOR INDEX vi ON docs(embedding)"); err != nil {
			b.Fatal(err)
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

func decompSQL(q []float32) string {
	return "SELECT id, L2_DISTANCE(embedding, '" + vectorText(q) + "') AS distance FROM docs ORDER BY distance LIMIT 10"
}

func BenchmarkScanExact100k(b *testing.B) {
	decompSetup(b)
	old := prefilterEnabled
	prefilterEnabled = false
	defer func() { prefilterEnabled = old }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decompDB.Exec(decompSQL(decompQueries[i%len(decompQueries)])); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanPrefilter100k(b *testing.B) {
	decompSetup(b)
	old := prefilterEnabled
	prefilterEnabled = true
	defer func() { prefilterEnabled = old }()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decompDB.Exec(decompSQL(decompQueries[i%len(decompQueries)])); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScanTiny(b *testing.B) {
	db, err := Open(b.TempDir(), Options{Sync: storage.SyncNone})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	mustExecB(b, db, "CREATE TABLE t (id BIGINT PRIMARY KEY, e VECTOR(256))")
	rng := rand.New(rand.NewSource(3))
	var sb strings.Builder
	sb.WriteString("INSERT INTO t VALUES ")
	for i := 0; i < 100; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, "(%d,'[", i+1)
		for j := 0; j < 256; j++ {
			if j > 0 {
				sb.WriteByte(',')
			}
			fmt.Fprintf(&sb, "%v", float64(float32(rng.Float64())))
		}
		sb.WriteString("]')")
	}
	mustExecB(b, db, sb.String())
	mustExecB(b, db, "CREATE VECTOR INDEX vi ON t(e)")
	q := make([]float32, 256)
	for j := range q {
		q[j] = rng.Float32()
	}
	sql := "SELECT id, L2_DISTANCE(e, '" + vectorText(q) + "') AS distance FROM t ORDER BY distance LIMIT 10"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.Exec(sql); err != nil {
			b.Fatal(err)
		}
	}
}

// kernelData builds the inputs for the kernel-only benchmarks: a base
// mapping of 100k float32 vectors laid out sequentially, the gather
// offsets, and matching int8 codes with parameters.
func decompKernelData() (qw []float64, base []byte, offs []int64, q []float32, codes []byte, coffs []int64, qp []qparam) {
	const n, dim = 100000, 256
	rng := rand.New(rand.NewSource(5))
	q = make([]float32, dim)
	for i := range q {
		q[i] = rng.Float32()
	}
	qw = widen(q)
	base = make([]byte, n*dim*4)
	for i := range base {
		base[i] = byte(rng.Intn(256))
	}
	offs = make([]int64, n)
	coffs = make([]int64, n)
	for i := range offs {
		offs[i] = int64(i * dim * 4)
		coffs[i] = int64(i * dim)
	}
	codes = make([]byte, n*dim)
	qp = make([]qparam, n)
	for i := range qp {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		qp[i] = quantizeRow(v, codes[i*dim:(i+1)*dim])
	}
	return
}

func BenchmarkExactKernel100k(b *testing.B) {
	qw, base, offs, _, _, _, _ := decompKernelData()
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

func BenchmarkInt8Kernel100k(b *testing.B) {
	_, _, _, q, codes, coffs, _ := decompKernelData()
	out := make([]float32, len(coffs))
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(coffs); s += scanBatch {
			e := s + scanBatch
			if e > len(coffs) {
				e = len(coffs)
			}
			dotInt8Gather(q, codes, coffs[s:e], out[s:e])
		}
	}
}

// BenchmarkInt8KernelInt100k is the integer-dot variant of
// BenchmarkInt8Kernel100k: the query is quantized once (outside the timed
// loop) and the kernel accumulates exact int32 dot products.
func BenchmarkInt8KernelInt100k(b *testing.B) {
	_, _, _, q, codes, coffs, _ := decompKernelData()
	_, _, qc, _, _, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantizeQuery rejected the benchmark query")
	}
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

// BenchmarkInt8KernelInt6_100k is the 6-bit variant of
// BenchmarkInt8KernelInt100k: the query is quantized to 6-bit codes once
// (outside the timed loop) and the VPMADDUBSW kernel accumulates exact
// int32 dot products.
func BenchmarkInt8KernelInt6_100k(b *testing.B) {
	_, _, _, q, codes, coffs, _ := decompKernelData()
	_, _, qc, _, _, ok := quantizeQuery6(q)
	if !ok {
		b.Fatal("quantizeQuery6 rejected the benchmark query")
	}
	out := make([]int32, len(coffs))
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(coffs); s += scanBatch {
			e := s + scanBatch
			if e > len(coffs) {
				e = len(coffs)
			}
			dotInt8GatherInt6(qc, codes, coffs[s:e], out[s:e])
		}
	}
}

// BenchmarkInt8KernelIntAVX2_100k and BenchmarkInt8KernelIntAVX512_100k
// call the two integer-dot kernels directly (dim 256, so no scalar tail
// on either side) to isolate the kernel itself from the dispatch wrapper
// and the rest of the scan. The AVX-512 one is skipped on CPUs without
// AVX-512; running it would fault.
func BenchmarkInt8KernelIntAVX2_100k(b *testing.B) {
	_, _, _, q, codes, coffs, _ := decompKernelData()
	_, _, qc, _, _, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantizeQuery rejected the benchmark query")
	}
	out := make([]int32, len(coffs))
	blocks := len(qc) / 32
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(coffs); s += scanBatch {
			e := s + scanBatch
			if e > len(coffs) {
				e = len(coffs)
			}
			dotInt8GatherIntAVX2(&qc[0], &codes[0], &coffs[s], blocks, e-s, &out[s])
		}
	}
}

func BenchmarkInt8KernelIntAVX512_100k(b *testing.B) {
	if !useAVX512 {
		b.Skip("CPU/OS lacks AVX-512")
	}
	_, _, _, q, codes, coffs, _ := decompKernelData()
	_, _, qc, _, _, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantizeQuery rejected the benchmark query")
	}
	out := make([]int32, len(coffs))
	blocks := len(qc) / 64
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		for s := 0; s < len(coffs); s += scanBatch {
			e := s + scanBatch
			if e > len(coffs) {
				e = len(coffs)
			}
			dotInt8GatherIntAVX512(&qc[0], &codes[0], &coffs[s], blocks, e-s, &out[s])
		}
	}
}

func BenchmarkThresholdBound100k(b *testing.B) {
	const n, dim = 100000, 256
	rng := rand.New(rand.NewSource(6))
	// Same inputs as BenchmarkApproxBound100k: realistic dots and
	// parameters for uniform [0,1) vectors at dim 256.
	q := make([]float32, dim)
	for i := range q {
		q[i] = rng.Float32()
	}
	codes := make([]byte, n*dim)
	qp := make([]qparam, n)
	var maxScale float32
	for i := range qp {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		qp[i] = quantizeRow(v, codes[i*dim:(i+1)*dim])
		if qp[i].scale > maxScale {
			maxScale = qp[i].scale
		}
	}
	coffs := make([]int64, n)
	for i := range coffs {
		coffs[i] = int64(i * dim)
	}
	qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantizeQuery rejected the benchmark query")
	}
	dots := make([]int32, n)
	dotInt8GatherInt(qc, codes, coffs, dots)
	thresh2 := prefilterThreshold(maxScale+qscale, dim, 35)
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		kept := 0
		for i := 0; i < n; i++ {
			deq := float64(qmin)*float64(qp[i].sumC) + float64(qscale)*float64(dots[i])
			if approxDist2(qSum, qNorm2, deq, qp[i]) <= thresh2 {
				kept++
			}
		}
		if kept == 0 {
			b.Fatal("no survivors")
		}
	}
}

func BenchmarkApproxBound100k(b *testing.B) {
	const n, dim = 100000, 256
	rng := rand.New(rand.NewSource(6))
	// Realistic dots: the actual int8 dot products of a query against the
	// codes of uniform [0,1) vectors, so the bound sees the true
	// distribution (10th-place squared distance ~31-35 at dim 256).
	q := make([]float32, dim)
	for i := range q {
		q[i] = rng.Float32()
	}
	codes := make([]byte, n*dim)
	qp := make([]qparam, n)
	for i := range qp {
		v := make([]float32, dim)
		for j := range v {
			v[j] = rng.Float32()
		}
		qp[i] = quantizeRow(v, codes[i*dim:(i+1)*dim])
	}
	coffs := make([]int64, n)
	for i := range coffs {
		coffs[i] = int64(i * dim)
	}
	qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantizeQuery rejected the benchmark query")
	}
	dots := make([]int32, n)
	dotInt8GatherInt(qc, codes, coffs, dots)
	sqrtDim := math.Sqrt(float64(dim))
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		kept := 0
		for i := 0; i < n; i++ {
			deq := float64(qmin)*float64(qp[i].sumC) + float64(qscale)*float64(dots[i])
			dhat2, bound := approxBound(qSum, qNorm2, sqrtDim, dim, deq, qp[i], qscale)
			if dhat2-bound <= 35 {
				kept++
			}
		}
		if kept == 0 {
			b.Fatal("no survivors")
		}
	}
}
