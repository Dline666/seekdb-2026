"""Cache existing conservative candidate bounds; no new pruning inequality."""
from pathlib import Path
import shutil
root=Path('/home/platemo/seekdb-experiments/20261117/experiment-runtime')
dest=root/'advanced-candidates/eqbounds';shutil.copytree(root/'remote-final',dest)
p=dest/'internal/engine/eqset.go';s=p.read_text().replace('slots []int32\n\tn     int','slots []int32\n\tn     int\n\tbounds []quantBlockBound')
s=s.replace('int64(len(key)) + 96','int64(len(key)) + 128')
s+='''
// candidateBounds caches exactly the existing boundForSlots merge, in groups
// of quantBlockRows candidate positions. A subrange may use its containing
// group bound, but must never extend into the next group. Derived sets are
// invalidated on every mutation, and all retained capacity shares the index
// budget. No vectors or quantized codes are copied.
func (x *VecIndex) candidateBounds(db *DB, col int, val string) []quantBlockBound {
	if !candidateBoundsEnabled { return nil }
	e := x.eq[col]
	if e == nil || e.gen != x.gen { return nil }
	g := e.m[val]
	if g == nil || len(g.slots) == 0 { return nil }
	if g.bounds != nil { return g.bounds }
	n := (len(g.slots)+quantBlockRows-1)/quantBlockRows
	bytes := int64(n)*16
	if bytes > db.indexBudget(x)-x.derivedBytes() { return nil }
	g.bounds = make([]quantBlockBound,n)
	for i := range g.bounds {
		lo := i*quantBlockRows
		g.bounds[i] = x.boundForSlots(g.slots[lo:min(lo+quantBlockRows,len(g.slots))])
	}
	x.eqBytes += bytes
	return g.bounds
}

var candidateBoundsEnabled = os.Getenv("MVDB_NO_EQBOUNDS") == ""
''';p.write_text(s)
p=dest/'internal/engine/topk.go';s=p.read_text()
a='\t// The fused integer scan does much less work per row.'
assert a in s;s=s.replace(a,'''\tvar candidateBounds []quantBlockBound
	if fusedCandidateOK {
		candidateBounds = x.candidateBounds(db, eq.col, eq.val)
	}
'''+a)
a='\t\t\t\t\tbound = x.boundForSlots(cand[i : i+n])';assert a in s
s=s.replace(a,'''\t\t\t\t\tif candidateBounds != nil {
						n = min(n, quantBlockRows-i%quantBlockRows)
						bound = candidateBounds[i/quantBlockRows]
					} else {
						bound = x.boundForSlots(cand[i : i+n])
					}''');p.write_text(s)
print(dest)
