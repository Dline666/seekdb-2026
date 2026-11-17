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

func TestQuadAVXVNNIBoundaries(t *testing.T) {
	if !hasAVXVNNI() {
		t.Skip("AVX-VNNI unavailable")
	}
	old := quadAVXVNNI
	quadAVXVNNI = true
	defer func() { quadAVXVNNI = old }()
	rng := rand.New(rand.NewSource(81717))
	for _, dim := range []int{31, 32, 64, 96, 127, 128, 129, 256, 1024} {
		x := &VecIndex{dim: dim, pks: make([]int64, 140), off: make([]int64, 140), codes: make([]byte, 140*dim), qf: make([]float64, 140*4)}
		for i := range x.pks {
			x.pks[i], x.off[i] = int64(71-i), int64(101*i)
			v := make([]float32, dim)
			for j := range v {
				v[j] = rng.Float32()*10 - 5
			}
			p := quantizeRow(v, x.codes[i*dim:(i+1)*dim])
			x.qf[4*i], x.qf[4*i+1], x.qf[4*i+2], x.qf[4*i+3] = foldQparam(p, dim)
		}
		q := make([]float32, dim)
		for j := range q {
			q[j] = rng.Float32()*10 - 5
		}
		mn, sc, codes, sum, norm, ok := quantizeQuery(q)
		if !ok {
			t.Fatal("finite query rejected")
		}
		qc, adj := heBiasQuery(codes, mn, sc)
		thresholds := []float64{math.Inf(-1), 0, math.Inf(1), math.NaN()}
		for _, slot := range []int32{1, 2, 3, 4, 5, 127, 128, 129} {
			d := avxVNNIDistance(qc, x, slot, sum, norm, adj, float64(sc))
			thresholds = append(thresholds, math.Nextafter(d, math.Inf(-1)), d, math.Nextafter(d, math.Inf(1)))
		}
		x.qf[4*6+3] = math.NaN()
		x.qf[4*7+3] = math.Inf(1)
		for _, start := range []int{0, 1, 3, 7} {
			for _, rows := range []int{0, 1, 2, 3, 4, 5, 7, 8, 15, 16, 31, 32, 127, 128, 129} {
				slots := make([]int32, rows)
				for j := range slots {
					slots[j] = int32(start + j)
				}
				for _, threshold := range thresholds {
					want := avxVNNIScalar(qc, x, slots, sum, norm, adj, float64(sc), threshold)
					buf := make([]int64, 2*rows+2)
					buf[2*rows], buf[2*rows+1] = -17, -31
					n := fusedScanAVXVNNI(qc, x, int32(start), rows, sum, norm, adj, float64(sc), threshold, buf)
					if n < 0 || n > rows || !slices.Equal(buf[:2*n], want) || buf[2*rows] != -17 || buf[2*rows+1] != -31 {
						t.Fatalf("dim=%d start=%d rows=%d threshold=%g count=%d want=%v got=%v", dim, start, rows, threshold, n, want, buf)
					}
				}
				// A gathered batch must preserve supplied order, including
				// duplicates, both boundary slots and a final partial group.
				for j := range slots {
					slots[j] = int32((139 - j*17 + 140*17) % 140)
					if j%7 == 0 {
						slots[j] = 0
					}
				}
				for _, threshold := range thresholds {
					want := avxVNNIScalar(qc, x, slots, sum, norm, adj, float64(sc), threshold)
					buf := make([]int64, 2*rows+2)
					buf[2*rows], buf[2*rows+1] = -17, -31
					n := fusedScanCandidateAVXVNNI(qc, x, slots, sum, norm, adj, float64(sc), threshold, buf)
					if n < 0 || n > rows || !slices.Equal(buf[:2*n], want) || buf[2*rows] != -17 || buf[2*rows+1] != -31 {
						t.Fatalf("gather dim=%d rows=%d threshold=%g count=%d", dim, rows, threshold, n)
					}
				}
			}
		}
	}
}

func BenchmarkQuadAVXVNNI(b *testing.B) {
	if !hasAVXVNNI() {
		b.Skip("AVX-VNNI unavailable")
	}
	old := quadAVXVNNI
	defer func() { quadAVXVNNI = old }()
	for _, dim := range []int{128, 256, 1024} {
		x := &VecIndex{dim: dim, pks: make([]int64, 128), off: make([]int64, 128), codes: make([]byte, 128*dim), qf: make([]float64, 128*4)}
		qc := make([]int8, dim)
		for i := range qc {
			qc[i] = int8(i)
		}
		for i := range x.codes {
			x.codes[i] = byte(i)
		}
		surv := make([]int64, 256)
		for _, enabled := range []bool{false, true} {
			quadAVXVNNI = enabled
			b.Run(fmt.Sprintf("dim%d/quad%t", dim, enabled), func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					fusedScanAVXVNNI(qc, x, 0, 128, 0, 0, 0, 1, -1, surv)
				}
			})
		}
	}
}
