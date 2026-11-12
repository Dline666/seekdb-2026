package engine

import (
	"math"
	"math/rand"
	"testing"
)

// TestNearestCentroidMatchesNaive pins the widened-query path against the
// per-centroid widening it replaced.
//
// nearestCentroid and probeOrder used to call l2sq(v, centroid), which widens
// the query for every centroid: one kilobyte of garbage per comparison. The
// training of a large table spent most of its time on the allocation and the
// collector, not on the distance arithmetic. The query is now widened once.
//
// The distances must not change: l2sq(v, c) is l2sqBound(widen(v), c, +Inf)
// by definition, and a bound of +Inf never ends the sum early.
func TestNearestCentroidMatchesNaive(t *testing.T) {
	const dim, nlist = 8, 32
	rng := rand.New(rand.NewSource(7))
	x := &VecIndex{dim: dim, nlist: nlist, cents: make([]float32, dim*nlist)}
	for i := range x.cents {
		x.cents[i] = float32(rng.NormFloat64())
	}
	q := make([]float32, dim)
	for i := range q {
		q[i] = float32(rng.NormFloat64())
	}

	naiveNearest := func(v []float32) int {
		best, bd := 0, math.Inf(1)
		for c := 0; c < nlist; c++ {
			if d := l2sq(v, x.cents[c*dim:(c+1)*dim]); d < bd {
				best, bd = c, d
			}
		}
		return best
	}
	naiveOrder := func(v []float32) []int {
		type cd struct {
			c int
			d float64
		}
		ds := make([]cd, nlist)
		for c := 0; c < nlist; c++ {
			ds[c] = cd{c, l2sq(v, x.cents[c*dim:(c+1)*dim])}
		}
		// insertion sort on the distance, the same order as sort.Slice on it
		for i := 1; i < len(ds); i++ {
			for j := i; j > 0 && ds[j].d < ds[j-1].d; j-- {
				ds[j], ds[j-1] = ds[j-1], ds[j]
			}
		}
		out := make([]int, nlist)
		for i, e := range ds {
			out[i] = e.c
		}
		return out
	}

	if got, want := x.nearestCentroid(q), naiveNearest(q); got != want {
		t.Fatalf("nearestCentroid = %d, want %d", got, want)
	}
	got, want := x.probeOrder(q), naiveOrder(q)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("probeOrder = %v, want %v", got, want)
		}
	}
}
