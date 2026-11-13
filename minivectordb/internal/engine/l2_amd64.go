package engine

import (
	"encoding/binary"
	"math"
	"os"
	"unsafe"
)

func cpuid(eax, ecx uint32) (a, b, c, d uint32)
func xgetbv0() uint32

// l2sqBlocksAVX2 sums the squared distance over blocks of 16 elements.
// The second operand points at the stored little-endian float32 values;
// it is declared as a byte pointer so the kernel can also be fed the
// encoded vector straight out of the log, without a decode step.
//
//go:noescape
func l2sqBlocksAVX2(a *float64, b *byte, blocks int) float64

// l2sqGatherAVX2 runs l2sqBlocksAVX2 for rows vectors at base+offs[r] and
// stores row r's sum in out[r].
//
//go:noescape
func l2sqGatherAVX2(a *float64, base *byte, offs *int64, blocks, rows int, out *float64)

// dotInt8GatherAVX2 stores, for each row r, the dot product of q with the
// codes at codes[offs[r]:] over the first blocks*32 elements in out[r].
//
//go:noescape
func dotInt8GatherAVX2(q *float32, codes *byte, offs *int64, blocks, rows int, out *float32)

// dotInt8GatherInt6AVX2 stores, for each row r, the exact integer dot
// product of the int8 query codes (in [0,63]) with the codes at
// codes[offs[r]:] over the first blocks*32 elements in out[r].
//
//go:noescape
func dotInt8GatherInt6AVX2(q *int8, codes *byte, offs *int64, blocks, rows int, out *int32)

// dotInt8GatherIntAVX2 stores, for each row r, the exact integer dot
// product of the int16 query codes with the codes at codes[offs[r]:]
// over the first blocks*32 elements in out[r].
//
//go:noescape
func dotInt8GatherIntAVX2(q *int16, codes *byte, offs *int64, blocks, rows int, out *int32)

// fusedScanInt8AVX2 is the fused single-pass scan kernel: for rows
// sequential slots starting at baseSlot it computes the exact integer dot
// of the int16 query codes against each slot's codes (blocks*32 elements
// with AVX2, tail elements scalar), expands it to the approximate squared
// distance with the slot's folded constants from qf (four float64s per
// slot: m2, t2, s2, cRow — see foldQparam), and keeps the rows whose
// approximate distance does not exceed thresh2. Survivors are written
// interleaved as (log offset, primary key) pairs into surv; the return
// value is the survivor count. pks, offs and qf are indexed by slot; the
// codes cursor runs over codes[baseSlot*dim:] without an offset array.
// The caller guarantees the fast-path gates (no candidate set, no overlay,
// no WHERE, no tombstones, live == len(pks)) and that pks, offs, qf and
// codes cover every visited slot. dhat2 is computed with the same
// association order as foldDist2, so the keep/drop decisions agree with
// the scalar reference bit for bit (including NaN: kept).
//
//go:noescape
func fusedScanInt8AVX2(qc *int16, codes *byte, pks *int64, offs *int64, qf *float64, qSum, qNorm2, qmin, qscale, thresh2 float64, baseSlot, rows, blocks, tail int, surv *int64) int

// fusedScanInt8VNNI is the VNNI variant of fusedScanInt8AVX2: same
// contract, but the integer dot uses one VPDPBUSD per 64 codes and the
// query codes enter biased by -128 (qc8[i] = qc[i]-128, signed int8)
// with qminAdj = qmin + 128*qscale absorbing the bias (see the asm
// comment). blocks64 counts 64-element blocks; tail is the scalar
// remainder. Only called when useVNNI is true.
//
//go:noescape
func fusedScanInt8VNNI(qc8 *int8, codes *byte, pks *int64, offs *int64, qf *float64, qSum, qNorm2, qminAdj, qscale, thresh2 float64, baseSlot, rows, blocks64, tail int, surv *int64) int

// fusedScanInt8VNNI256 is the dim-256 fast path of fusedScanInt8VNNI:
// same contract, but the dot loop is fully unrolled for exactly 4 blocks
// of 64 codes with the query preloaded into Z16-Z19. Only called when
// blocks64 == 4 && tail == 0; the Go wrapper enforces that.
//
//go:noescape
func fusedScanInt8VNNI256(qc8 *int8, codes *byte, pks *int64, offs *int64, qf *float64, qSum, qNorm2, qminAdj, qscale, thresh2 float64, baseSlot, rows, blocks64, tail int, surv *int64) int

