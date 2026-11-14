#include "textflag.h"

// Go 1.22 emits EVEX for its VPDPBUSD mnemonic, even with YMM operands.
// The explicit instruction bytes below are VEX.256, requiring AVX-VNNI
// and YMM state only. unsigned row codes are the register source, signed
// query bytes the memory source. Both kernels use exact int32 dots and
// the existing VNNI folded-distance epilogue, with no saturating sums.
// Verify their disassembly before executing on a host without AVX512.

TEXT ·fusedScanInt8AVXVNNI(SB), NOSPLIT, $0-128
	MOVQ qc8+0(FP), R8
	MOVQ blocks+96(FP), R9
	MOVQ tail+104(FP), R11
	MOVQ R9, CX
	SHLQ $5, CX
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
	VMOVSD qSum+40(FP), X12
	VMOVSD qNorm2+48(FP), X13
	VMOVSD qminAdj+56(FP), X14
	VMOVSD qscale+64(FP), X15
	VMOVSD thresh2+72(FP), X11
	MOVQ rows+88(FP), R10
	MOVQ surv+112(FP), AX
	TESTQ R10, R10
	JZ done
row:
	MOVQ R8, SI
	MOVQ R9, CX
	VPXOR Y8, Y8, Y8
	VPXOR Y9, Y9, Y9
	CMPQ CX, $4
	JL pairloop
wideLoop:
	VMOVDQU 0(DI), Y4
	// VPDPBUSD 0(SI), Y4, Y8.
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x06
	VMOVDQU 32(DI), Y6
	// VPDPBUSD 32(SI), Y6, Y9.
	BYTE $0xC4; BYTE $0x62; BYTE $0x4D; BYTE $0x50; BYTE $0x4E; BYTE $0x20
	VMOVDQU 64(DI), Y4
	// VPDPBUSD 64(SI), Y4, Y8; signed-byte displacement 0x40.
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x46; BYTE $0x40
	VMOVDQU 96(DI), Y6
	// VPDPBUSD 96(SI), Y6, Y9; signed-byte displacement 0x60.
	BYTE $0xC4; BYTE $0x62; BYTE $0x4D; BYTE $0x50; BYTE $0x4E; BYTE $0x60
	ADDQ $128, SI
	ADDQ $128, DI
	SUBQ $4, CX
	CMPQ CX, $4
	JGE wideLoop
pairloop:
	CMPQ CX, $2
	JL single
	VMOVDQU 0(DI), Y4
	// VEX.256.66.0F38.W0 50 /r: VPDPBUSD 0(SI), Y4, Y8.
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x06
	VMOVDQU 32(DI), Y6
	// VPDPBUSD 32(SI), Y6, Y9, with the same VEX-only encoding.
	BYTE $0xC4; BYTE $0x62; BYTE $0x4D; BYTE $0x50; BYTE $0x4E; BYTE $0x20
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $2, CX
	JMP pairloop
single:
	TESTQ CX, CX
	JZ reduce
	VMOVDQU 0(DI), Y4
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x06
	ADDQ $32, SI
	ADDQ $32, DI
reduce:
	VPADDD Y9, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	VMOVD X8, CX
	TESTQ R11, R11
	JZ epilogue
	MOVQ R11, R12
	// Keep this 24-byte scalar loop within one instruction-fetch block,
	// independently of code layout changes in unrelated Go functions.
	PCALIGN $32
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
	VCVTSI2SDL CX, X5, X5
	VMULSD 0(R15), X12, X6
	VADDSD X13, X6, X6
	VADDSD 24(R15), X6, X6
	VMULSD 8(R15), X14, X7
	VADDSD X7, X6, X6
	VMULSD 16(R15), X15, X10
	VMULSD X5, X10, X10
	VADDSD X10, X6, X6
	VUCOMISD X11, X6
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


