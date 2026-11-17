package types

import (
	"math"
	"math/big"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestEiselLemireTableMatchesGoSource pins our copy of the Eisel-Lemire
// table to the one in the Go source tree that built this toolchain: every
// one of the 696 rows and both range constants must be identical.
func TestEiselLemireTableMatchesGoSource(t *testing.T) {
	p := filepath.Join(runtime.GOROOT(), "src", "strconv", "eisel_lemire.go")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Skipf("cannot read %s: %v", p, err)
	}
	src := string(data)

	rowRe := regexp.MustCompile(`\{0x([0-9A-Fa-f]{16}), 0x([0-9A-Fa-f]{16})\}`)
	rows := rowRe.FindAllStringSubmatch(src, -1)
	if len(rows) != len(detailedPowersOfTen) {
		t.Fatalf("upstream table has %d rows, ours has %d", len(rows), len(detailedPowersOfTen))
	}
	for i, r := range rows {
		hi, err := strconv.ParseUint(r[1], 16, 64)
		if err != nil {
			t.Fatalf("row %d: bad hi word %q: %v", i, r[1], err)
		}
		lo, err := strconv.ParseUint(r[2], 16, 64)
		if err != nil {
			t.Fatalf("row %d: bad lo word %q: %v", i, r[2], err)
		}
		if got := detailedPowersOfTen[i]; got != [2]uint64{hi, lo} {
			t.Fatalf("row %d: ours is {0x%X, 0x%X}, upstream is %s", i, got[0], got[1], r[0])
		}
	}

	for _, tc := range []struct {
		name string
		re   *regexp.Regexp
		want int64
	}{
		{"detailedPowersOfTenMinExp10", regexp.MustCompile(`detailedPowersOfTenMinExp10 = ([+-]?\d+)`), detailedPowersOfTenMinExp10},
		{"detailedPowersOfTenMaxExp10", regexp.MustCompile(`detailedPowersOfTenMaxExp10 = ([+-]?\d+)`), detailedPowersOfTenMaxExp10},
	} {
		m := tc.re.FindStringSubmatch(src)
		if m == nil {
			t.Fatalf("constant %s not found in upstream source", tc.name)
		}
		v, err := strconv.ParseInt(m[1], 10, 32)
		if err != nil || int64(tc.want) != v {
			t.Fatalf("%s: ours %d, upstream %q", tc.name, tc.want, m[1])
		}
	}
}

// TestEiselLemireMatchesStrconv sweeps millions of (mantissa, exponent)
// pairs across the full input space. Whenever the conversion reports a
// result it must be the very same bits strconv produces for the same value,
// and the cases strconv converts to a normal float64 must almost always be
// taken: a fast path that silently declines would hide behind decimalExact.
func TestEiselLemireMatchesStrconv(t *testing.T) {
	r := rand.New(rand.NewSource(31337))
	okCount, convertible := 0, 0
	for i := 0; i < 5000000; i++ {
		var man uint64
		switch r.Intn(4) {
		case 0: // uniform over the full uint64 range
			man = r.Uint64()
		case 1: // 1..19 decimal digits
			d := 1 + r.Intn(19)
			man = uint64(r.Intn(9) + 1)
			for j := 1; j < d; j++ {
				man = man*10 + uint64(r.Intn(10))
			}
		case 2: // at or near a power of two
			b := uint64(1) << r.Intn(64)
			man = b + uint64(r.Intn(5)) - 2
		default: // small integers
			man = uint64(r.Intn(1 << 30))
		}
		var exp10 int
		if i%3 == 0 {
			exp10 = r.Intn(61) - 30 // dense sweep of [-30, 30]
		} else {
			exp10 = r.Intn(696) - 348 // [-348, 347]
		}
		s := strconv.FormatUint(man, 10) + "e" + strconv.Itoa(exp10)
		want, err := strconv.ParseFloat(s, 64)
		if err == nil && want != 0 && math.Abs(want) >= smallestNormal {
			convertible++
		}
		got, ok := eiselLemire64(man, exp10)
		if !ok {
			continue
		}
		okCount++
		if err != nil {
			t.Fatalf("eiselLemire64(%d, %d) = %v, strconv reports %v", man, exp10, got, err)
		}
		if math.Float64bits(got) != math.Float64bits(want) {
			t.Fatalf("eiselLemire64(%d, %d) = %v (%016x), strconv = %v (%016x)",
				man, exp10, got, math.Float64bits(got), want, math.Float64bits(want))
		}
	}
	if convertible == 0 {
		t.Fatal("no convertible cases generated")
	}
	if float64(okCount) < 0.99*float64(convertible) {
		t.Fatalf("eiselLemire64 took only %d of %d convertible cases", okCount, convertible)
	}
}

