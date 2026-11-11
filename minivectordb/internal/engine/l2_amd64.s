#include "textflag.h"

// func cpuid(eax, ecx uint32) (a, b, c, d uint32)
TEXT ·cpuid(SB), NOSPLIT, $0-24
	MOVL eax+0(FP), AX
	MOVL ecx+4(FP), CX
	CPUID
	MOVL AX, a+8(FP)
	MOVL BX, b+12(FP)
	MOVL CX, c+16(FP)
	MOVL DX, d+20(FP)
	RET

// func xgetbv0() uint32
TEXT ·xgetbv0(SB), NOSPLIT, $0-4
	MOVL $0, CX
	XGETBV
	MOVL AX, ret+0(FP)
	RET

// func l2sqBlocksAVX2(a *float64, b *float32, blocks int) float64
//
// Sum over blocks of 16 elements of (b[i]-a[i])^2 in float64. Element i of
// a block goes to lane i%4 of accumulator (i/4)%4; the result is
// ((acc0+acc1)+(acc2+acc3)) reduced as (lane0+lane2)+(lane1+lane3).
TEXT ·l2sqBlocksAVX2(SB), NOSPLIT, $0-32
	MOVQ a+0(FP), SI
	MOVQ b+8(FP), DI
	MOVQ blocks+16(FP), CX
	VXORPD Y0, Y0, Y0
	VXORPD Y1, Y1, Y1
	VXORPD Y2, Y2, Y2
	VXORPD Y3, Y3, Y3
	TESTQ CX, CX
	JZ done
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
done:
	VADDPD Y1, Y0, Y0
	VADDPD Y3, Y2, Y2
	VADDPD Y2, Y0, Y0
	VEXTRACTF128 $1, Y0, X1
	VADDPD X1, X0, X0
	VHADDPD X0, X0, X0
	VZEROUPPER
	MOVSD X0, ret+24(FP)
	RET

// func l2sq32AVX2(a *float32, b *float32, groups int) float32
//
// Approximate squared distance in float32 over groups of 8 elements, used
// only as a pre-filter with a proven error margin; exact distances are
// always recomputed in float64.
TEXT ·l2sq32AVX2(SB), NOSPLIT, $0-28
	MOVQ a+0(FP), SI
	MOVQ b+8(FP), DI
	MOVQ groups+16(FP), CX
	VXORPS Y0, Y0, Y0
	VXORPS Y1, Y1, Y1
	VXORPS Y2, Y2, Y2
	VXORPS Y3, Y3, Y3
loop4:
	CMPQ CX, $4
	JLT loop1
	VMOVUPS 0(DI), Y4
	VMOVUPS 32(DI), Y5
	VMOVUPS 64(DI), Y6
	VMOVUPS 96(DI), Y7
	VSUBPS 0(SI), Y4, Y4
	VSUBPS 32(SI), Y5, Y5
	VSUBPS 64(SI), Y6, Y6
	VSUBPS 96(SI), Y7, Y7
	VFMADD231PS Y4, Y4, Y0
	VFMADD231PS Y5, Y5, Y1
	VFMADD231PS Y6, Y6, Y2
	VFMADD231PS Y7, Y7, Y3
	ADDQ $128, SI
	ADDQ $128, DI
	SUBQ $4, CX
	JMP loop4
loop1:
	TESTQ CX, CX
	JZ reduce
	VMOVUPS 0(DI), Y4
	VSUBPS 0(SI), Y4, Y4
	VFMADD231PS Y4, Y4, Y0
	ADDQ $32, SI
	ADDQ $32, DI
	DECQ CX
	JMP loop1
reduce:
	VADDPS Y1, Y0, Y0
	VADDPS Y3, Y2, Y2
	VADDPS Y2, Y0, Y0
	VEXTRACTF128 $1, Y0, X1
	VADDPS X1, X0, X0
	VHADDPS X0, X0, X0
	VHADDPS X0, X0, X0
	VZEROUPPER
	MOVSS X0, ret+24(FP)
	RET
