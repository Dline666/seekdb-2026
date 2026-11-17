package engine

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// Expected nearest ID and distance are analytic: one differing coordinate,
// versus 32 identical differences in every earlier row. No production heap,
// comparator, quantizer or distance function is used for the expectation.
func TestSubnormalQuantizationExactness(t *testing.T) {
	for _, scale := range []uint32{64, 300} {
		for _, tinyQuery := range []bool{false, true} {
			t.Run(fmt.Sprintf("units%d_query%v", scale, tinyQuery), func(t *testing.T) {
				db := open(t, t.TempDir())
				defer db.Close()
				mustExec(t, db, "CREATE TABLE docs(id BIGINT PRIMARY KEY, category VARCHAR(8), embedding VECTOR(32))")
				high := math.Float32frombits(scale)
				delta := scale / 2
				if scale == 300 {
					delta = 60
					if tinyQuery {
						delta = 50
					}
				}
				middle := math.Float32frombits(scale - delta)
				q, target, other := make([]float32, 32), make([]float32, 32), make([]float32, 32)
				for i := range q {
					q[i] = high
					target[i] = high
					other[i] = middle
				}
				if tinyQuery {
					q[0] = 0
				} else {
					target[0] = 0
				}
				rows := make([]string, 0, 129)
				for i := 1; i <= 128; i++ {
					rows = append(rows, fmt.Sprintf("(%d,'x','%s')", i, vectorText(other)))
				}
				rows = append(rows, fmt.Sprintf("(129,'x','%s')", vectorText(target)))
				mustExec(t, db, "INSERT INTO docs VALUES "+strings.Join(rows, ","))
				for _, where := range []string{"", " WHERE category='x'"} {
					sql := fmt.Sprintf("SELECT id,L2_DISTANCE(embedding,'%s') AS d FROM docs%s ORDER BY d LIMIT 1", vectorText(q), where)
					got := mustExec(t, db, sql).Rows
					if len(got) != 1 || got[0][0].I != 129 {
						t.Fatalf("unsafe tiny-scale prune: got %v, expected ID 129", got)
					}
					if got[0][1].F != float64(high) {
						t.Fatalf("distance got %.17g want %.17g", got[0][1].F, float64(high))
					}
				}
			})
		}
	}
}