// ratSigDigits decomposes an exact positive rational into digits and a
// decimal exponent so that value = 0.digits * 10^exp10, with no leading or
// trailing zeros in digits. A dyadic rational needs at most 1075 fraction
// digits, so 1100 places always captures its full expansion.
func ratSigDigits(x *big.Rat) (digits string, exp10 int) {
	s := x.FloatString(1100)
	dot := strings.IndexByte(s, '.')
	intPart, fracPart := s[:dot], s[dot+1:]
	all := intPart + fracPart
	z := 0
	for z < len(all) && all[z] == '0' {
		z++
	}
	digits = strings.TrimRight(all[z:], "0")
	return digits, len(intPart) - z
}

// roundSig rounds a digit string to k digits (half up). It returns the new
// digit string and the exponent shift a carry produced.
func roundSig(digits string, k int) (string, int) {
	if len(digits) <= k {
		return digits, 0
	}
	head := []byte(digits[:k])
	if digits[k] < '5' {
		return string(head), 0
	}
	for j := k - 1; j >= 0; j-- {
		if head[j] != '9' {
			head[j]++
			return string(head), 0
		}
		head[j] = '0'
	}
	return "1", 1
}

// adjustSig adds delta to the integer spelled by digits, keeping it a plain
// digit string.
func adjustSig(digits string, delta int64) string {
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return digits
	}
	return strconv.FormatInt(n+delta, 10)
}

// TestParseFloatHalfwayCases feeds literals that sit exactly on, or one last
// digit unit away from, the midpoint between two adjacent float64 values.
// Every accepted result must equal strconv bit for bit: these are the cases
// where the rounding decision is decided by a single bit.
func TestParseFloatHalfwayCases(t *testing.T) {
	r := rand.New(rand.NewSource(555))
	for i := 0; i < 4000; i++ {
		var v float64
		switch r.Intn(4) {
		case 0:
			v = r.Float64() // in [0, 1)
		case 1:
			v = 1 + r.Float64() // next to 1.0
		case 2:
			v = math.Ldexp(1+r.Float64(), r.Intn(2000)-1000)
		default:
			v = math.Ldexp(1+r.Float64(), -(1021 + r.Intn(50))) // near subnormal
		}
		if v == 0 || math.IsInf(v, 0) {
			continue
		}
		mid := new(big.Rat).SetFloat64(v)
		mid.Add(mid, new(big.Rat).SetFloat64(math.Nextafter(v, math.Inf(1))))
		mid.Mul(mid, big.NewRat(1, 2))
		digits, exp10 := ratSigDigits(mid)
		if len(digits) < 20 {
			continue
		}
		for _, k := range []int{17, 18, 19} {
			rd, shift := roundSig(digits, k)
			lits := []string{
				"0." + digits[:k] + "e" + strconv.Itoa(exp10),                // truncated midpoint
				"0." + rd + "e" + strconv.Itoa(exp10+shift),                  // rounded midpoint
				"0." + adjustSig(digits[:k], 1) + "e" + strconv.Itoa(exp10),  // +1 ulp last digit
				"0." + adjustSig(digits[:k], -1) + "e" + strconv.Itoa(exp10), // -1 ulp last digit
			}
			for _, lit := range lits {
				checkFastAgainstStrconv(t, lit)
				checkFastAgainstStrconv(t, "-"+lit)
				checkAgainstReference(t, "["+lit+"]")
			}
		}
	}
}

// fuzzSpace returns a random padding of the kinds a client can put around
// an element, including Unicode spaces that strings.TrimSpace removes.
func fuzzSpace(r *rand.Rand) string {
	spaces := []string{"", " ", "  ", "\t", "\n", "\r", " \t", "\u00a0", "\u3000", " \u00a0 "}
	return spaces[r.Intn(len(spaces))]
}

