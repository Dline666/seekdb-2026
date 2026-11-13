//go:build !amd64

package engine

var useAVXVNNI = false

func fusedScanAVXVNNI(qc8 []int8, x *VecIndex, baseSlot int32, rows int, qSum, qNorm2, qminAdj, qscale, thresh2 float64, surv []int64) int {
	panic("AVX-VNNI is unavailable on this platform")
}

func fusedScanBlockAVXVNNI(qc8 []int8, codes []byte, pks, offs []int64, qf []float64, baseSlot, rows int, qSum, qNorm2, qminAdj, qscale, thresh2 float64, surv []int64) int {
	panic("AVX-VNNI is unavailable on this platform")
}

func fusedScanCandidateAVXVNNI(qc8 []int8, x *VecIndex, slots []int32, qSum, qNorm2, qminAdj, qscale, thresh2 float64, surv []int64) int {
	panic("AVX-VNNI is unavailable on this platform")
}
