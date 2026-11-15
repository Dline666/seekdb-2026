package engine

import "sync/atomic"

// Diagnostics distinguish the coded subset from rows that must remain exact.
var partialPrefilterBatches, partialPrefilterRows, partialUncodedRows atomic.Int64

// filterPartialBatch filters only initialized prefix slots. offs/pks/rows are
// compacted together, in their original order. Uncoded rows are never passed
// to a dot-product kernel or tested against an approximate distance. The caller
// has already checked tombstones, transaction overlays and any residual WHERE.
func (x *VecIndex) filterPartialBatch(qc []int16, qc6 []int8, qSum, qNorm2 float64,
	qmin, qscale float32, bound float64, slots []int32, offs, pks, rows []int64) int {
	var codedSlots [scanBatch]int32
	var positions [scanBatch]int
	var coffs [scanBatch]int64
	var dots [scanBatch]int32
	var rejected [scanBatch]bool
	coded := 0
	for i, s := range slots {
		if int(s) >= x.quantRows {
			continue
		}
		codedSlots[coded], positions[coded] = s, i
		coffs[coded] = int64(s) * int64(x.dim)
		coded++
	}
	partialUncodedRows.Add(int64(len(slots) - coded))
	if coded == 0 {
		return len(slots)
	}
	partialPrefilterBatches.Add(1)
	partialPrefilterRows.Add(int64(coded))
	if prefilter6Bit {
		dotInt8GatherInt6(qc6, x.codes, coffs[:coded], dots[:coded])
	} else {
		dotInt8GatherInt(qc, x.codes, coffs[:coded], dots[:coded])
	}
	threshold := x.boundForSlots(codedSlots[:coded]).threshold(qmin, qscale, x.dim, bound)
	for j := 0; j < coded; j++ {
		s := int(codedSlots[j])
		f := x.qf[s*4 : s*4+4]
		d := foldDist2(qSum, qNorm2, qmin, qscale, dots[j], f[0], f[1], f[2], f[3])
		// Strict comparison preserves ties and does not reject a NaN distance.
		rejected[positions[j]] = d > threshold
	}
	kept := 0
	for i := range slots {
		if rejected[i] {
			continue
		}
		offs[kept], pks[kept], rows[kept] = offs[i], pks[i], rows[i]
		kept++
	}
	return kept
}
