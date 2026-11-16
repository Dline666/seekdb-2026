package types

// Diagnostic-only tests. Copy into a separate module snapshot before running.
// The decimal oracle is math/big.Rat, never strconv or candidate conversion code.
// Vector semantics intentionally round decimal -> float64 -> float32. Direct
// decimal -> float32 rounding is a different contract near double-rounding ties.

import (
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"sort"
	"strings"
	"testing"
)

func diagnosticDecimal64(t *testing.T, literal string) float64 {
	t.Helper()
	r, ok := new(big.Rat).SetString(literal)
	if !ok {
		t.Fatalf("invalid diagnostic decimal %q", literal)
	}
	want, _ := r.Float64() // Exact rational -> correctly rounded binary64.
	if want == 0 && strings.HasPrefix(literal, "-") {
		want = math.Copysign(0, -1) // Rat has no signed zero.
	}
	return want
}

func diagnosticFloat32Rat(bits uint32) *big.Rat {
	// Reinterpreting binary32 bits and widening to binary64 is exact. This
	// does not use a float64 -> float32 cast or candidate ToFloat32.
	return new(big.Rat).SetFloat64(float64(math.Float32frombits(bits)))
}

func diagnosticRound32(f float64) (uint32, bool) {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	var sign uint32
	if math.Signbit(f) {
		sign = 1 << 31
	}
	if f == 0 {
		return sign, true
	}
	x := new(big.Rat).SetFloat64(math.Abs(f))
	const largest = uint32(0x7f7fffff)
	max := diagnosticFloat32Rat(largest)
	if x.Cmp(max) > 0 {
		// Finite binary64 values slightly above MaxFloat32 can still round
		// to MaxFloat32. Reject only at/above the overflow midpoint.
		ulp := new(big.Rat).Sub(max, diagnosticFloat32Rat(largest-1))
		cut := new(big.Rat).Add(max, new(big.Rat).Quo(ulp, big.NewRat(2, 1)))
		if x.Cmp(cut) >= 0 {
			return 0, false
		}
		return sign | largest, true
	}
	lo, hi := uint32(0), largest
	for lo < hi {
		mid := lo + (hi-lo+1)/2
		if diagnosticFloat32Rat(mid).Cmp(x) <= 0 {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	lower := diagnosticFloat32Rat(lo)
	if lower.Cmp(x) == 0 || lo == largest {
		return sign | lo, true
	}
	mid := new(big.Rat).Add(lower, diagnosticFloat32Rat(lo+1))
	mid.Quo(mid, big.NewRat(2, 1))
	cmp := x.Cmp(mid)
	if cmp > 0 || cmp == 0 && lo&1 != 0 {
		lo++
	}
	return sign | lo, true
}

func diagnosticDyadicDecimal(t *testing.T, x *big.Rat) string {
	t.Helper()
	// The generated midpoint perturbations have denominator at most 2^1084.
	s := strings.TrimSuffix(strings.TrimRight(x.FloatString(1100), "0"), ".")
	back, ok := new(big.Rat).SetString(s)
	if !ok || back.Cmp(x) != 0 {
		t.Fatalf("diagnostic decimal generator lost exactness: %s", s)
	}
	return s
}

func diagnosticNear19(x *big.Rat) []string {
	// x is positive. Emit integer-mantissa scientific literals, avoiding the
	// extra leading zero of "0." + 19 digits and any signed-int64 restriction.
	s := x.FloatString(1100)
	dot := strings.IndexByte(s, '.')
	all := s[:dot] + s[dot+1:]
	first := 0
	for first < len(all) && all[first] == '0' {
		first++
	}
	if len(all)-first < 19 {
		panic("diagnosticNear19 requires a nonzero representable dyadic")
	}
	q, ok := new(big.Int).SetString(all[first:first+19], 10)
	if !ok {
		panic("invalid diagnostic significand")
	}
	exp := dot - first - 19
	var out []string
	for _, delta := range []int64{-1, 0, 1} {
		n := new(big.Int).Add(q, big.NewInt(delta))
		out = append(out, fmt.Sprintf("%se%+d", n.String(), exp))
	}
	return out
}

type diagnosticCounts struct {
	inputs, accepted, refused, vectors int
}

func TestFableDiagnosticDecimalOracle(t *testing.T) {
	groups := map[string]*diagnosticCounts{}
	vectorAccepted, vectorRejected, vectorSubnormal, negativeZero := 0, 0, 0, 0
	check := func(group, literal string, vector bool) {
		t.Helper()
		count := groups[group]
		if count == nil {
			count = &diagnosticCounts{}
			groups[group] = count
		}
		count.inputs++
		want := diagnosticDecimal64(t, literal)
		got, end, ok := scanFloat(literal, 0)
		if ok {
			count.accepted++
			if end != len(literal) || math.Float64bits(got) != math.Float64bits(want) {
				t.Fatalf("group=%s literal=%q scan=(%016x,end=%d) rational=%016x",
					group, literal, math.Float64bits(got), end, math.Float64bits(want))
			}
		} else {
			count.refused++
		}
		// Exercise the nonzero-offset scanner and its eight-byte read boundary.
		wrapped := "xxx" + literal + ",next"
		offset, offsetEnd, offsetOK := scanFloat(wrapped, 3)
		if offsetOK != ok || offsetOK && (offsetEnd != 3+len(literal) || math.Float64bits(offset) != math.Float64bits(want)) {
			t.Fatalf("group=%s offset scanner disagrees for %q", group, literal)
		}
		if !vector {
			return
		}
		count.vectors++
		want32, finite := diagnosticRound32(want)
		for _, padded := range []string{"[" + literal + "]", " [\t" + literal + "\u3000] "} {
			v, err := ParseVector(padded)
			if !finite {
				if err == nil || err.Code != 1366 || err.State != "HY000" || err.Name != "invalid_vector" {
					t.Fatalf("expected float32 range error for %q, got %v / %v", padded, v, err)
				}
				continue
			}
			if err != nil || len(v) != 1 || math.Float32bits(v[0]) != want32 {
				t.Fatalf("two-stage oracle mismatch for %q: got %v / %v, expected bits %08x", padded, v, err, want32)
			}
		}
		if finite {
			vectorAccepted++
			absBits := want32 & 0x7fffffff
			if absBits != 0 && absBits < 0x00800000 {
				vectorSubnormal++
			}
			if want32 == 0x80000000 {
				negativeZero++
			}
		} else {
			vectorRejected++
		}
	}

	rng := rand.New(rand.NewSource(0x5236))
	for i := 0; i < 10000; i++ {
		// Full 19-digit interval, including values above signed MaxInt64.
		man := uint64(1000000000000000000) + rng.Uint64()%uint64(9000000000000000000)
		digits := fmt.Sprintf("%d", man)
		exp := rng.Intn(696) - 348
		group, literal := "19-digit", ""
		switch i % 5 {
		case 0:
			literal = fmt.Sprintf("%se%+d", digits, rng.Intn(61)-30)
		case 1:
			group = "wide-exponents"
			literal = fmt.Sprintf("%se%+d", digits, exp)
		case 2:
			group = "point-19-digit"
			point := 1 + rng.Intn(18)
			literal = fmt.Sprintf("%s.%se%+d", digits[:point], digits[point:], exp)
		case 3:
			group = "leading-zero-fallback"
			literal = fmt.Sprintf("%s%se%+d", strings.Repeat("0", 1+i%4), digits, exp)
		case 4:
			group = "leading-zero-fit"
			literal = fmt.Sprintf("00%017de%+d", man%uint64(100000000000000000), rng.Intn(45)-22)
		}
		if i%2 == 0 {
			literal = "-" + literal
		} else if i%3 == 0 {
			literal = "+" + literal
		}
		check(group, literal, i%7 == 0)
	}

	for _, literal := range []string{
		"9007199254740989", "9007199254740990", "9007199254740991", "9007199254740992",
		"9007199254740993", "9007199254740995", "9007199254740991.5", "9007199254740991.4",
		"9007199254740991.6", "9223372036854775807", "9223372036854775808", "9999999999999999999",
		"10000000000000000000", "18446744073709551615", "18446744073709551616",
		"1e000000005", "1e0000000005", "1e00000000000000000005", ".5", "5.", "000001.25",
	} {
		check("mantissa-exponent-transition", literal, true)
		check("mantissa-exponent-transition", "-"+literal, true)
	}
	for _, literal := range []string{"0", "-0", "+0", "-0.0", "-0e000000400", "-0e-000000400"} {
		check("signed-zero", literal, true)
	}
	for _, literal := range []string{"5e-324", "4.9406564584124654e-324", "1e-323", "1e-324", "1e-400"} {
		check("float64-tiny", literal, true)
		check("float64-tiny", "-"+literal, true)
	}
	for _, literal := range []string{
		"2.2250738585072011e-308", "2.2250738585072012e-308", "2.2250738585072013e-308",
		"2.2250738585072014e-308", "2.2250738585072015e-308", "1.7976931348623157e308",
		"1.7976931348623158e308", "1.7976931348623159e308", "1e309", "1e-349", "1e348",
	} {
		check("float64-range-edge", literal, true)
		check("float64-range-edge", "-"+literal, true)
	}
	for _, literal := range []string{
		"3.4028234663852886e38", "3.4028235e38", "3.4028236e38", "1e39",
		"1.1754943508222875e-38", "1.4012984643248171e-45", "7e-46", "8e-46", "1e-46",
	} {
		check("float32-edge", literal, true)
		check("float32-edge", "-"+literal, true)
	}

	anchors := []uint64{0, 1, 0x000ffffffffffffe, 0x000fffffffffffff, 0x0010000000000000,
		0x3fefffffffffffff, 0x3ff0000000000000, 0x433fffffffffffff, 0x4340000000000000, 0x7feffffffffffffe}
	for i := 0; i < 40; i++ {
		anchors = append(anchors, uint64(1+rng.Intn(2045))<<52|rng.Uint64()&((1<<52)-1))
	}
	for _, bits := range anchors {
		low := new(big.Rat).SetFloat64(math.Float64frombits(bits))
		high := new(big.Rat).SetFloat64(math.Float64frombits(bits + 1))
		mid := new(big.Rat).Quo(new(big.Rat).Add(low, high), big.NewRat(2, 1))
		epsilon := new(big.Rat).Quo(new(big.Rat).Sub(high, low), big.NewRat(1024, 1))
		for _, delta := range []int64{-1, 0, 1} {
			x := new(big.Rat).Add(mid, new(big.Rat).Mul(epsilon, big.NewRat(delta, 1)))
			literal := diagnosticDyadicDecimal(t, x)
			group := "exact-midpoint-neighbor"
			if delta == 0 {
				group = "exact-midpoint"
			}
			check(group, literal, true)
			check(group, "-"+literal, true)
		}
		for _, literal := range diagnosticNear19(mid) {
			check("near-midpoint-19-digit", literal, true)
			check("near-midpoint-19-digit", "-"+literal, true)
		}
	}
	for _, bits := range []uint32{0, 1, 0x007fffff, 0x00800000, 0x3f7fffff, 0x3f800000, 0x3f800001, 0x7f7ffffe} {
		low, high := diagnosticFloat32Rat(bits), diagnosticFloat32Rat(bits+1)
		mid := new(big.Rat).Quo(new(big.Rat).Add(low, high), big.NewRat(2, 1))
		for _, literal := range append(diagnosticNear19(mid), diagnosticDyadicDecimal(t, mid)) {
			check("float32-midpoint", literal, true)
			check("float32-midpoint", "-"+literal, true)
		}
	}
	max32 := diagnosticFloat32Rat(0x7f7fffff)
	halfULP := new(big.Rat).Quo(new(big.Rat).Sub(max32, diagnosticFloat32Rat(0x7f7ffffe)), big.NewRat(2, 1))
	overflowMid := new(big.Rat).Add(max32, halfULP)
	for _, literal := range append(diagnosticNear19(overflowMid), diagnosticDyadicDecimal(t, overflowMid)) {
		check("float32-overflow-midpoint", literal, true)
		check("float32-overflow-midpoint", "-"+literal, true)
	}
	// Deliberate double-rounding cases: the decimal lies on one side of a
	// binary32 midpoint, but first rounds to that exact midpoint in binary64.
	for _, bits := range []uint32{0x3f800000, 0x3f800001} {
		mid := new(big.Rat).Quo(new(big.Rat).Add(diagnosticFloat32Rat(bits), diagnosticFloat32Rat(bits+1)), big.NewRat(2, 1))
		epsilon := new(big.Rat).SetFrac(big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 80))
		if bits&1 != 0 {
			epsilon.Neg(epsilon)
		}
		literal := diagnosticDyadicDecimal(t, new(big.Rat).Add(mid, epsilon))
		want32, finite := diagnosticRound32(diagnosticDecimal64(t, literal))
		even := bits + (bits & 1)
		if !finite || want32 != even {
			t.Fatalf("double-rounding diagnostic was not constructed correctly: %q -> %08x, expected %08x", literal, want32, even)
		}
		check("double-rounding", literal, true)
		check("double-rounding", "-"+literal, true)
	}

	for _, name := range []string{"19-digit", "point-19-digit", "leading-zero-fit", "signed-zero",
		"mantissa-exponent-transition", "float64-range-edge", "float32-edge", "exact-midpoint", "near-midpoint-19-digit", "float32-midpoint"} {
		if groups[name] == nil || groups[name].accepted == 0 {
			t.Fatalf("missing actual scanner fast-path acceptance for group %s", name)
		}
	}
	if groups["19-digit"].accepted < 1000 || groups["leading-zero-fallback"].accepted != 0 ||
		groups["float64-tiny"].accepted != 0 || groups["exact-midpoint"].refused == 0 {
		t.Fatalf("unexpected fast/fallback coverage: %+v", groups)
	}
	if vectorAccepted == 0 || vectorRejected == 0 || vectorSubnormal == 0 || negativeZero == 0 {
		t.Fatalf("missing vector boundary class: finite=%d rejected=%d subnormal=%d negative-zero=%d",
			vectorAccepted, vectorRejected, vectorSubnormal, negativeZero)
	}
	var names []string
	for name := range groups {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := groups[name]
		t.Logf("group=%s input=%d scan_fast=%d fallback=%d vector_oracle=%d", name, c.inputs, c.accepted, c.refused, c.vectors)
	}
	t.Logf("vector classes: finite=%d rejected=%d subnormal=%d negative-zero=%d", vectorAccepted, vectorRejected, vectorSubnormal, negativeZero)
}

func TestFableDiagnosticEiselRatOracle(t *testing.T) {
	inputs, accepted, refused, normalAccepted := 0, 0, 0, 0
	check := func(man uint64, exp int) {
		t.Helper()
		inputs++
		literal := fmt.Sprintf("%de%+d", man, exp)
		want := diagnosticDecimal64(t, literal)
		got, ok := eiselLemire64(man, exp)
		if !ok {
			refused++
			return
		}
		accepted++
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("Eisel (%d,%d)=%016x, exact rational=%016x", man, exp, math.Float64bits(got), math.Float64bits(want))
		}
		if man != 0 {
			if exp < -348 || exp > 347 || got == 0 || math.IsInf(got, 0) || math.Float64bits(got)&0x7ff0000000000000 == 0 {
				t.Fatalf("Eisel accepted outside its finite normal result domain: %s", literal)
			}
			normalAccepted++
		}
	}
	for exp := -349; exp <= 348; exp++ {
		for _, man := range []uint64{1, 9999999999999999999, ^uint64(0)} {
			check(man, exp)
		}
	}
	rng := rand.New(rand.NewSource(0x52e1))
	for i := 0; i < 6000; i++ {
		man := rng.Uint64()
		if i%3 == 0 {
			man = uint64(1000000000000000000) + man%uint64(9000000000000000000)
		} else if i%3 == 1 {
			man = uint64(1)<<uint(rng.Intn(64)) + uint64(i%3)
		}
		exp := rng.Intn(700) - 350
		if i%3 == 0 {
			exp = rng.Intn(61) - 30
		}
		check(man, exp)
	}
	for _, exp := range []int{-400, -348, 0, 347, 400} {
		check(0, exp)
	}
	if accepted == 0 || refused == 0 || normalAccepted < 2000 {
		t.Fatalf("insufficient Eisel coverage: input=%d accepted=%d refused=%d normal=%d", inputs, accepted, refused, normalAccepted)
	}
	t.Logf("Eisel rational oracle: input=%d accepted=%d refused=%d normal=%d", inputs, accepted, refused, normalAccepted)
}

func TestFableDiagnosticVectorLexicalEdges(t *testing.T) {
	for _, literal := range []string{"[1e]", "[1e+]", "[NaN]", "[Inf]", "[0x1p0]", "[1_0]", "[1,,2]", "[1.2junk]", "[.]", "[+]"} {
		if _, err := ParseVector(literal); err == nil || err.Code != 1366 || err.State != "HY000" || err.Name != "invalid_vector" {
			t.Fatalf("malformed literal unexpectedly accepted or wrong error: %q / %v", literal, err)
		}
	}
	for _, literal := range []string{"[1e39,]", "[1e39,\u3000]", "[1e39, \t ]"} {
		if _, err := ParseVector(literal); err == nil || !strings.HasPrefix(err.Msg, "Invalid vector literal ") {
			t.Fatalf("trailing-comma format precedence changed: %q / %v", literal, err)
		}
	}
	if _, err := ParseVector("[1e39,0]"); err == nil || err.Msg != "Vector element '1e39' is not a finite float32" {
		t.Fatalf("nontrailing overflow must remain an element-value error: %v", err)
	}
}
