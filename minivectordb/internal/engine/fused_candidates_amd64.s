#include "textflag.h"

// The equality set contains live, valid slots in scan order. Load codes and
// folded parameters from the primary index, apply exactly the same bound as
// the contiguous kernel, and emit (offset, PK) pairs for exact verification.
// No duplicated per-category codes or intermediate dot/offset arrays.
TEXT ·fusedScanCandidatesAVX2(SB), NOSPLIT, $0-128
	MOVQ qc+0(FP), R8
	MOVQ blocks+96(FP), R9
	MOVQ R9, R14
	SHLQ $5, R14
	ADDQ tail+104(FP), R14
	MOVQ slots+80(FP), R13
	VMOVSD qSum+40(FP), X12
	VMOVSD qNorm2+48(FP), X13
	VMOVSD qmin+56(FP), X14
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
	// Unroll four blocks without changing the exact integer dot product.
	// Remaining blocks and dimensions below 128 use the original loop.
	CMPQ CX, $4
	JL remainder
wideLoop:
	VPMOVZXBW 0(DI), Y4
	VPMADDWD 0(SI), Y4, Y4
	VPADDD Y4, Y8, Y8
	VPMOVZXBW 16(DI), Y5
	VPMADDWD 32(SI), Y5, Y5
	VPADDD Y5, Y9, Y9
	VPMOVZXBW 32(DI), Y4
	VPMADDWD 64(SI), Y4, Y4
	VPADDD Y4, Y8, Y8
	VPMOVZXBW 48(DI), Y5
	VPMADDWD 96(SI), Y5, Y5
	VPADDD Y5, Y9, Y9
	VPMOVZXBW 64(DI), Y4
	VPMADDWD 128(SI), Y4, Y4
	VPADDD Y4, Y8, Y8
	VPMOVZXBW 80(DI), Y5
	VPMADDWD 160(SI), Y5, Y5
	VPADDD Y5, Y9, Y9
	VPMOVZXBW 96(DI), Y4
	VPMADDWD 192(SI), Y4, Y4
	VPADDD Y4, Y8, Y8
	VPMOVZXBW 112(DI), Y5
	VPMADDWD 224(SI), Y5, Y5
	VPADDD Y5, Y9, Y9
	ADDQ $256, SI
	ADDQ $128, DI
	SUBQ $4, CX
	CMPQ CX, $4
	JGE wideLoop
remainder:
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
	VMOVD X8, CX
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