TEXT ·fusedScanCandidatesAVXVNNI(SB), NOSPLIT, $0-128
	MOVQ qc8+0(FP), R8
	MOVQ blocks+96(FP), R9
	MOVQ R9, R14
	SHLQ $5, R14
	MOVQ tail+104(FP), R11
	ADDQ R11, R14
	MOVQ slots+80(FP), R13
	VMOVSD qSum+40(FP), X12
	VMOVSD qNorm2+48(FP), X13
	VMOVSD qminAdj+56(FP), X14
	VMOVSD qscale+64(FP), X15
	VMOVSD thresh2+72(FP), X11
	MOVQ rows+88(FP), R10
	MOVQ surv+112(FP), AX
	TESTQ R10, R10
	JZ done
row:
	MOVL 0(R13), BX
	MOVQ BX, DI
	IMULQ R14, DI
	ADDQ codes+8(FP), DI
	MOVQ BX, R15
	SHLQ $5, R15
	ADDQ qf+32(FP), R15
	MOVQ R8, SI
	MOVQ R9, CX
	VPXOR Y8, Y8, Y8
	VPXOR Y9, Y9, Y9
	CMPQ CX, $4
	JL pairloop
wideLoop:
	VMOVDQU 0(DI), Y4
	// VPDPBUSD 0(SI), Y4, Y8.
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x06
	VMOVDQU 32(DI), Y6
	// VPDPBUSD 32(SI), Y6, Y9.
	BYTE $0xC4; BYTE $0x62; BYTE $0x4D; BYTE $0x50; BYTE $0x4E; BYTE $0x20
	VMOVDQU 64(DI), Y4
	// VPDPBUSD 64(SI), Y4, Y8; signed-byte displacement 0x40.
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x46; BYTE $0x40
	VMOVDQU 96(DI), Y6
	// VPDPBUSD 96(SI), Y6, Y9; signed-byte displacement 0x60.
	BYTE $0xC4; BYTE $0x62; BYTE $0x4D; BYTE $0x50; BYTE $0x4E; BYTE $0x60
	ADDQ $128, SI
	ADDQ $128, DI
	SUBQ $4, CX
	CMPQ CX, $4
	JGE wideLoop
pairloop:
	CMPQ CX, $2
	JL single
	VMOVDQU 0(DI), Y4
	// VEX.256.66.0F38.W0 50 /r: VPDPBUSD 0(SI), Y4, Y8.
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x06
	VMOVDQU 32(DI), Y6
	// VPDPBUSD 32(SI), Y6, Y9, with the same VEX-only encoding.
	BYTE $0xC4; BYTE $0x62; BYTE $0x4D; BYTE $0x50; BYTE $0x4E; BYTE $0x20
	ADDQ $64, SI
	ADDQ $64, DI
	SUBQ $2, CX
	JMP pairloop
single:
	TESTQ CX, CX
	JZ reduce
	VMOVDQU 0(DI), Y4
	BYTE $0xC4; BYTE $0x62; BYTE $0x5D; BYTE $0x50; BYTE $0x06
	ADDQ $32, SI
	ADDQ $32, DI
reduce:
	VPADDD Y9, Y8, Y8
	VEXTRACTF128 $1, Y8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0x4E, X8, X1
	VPADDD X1, X8, X8
	VPSHUFD $0xB1, X8, X1
	VPADDD X1, X8, X8
	VMOVD X8, CX
	TESTQ R11, R11
	JZ epilogue
	MOVQ R11, R12
	PCALIGN $32
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
	VCVTSI2SDL CX, X5, X5
	VMULSD 0(R15), X12, X6
	VADDSD X13, X6, X6
	VADDSD 24(R15), X6, X6
	VMULSD 8(R15), X14, X7
	VADDSD X7, X6, X6
	VMULSD 16(R15), X15, X10
	VMULSD X5, X10, X10
	VADDSD X10, X6, X6
	VUCOMISD X11, X6
	JA next
	MOVL 0(R13), BX
	MOVQ offs+24(FP), DX
	MOVQ 0(DX)(BX*8), DX
	MOVQ DX, 0(AX)
	MOVQ pks+16(FP), DX
	MOVQ 0(DX)(BX*8), DX
	MOVQ DX, 8(AX)
	ADDQ $16, AX
next:
	ADDQ $4, R13
	DECQ R10
	JNZ row
done:
	MOVQ surv+112(FP), CX
	SUBQ CX, AX
	SHRQ $4, AX
	MOVQ AX, ret+120(FP)
	VZEROUPPER
	RET
