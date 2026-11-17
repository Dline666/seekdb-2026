package engine

import (
	"bytes"
	"math"
	"math/big"
	"math/rand"
	"testing"
)

// Round a positive IEEE quotient with independent rational arithmetic.
// Two 53-bit big.Float operations reproduce the specified subtract/divide
// rounding without the production scalar loop, SIMD, or math.Round.
func oracleQuantCode(v, mn, scale float32) byte {
	if scale == 0 || math.IsInf(float64(scale), 1) {
		return 0
	}
	a := new(big.Float).SetPrec(53).SetMode(big.ToNearestEven).SetFloat64(float64(v))
	a.Sub(a, new(big.Float).SetPrec(53).SetFloat64(float64(mn)))
	a.Quo(a, new(big.Float).SetPrec(53).SetFloat64(float64(scale)))
	exact, _ := a.Rat(nil)
	integer, remainder := new(big.Int), new(big.Int)
	integer.QuoRem(exact.Num(), exact.Denom(), remainder)
	remainder.Lsh(remainder, 1)
	if remainder.Cmp(exact.Denom()) >= 0 {
		integer.Add(integer, big.NewInt(1))
	}
	x := integer.Int64()
	if x < 0 {
		x = 0
	}
	if x > 255 {
		x = 255
	}
	return byte(x)
}

func TestQuantizeEncodingIndependentOracle(t *testing.T) {
	old := quantizeEncodingEnabled
	defer func() { quantizeEncodingEnabled = old }()
	rng := rand.New(rand.NewSource(41717))
	for trial := 0; trial < 180; trial++ {
		n := []int{16, 17, 31, 32, 127, 128, 255, 256, 1023, 1024}[trial%10]
		v := make([]float32, n)
		for i := range v {
			switch trial % 6 {
			case 0:
				v[i] = float32(rng.NormFloat64() * 100)
			case 1:
				v[i] = float32(i%256) + 0.5
				switch i % 3 {
				case 1:
					v[i] = math.Nextafter32(v[i], float32(math.Inf(-1)))
				case 2:
					v[i] = math.Nextafter32(v[i], float32(math.Inf(1)))
				}
			case 2:
				v[i] = float32(0.00001 * float64(i%13))
			case 3:
				v[i] = float32(1e30 + 1e25*float64(i%13))
			case 4:
				v[i] = 7
			case 5:
				v[i] = float32(i%129) * math.SmallestNonzeroFloat32
			}
		}
		if trial%6 == 1 {
			v[0], v[1] = 0, 255
		}
		if trial == 0 {
			v[0], v[n-1] = -math.MaxFloat32, math.MaxFloat32
		}
		gotbuf := bytes.Repeat([]byte{0xA5}, n+2)
		got := gotbuf[1 : n+1]
		scalar := make([]byte, n)
		quantizeEncodingEnabled = false
		wantParam := quantizeRow(v, scalar)
		quantizeEncodingEnabled = true
		gotParam := quantizeRow(v, got)
		if !bytes.Equal(got, scalar) {
			t.Fatalf("scalar code mismatch trial %d", trial)
		}
		if gotbuf[0] != 0xA5 || gotbuf[n+1] != 0xA5 {
			t.Fatal("encoder wrote beyond slice")
		}
		// The folded expression may be NaN for an infinite scale in both paths.
		if gotParam.min != wantParam.min || gotParam.scale != wantParam.scale || gotParam.sumC != wantParam.sumC || !(gotParam.cRow == wantParam.cRow || math.IsNaN(gotParam.cRow) && math.IsNaN(wantParam.cRow)) {
			t.Fatal("parameter mismatch", trial)
		}
		for i, x := range v {
			if want := oracleQuantCode(x, wantParam.min, wantParam.scale); got[i] != want {
				t.Fatalf("independent rounding mismatch trial=%d coordinate=%d got=%d want=%d", trial, i, got[i], want)
			}
		}
	}
}

func TestQuantizeEncodingMaxIntegerSum(t *testing.T) {
	old := quantizeEncodingEnabled
	defer func() { quantizeEncodingEnabled = old }()
	for _, n := range []int{maxInt8Dim - 1, maxInt8Dim, maxInt8Dim + 1} {
		v := make([]float32, n)
		for i := range v {
			v[i] = 255
		}
		v[0] = 0
		a, b := make([]byte, n), make([]byte, n)
		quantizeEncodingEnabled = false
		p := quantizeRow(v, a)
		quantizeEncodingEnabled = true
		q := quantizeRow(v, b)
		if p != q || !bytes.Equal(a, b) {
			t.Fatal("sum overflow or tail mismatch", n)
		}
	}
}
