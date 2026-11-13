package engine

import "os"

// AVX-VNNI uses VEX-encoded 256-bit instructions and needs only XMM/YMM
// OS state. It is independent of AVX512-VNNI and does not enable category
// code copies. Keep the AVX2/FMA requirements of the surrounding scan.
func avxVNNISupported(id func(uint32, uint32) (uint32, uint32, uint32, uint32), xcr0 func() uint32) bool {
	maxLeaf, _, _, _ := id(0, 0)
	if maxLeaf < 7 {
		return false
	}
	_, _, c1, _ := id(1, 0)
	const required = 1<<12 | 1<<27 | 1<<28 // FMA, OSXSAVE, AVX
	if c1&required != required {
		return false
	}
	if xcr0()&6 != 6 {
		return false
	}
	maxSubleaf, b7, _, _ := id(7, 0)
	if b7&(1<<5) == 0 || maxSubleaf < 1 { // AVX2 and CPUID subleaf 1
		return false
	}
	a71, _, _, _ := id(7, 1)
	return a71&(1<<4) != 0 // AVX-VNNI
}

func hasAVXVNNI() bool {
	if os.Getenv("MVDB_NO_SIMD") != "" || os.Getenv("MVDB_NO_VNNI") != "" || os.Getenv("MVDB_NO_AVXVNNI") != "" {
		return false
	}
	return avxVNNISupported(cpuid, xgetbv0)
}

var useAVXVNNI = hasAVXVNNI()

// The query is biased by -128, with the same qminAdj correction and
// conservative floating-point allowance as the AVX512-VNNI path. Integer
// accumulation is exact. Only the final folded-distance association can
// differ from the unshifted AVX2 reference; survivors still get exact L2.
// blocks counts 32-byte blocks and tail is at most 31 elements.
//
//go:noescape
func fusedScanInt8AVXVNNI(qc8 *int8, codes *byte, pks *int64, offs *int64, qf *float64, qSum, qNorm2, qminAdj, qscale, thresh2 float64, baseSlot, rows, blocks, tail int, surv *int64) int

//go:noescape
func fusedScanCandidatesAVXVNNI(qc8 *int8, codes *byte, pks *int64, offs *int64, qf *float64, qSum, qNorm2, qminAdj, qscale, thresh2 float64, slots *int32, rows, blocks, tail int, surv *int64) int

func fusedScanAVXVNNI(qc8 []int8, x *VecIndex, baseSlot int32, rows int, qSum, qNorm2, qminAdj, qscale, thresh2 float64, surv []int64) int {
	return fusedScanBlockAVXVNNI(qc8, x.codes, x.pks, x.off, x.qf, int(baseSlot), rows, qSum, qNorm2, qminAdj, qscale, thresh2, surv)
}

func fusedScanBlockAVXVNNI(qc8 []int8, codes []byte, pks, offs []int64, qf []float64, baseSlot, rows int, qSum, qNorm2, qminAdj, qscale, thresh2 float64, surv []int64) int {
	if rows == 0 {
		return 0
	}
	return fusedScanInt8AVXVNNI(&qc8[0], &codes[0], &pks[0], &offs[0], &qf[0],
		qSum, qNorm2, qminAdj, qscale, thresh2,
		baseSlot, rows, len(qc8)/32, len(qc8)%32, &surv[0])
}

func fusedScanCandidateAVXVNNI(qc8 []int8, x *VecIndex, slots []int32, qSum, qNorm2, qminAdj, qscale, thresh2 float64, surv []int64) int {
	if len(slots) == 0 {
		return 0
	}
	return fusedScanCandidatesAVXVNNI(&qc8[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
		qSum, qNorm2, qminAdj, qscale, thresh2,
		&slots[0], len(slots), len(qc8)/32, len(qc8)%32, &surv[0])
}
