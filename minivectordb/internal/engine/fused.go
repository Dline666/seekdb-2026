package engine

import "os"

// The candidate kernel reads the existing quantized index directly instead
// of retaining a second copy for each categorical value.
var fusedCandidatesEnabled = os.Getenv("MVDB_NO_FUSED_CANDIDATES") == ""

func fusedScanCandidatesScalar(qc []int16, x *VecIndex, slots []int32, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	n := 0
	for _, s := range slots {
		n += fusedScanInt8Scalar(qc, x, s, 1, qSum, qNorm2, qmin, qscale, thresh2, surv[2*n:])
	}
	return n
}

// fusedScanInt8Scalar is the portable reference for the fused single-pass
// scan kernel (fusedScanInt8AVX2): over rows sequential slots starting at
// baseSlot it computes the exact integer dot of the quantized query against
// each slot's codes, expands it to the approximate squared distance with
// the slot's folded constants (foldQparam), and keeps the rows whose
// approximate distance does not exceed thresh2. Survivors are written
// interleaved as (log offset, primary key) pairs into surv; the return
// value is the survivor count. Slot order is preserved, and the caller
// runs the exact kernel on the survivors.
//
// It is also the fallback the amd64 wrapper uses when AVX2 is unavailable,
// and the reference the asm test compares against bit for bit (same
// association order as foldDist2, same keep/drop decisions including NaN:
// a non-finite dhat2 is kept, matching the Go threshold loop).
// fusedScanInt8BlockScalar is the portable reference for fusedScanInt8Block:
// same contract over caller-owned slices (codes row-major with row length
// len(qc), qf four float64s per row), same keep/drop decisions including
// NaN (a non-finite dhat2 is kept).
func fusedScanInt8BlockScalar(qc []int16, codes []byte, pks, offs []int64, qf []float64, baseSlot, rows int, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	if rows == 0 {
		return 0
	}
	dim := len(qc)
	m := 0
	for r := 0; r < rows; r++ {
		s := baseSlot + r
		var dot int32
		for i, qi := range qc {
			dot += int32(qi) * int32(codes[s*dim+i])
		}
		q := qf[4*s:]
		if d := foldDist2(qSum, qNorm2, qmin, qscale, dot, q[0], q[1], q[2], q[3]); d > thresh2 {
			continue
		}
		surv[2*m] = offs[s]
		surv[2*m+1] = pks[s]
		m++
	}
	return m
}

func fusedScanInt8Scalar(qc []int16, x *VecIndex, baseSlot int32, rows int, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	if rows == 0 {
		return 0
	}
	m := 0
	for r := 0; r < rows; r++ {
		s := int(baseSlot) + r
		var dot int32
		for i, qi := range qc {
			dot += int32(qi) * int32(x.codes[s*x.dim+i])
		}
		qf := x.qf[4*s:]
		if d := foldDist2(qSum, qNorm2, qmin, qscale, dot, qf[0], qf[1], qf[2], qf[3]); d > thresh2 {
			continue
		}
		surv[2*m] = x.off[s]
		surv[2*m+1] = x.pks[s]
		m++
	}
	return m
}
