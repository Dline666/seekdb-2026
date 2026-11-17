"""Derive the gathered variant with the identical dot and float64 epilogue."""
from pathlib import Path

root = Path(__file__).resolve().parents[2]
source = root/'minivectordb/internal/engine/fused_quad_amd64.s'
text = source.read_text()
assert 'fusedScanQuadCandidatesAVXVNNI' not in text
body = text[text.index('TEXT '):]
body = body.replace('fusedScanQuadAVXVNNI', 'fusedScanQuadCandidatesAVXVNNI')
start = body.index('\tMOVQ baseSlot+80(FP), AX')
end = body.index('\tVBROADCASTSD qSum', start)
body = body[:start]+'''\tMOVQ slots+80(FP), BX
\tMOVQ pks+16(FP), R13
\tMOVQ offs+24(FP), R14
\tMOVQ qf+32(FP), R15
'''+body[end:]
start = body.index('group:\n')+len('group:\n')
end = body.index('\tMOVQ blocks+96(FP), CX', start)
setup = ''
for i, reg in enumerate(['DI','SI','DX','R12']):
    setup += f'\tMOVL {4*i}(BX), {reg}\n\tIMULQ R9, {reg}\n\tADDQ codes+8(FP), {reg}\n'
body = body[:start]+setup+body[end:]
body = body.replace('\tMOVQ R12, DI\n', '')
for i in range(4):
    body = body.replace(f'\tVMOVUPD {32*i}(R15), Y{i+1}',
                        f'\tMOVL {4*i}(BX), CX\n\tSHLQ $5, CX\n\tVMOVUPD (R15)(CX*1), Y{i+1}')
start = body.index('\tVMOVMSKPD Y1, CX')
end = body.index('next:\n', start)
output = '\tVMOVMSKPD Y1, SI\n'
labels = ['second', 'third', 'fourth', 'next']
for i, label in enumerate(labels):
    output += (f'\tTESTQ ${1<<i}, SI\n\tJNZ {label}\n'
               f'\tMOVL {4*i}(BX), CX\n\tMOVQ (R14)(CX*8), DX\n\tMOVQ DX, 0(AX)\n'
               f'\tMOVQ (R13)(CX*8), DX\n\tMOVQ DX, 8(AX)\n\tADDQ $16, AX\n')
    if label != 'next':
        output += label+':\n'
body = body[:start]+output+body[end:]
body = body.replace('\tADDQ $32, R13\n\tADDQ $128, R15\n', '\tADDQ $16, BX\n')
with source.open('a') as out:
    out.write('\n// Candidate slots may be noncontiguous, repeated, or descending.\n'+body)
