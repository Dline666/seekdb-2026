//go:build !amd64

package engine

// useSIMD is a variable only so tests can toggle it on every platform.
var useSIMD = false

func l2sqSIMD(a []float64, b []float32) float64 { panic("no SIMD kernel on this platform") }

func l2sqSIMDBytes(a []float64, b []byte) float64 { panic("no SIMD kernel on this platform") }

func l2sqGatherBytes(a []float64, base []byte, offs []int64, out []float64) {
	panic("no SIMD kernel on this platform")
}

// dotInt8Gather sets out[r] to the dot product of q with the codes at
// codes[offs[r]:offs[r]+len(q)] for every row r, using the portable scalar
// loop (see dotInt8GatherScalar in prefilter.go).
func dotInt8Gather(q []float32, codes []byte, offs []int64, out []float32) {
	dotInt8GatherScalar(q, codes, offs, out)
}

// dotInt8GatherInt6 sets out[r] to the exact integer dot product of the
// int8 query codes qc (in [0,63]) with the codes at codes[offs[r]:]
// for every row r, using the portable scalar loop.
func dotInt8GatherInt6(qc []int8, codes []byte, offs []int64, out []int32) {
	dotInt8GatherInt6Scalar(qc, codes, offs, out)
}

// dotInt8GatherInt sets out[r] to the exact integer dot product of the
// int16 query codes qc with the codes at codes[offs[r]:offs[r]+len(qc)]
// for every row r, using the portable scalar loop.
func dotInt8GatherInt(qc []int16, codes []byte, offs []int64, out []int32) {
	dotInt8GatherIntScalar(qc, codes, offs, out)
}

// fusedScanInt8 runs one fused scan batch (see fusedScanInt8AVX2) using
// the portable scalar reference, which makes identical keep/drop
// decisions.
func fusedScanInt8(qc []int16, x *VecIndex, baseSlot int32, rows int, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	return fusedScanInt8Scalar(qc, x, baseSlot, rows, qSum, qNorm2, qmin, qscale, thresh2, surv)
}

// fusedScanInt8Block runs one fused scan batch over a compacted category
// block (see catBlock) using the portable scalar reference.
func fusedScanInt8Block(qc []int16, codes []byte, pks, offs []int64, qf []float64, baseSlot, rows int, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	return fusedScanInt8BlockScalar(qc, codes, pks, offs, qf, baseSlot, rows, qSum, qNorm2, qmin, qscale, thresh2, surv)
}
