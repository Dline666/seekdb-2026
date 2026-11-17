// Copyright (c) 2026 OceanBase.
// Licensed under the Apache License, Version 2.0.

package engine

import "os"

// Keep the original single-row kernels available for controlled comparisons.
var quadAVX2 = os.Getenv("MVDB_NO_QUAD_AVX2") == ""

// Four exact int32 row dots share query loads and a packed float64 epilogue.
// Call only with AVX2 enabled, a positive multiple of four rows, blocks > 0,
// dim == blocks*32 <= maxInt8Dim, and a nonnegative (or NaN) threshold.
//
//go:noescape
func fusedScanQuadAVX2(qc *int16, codes *byte, pks *int64, offs *int64, qf *float64, qSum, qNorm2, qmin, qscale, thresh2 float64, baseSlot, rows, blocks int, surv *int64) int

// Same arithmetic, over valid candidate slots in the supplied order.
//
//go:noescape
func fusedScanQuadCandidatesAVX2(qc *int16, codes *byte, pks *int64, offs *int64, qf *float64, qSum, qNorm2, qmin, qscale, thresh2 float64, slots *int32, rows, blocks int, surv *int64) int

func fusedScanBlockAVX2(qc []int16, codes []byte, pks, offs []int64, qf []float64, baseSlot, rows int, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	if quadAVX2 && len(qc) >= 32 && len(qc)%32 == 0 && rows >= 4 && !(thresh2 < 0) {
		whole := rows &^ 3
		n := fusedScanQuadAVX2(&qc[0], &codes[0], &pks[0], &offs[0], &qf[0],
			qSum, qNorm2, float64(qmin), float64(qscale), thresh2, baseSlot, whole, len(qc)/32, &surv[0])
		if whole < rows {
			n += fusedScanInt8AVX2(&qc[0], &codes[0], &pks[0], &offs[0], &qf[0],
				qSum, qNorm2, float64(qmin), float64(qscale), thresh2,
				baseSlot+whole, rows-whole, len(qc)/32, 0, &surv[2*n])
		}
		return n
	}
	return fusedScanInt8AVX2(&qc[0], &codes[0], &pks[0], &offs[0], &qf[0],
		qSum, qNorm2, float64(qmin), float64(qscale), thresh2,
		baseSlot, rows, len(qc)/32, len(qc)%32, &surv[0])
}

func fusedScanCandidateAVX2(qc []int16, x *VecIndex, slots []int32, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	if quadAVX2 && len(qc) >= 32 && len(qc)%32 == 0 && len(slots) >= 4 && !(thresh2 < 0) {
		whole := len(slots) &^ 3
		n := fusedScanQuadCandidatesAVX2(&qc[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
			qSum, qNorm2, float64(qmin), float64(qscale), thresh2, &slots[0], whole, len(qc)/32, &surv[0])
		if whole < len(slots) {
			n += fusedScanCandidatesAVX2(&qc[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
				qSum, qNorm2, float64(qmin), float64(qscale), thresh2,
				&slots[whole], len(slots)-whole, len(qc)/32, 0, &surv[2*n])
		}
		return n
	}
	return fusedScanCandidatesAVX2(&qc[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
		qSum, qNorm2, float64(qmin), float64(qscale), thresh2,
		&slots[0], len(slots), len(qc)/32, len(qc)%32, &surv[0])
}
