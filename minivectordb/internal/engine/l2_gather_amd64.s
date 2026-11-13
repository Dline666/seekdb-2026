#include "textflag.h"

// func l2sqGatherAVX2(a *float64, base *byte, offs *int64, blocks, rows int, out *float64)
//
// l2sqBlocksAVX2 for rows vectors at base+offs[r], one result per row in
// out[r]. Each row uses exactly the lane assignment and reduction order of
// l2sqBlocksAVX2, so out[r] is bit-identical to calling it on that row; one
// call per batch removes the per-row call overhead and lets the loads of the
// next row overlap the reduction of the previous one.
TEXT ·l2sqGatherAVX2(SB), NOSPLIT, $0-48
	MOVQ a+0(FP), R8
	MOVQ base+8(FP), R12
	MOVQ offs+16(FP), R13
	MOVQ blocks+24(FP), R9
	MOVQ rows+32(FP), R10
	MOVQ out+40(FP), R11
	TESTQ R10, R10
	JZ done
row:
	MOVQ R8, SI
	MOVQ R12, DI
	ADDQ 0(R13), DI
	MOVQ R9, CX
	VXORPD Y0, Y0, Y0
	VXORPD Y1, Y1, Y1
	VXORPD Y2, Y2, Y2
	VXORPD Y3, Y3, Y3
loop:
	VCVTPS2PD 0(DI), Y4
	VCVTPS2PD 16(DI), Y5
	VCVTPS2PD 32(DI), Y6
	VCVTPS2PD 48(DI), Y7
	VSUBPD 0(SI), Y4, Y4
	VSUBPD 32(SI), Y5, Y5
	VSUBPD 64(SI), Y6, Y6
	VSUBPD 96(SI), Y7, Y7
	VFMADD231PD Y4, Y4, Y0
	VFMADD231PD Y5, Y5, Y1
	VFMADD231PD Y6, Y6, Y2
	VFMADD231PD Y7, Y7, Y3
	ADDQ $128, SI
	ADDQ $64, DI
	DECQ CX
	JNZ loop
	VADDPD Y1, Y0, Y0
	VADDPD Y3, Y2, Y2
	VADDPD Y2, Y0, Y0
	VEXTRACTF128 $1, Y0, X1
	VADDPD X1, X0, X0
	VHADDPD X0, X0, X0
	MOVSD X0, 0(R11)
	ADDQ $8, R11
	ADDQ $8, R13
	DECQ R10
	JNZ row
done:
	VZEROUPPER
	RET

// func dotInt8GatherAVX2(q *float32, codes *byte, offs *int64, blocks, rows int, out *float32)
//
// out[r] = sum_i q[i]*float32(codes[offs[r]+i]) over the first blocks*32
// elements. The codes are unsigned bytes in [0,255]. Each group of 8 codes
// is widened to float32 and accumulated with FMA against the query, four
// independent accumulators per row.
//
// This kernel is an APPROXIMATION input only: its result feeds the int8
// prefilter bound, which absorbs float32 rounding, so unlike
// l2sqGatherAVX2 it need not agree bit for bit with any scalar path. The
// caller adds the scalar tail (elements past blocks*32) in Go.
TEXT ·dotInt8GatherAVX2(SB), NOSPLIT, $0-48
	MOVQ q+0(FP), R8
	MOVQ codes+8(FP), R12
	MOVQ offs+16(FP), R13
	MOVQ blocks+24(FP), R9
	MOVQ rows+32(FP), R10
	MOVQ out+40(FP), R11
	TESTQ R10, R10
	JZ done
row:
	MOVQ R8, SI
	MOVQ R12, DI
	ADDQ 0(R13), DI
	MOVQ R9, CX
	VXORPS Y8, Y8, Y8
	VXORPS Y9, Y9, Y9
	VXORPS Y10, Y10, Y10
	VXORPS Y11, Y11, Y11
	TESTQ CX, CX
	JZ reduce