// fuzzElement returns one vector element, well formed or not.
func fuzzElement(r *rand.Rand) string {
	switch r.Intn(14) {
	case 0, 1, 2: // workload shape: float32 widened to float64
		return strconv.FormatFloat(float64(float32(r.NormFloat64())), 'f', -1, 64)
	case 3: // short decimal
		return strconv.FormatFloat(r.NormFloat64()*math.Pow(10, float64(r.Intn(20)-10)), 'g', -1, 64)
	case 4: // 1-25 digit mantissa, optional point and exponent
		var sb strings.Builder
		if r.Intn(4) == 0 {
			sb.WriteByte("+- "[r.Intn(3)]) // includes a stray space sometimes
		}
		nd := 1 + r.Intn(25)
		for i := 0; i < nd; i++ {
			if i > 0 && r.Intn(8) == 0 {
				sb.WriteByte('.')
			}
			sb.WriteByte(byte('0' + r.Intn(10)))
		}
		if r.Intn(2) == 0 {
			sb.WriteByte("eE"[r.Intn(2)])
			sb.WriteByte("+-e"[r.Intn(3)]) // sign, or an 'e' glitch
			n := r.Intn(12)
			for i := 0; i < n; i++ {
				sb.WriteByte(byte('0' + r.Intn(10)))
			}
		}
		return sb.String()
	case 5: // forms without an integer part or a fraction part
		forms := []string{".5", "-.5", "+.5", "5.", "-5.", ".0", "0.", "-.0"}
		return forms[r.Intn(len(forms))]
	case 6: // exponent shapes, some broken
		forms := []string{
			"1e5", "2E-3", "3e+07", "1e", "1e+", "1e-", "2E", "1e00005",
			"1e0000000000000000000005", "1e309", "1e-330", "1e-400", "9e999",
		}
		return forms[r.Intn(len(forms))]
	case 7: // float32 overflow and underflow
		forms := []string{
			"3.4028234663852886e38", "3.4028235e38", "3.4028236e38",
			"-3.4028235e38", "1e38", "1e39", "-1e39", "1e-45", "1e-46", "1e-50",
			"7e-46", "1.1754943508222875e-38",
		}
		return forms[r.Intn(len(forms))]
	case 8: // words and other bases that the format gate rejects
		forms := []string{
			"nan", "NaN", "inf", "Inf", "+Inf", "-Inf", "Infinity",
			"-infinity", "0x1p3", "0X10", "1_000", "1__0", "0b101",
		}
		return forms[r.Intn(len(forms))]
	case 9: // plain junk
		junk := []string{
			"abc", "o", "e5", "-", "+", ".", "+.", "-.", "--1", "++1",
			"1-", "1+", "1.2.3", "1e1e1", "1 2", "", " ",
		}
		return junk[r.Intn(len(junk))]
	case 10: // integer-looking elements, also huge ones
		if r.Intn(2) == 0 {
			return strconv.Itoa(r.Intn(100000) - 50000)
		}
		return strconv.FormatUint(r.Uint64(), 10) + "0000000000"
	case 11: // tiny and huge float64 boundaries
		forms := []string{
			"1.7976931348623157e308", "1.7976931348623159e308", "1e308",
			"5e-324", "4.9406564584124654e-324", "2.2250738585072014e-308", "1e-323",
			"9007199254740993", "0.1", "0.30000000000000004",
		}
		return forms[r.Intn(len(forms))]
	default: // plain mid-range literals
		return strconv.FormatFloat(r.Float64()*100-50, 'f', 1+r.Intn(18), 64)
	}
}

// TestParseVectorMatchesReferenceFuzz drives random vector literals, many of
// them malformed, through the oracle comparison: acceptance, error identity
// and every float32 bit must match, including on dirty and undersized dst
// buffers.
func TestParseVectorMatchesReferenceFuzz(t *testing.T) {
	r := rand.New(rand.NewSource(24681012))
	for i := 0; i < 2000000; i++ {
		var b strings.Builder
		b.WriteString(fuzzSpace(r))
		if r.Intn(20) != 0 {
			b.WriteByte('[') // missing bracket in 1/20 of cases
		}
		n := r.Intn(6)
		for j := 0; j < n; j++ {
			if j > 0 {
				b.WriteString(fuzzSpace(r))
				b.WriteByte(',')
			}
			b.WriteString(fuzzSpace(r))
			b.WriteString(fuzzElement(r))
		}
		// Commas in odd places: leading, trailing, doubled.
		switch r.Intn(10) {
		case 0:
			b.WriteString(",")
		case 1:
			b.WriteString(", , ")
		case 2:
			b.WriteString(" ,,")
		case 3:
			b.WriteString(",")
			b.WriteString(fuzzElement(r)) // trailing comma then element: still fine
		}
		b.WriteString(fuzzSpace(r))
		if r.Intn(20) != 0 {
			b.WriteByte(']')
		}
		b.WriteString(fuzzSpace(r))
		checkAgainstReference(t, b.String())
	}
}
