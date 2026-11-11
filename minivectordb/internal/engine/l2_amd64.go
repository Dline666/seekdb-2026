package engine

import "os"

func cpuid(eax, ecx uint32) (a, b, c, d uint32)
func xgetbv0() uint32

//go:noescape
func l2sqBlocksAVX2(a *float64, b *float32, blocks int) float64

//go:noescape
func l2sq32AVX2(a *float32, b *float32, groups int) float32

// hasAVX2FMA reports whether the CPU and OS support AVX2 and FMA, so the
// vector kernel can be used. MVDB_NO_SIMD=1 forces the portable kernel.
func hasAVX2FMA() bool {
	if os.Getenv("MVDB_NO_SIMD") != "" {
		return false
	}
	max, _, _, _ := cpuid(0, 0)
	if max < 7 {
		return false
	}
	_, _, c1, _ := cpuid(1, 0)
	const fma, osxsave, avx = 1 << 12, 1 << 27, 1 << 28
	if c1&fma == 0 || c1&osxsave == 0 || c1&avx == 0 {
		return false
	}
	if xgetbv0()&6 != 6 { // XMM and YMM state enabled by the OS
		return false
	}
	_, b7, _, _ := cpuid(7, 0)
	return b7&(1<<5) != 0
}

var useSIMD = hasAVX2FMA()

// usePrefilter enables the float32 pre-filter in the exact scan;
// MVDB_NO_PREFILTER=1 turns it off for experiments.
var usePrefilter = useSIMD && os.Getenv("MVDB_NO_PREFILTER") == ""

// Error bound of l2sqApprox against the exact squared distance d (computed
// in float64 from the same float32 inputs). Each float32 difference and
// square is rounded once (relative error <= 2^-24 each), and the sum of up
// to n/32+3 terms per accumulator adds at most (n/32+8)*2^-24 relative
// error for non-negative terms. For dimensions up to 4096 the total is
// below 2^-16; we use 2^-12 as the margin. prefilterTiny covers results
// near the float32 underflow range, where relative bounds do not hold.
const (
	prefilterEps  = 1.0 / 4096
	prefilterTiny = 1e-30
)

// l2sqSIMD is the AVX2 kernel for whole 16-element blocks plus a scalar
// tail added in order.
func l2sqSIMD(a []float64, b []float32) float64 {
	n := len(a)
	blocks := n / 16
	var s float64
	if blocks > 0 {
		s = l2sqBlocksAVX2(&a[0], &b[0], blocks)
	}
	for i := blocks * 16; i < n; i++ {
		d := a[i] - float64(b[i])
		s += d * d
	}
	return s
}

// l2sqApprox is a float32 estimate of the squared distance. Its relative
// error is below (len/32 + 16) * 2^-24 for normal results; underflow only
// lowers it and overflow gives +Inf.
func l2sqApprox(a, b []float32) float64 {
	n := len(a)
	g := n / 8
	var s float64
	if g > 0 {
		s = float64(l2sq32AVX2(&a[0], &b[0], g))
	}
	for i := g * 8; i < n; i++ {
		d := float64(a[i]) - float64(b[i])
		s += d * d
	}
	return s
}