loop:
	MOVQ 0(DI), X0
	VPMOVZXBD X0, Y4
	VCVTDQ2PS Y4, Y4
	VFMADD231PS 0(SI), Y4, Y8
	MOVQ 8(DI), X1
	VPMOVZXBD X1, Y5
	VCVTDQ2PS Y5, Y5
	VFMADD231PS 32(SI), Y5, Y9
	MOVQ 16(DI), X2
	VPMOVZXBD X2, Y6
	VCVTDQ2PS Y6, Y6
	VFMADD231PS 64(SI), Y6, Y10
	MOVQ 24(DI), X3
	VPMOVZXBD X3, Y7
	VCVTDQ2PS Y7, Y7
	VFMADD231PS 96(SI), Y7, Y11
	ADDQ $128, SI
	ADDQ $32, DI
	DECQ CX
	JNZ loop
reduce:
	VADDPS Y9, Y8, Y8
	VADDPS Y11, Y10, Y10
	VADDPS Y10, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VADDPS X1, X8, X8
	VHADDPS X8, X8, X8
	VHADDPS X8, X8, X8
	MOVSS X8, 0(R11)
	ADDQ $4, R11
	ADDQ $8, R13
	DECQ R10
	JNZ row
done:
	VZEROUPPER
	RET

// func dotInt8GatherIntAVX2(q *int16, codes *byte, offs *int64, blocks, rows int, out *int32)
//
// out[r] = sum_i q[i]*codes[offs[r]+i] over the first blocks*32
// elements, in EXACT int32 arithmetic. q holds the query codes as int16
// in [0,255]; codes are unsigned bytes. Per 32 codes: two VPMOVZXBW widen
// the codes to int16, two VPMADDWD multiply-accumulate against the query
// int16 (read straight from memory), two VPADDD accumulate — 6
// instructions where the float kernel needs 16 (load + widen + convert +
// FMA, four times). The total fits int32 easily: at most
// 255*255*dim (16.6M at dim 1024), and each VPMADDWD pair sum is at most
// 255*255*2. The caller adds the scalar tail (elements past blocks*32)
// in Go, also in integer arithmetic, so the whole dot is exact; the only
// approximation is the query quantization itself, whose error the
// prefilter bound absorbs (see prefilterThreshold).
TEXT ·dotInt8GatherIntAVX2(SB), NOSPLIT, $0-48
	MOVQ q+0(FP), R8
	MOVQ codes+8(FP), R12
	MOVQ offs+16(FP), R13
	MOVQ blocks+24(FP), R9
	MOVQ rows+32(FP), R10
	MOVQ out+40(FP), R11
	TESTQ R10, R10
	JZ done
row:
	MOVQ R8, SI
	MOVQ R12, DI
	ADDQ 0(R13), DI
	MOVQ R9, CX
	VPXOR Y8, Y8, Y8
	VPXOR Y9, Y9, Y9
	TESTQ CX, CX
	JZ reduce
loop:
	VPMOVZXBW 0(DI), Y4
	VPMADDWD 0(SI), Y4, Y4
	VPADDD Y4, Y8, Y8
	VPMOVZXBW 16(DI), Y5
	VPMADDWD 32(SI), Y5, Y5
	VPADDD Y5, Y9, Y9
	ADDQ $64, SI
	ADDQ $32, DI
	DECQ CX
	JNZ loop
reduce:
	VPADDD Y9, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	MOVL X8, 0(R11)
	ADDQ $4, R11
	ADDQ $8, R13
	DECQ R10
	JNZ row
done:
	VZEROUPPER
	RET

