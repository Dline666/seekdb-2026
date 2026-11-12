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