// fusedScanVNNI runs one fused scan batch with the VNNI kernel (see
// fusedScanInt8VNNI). qc8 holds the biased query codes (qc[i]-128) and
// qminAdj the bias-absorbing qmin (qmin + 128*qscale); both are built
// once per query by the caller. The caller must check useVNNI before
// calling; without VNNI the asm entry would fault. At dim 256
// (blocks64 == 4, tail == 0) the unrolled fast path runs instead.
func fusedScanVNNI(qc8 []int8, x *VecIndex, baseSlot int32, rows int, qSum, qNorm2, qminAdj, qscale float64, thresh2 float64, surv []int64) int {
	if rows == 0 {
		return 0
	}
	blocks64 := len(qc8) / 64
	tail := len(qc8) - blocks64*64
	if blocks64 == 4 && tail == 0 {
		return fusedScanInt8VNNI256(&qc8[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
			qSum, qNorm2, qminAdj, qscale, thresh2,
			int(baseSlot), rows, blocks64, tail, &surv[0])
	}
	return fusedScanInt8VNNI(&qc8[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
		qSum, qNorm2, qminAdj, qscale, thresh2,
		int(baseSlot), rows, blocks64, tail, &surv[0])
}

// hasAVX512VNNI reports whether the CPU and OS support AVX512_VNNI with
// full ZMM state, so the VPDPBUSD fused kernel can be used. CPUID.(7,0)
// ECX bit 11 is AVX512_VNNI. MVDB_NO_VNNI=1 forces the AVX2 fused
// kernel (fallback testing). The check is done once at startup; a binary
// built with the VNNI kernel never executes VPDPBUSD on a CPU that
// lacks it.
func hasAVX512VNNI() bool {
	if os.Getenv("MVDB_NO_VNNI") != "" {
		return false
	}
	if !hasAVX512() {
		return false
	}
	_, _, c7, _ := cpuid(7, 0)
	return c7&(1<<11) != 0
}

var useVNNI = hasAVX512VNNI()

// fusedScanInt8Block is fusedScanInt8 over caller-owned sequential slices
// instead of a VecIndex: the fused kernel over rows sequential rows of a
// compacted category block (see catBlock), starting at baseSlot. The asm
// kernel takes raw pointers, so the block's slices plug in exactly as the
// index's arrays do (baseSlot indexes codes, pks, offs and qf identically);
// keep/drop decisions agree with the scalar reference bit for bit, as with
// fusedScanInt8. When AVX2 is unavailable it falls back to the portable
// scalar reference.
func fusedScanInt8Block(qc []int16, codes []byte, pks, offs []int64, qf []float64, baseSlot, rows int, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	if rows == 0 {
		return 0
	}
	if !useSIMD {
		return fusedScanInt8BlockScalar(qc, codes, pks, offs, qf, baseSlot, rows, qSum, qNorm2, qmin, qscale, thresh2, surv)
	}
	blocks := len(qc) / 32
	tail := len(qc) - blocks*32
	return fusedScanInt8AVX2(&qc[0], &codes[0], &pks[0], &offs[0], &qf[0],
		qSum, qNorm2, float64(qmin), float64(qscale), thresh2,
		baseSlot, rows, blocks, tail, &surv[0])
}

// fusedScanInt8BlockVNNI is the VNNI variant of fusedScanInt8Block: the
// same fused kernel over a compacted category block, but with the
// VPDPBUSD dot (see fusedScanInt8VNNI). qc8 holds the biased query
// codes (qc[i]-128) and qminAdj the bias-absorbing qmin
// (qmin + 128*qscale); both are built once per query by the caller.
// The caller must check useVNNI before calling. Keep/drop decisions
// agree with fusedScanInt8BlockScalar bit for bit: the bias folds into
// qminAdj exactly as in the unfiltered VNNI path.
func fusedScanInt8BlockVNNI(qc8 []int8, codes []byte, pks, offs []int64, qf []float64, baseSlot, rows int, qSum, qNorm2, qminAdj, qscale float64, thresh2 float64, surv []int64) int {
	if rows == 0 {
		return 0
	}
	blocks64 := len(qc8) / 64
	tail := len(qc8) - blocks64*64
	return fusedScanInt8VNNI(&qc8[0], &codes[0], &pks[0], &offs[0], &qf[0],
		qSum, qNorm2, qminAdj, qscale, thresh2,
		baseSlot, rows, blocks64, tail, &surv[0])
}
// fusedScanInt8 runs one fused scan batch over rows sequential slots
// starting at baseSlot (see fusedScanInt8AVX2). When AVX2 is unavailable
// it falls back to the portable scalar reference, which makes identical
// keep/drop decisions.
func fusedScanInt8(qc []int16, x *VecIndex, baseSlot int32, rows int, qSum, qNorm2 float64, qmin, qscale float32, thresh2 float64, surv []int64) int {
	if rows == 0 {
		return 0
	}
	if !useSIMD {
		return fusedScanInt8Scalar(qc, x, baseSlot, rows, qSum, qNorm2, qmin, qscale, thresh2, surv)
	}
	blocks := len(qc) / 32
	tail := len(qc) - blocks*32
	return fusedScanInt8AVX2(&qc[0], &x.codes[0], &x.pks[0], &x.off[0], &x.qf[0],
		qSum, qNorm2, float64(qmin), float64(qscale), thresh2,
		int(baseSlot), rows, blocks, tail, &surv[0])
}

// dotInt8GatherInt6 sets out[r] to the exact integer dot product of the
// int8 query codes qc (in [0,63]) with the codes at codes[offs[r]:]
// for every row r. When AVX2 is available the kernel above handles whole
// 32-element blocks and the scalar tail is added in order, also in integer
// arithmetic; otherwise the portable scalar loop covers the whole vector.
// The result is exact: the only approximation in the prefilter is the
// query quantization itself, whose error the prefilter bound absorbs.
func dotInt8GatherInt6(qc []int8, codes []byte, offs []int64, out []int32) {
	out = out[:len(offs)]
	for r := range out {
		out[r] = 0
	}
	if len(qc) == 0 || len(offs) == 0 {
		return
	}
	blocks := 0
	if useSIMD {
		blocks = len(qc) / 32
		if blocks > 0 {
			dotInt8GatherInt6AVX2(&qc[0], &codes[0], &offs[0], blocks, len(offs), &out[0])
		}
	}
	for i := blocks * 32; i < len(qc); i++ {
		qi := int32(qc[i])
		for r, o := range offs {
			out[r] += qi * int32(codes[o+int64(i)])
		}
	}
}

// dotInt8GatherIntAVX512 is the AVX-512 variant of dotInt8GatherIntAVX2:
// same contract, but blocks count 64-element blocks and one iteration
// covers 64 codes. Only called when useAVX512 is true (CPU + OS support
// AVX512F/AVX512BW with ZMM state); see hasAVX512.
//
//go:noescape
func dotInt8GatherIntAVX512(q *int16, codes *byte, offs *int64, blocks, rows int, out *int32)

// dotInt8GatherInt sets out[r] to the exact integer dot product of the
// int16 query codes qc with the codes at codes[offs[r]:offs[r]+len(qc)]
// for every row r. When AVX-512 is available the 512-bit kernel handles
// whole 64-element blocks; otherwise, when AVX2 is available, the AVX2
// kernel handles whole 32-element blocks; otherwise the portable scalar
// loop covers the whole vector. The scalar tail (elements past the last
// whole block) is added in order, also in integer arithmetic. The result
// is exact on every path: the only approximation in the prefilter is the
// query quantization itself, whose error the prefilter bound absorbs.
func dotInt8GatherInt(qc []int16, codes []byte, offs []int64, out []int32) {
	out = out[:len(offs)]
	for r := range out {
		out[r] = 0
	}
	if len(qc) == 0 || len(offs) == 0 {
		return
	}
	elem := 0
	if useAVX512 {
		if b := len(qc) / 64; b > 0 {
			dotInt8GatherIntAVX512(&qc[0], &codes[0], &offs[0], b, len(offs), &out[0])
			elem = b * 64
		}
	} else if useSIMD {
		if b := len(qc) / 32; b > 0 {
			dotInt8GatherIntAVX2(&qc[0], &codes[0], &offs[0], b, len(offs), &out[0])
			elem = b * 32
		}
	}
	for i := elem; i < len(qc); i++ {
		qi := int32(qc[i])
		for r, o := range offs {
			out[r] += qi * int32(codes[o+int64(i)])
		}
	}
}

// dotInt8Gather sets out[r] to the dot product of q with the codes at
// codes[offs[r]:offs[r]+len(q)] for every row r. When AVX2+FMA is available
// the kernel above handles whole 32-element blocks and the scalar tail is
// added in order; otherwise the portable scalar loop covers the whole
// vector. The result feeds the int8 prefilter bound, which absorbs float
// rounding, so the two paths need not agree bit for bit. The caller
// guarantees that codes[offs[r]:offs[r]+len(q)] is in range for every row.
func dotInt8Gather(q []float32, codes []byte, offs []int64, out []float32) {
	out = out[:len(offs)]
	for r := range out {
		out[r] = 0
	}
	if len(q) == 0 || len(offs) == 0 {
		return
	}
	blocks := 0
	if useSIMD {
		blocks = len(q) / 32
		if blocks > 0 {
			dotInt8GatherAVX2(&q[0], &codes[0], &offs[0], blocks, len(offs), &out[0])
		}
	}
	for i := blocks * 32; i < len(q); i++ {
		for r, o := range offs {
			out[r] += q[i] * float32(codes[o+int64(i)])
		}
	}
}

// l2sqGatherBytes sets out[r] to l2sqSIMDBytes(a, base[offs[r]:]) for every
// row r: the same kernel per row, then the scalar tail added in order, so
// each result is bit-identical to the one-row path. The caller guarantees
// that base[offs[r] : offs[r]+4*len(a)] is in range for every row.
func l2sqGatherBytes(a []float64, base []byte, offs []int64, out []float64) {
	out = out[:len(offs)]
	n := len(a)
	blocks := n / 16
	if blocks > 0 && len(offs) > 0 {
		l2sqGatherAVX2(&a[0], &base[0], &offs[0], blocks, len(offs), &out[0])
	} else {
		for r := range out {
			out[r] = 0
		}
	}
	for i := blocks * 16; i < n; i++ {
		for r, o := range offs {
			d := a[i] - float64(math.Float32frombits(binary.LittleEndian.Uint32(base[o+4*int64(i):])))
			out[r] += d * d
		}
	}
}

// hasAVX2FMA reports whether the CPU and OS support AVX2 and FMA, so the
// vector kernel can be used. MVDB_NO_SIMD=1 forces the portable kernel.
func hasAVX2FMA() bool {
	if os.Getenv("MVDB_NO_SIMD") != "" {
		return false
	}
	max, _, _, _ := cpuid(0, 0)
	if max < 7 {
		return false
	}
	_, _, c1, _ := cpuid(1, 0)
	const fma, osxsave, avx = 1 << 12, 1 << 27, 1 << 28
	if c1&fma == 0 || c1&osxsave == 0 || c1&avx == 0 {
		return false
	}
	if xgetbv0()&6 != 6 { // XMM and YMM state enabled by the OS
		return false
	}
	_, b7, _, _ := cpuid(7, 0)
	return b7&(1<<5) != 0
}

var useSIMD = hasAVX2FMA()

// hasAVX512 reports whether the CPU and OS support AVX-512F and AVX-512BW
// with full ZMM state, so the 512-bit integer dot kernel can be used.
// The kernel needs AVX512BW (VPMOVZXBW on a zmm) as well as AVX512F
// (VPMADDWD/VPADDD on a zmm). The OS must enable the opmask register
// (XCR0 bit 5), ZMM_Hi256 (bit 6) and Hi16_ZMM (bit 7); without those, a
// zmm instruction faults even on a CPU that has the feature bits.
// MVDB_NO_AVX512=1 forces the AVX2 kernel (fallback testing), and
// MVDB_NO_SIMD=1 disables SIMD altogether (via hasAVX2FMA). The check is
// done once at startup; a binary built with the AVX-512 kernel never
// executes a zmm instruction on a CPU that lacks it.
func hasAVX512() bool {
	if os.Getenv("MVDB_NO_AVX512") != "" {
		return false
	}
	if !hasAVX2FMA() {
		return false
	}
	max, _, _, _ := cpuid(0, 0)
	if max < 7 {
		return false
	}
	_, b7, _, _ := cpuid(7, 0)
	const avx512f, avx512bw = 1 << 16, 1 << 30
	if b7&avx512f == 0 || b7&avx512bw == 0 {
		return false
	}
	// XCR0 bits 5 (opmask), 6 (ZMM_Hi256), 7 (Hi16_ZMM) all set.
	return xgetbv0()&0xE0 == 0xE0
}

var useAVX512 = hasAVX512()

// l2sqSIMD is the AVX2 kernel for whole 16-element blocks plus a scalar
// tail added in order.
func l2sqSIMD(a []float64, b []float32) float64 {
	n := len(a)
	blocks := n / 16
	var s float64
	if blocks > 0 {
		// The kernel reads the stored float32 values as bytes; the two
		// views describe the same little-endian words.
		s = l2sqBlocksAVX2(&a[0], (*byte)(unsafe.Pointer(&b[0])), blocks)
	}
	for i := blocks * 16; i < n; i++ {
		d := a[i] - float64(b[i])
		s += d * d
	}
	return s
}

// l2sqSIMDBytes is l2sqSIMD against an encoded little-endian float32 vector.
// The kernel converts the stored floats itself, so the encoded bytes are read
// straight from the log with no decode step and no copy.
func l2sqSIMDBytes(a []float64, b []byte) float64 {
	n := len(a)
	blocks := n / 16
	var s float64
	if blocks > 0 {
		s = l2sqBlocksAVX2(&a[0], &b[0], blocks)
	}
	for i := blocks * 16; i < n; i++ {
		d := a[i] - float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:])))
		s += d * d
	}
	return s
}