// func fusedScanInt8AVX2(qc *int16, codes *byte, pks *int64, offs *int64, qf *float64,
//   qSum, qNorm2, qmin, qscale, thresh2 float64, baseSlot, rows, blocks, tail int, surv *int64) int
//
// The fused single-pass scan kernel: for rows sequential slots starting at
// baseSlot, the exact integer dot of the int16 query codes against the
// slot's codes (blocks*32 elements with the same 6-instruction/32-code
// loop as dotInt8GatherIntAVX2, tail elements scalar), expanded to the
// approximate squared distance with the slot's folded constants from qf
// (four float64s per slot: m2, t2, s2, cRow) and kept when it does not
// exceed thresh2. Survivors are written interleaved as (log offset,
// primary key) pairs into surv; the return value is the survivor count.
//
// The dot loop's codes cursor runs over codes[baseSlot*dim:] with no
// offset array, and the pks/offs/qf cursors advance one slot per row, so
// the gather loop, the coffs multiply, the dots array, the threshold loop
// and the compaction are all gone: one pass over the slots. dhat2 uses the
// same association order as foldDist2, so keep/drop decisions agree with
// the scalar reference bit for bit; a non-finite dhat2 is kept (UCOMISD
// leaves JA untaken on unordered, matching the Go loop's strict-> test).
// The caller guarantees the fast-path gates (see fusedScanInt8AVX2's Go
// comment); rows == 0 exits immediately.
TEXT ·fusedScanInt8AVX2(SB), NOSPLIT, $0-128
	MOVQ qc+0(FP), R8
	MOVQ blocks+96(FP), R9
	MOVQ tail+104(FP), R12
	// dim = blocks*32 + tail; DI = codes + baseSlot*dim.
	MOVQ R9, CX
	SHLQ $5, CX
	ADDQ R12, CX
	MOVQ baseSlot+80(FP), AX
	IMULQ CX, AX
	MOVQ codes+8(FP), DI
	ADDQ AX, DI
	// pks/offs cursors start at baseSlot; qf cursor at baseSlot*4 float64s.
	MOVQ baseSlot+80(FP), AX
	SHLQ $3, AX
	MOVQ pks+16(FP), R13
	ADDQ AX, R13
	MOVQ offs+24(FP), R14
	ADDQ AX, R14
	MOVQ baseSlot+80(FP), AX
	SHLQ $5, AX
	MOVQ qf+32(FP), R15
	ADDQ AX, R15
	MOVSD qSum+40(FP), X12
	MOVSD qNorm2+48(FP), X13
	MOVSD qmin+56(FP), X14
	MOVSD qscale+64(FP), X15
	MOVSD thresh2+72(FP), X11
	MOVQ rows+88(FP), R10
	MOVQ surv+112(FP), AX
	TESTQ R10, R10
	JZ done
row:
	MOVQ R8, SI
	MOVQ R9, CX
	VPXOR Y8, Y8, Y8
	VPXOR Y9, Y9, Y9
	TESTQ CX, CX
	JZ reduce
loop:
	VPMOVZXBW 0(DI), Y4
	VPMADDWD 0(SI), Y4, Y4
	VPADDD Y4, Y8, Y8
	VPMOVZXBW 16(DI), Y5
	VPMADDWD 32(SI), Y5, Y5
	VPADDD Y5, Y9, Y9
	ADDQ $64, SI
	ADDQ $32, DI
	DECQ CX
	JNZ loop
reduce:
	VPADDD Y9, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	MOVL X8, CX
	MOVQ tail+104(FP), R12
	TESTQ R12, R12
	JZ epilogue
tailLoop:
	MOVWLSX 0(SI), BX
	MOVBLZX 0(DI), DX
	IMULL DX, BX
	ADDL BX, CX
	ADDQ $2, SI
	ADDQ $1, DI
	DECQ R12
	JNZ tailLoop
epilogue:
	CVTSL2SD CX, X5
	MOVSD 0(R15), X6
	VMULSD X12, X6, X6
	VADDSD X13, X6, X6
	VADDSD 24(R15), X6, X6
	MOVSD 8(R15), X7
	VMULSD X14, X7, X7
	VADDSD X7, X6, X6
	MOVSD 16(R15), X10
	VMULSD X15, X10, X10
	VMULSD X5, X10, X10
	VADDSD X10, X6, X6
	UCOMISD X11, X6
	JA next
	MOVQ 0(R14), BX
	MOVQ BX, 0(AX)
	MOVQ 0(R13), BX
	MOVQ BX, 8(AX)
	ADDQ $16, AX
next:
	ADDQ $8, R13
	ADDQ $8, R14
	ADDQ $32, R15
	DECQ R10
	JNZ row
done:
	MOVQ surv+112(FP), CX
	SUBQ CX, AX
	SHRQ $4, AX
	MOVQ AX, ret+120(FP)
	VZEROUPPER
	RET

