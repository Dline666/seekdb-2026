"""Derive the AVX2 experiment from the archived four-row VEX layout."""
from pathlib import Path

repo = Path(__file__).resolve().parents[2]
src = (repo / 'minivectordb/internal/engine/fused_quad_amd64.s').read_text()
src = src.replace('fusedScanQuadAVXVNNI', 'fusedScanQuadAVX2').replace('fusedScanQuadCandidatesAVXVNNI', 'fusedScanQuadCandidatesAVX2')
src = src.replace('qc8+0(FP)', 'qc+0(FP)').replace('qminAdj+56(FP)', 'qmin+56(FP)')
src = src.replace('// Explicit VEX VPDPBUSD bytes avoid Go 1.22\'s EVEX mnemonic encoding.',
'''// AVX2 widens unsigned row bytes to words before VPMADDWD. Query words
// are in [0,255], and dim <= maxInt8Dim keeps the complete sum in int32.
// Query loads are shared across four rows. No saturating byte dot or FMA.
// As in the original fused AVX2 kernel, callers pass nonnegative thresholds;
// omitting the scalar clamp-to-zero then preserves every keep/drop decision.''')
start = src.index('dot:\n')
end = src.index('\tADDQ $32, R8', start)
old = src[start:end]
new = 'dot:\n\tVMOVDQU 0(R8), Y9\n\tVMOVDQU 32(R8), Y10\n'
for offset, q in [(0, 'Y9'), (16, 'Y10')]:
    for row, ptr in enumerate(['DI', 'SI', 'DX', 'R12']):
        new += f'\tVPMOVZXBW {offset}({ptr}), Y{row+4}\n'
        new += f'\tVPMADDWD {q}, Y{row+4}, Y{row+4}\n'
        new += f'\tVPADDD Y{row+4}, Y{row}, Y{row}\n'
assert src.count(old) == 2
src = src.replace(old, new).replace('ADDQ $32, R8', 'ADDQ $64, R8').replace('SUBQ R9, R8', 'SUBQ R9, R8\n\tSUBQ R9, R8')
(repo / 'minivectordb/internal/engine/fused_quad_avx2_amd64.s').write_text(src)
