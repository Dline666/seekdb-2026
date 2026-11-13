package engine

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"testing"
)

func TestAVXVNNIGate(t *testing.T) {
	const required = 1<<12 | 1<<27 | 1<<28
	for _, tc := range []struct {
		name                           string
		maxLeaf, c1, xcr, sub, b7, a71 uint32
		want, readXCR, readSubleaf     bool
	}{
		{"ymm-only", 7, required, 6, 1, 1 << 5, 1 << 4, true, true, true},
		{"zmm-optional", 7, required, 0xe6, 1, 1 << 5, 1 << 4, true, true, true},
		{"no-leaf7", 6, required, 6, 1, 1 << 5, 1 << 4, false, false, false},
		{"no-fma", 7, required &^ (1 << 12), 6, 1, 1 << 5, 1 << 4, false, false, false},
		{"no-osxsave", 7, required &^ (1 << 27), 6, 1, 1 << 5, 1 << 4, false, false, false},
		{"no-avx", 7, required &^ (1 << 28), 6, 1, 1 << 5, 1 << 4, false, false, false},
		{"no-xmm", 7, required, 4, 1, 1 << 5, 1 << 4, false, true, false},
		{"no-ymm", 7, required, 2, 1, 1 << 5, 1 << 4, false, true, false},
		{"no-avx2", 7, required, 6, 1, 0, 1 << 4, false, true, false},
		{"no-subleaf1", 7, required, 6, 0, 1 << 5, 1 << 4, false, true, false},
		{"no-avx-vnni", 7, required, 6, 1, 1 << 5, 0, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			readXCR, readSubleaf := false, false
			id := func(leaf, subleaf uint32) (uint32, uint32, uint32, uint32) {
				switch {
				case leaf == 0 && subleaf == 0:
					return tc.maxLeaf, 0, 0, 0
				case leaf == 1 && subleaf == 0:
					return 0, 0, tc.c1, 0
				case leaf == 7 && subleaf == 0:
					return tc.sub, tc.b7, 0, 0
				case leaf == 7 && subleaf == 1:
					readSubleaf = true
					return tc.a71, 0, 0, 0
				default:
					t.Fatalf("unexpected CPUID(%d,%d)", leaf, subleaf)
					return 0, 0, 0, 0
				}
			}
			got := avxVNNISupported(id, func() uint32 { readXCR = true; return tc.xcr })
			if got != tc.want || readXCR != tc.readXCR || readSubleaf != tc.readSubleaf {
				t.Fatalf("got support/xcr/subleaf=%v/%v/%v, want %v/%v/%v", got, readXCR, readSubleaf, tc.want, tc.readXCR, tc.readSubleaf)
			}
		})
	}
}

func TestAVXVNNIEnvOverrides(t *testing.T) {
	before := hasAVXVNNI()
	t.Setenv("MVDB_NO_AVX512", "1")
	if hasAVXVNNI() != before {
		t.Fatal("disabling AVX512 changed the independent AVX-VNNI gate")
	}
	for _, key := range []string{"MVDB_NO_SIMD", "MVDB_NO_VNNI", "MVDB_NO_AVXVNNI"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "1")
			if hasAVXVNNI() {
				t.Fatalf("AVX-VNNI enabled with %s=1", key)
			}
		})
	}
}

// Compare the actual signed dot and the same biased float64 association.
// The unbiased AVX2 fold can differ by ulps; final exact results, rather
// than arbitrary-threshold equality with that fold, are the contract.
func avxVNNIDistance(qc8 []int8, x *VecIndex, slot int32, qSum, qNorm2, qminAdj, qscale float64) float64 {
	var dot int32
	for j, q := range qc8 {
		dot += int32(q) * int32(x.codes[int(slot)*x.dim+j])
	}
	f := x.qf[4*int(slot):]
	d := f[0] * qSum
	d += qNorm2
	d += f[3]
	d += f[1] * qminAdj
	p := f[2] * qscale
	p *= float64(dot)
	return d + p
}

func avxVNNIScalar(qc8 []int8, x *VecIndex, slots []int32, qSum, qNorm2, qminAdj, qscale, threshold float64) []int64 {
	var out []int64
	for _, slot := range slots {
		d := avxVNNIDistance(qc8, x, slot, qSum, qNorm2, qminAdj, qscale)
		if !(d > threshold) {
			out = append(out, x.off[slot], x.pks[slot])
		}
	}
	return out
}

func checkAVXVNNIKernels(t *testing.T, qc8 []int8, x *VecIndex, qSum, qNorm2, qminAdj, qscale float64, thresholds []float64) {
	t.Helper()
	for _, mode := range []string{"sequential", "candidates"} {
		slots := make([]int32, len(x.pks)-3)
		for i := range slots {
			slots[i] = int32(i + 3)
		}
		if mode == "candidates" {
			// Non-contiguous, reversed and repeated candidates preserve their
			// supplied order. Include slots at both ends of the arrays.
			slots = []int32{int32(len(x.pks) - 1), 0, 4, 4, 2, 12, 3}
		}
		for _, threshold := range thresholds {
			want := avxVNNIScalar(qc8, x, slots, qSum, qNorm2, qminAdj, qscale, threshold)
			got := make([]int64, 2*len(slots)+2)
			got[len(got)-2], got[len(got)-1] = -101, -103
			var n int
			if mode == "sequential" {
				n = fusedScanAVXVNNI(qc8, x, 3, len(slots), qSum, qNorm2, qminAdj, qscale, threshold, got)
			} else {
				n = fusedScanCandidateAVXVNNI(qc8, x, slots, qSum, qNorm2, qminAdj, qscale, threshold, got)
			}
			if n < 0 || n > len(slots) || !slices.Equal(got[:2*n], want) {
				t.Fatalf("%s dim=%d threshold=%g: got count %d, want %v", mode, len(qc8), threshold, n, want)
			}
			if got[len(got)-2] != -101 || got[len(got)-1] != -103 {
				t.Fatal("survivor buffer overwrite")
			}
		}
	}
}

