"""AVX2 row encoding: same float64 divide and half-away rounding as Go."""
from pathlib import Path
import shutil
root=Path('/home/platemo/seekdb-experiments/20261117/experiment-runtime')
dest=root/'advanced-candidates/quantize';shutil.copytree(root/'advanced-combo2',dest)
p=dest/'internal/engine/prefilter.go';s=p.read_text()
a='\tvar sumC, sumC2 float64\n\tfor i, f := range v {';assert s.count(a)==1
s=s.replace(a,'''\tvar sumC, sumC2 float64
	dim := len(v)
	if quantizeEncodingEnabled && useSIMD && sc > 0 && dim >= 16 && dim <= maxInt8Dim {
		n := dim &^ 3
		a, b := quantizeCodesAVX2(v[:n], codes[:n], float64(mn), float64(sc))
		sumC, sumC2 = float64(a), float64(b)
		v, codes = v[n:], codes[n:]
	}
	for i, f := range v {''')
s=s.replace('cRow := float64(len(v))*mnf*mnf','cRow := float64(dim)*mnf*mnf')
s+='\nvar quantizeEncodingEnabled = os.Getenv("MVDB_NO_QUANTIZE_AVX2") == ""\n';p.write_text(s)
(dest/'internal/engine/quantize_amd64.go').write_text('''package engine

// quantizeCodesAVX2 encodes a multiple of four finite float32 coordinates.
// Scale is positive (possibly +Inf), mn is their finite row minimum.
// Length is at most maxInt8Dim; int32 lane sums cannot overflow. It uses
// the scalar path's float64 division, then floor plus fraction >= 0.5,
// avoiding the different tie behavior of nearest-even vector rounding.
//go:noescape
func quantizeCodesAVX2(v []float32, codes []byte, mn, scale float64) (sum, square uint64)
''')
(dest/'internal/engine/quantize_generic.go').write_text('''//go:build !amd64

package engine

// useSIMD is false on these targets; quantizeRow retains its Go fallback.
func quantizeCodesAVX2(v []float32, codes []byte, mn, scale float64) (sum, square uint64) {
	panic("AVX2 encoder unavailable")
}
''')
(dest/'internal/engine/quantize_amd64.s').write_text('''#include "textflag.h"

DATA ·quantHalf+0(SB)/8, $0x3fe0000000000000
GLOBL ·quantHalf(SB), RODATA|NOPTR, $8
DATA ·quantOne+0(SB)/8, $0x3ff0000000000000
GLOBL ·quantOne(SB), RODATA|NOPTR, $8
DATA ·quantMax+0(SB)/4, $255
GLOBL ·quantMax(SB), RODATA|NOPTR, $4

TEXT ·quantizeCodesAVX2(SB), NOSPLIT, $0-80
	MOVQ v_base+0(FP), DI
	MOVQ v_len+8(FP), CX
	MOVQ codes_base+24(FP), SI
	VBROADCASTSD mn+48(FP), Y6
	VBROADCASTSD scale+56(FP), Y7
	VBROADCASTSD ·quantHalf(SB), Y4
	VBROADCASTSD ·quantOne(SB), Y3
	VPBROADCASTD ·quantMax(SB), X5
	VPXOR X8, X8, X8
	VPXOR X9, X9, X9
	VPXOR X10, X10, X10
qencode_loop:
	CMPQ CX, $4
	JL qencode_done
	VCVTPS2PD (DI), Y0
	VSUBPD Y6, Y0, Y0
	VDIVPD Y7, Y0, Y0
	VCVTTPD2DQY Y0, X1
	VCVTDQ2PD X1, Y2
	VSUBPD Y2, Y0, Y0
	VCMPPD $29, Y4, Y0, Y0
	VANDPD Y3, Y0, Y0
	VCVTTPD2DQY Y0, X2
	VPADDD X2, X1, X1
	VPMAXSD X10, X1, X1
	VPMINSD X5, X1, X1
	VPADDD X1, X8, X8
	VPMULLD X1, X1, X2
	VPADDD X2, X9, X9
	VPACKUSDW X1, X1, X1
	VPACKUSWB X1, X1, X1
	VMOVD X1, (SI)
	ADDQ $16, DI
	ADDQ $4, SI
	SUBQ $4, CX
	JMP qencode_loop
qencode_done:
	VPHADDD X8, X8, X8
	VPHADDD X8, X8, X8
	VPHADDD X9, X9, X9
	VPHADDD X9, X9, X9
	VMOVD X8, AX
	MOVQ AX, sum+64(FP)
	VMOVD X9, AX
	MOVQ AX, square+72(FP)
	VZEROUPPER
	RET
''')
print(dest)