// func dotInt8GatherInt6AVX2(q *int8, codes *byte, offs *int64, blocks, rows int, out *int32)
//
// out[r] = sum_i q[i]*codes[offs[r]+i] over the first blocks*32
// elements, in EXACT int32 arithmetic. q holds the query codes as signed
// bytes in [0,63]; codes are unsigned bytes. Per 32 codes: one VMOVDQU
// loads the row codes, one VPMADDUBSW multiplies the query codes (signed,
// first operand, read straight from memory) by the row codes (unsigned,
// second operand) and pairwise-sums the products into int16,
// one VPMADDWD against a ones vector widens adjacent pairs into int32,
// one VPADDD accumulates — 4 instructions where the 8-bit integer kernel
// needs 6. Overflow is impossible: each product is at most 63*255 =
// 16065, each pairwise int16 sum at most 32130 < 32767, each widened
// int32 pair sum at most 64260, and the whole dot is at most
// 63*255*dim (4.1M at dim 1024). The caller adds the scalar tail
// (elements past blocks*32) in Go, also in integer arithmetic, so the
// whole dot is exact; the only approximation is the coarser query
// quantization itself, whose error the prefilter bound absorbs (see
// prefilterThreshold; the combined scale becomes maxScale+qscale with
// the 6-bit qscale).
TEXT ·dotInt8GatherInt6AVX2(SB), NOSPLIT, $0-48
	MOVQ q+0(FP), R8
	MOVQ codes+8(FP), R12
	MOVQ offs+16(FP), R13
	MOVQ blocks+24(FP), R9
	MOVQ rows+32(FP), R10
	MOVQ out+40(FP), R11
	TESTQ R10, R10
	JZ done
	// Ones vector: Y6 = 16 x int16(1), for VPMADDWD's pairwise widening
	// sum. VPCMPEQW gives all -1; a logical shift right by 15 turns
	// 0xFFFF into 1.
	VPCMPEQW Y6, Y6, Y6
	VPSRLW $15, Y6, Y6
row:
	MOVQ R8, SI
	MOVQ R12, DI
	ADDQ 0(R13), DI
	MOVQ R9, CX
	VPXOR Y8, Y8, Y8
	TESTQ CX, CX
	JZ reduce
loop:
	VMOVDQU 0(DI), Y4
	VPMADDUBSW 0(SI), Y4, Y4
	VPMADDWD Y4, Y6, Y5
	VPADDD Y5, Y8, Y8
	ADDQ $32, SI
	ADDQ $32, DI
	DECQ CX
	JNZ loop
reduce:
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	MOVL X8, 0(R11)
	ADDQ $4, R11
	ADDQ $8, R13
	DECQ R10
	JNZ row
done:
	VZEROUPPER
	RET

// func dotInt8GatherIntAVX512(q *int16, codes *byte, offs *int64, blocks, rows int, out *int32)
//
// Same contract as dotInt8GatherIntAVX2, but one 64-element block per
// iteration: two VPMOVZXBW widen 32 codes each to int16 in a zmm (a zmm
// widen takes a 32-byte source, not 64), two VPMADDWD multiply-accumulate
// against the query int16 (read straight from memory), two VPADDD
// accumulate — 6 vector instructions per 64 codes vs 12 for AVX2, i.e.
// 2x fewer vector instructions per element. The loop is unrolled two
// blocks at a time (two independent accumulators
// Z8/Z9) with a single-block tail for an odd block count. Exact int32
// arithmetic, so the result is bit-identical to the AVX2 kernel: integer
// addition is associative and the totals fit int32 easily (see the AVX2
// comment). Requires AVX512F + AVX512BW and OS-enabled ZMM state; the Go
// side dispatches here only when hasAVX512 reports true, and the AVX2
// kernel above is the fallback. VZEROUPPER before RET clears the upper
// ZMM state so the caller's SSE code pays no transition penalty.
TEXT ·dotInt8GatherIntAVX512(SB), NOSPLIT, $0-48
	MOVQ q+0(FP), R8
	MOVQ codes+8(FP), R12
	MOVQ offs+16(FP), R13
	MOVQ blocks+24(FP), R9
	MOVQ rows+32(FP), R10
	MOVQ out+40(FP), R11
	TESTQ R10, R10
	JZ done
