"""Inspect hot-loop placement in otherwise identical unstripped builds."""
import re
import subprocess
import sys
from run import ENV, GO, ROOT, RESULTS, dump, run

locations = []
for name in sys.argv[1:] or ['base','preload','scratch']:
    binary = str(ROOT / 'bin' / (name + '-symbols'))
    run(name + '-symbol-build', [GO, 'build', '-trimpath', '-o', binary, './cmd/minivectordb'],
        ROOT / name / 'minivectordb')
    for symbol in ['fusedScanInt8AVXVNNI','fusedScanCandidatesAVXVNNI','l2sqGatherAVX2']:
        text = subprocess.check_output(['objdump','-d','--disassemble=minivectordb/internal/engine.'+symbol+'.abi0',binary], text=True)
        (RESULTS / (name + '-' + symbol + '-layout.txt')).write_text(text)
        # Emit every instruction and branch target modulo 32/64 for review.
        insns = []
        for line in text.splitlines():
            m = re.match(r'\s*([0-9a-f]+):\s+(.+)', line)
            if m:
                addr = int(m[1],16)
                insns.append({'address': m[1], 'mod32': addr%32, 'mod64': addr%64, 'instruction': m[2]})
        locations.append({'binary': name, 'symbol': symbol, 'instructions': insns})
suffix = '-' + '-'.join(sys.argv[1:]) if sys.argv[1:] else ''
dump('hot-loop-layout' + suffix + '.json', locations)
