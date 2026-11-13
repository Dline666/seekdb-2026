package engine

// H-E fresh profile of the fused stack (perf/kernel2 @ 712f9ab7).
// Decomposition + CPU profile of the CURRENT tree: the W-E profile is
// stale (it was taken on the pre-fused kernel binary).

import (
	"fmt"
	"os"
	"runtime/pprof"
	"testing"
)

// heIndex returns the VecIndex of the decomp 100k-row table.
func heIndex(b *testing.B) *VecIndex {
	b.Helper()
	decompSetup(b)
	t, err := decompDB.table("docs")
	if err != nil {
		b.Fatal(err)
	}
	return t.Index
}

// BenchmarkHeFusedVNNI100k: the VNNI fused kernel alone, serial, same
// setup as BenchmarkHeFusedKernel100k.
func BenchmarkHeFusedVNNI100k(b *testing.B) {
	if !useVNNI {
		b.Skip("no AVX512_VNNI")
	}
	x := heIndex(b)
	q := decompQueries[0]
	qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantizeQuery rejected the benchmark query")
	}
	qc8, qminAdj := heBiasQuery(qc, qmin, qscale)
	for _, thresh := range []float64{35, 1e18} {
		b.Run(fmt.Sprintf("thresh=%g", thresh), func(b *testing.B) {
			surv := make([]int64, 2*scanBatch)
			b.ResetTimer()
			for it := 0; it < b.N; it++ {
				for s := 0; s < 100000; s += scanBatch {
					n := scanBatch
					if s+n > 100000 {
						n = 100000 - s
					}
					fusedScanVNNI(qc8, x, int32(s), n, qSum, qNorm2, qminAdj, float64(qscale), thresh, surv)
				}
			}
		})
	}
}

// BenchmarkHeFusedKernel100k: the fused kernel alone, serial, over the
// full 100k rows in 128-row batches, threshold +Inf (keeps everything,
// so the count is the dot+epilogue cost with no survivor-store variance)
// and at a realistic bound (35).
func BenchmarkHeFusedKernel100k(b *testing.B) {
	x := heIndex(b)
	q := decompQueries[0]
	qmin, qscale, qc, qSum, qNorm2, ok := quantizeQuery(q)
	if !ok {
		b.Fatal("quantizeQuery rejected the benchmark query")
	}
	for _, thresh := range []float64{35, 1e18} {
		b.Run(fmt.Sprintf("thresh=%g", thresh), func(b *testing.B) {
			surv := make([]int64, 2*scanBatch)
			b.ResetTimer()
			for it := 0; it < b.N; it++ {
				for s := 0; s < 100000; s += scanBatch {
					n := scanBatch
					if s+n > 100000 {
						n = 100000 - s
					}
					fusedScanInt8(qc, x, int32(s), n, qSum, qNorm2, qmin, qscale, thresh, surv)
				}
			}
		})
	}
}

// BenchmarkHeDotAVX512_100k / BenchmarkHeDotAVX2_100k: the multi-pass
// integer dot kernels alone, serial, for the same rows.
func BenchmarkHeDotAVX512_100k(b *testing.B) {
	if !useAVX512 {
		b.Skip("no AVX-512")
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

func BenchmarkHeDotAVX2_100k(b *testing.B) {
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

// BenchmarkHeExactSurvivors300: the exact float64 kernel on 300 rows
// (the ~287 survivors/query W-F measured), serial.
func BenchmarkHeExactSurvivors300(b *testing.B) {
	qw, base, offs, _, _, _, _ := decompKernelData()
	out := make([]float64, 300)
	sub := offs[:300]
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		l2sqGatherBytes(qw, base, sub, out)
	}
}

// BenchmarkHeScanFiltered100k: the filtered half of the bench workload
// (equality filter -> candidate set -> multi-pass scan), in-process.
func BenchmarkHeScanFiltered100k(b *testing.B) {
	decompSetup(b)
	old := prefilterEnabled
	prefilterEnabled = true
	defer func() { prefilterEnabled = old }()
	sql := "SELECT id, L2_DISTANCE(embedding, '" + vectorText(decompQueries[0]) + "') AS distance FROM docs WHERE category = 'c3' ORDER BY distance LIMIT 10"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := decompDB.Exec(sql); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkHeScanUnfiltered100k: the unfiltered half (fused path).
func BenchmarkHeScanUnfiltered100k(b *testing.B) {
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

// BenchmarkHeCPUProfile runs a mixed unfiltered+filtered query loop
// under the CPU profiler and writes he-query.prof into the test temp
// dir for `go tool pprof -top`.
// Run with -benchtime=1x.
func BenchmarkHeCPUProfile(b *testing.B) {
	decompSetup(b)
	old := prefilterEnabled
	prefilterEnabled = true
	defer func() { prefilterEnabled = old }()
	f, err := os.Create(b.TempDir() + "/he-query.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()
	if err := pprof.StartCPUProfile(f); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 300; i++ {
		if _, err := decompDB.Exec(decompSQL(decompQueries[i%len(decompQueries)])); err != nil {
			b.Fatal(err)
		}
	}
	for i := 0; i < 300; i++ {
		sql := "SELECT id, L2_DISTANCE(embedding, '" + vectorText(decompQueries[i%len(decompQueries)]) + "') AS distance FROM docs WHERE category = 'c3' ORDER BY distance LIMIT 10"
		if _, err := decompDB.Exec(sql); err != nil {
			b.Fatal(err)
		}
	}
	pprof.StopCPUProfile()
}