row:
	MOVQ R8, SI
	MOVQ R12, DI
	ADDQ 0(R13), DI
	MOVQ R9, CX
	VPXORQ Z8, Z8, Z8
	VPXORQ Z9, Z9, Z9
	TESTQ CX, CX
	JZ reduce
pairloop:
	CMPQ CX, $2
	JL single
	VPMOVZXBW 0(DI), Z4
	VPMADDWD 0(SI), Z4, Z4
	VPADDD Z4, Z8, Z8
	VPMOVZXBW 32(DI), Z5
	VPMADDWD 64(SI), Z5, Z5
	VPADDD Z5, Z8, Z8
	VPMOVZXBW 64(DI), Z6
	VPMADDWD 128(SI), Z6, Z6
	VPADDD Z6, Z9, Z9
	VPMOVZXBW 96(DI), Z7
	VPMADDWD 192(SI), Z7, Z7
	VPADDD Z7, Z9, Z9
	ADDQ $256, SI
	ADDQ $128, DI
	SUBQ $2, CX
	JMP pairloop
single:
	TESTQ CX, CX
	JZ reduce
	VPMOVZXBW 0(DI), Z4
	VPMADDWD 0(SI), Z4, Z4
	VPADDD Z4, Z8, Z8
	VPMOVZXBW 32(DI), Z5
	VPMADDWD 64(SI), Z5, Z5
	VPADDD Z5, Z8, Z8
reduce:
	VPADDD Z9, Z8, Z8
	VEXTRACTI64X4 $1, Z8, Y1
	VPADDD Y1, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	MOVL X8, 0(R11)
	ADDQ $4, R11
	ADDQ $8, R13
	DECQ R10
	JNZ row
done:
	VZEROUPPER
	RET

// func fusedScanInt8VNNI(qc8 *int8, codes *byte, pks *int64, offs *int64, qf *float64,
//   qSum, qNorm2, qminAdj, qscale, thresh2 float64, baseSlot, rows, blocks64, tail int, surv *int64) int
//
// The VNNI fused single-pass scan kernel: same contract as
// fusedScanInt8AVX2, but the integer dot uses one VPDPBUSD per 64 codes
// instead of the AVX2 loop's 12 instructions per 64 codes (2x VPMOVZXBW
// + 2x VPMADDWD + 2x VPADDD, twice). VPDPBUSD multiplies UNSIGNED bytes
// (first source) by SIGNED bytes (second source) and sums groups of 4
// into int32 lanes, so the query codes enter biased by -128: qc8[i] =
// qc[i]-128 in [-128,127] (signed int8), the row codes stay unsigned
// bytes. The biased dot is exact int32 arithmetic and differs from the
// true dot by exactly 128*sumC per row; the caller folds that into the
// qmin operand (qminAdj = qmin + 128*qscale), because the epilogue's
// qmin*t2 term has t2 = -2*scale*sumC, so (qmin+128*qscale)*t2 absorbs
// the bias with no per-row work. The dot is therefore bit-identical to
// the AVX2 kernel's dot; only the dhat2 re-association differs, by a
// few float64 ulps (same class as the existing fold — see foldQparam).
// Per-lane overflow is impossible: each lane accumulates at most
// dim/16 products of at most 128*255, i.e. ~2.1M at dim 1024, and the
// total dot is at most 255*255*dim. Requires AVX512_VNNI (+ AVX512F/BW
// and ZMM state); the Go side dispatches here only when hasAVX512VNNI
// reports true, and fusedScanInt8AVX2 is the fallback. Go asm writes
// VPDPBUSD with the destination last (AT&T order): the signed query is
// the first operand, the unsigned codes the second.
//
// Epilogue-leaning (same arithmetic, same association order, so the dot
// and every keep/drop decision are bit-identical to the original kernel):
// the epilogue's three qf loads are folded into the VMULSD memory
// operands, the dead blocks64==0 check after the accumulator zeroing is
// gone (the pair loop's CMPQ/JL and the single-block TESTQ already cover
// it), the tail count is hoisted into R11 (loaded once per call), and
// the offs cursor is kept as a constant pks-relative delta in R14, so
// the per-row loop keeps one cursor bump instead of two. At dim 256 the
// Go wrapper dispatches to fusedScanInt8VNNI256 below, which additionally
// preloads the query and fully unrolls the dot loop.
TEXT ·fusedScanInt8VNNI(SB), NOSPLIT, $0-128
	MOVQ qc8+0(FP), R8
	MOVQ blocks64+96(FP), R9
	MOVQ tail+104(FP), R11
	MOVQ R9, CX
	SHLQ $6, CX
	ADDQ R11, CX
	MOVQ baseSlot+80(FP), AX
	IMULQ CX, AX
	MOVQ codes+8(FP), DI
	ADDQ AX, DI
	// pks/offs cursors start at baseSlot; qf cursor at baseSlot*4 float64s.
	MOVQ baseSlot+80(FP), AX
	SHLQ $3, AX
	MOVQ pks+16(FP), R13
	ADDQ AX, R13
	MOVQ offs+24(FP), R14
	ADDQ AX, R14
	SUBQ R13, R14 // R14 = offs-pks delta (constant: both cursors +8/row)
	MOVQ baseSlot+80(FP), AX
	SHLQ $5, AX
	MOVQ qf+32(FP), R15
	ADDQ AX, R15
	MOVSD qSum+40(FP), X12
	MOVSD qNorm2+48(FP), X13
	MOVSD qminAdj+56(FP), X14
	MOVSD qscale+64(FP), X15
	MOVSD thresh2+72(FP), X11
	MOVQ rows+88(FP), R10
	MOVQ surv+112(FP), AX
	TESTQ R10, R10
	JZ done
