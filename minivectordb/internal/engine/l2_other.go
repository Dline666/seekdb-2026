//go:build !amd64

package engine

// useSIMD is a variable only so tests can toggle it on every platform.
var useSIMD = false

var usePrefilter = false

const (
	prefilterEps  = 1.0 / 4096
	prefilterTiny = 1e-30
)

func l2sqSIMD(a []float64, b []float32) float64 { panic("no SIMD kernel on this platform") }

func l2sqApprox(a, b []float32) float64 { panic("no SIMD kernel on this platform") }
