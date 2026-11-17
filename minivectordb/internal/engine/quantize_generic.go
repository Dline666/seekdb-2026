//go:build !amd64

package engine

// useSIMD is false on these targets; quantizeRow retains its Go fallback.
func quantizeCodesAVX2(v []float32, codes []byte, mn, scale float64) (sum, square uint64) {
	panic("AVX2 encoder unavailable")
}