row:
	MOVQ R8, SI
	MOVQ R9, CX
	VPXORQ Z8, Z8, Z8
	VPXORQ Z9, Z9, Z9
pairloop:
	CMPQ CX, $2
	JL single
	VMOVDQU64 0(DI), Z4
	VMOVDQU64 0(SI), Z5
	VPDPBUSD Z5, Z4, Z8
	VMOVDQU64 64(DI), Z6
	VMOVDQU64 64(SI), Z7
	VPDPBUSD Z7, Z6, Z9
	ADDQ $128, SI
	ADDQ $128, DI
	SUBQ $2, CX
	JMP pairloop
single:
	TESTQ CX, CX
	JZ reduce
	VMOVDQU64 0(DI), Z4
	VMOVDQU64 0(SI), Z5
	VPDPBUSD Z5, Z4, Z8
	ADDQ $64, SI
	ADDQ $64, DI
reduce:
	VPADDD Z9, Z8, Z8
	VEXTRACTI64X4 $1, Z8, Y1
	VPADDD Y1, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	MOVL X8, CX
	TESTQ R11, R11
	JZ epilogue
	MOVQ R11, R12
tailLoop:
	MOVBLSX 0(SI), BX
	MOVBLZX 0(DI), DX
	IMULL DX, BX
	ADDL BX, CX
	ADDQ $1, SI
	ADDQ $1, DI
	DECQ R12
	JNZ tailLoop
epilogue:
	CVTSL2SD CX, X5
	VMULSD 0(R15), X12, X6
	VADDSD X13, X6, X6
	VADDSD 24(R15), X6, X6
	VMULSD 8(R15), X14, X7
	VADDSD X7, X6, X6
	VMULSD 16(R15), X15, X10
	VMULSD X5, X10, X10
	VADDSD X10, X6, X6
	UCOMISD X11, X6
	JA next
	MOVQ 0(R13)(R14*1), BX
	MOVQ BX, 0(AX)
	MOVQ 0(R13), BX
	MOVQ BX, 8(AX)
	ADDQ $16, AX
next:
	ADDQ $8, R13
	ADDQ $32, R15
	DECQ R10
	JNZ row
done:
	MOVQ surv+112(FP), CX
	SUBQ CX, AX
	SHRQ $4, AX
	MOVQ AX, ret+120(FP)
	VZEROUPPER
	RET

