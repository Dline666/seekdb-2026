// Copyright (c) 2026 OceanBase.
// Licensed under the Apache License, Version 2.0.

package engine

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"testing"
)

// Deliberately independent of the production dot/fold/quantization helpers.
// int64 accumulation detects int32 overflow as well as lane reduction errors.
func quadAVX2Oracle(q []int16, x *VecIndex, slots []int32, sum, norm float64, mn, scale float32, threshold float64) []int64 {
	var out []int64
	for _, slot := range slots {
		d := quadAVX2OracleDistance(q, x, int(slot), sum, norm, mn, scale)
		if !(d > threshold) {
			out = append(out, x.off[slot], x.pks[slot])
		}
	}
	return out
}

func quadAVX2OracleDistance(q []int16, x *VecIndex, slot int, sum, norm float64, mn, scale float32) float64 {
	var dot int64
	for j, c := range q {
		dot += int64(c) * int64(x.codes[slot*len(q)+j])
	}
	f := x.qf[4*slot : 4*slot+4]
	// Explicit conversions force rounding after each operation, prohibiting FMA.
	d := float64(norm + float64(f[0]*sum))
	d = float64(d + f[3])
	d = float64(d + float64(float64(mn)*f[1]))
	d = float64(d + float64(float64(float64(scale)*f[2])*float64(dot)))
	if d < 0 {
		d = 0
	}
	return d
}

func TestQuadAVX2IndependentOracle(t *testing.T) {
	if !useSIMD {
		t.Skip("AVX2 disabled or unavailable")
	}
	old := quadAVX2
	quadAVX2 = true
	defer func() { quadAVX2 = old }()
	rng := rand.New(rand.NewSource(2026111701))
	for _, dim := range []int{1, 15, 31, 32, 64, 96, 127, 128, 129, 256, 1024} {
		x := &VecIndex{dim: dim, pks: make([]int64, 140), off: make([]int64, 140), codes: make([]byte, 140*dim), qf: make([]float64, 140*4)}
		q := make([]int16, dim)
		for j := range q {
			q[j] = int16(rng.Intn(256))
		}
		for j := range x.codes {
			x.codes[j] = byte(rng.Intn(256))
		}
		for j := range x.pks {
			x.pks[j] = int64(91 - j)
			x.off[j] = int64(j*1003 + 17)
			x.qf[4*j] = -2 * rng.Float64()
			x.qf[4*j+1] = -50 * rng.Float64()
			x.qf[4*j+2] = -0.02 * rng.Float64()
			x.qf[4*j+3] = 100 * rng.Float64()
		}
		x.qf[4*6+3] = math.NaN()
		x.qf[4*7+3] = math.Inf(1)
		x.qf[4*8+3] = math.Inf(-1)
		const sum, norm, mn, scale = 37.5, 100., -0.25, 0.003
		thresholds := []float64{0, math.Inf(1), math.NaN()}
		for _, slot := range []int{0, 1, 2, 3, 4, 5, 127, 128, 129} {
			d := quadAVX2OracleDistance(q, x, slot, sum, norm, mn, scale)
			thresholds = append(thresholds, d, math.Nextafter(d, math.Inf(1)))
			if d > 0 {
				thresholds = append(thresholds, math.Nextafter(d, 0))
			}
		}
		for _, start := range []int{0, 1, 3, 7} {
			for _, rows := range []int{0, 1, 2, 3, 4, 5, 7, 8, 15, 32, 127, 128, 129} {
				slots := make([]int32, rows)
				for j := range slots {
					slots[j] = int32(start + j)
				}
				for _, gather := range []bool{false, true} {
					if gather {
						for j := range slots {
							slots[j] = int32((139 - j*17 + 140*17) % 140)
							if j%7 == 0 {
								slots[j] = 0
							}
						}
					}
					for _, threshold := range thresholds {
						want := quadAVX2Oracle(q, x, slots, sum, norm, mn, scale, threshold)
						buf := make([]int64, 2*rows+4)
						for j := range buf {
							buf[j] = -700001
						}
						var n int
						if gather {
							n = fusedScanCandidates(q, x, slots, sum, norm, mn, scale, threshold, buf[2:])
						} else {
							n = fusedScanInt8(q, x, int32(start), rows, sum, norm, mn, scale, threshold, buf[2:])
						}
						if n < 0 || n > rows || !slices.Equal(buf[2:2+2*n], want) {
							t.Fatalf("dim=%d start=%d rows=%d gather=%t threshold=%g n=%d want=%v got=%v", dim, start, rows, gather, threshold, n, want, buf)
						}
						if buf[0] != -700001 || buf[1] != -700001 {
							t.Fatal("prefix overwritten")
						}
						for _, v := range buf[2+2*n:] {
							if v != -700001 {
								t.Fatal("survivor buffer overrun")
							}
						}
					}
				}
			}
		}
	}
}

func TestQuadAVX2MaximumIntegerDot(t *testing.T) {
	if !useSIMD {
		t.Skip("AVX2 disabled or unavailable")
	}
	old := quadAVX2
	quadAVX2 = true
	defer func() { quadAVX2 = old }()
	dim := maxInt8Dim &^ 31
	x := &VecIndex{dim: dim, pks: []int64{1, 2, 3, 4}, off: []int64{7, 8, 9, 10}, codes: make([]byte, 4*dim), qf: make([]float64, 16)}
	q := make([]int16, dim)
	for j := range q {
		q[j] = 255
	}
	for j := range x.codes {
		x.codes[j] = 255
	}
	for j := range x.pks {
		x.qf[4*j+2] = 1
	}
	d := float64(dim) * 255 * 255
	for _, threshold := range []float64{math.Nextafter(d, 0), d, math.Nextafter(d, math.Inf(1))} {
		buf := make([]int64, 8)
		n := fusedScanInt8(q, x, 0, 4, 0, 0, 0, 1, threshold, buf)
		want := quadAVX2Oracle(q, x, []int32{0, 1, 2, 3}, 0, 0, 0, 1, threshold)
		if !slices.Equal(buf[:2*n], want) {
			t.Fatalf("dim=%d threshold=%g got=%v want=%v", dim, threshold, buf[:2*n], want)
		}
	}
}

func BenchmarkQuadAVX2(b *testing.B) {
	if !useSIMD {
		b.Skip("AVX2 disabled or unavailable")
	}
	old := quadAVX2
	defer func() { quadAVX2 = old }()
	for _, dim := range []int{128, 256, 1024} {
		x := &VecIndex{dim: dim, pks: make([]int64, 128), off: make([]int64, 128), codes: make([]byte, 128*dim), qf: make([]float64, 128*4)}
		q := make([]int16, dim)
		slots := make([]int32, 128)
		surv := make([]int64, 256)
		for j := range q {
			q[j] = int16(j % 256)
		}
		for j := range x.codes {
			x.codes[j] = byte(j)
		}
		for j := range slots {
			slots[j] = int32(127 - j)
			x.qf[4*j+2] = 1
		}
		for _, gather := range []bool{false, true} {
			for _, enabled := range []bool{false, true} {
				quadAVX2 = enabled
				b.Run(fmt.Sprintf("dim%d/gather%t/quad%t", dim, gather, enabled), func(b *testing.B) {
					for i := 0; i < b.N; i++ {
						if gather {
							fusedScanCandidates(q, x, slots, 0, 0, 0, 1, 0, surv)
						} else {
							fusedScanInt8(q, x, 0, 128, 0, 0, 0, 1, 0, surv)
						}
					}
				})
			}
		}
	}
}