func TestAVXVNNIIntegerDot(t *testing.T) {
	if !hasAVXVNNI() {
		t.Skip("AVX-VNNI unavailable")
	}
	rng := rand.New(rand.NewSource(4801))
	for _, dim := range []int{1, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128, 129, 255, 256, 257, 1024, maxInt8Dim} {
		x := &VecIndex{dim: dim, pks: make([]int64, 17), off: make([]int64, 17), codes: make([]byte, 17*dim), qf: make([]float64, 17*4)}
		for s := range x.pks {
			x.pks[s], x.off[s], x.qf[4*s+2] = int64(100-s), int64(1000*s), 1
			for j := 0; j < dim; j++ {
				x.codes[s*dim+j] = byte(rng.Intn(256))
				if s%3 == 0 {
					x.codes[s*dim+j] = 255
				}
			}
		}
		for _, pattern := range []string{"negative", "positive", "mixed", "random"} {
			qc8 := make([]int8, dim)
			for j := range qc8 {
				switch pattern {
				case "negative":
					qc8[j] = -128
				case "positive":
					qc8[j] = 127
				case "mixed":
					qc8[j] = int8((j%2)*255 - 128)
				case "random":
					qc8[j] = int8(rng.Intn(256) - 128)
				}
			}
			var dot int32
			for j, q := range qc8 {
				dot += int32(q) * int32(x.codes[3*dim+j])
			}
			thresholds := []float64{math.Inf(-1), -1, 0, 1, float64(dot) - 1, float64(dot), float64(dot) + 1, math.Inf(1), math.NaN()}
			t.Run(fmt.Sprintf("%d/%s", dim, pattern), func(t *testing.T) {
				checkAVXVNNIKernels(t, qc8, x, 0, 0, 0, 1, thresholds)
			})
		}
	}
	if fusedScanInt8AVXVNNI(nil, nil, nil, nil, nil, 0, 0, 0, 0, 0, 0, 0, 0, 0, nil) != 0 ||
		fusedScanCandidatesAVXVNNI(nil, nil, nil, nil, nil, 0, 0, 0, 0, 0, nil, 0, 0, 0, nil) != 0 {
		t.Fatal("empty raw scan produced survivors")
	}
	if fusedScanAVXVNNI(nil, &VecIndex{}, 0, 0, 0, 0, 0, 0, 0, nil) != 0 ||
		fusedScanCandidateAVXVNNI(nil, &VecIndex{}, nil, 0, 0, 0, 0, 0, nil) != 0 {
		t.Fatal("empty scan produced survivors")
	}
}

func TestAVXVNNIFoldedScalar(t *testing.T) {
	if !hasAVXVNNI() {
		t.Skip("AVX-VNNI unavailable")
	}
	rng := rand.New(rand.NewSource(4802))
	for _, dim := range []int{17, 32, 33, 64, 127, 128, 129, 256, 257, 1024} {
		x := &VecIndex{dim: dim, pks: make([]int64, 17), off: make([]int64, 17), codes: make([]byte, 17*dim), qf: make([]float64, 17*4)}
		for s := range x.pks {
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32()*4 - 2
			}
			qp := quantizeRow(v, x.codes[s*dim:(s+1)*dim])
			x.qf[4*s], x.qf[4*s+1], x.qf[4*s+2], x.qf[4*s+3] = foldQparam(qp, dim)
			x.pks[s], x.off[s] = int64(100-s), int64(1000*s)
		}
		q := make([]float32, dim)
		for j := range q {
			q[j] = rng.Float32()*4 - 2
		}
		qmin, qscale, qc, sum, norm, ok := quantizeQuery(q)
		if !ok {
			t.Fatal("finite query rejected")
		}
		qc8, qminAdj := heBiasQuery(qc, qmin, qscale)
		thresholds := []float64{-1, 0, 5, 35, 200, 1000, 1e6, math.Inf(1), math.NaN()}
		for _, slot := range []int32{0, 3, 8, 16} {
			d := avxVNNIDistance(qc8, x, slot, sum, norm, qminAdj, float64(qscale))
			thresholds = append(thresholds, math.Nextafter(d, math.Inf(-1)), d, math.Nextafter(d, math.Inf(1)))
		}
		checkAVXVNNIKernels(t, qc8, x, sum, norm, qminAdj, float64(qscale), thresholds)
		// Exceptional folded intermediates must fall through to exact
		// verification whenever the scalar comparison is unordered.
		x.qf[4*3+3], x.qf[4*4+3] = math.NaN(), math.Inf(1)
		checkAVXVNNIKernels(t, qc8, x, sum, norm, qminAdj, float64(qscale), []float64{0, 35, math.Inf(1), math.NaN()})
	}
}
