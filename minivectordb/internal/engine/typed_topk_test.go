package engine

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// This oracle sorts every candidate independently of topK and its comparator.
// The synthetic offsets deliberately have no relationship to primary-key order.
func typedTopKReference(in []hit, k int) []hit {
	out := append([]hit(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].d < out[j].d {
			return true
		}
		if out[i].d > out[j].d {
			return false
		}
		return out[i].pk < out[j].pk
	})
	if len(out) > k {
		out = out[:k]
	}
	return out
}

func typedTopKInputs() []hit {
	// Signed zero must retain its bits while comparing equal for ranking.
	// Infinite sentinels exercise the full distance-order boundary. Actual
	// SQL vector components are finite, so NaN is outside this input domain.
	in := []hit{
		{d: math.Inf(1), pk: math.MaxInt64, off: 907},
		{d: math.Inf(1), pk: math.MinInt64, off: 31},
		{d: math.MaxFloat64, pk: -17, off: 503},
		{d: math.SmallestNonzeroFloat64, pk: 9, off: 101},
		{d: math.Copysign(0, -1), pk: -1, off: 811},
		{d: 0, pk: 1, off: 77},
		{d: math.Nextafter(1, 2), pk: -99, off: 283},
		{d: 1, pk: -98, off: 601},
	}
	rng := rand.New(rand.NewSource(714))
	for i := 0; i < 129; i++ {
		// Scalar squared Euclidean distance over coarse float32 coordinates
		// creates exact ties. No production distance or quantization helper
		// participates in constructing the expected ranking.
		var d float64
		for j := 0; j < 7; j++ {
			q := float32(j%3) / 2
			v := float32(rng.Intn(5)-2) / 2
			delta := float64(q) - float64(v)
			d += delta * delta
		}
		if i%11 == 0 {
			d = 2 // repeated boundary ties arrive in different PK orders
		}
		in = append(in, hit{d: d, pk: int64(1000 + i), off: int64(5000 + (i*53)%129*29)})
	}
	return in
}

func checkTypedTopKResult(t *testing.T, tk *topK, all []hit) {
	t.Helper()
	want := typedTopKReference(all, tk.k)
	if len(all) < tk.k {
		if !math.IsInf(tk.bound(), 1) {
			t.Fatalf("unfilled heap bound = %v, want +Inf", tk.bound())
		}
	} else if got := math.Float64bits(tk.bound()); got != math.Float64bits(want[len(want)-1].d) {
		t.Fatalf("bound bits = %x, want %x", got, math.Float64bits(want[len(want)-1].d))
	}
	// result consumes the heap ordering. Copy only the test subject so the
	// original stream can keep accepting candidates after this check.
	copyTK := *tk
	copyTK.h = append(hitHeap(nil), tk.h...)
	offs, dists := copyTK.result()
	if len(offs) != len(want) || len(dists) != len(want) {
		t.Fatalf("result lengths = %d/%d, want %d", len(offs), len(dists), len(want))
	}
	for i, w := range want {
		if offs[i] != w.off || math.Float64bits(dists[i]) != math.Float64bits(w.d) {
			t.Fatalf("rank %d = (offset %d, distance bits %x), want (offset %d, distance bits %x), PK %d",
				i, offs[i], math.Float64bits(dists[i]), w.off, math.Float64bits(w.d), w.pk)
		}
	}
}

func TestTypedTopKIndependentOrderAndBound(t *testing.T) {
	input := typedTopKInputs()
	for _, k := range []int{1, 2, 3, 10, 31, len(input), len(input) + 7} {
		for order := 0; order < 6; order++ {
			t.Run(fmt.Sprintf("k=%d/order=%d", k, order), func(t *testing.T) {
				stream := append([]hit(nil), input...)
				switch order {
				case 0:
					// Best-first arrivals stress rejecting worse candidates.
					stream = typedTopKReference(stream, len(stream))
				case 1:
					// Worst-first arrivals repeatedly replace the root.
					stream = typedTopKReference(stream, len(stream))
					for i, j := 0, len(stream)-1; i < j; i, j = i+1, j-1 {
						stream[i], stream[j] = stream[j], stream[i]
					}
				default:
					rng := rand.New(rand.NewSource(int64(91 + order)))
					rng.Shuffle(len(stream), func(i, j int) { stream[i], stream[j] = stream[j], stream[i] })
				}
				tk := &topK{k: k, h: make(hitHeap, 0, k)}
				checkTypedTopKResult(t, tk, nil)
				for i, c := range stream {
					tk.admit(c.d, c.pk, c.off)
					checkTypedTopKResult(t, tk, stream[:i+1])
				}
			})
		}
	}
}

func TestTypedTopKPartitionMergeMatchesFullOrder(t *testing.T) {
	input := typedTopKInputs()
	for _, k := range []int{1, 10, 31, len(input) + 7} {
		for _, workers := range []int{2, 3, 7} {
			t.Run(fmt.Sprintf("k=%d/workers=%d", k, workers), func(t *testing.T) {
				parts := make([]topK, workers)
				for i := range parts {
					parts[i].k = k
				}
				for i, c := range input {
					parts[i%workers].admit(c.d, c.pk, c.off)
				}
				for _, reverse := range []bool{false, true} {
					tk := &topK{k: k}
					for i := range parts {
						p := i
						if reverse {
							p = len(parts) - 1 - i
						}
						for _, c := range parts[p].h {
							tk.admit(c.d, c.pk, c.off)
						}
					}
					checkTypedTopKResult(t, tk, input)
				}
			})
		}
	}
}
