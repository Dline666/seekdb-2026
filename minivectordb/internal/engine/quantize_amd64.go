package engine

// quantizeCodesAVX2 encodes a multiple of four finite float32 coordinates.
// Scale is positive (possibly +Inf), mn is their finite row minimum.
// Length is at most maxInt8Dim; int32 lane sums cannot overflow. It uses
// the scalar path's float64 division, then floor plus fraction >= 0.5,
// avoiding the different tie behavior of nearest-even vector rounding.
//
//go:noescape
func quantizeCodesAVX2(v []float32, codes []byte, mn, scale float64) (sum, square uint64)