// func fusedScanInt8VNNI256(qc8 *int8, codes *byte, pks *int64, offs *int64, qf *float64,
//   qSum, qNorm2, qminAdj, qscale, thresh2 float64, baseSlot, rows, blocks64, tail int, surv *int64) int
//
// The dim-256 fast path of fusedScanInt8VNNI: same contract and same
// bit-identical dot/epilogue, but the dot loop is fully unrolled for
// exactly 4 blocks of 64 codes and the query is preloaded once per call
// into Z16-Z19, so the per-row loop is 2 loads + 2 VPDPBUSD per 128
// codes with no loop control and no per-row query loads (the pair loop
// costs 12 instructions per 128 codes, 8 of them control/addressing).
// Only called when blocks64 == 4 && tail == 0 (dim 256); the Go wrapper
// enforces that. The query must live in Z16-Z19: the low halves of
// Z10-Z15 are the epilogue's XMM operands/temps (X10-X15), so preloading
// there would clobber qSum/qNorm2/thresh2 (prologue) and be clobbered by
// the epilogue (X10) on the first row.
TEXT ·fusedScanInt8VNNI256(SB), NOSPLIT, $0-128
	MOVQ qc8+0(FP), R8
	MOVQ codes+8(FP), DI
	MOVQ baseSlot+80(FP), AX
	MOVQ AX, CX
	SHLQ $8, CX // baseSlot*256 (dim = 4*64)
	ADDQ CX, DI
	MOVQ baseSlot+80(FP), AX
	SHLQ $3, AX
	MOVQ pks+16(FP), R13
	ADDQ AX, R13
	MOVQ offs+24(FP), R14
	ADDQ AX, R14
	SUBQ R13, R14 // R14 = offs-pks delta (constant: both cursors +8/row)
	MOVQ baseSlot+80(FP), AX
	SHLQ $5, AX
	MOVQ qf+32(FP), R15
	ADDQ AX, R15
	MOVSD qSum+40(FP), X12
	MOVSD qNorm2+48(FP), X13
	MOVSD qminAdj+56(FP), X14
	MOVSD qscale+64(FP), X15
	MOVSD thresh2+72(FP), X11
	VMOVDQU64 0(R8), Z16
	VMOVDQU64 64(R8), Z17
	VMOVDQU64 128(R8), Z18
	VMOVDQU64 192(R8), Z19
	MOVQ rows+88(FP), R10
	MOVQ surv+112(FP), AX
	TESTQ R10, R10
	JZ done
row:
	VPXORQ Z8, Z8, Z8
	VPXORQ Z9, Z9, Z9
	VMOVDQU64 0(DI), Z4
	VPDPBUSD Z16, Z4, Z8
	VMOVDQU64 64(DI), Z5
	VPDPBUSD Z17, Z5, Z8
	VMOVDQU64 128(DI), Z6
	VPDPBUSD Z18, Z6, Z9
	VMOVDQU64 192(DI), Z7
	VPDPBUSD Z19, Z7, Z9
reduce:
	VPADDD Z9, Z8, Z8
	VEXTRACTI64X4 $1, Z8, Y1
	VPADDD Y1, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	MOVL X8, CX
epilogue:
	CVTSL2SD CX, X5
	VMULSD 0(R15), X12, X6
	VADDSD X13, X6, X6
	VADDSD 24(R15), X6, X6
	VMULSD 8(R15), X14, X7
	VADDSD X7, X6, X6
	VMULSD 16(R15), X15, X10
	VMULSD X5, X10, X10
	VADDSD X10, X6, X6
	UCOMISD X11, X6
	JA next
	MOVQ 0(R13)(R14*1), BX
	MOVQ BX, 0(AX)
	MOVQ 0(R13), BX
	MOVQ BX, 8(AX)
	ADDQ $16, AX
next:
	ADDQ $8, R13
	ADDQ $32, R15
	ADDQ $256, DI
	DECQ R10
	JNZ row
done:
	MOVQ surv+112(FP), CX
	SUBQ CX, AX
	SHRQ $4, AX
	MOVQ AX, ret+120(FP)
	VZEROUPPER
	RET
