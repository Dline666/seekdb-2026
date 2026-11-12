package engine

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"minivectordb/internal/types"
)

// TestParallelVectorParseMatchesSerial runs the same stream of multi-row
// INSERTs, many of them invalid somewhere, against two databases: one with
// the vector literals parsed ahead on two goroutines, one with the plain
// row loop. Every statement must give the same affected count or the same
// error (name and message), and both tables must end up identical.
func TestParallelVectorParseMatchesSerial(t *testing.T) {
	defer func() { parallelInsertRows = 16 }()
	rng := rand.New(rand.NewSource(5))
	bad := []string{"nan", "inf", "1e39", "-1e39", "", "x", "1,2", "[1]", "1e", "0x1"}
	vec := func(dim int) string {
		v := make([]string, dim)
		for i := range v {
			v[i] = fmt.Sprint(rng.Float32())
		}
		switch rng.Intn(40) {
		case 0:
			v[rng.Intn(dim)] = bad[rng.Intn(len(bad))]
		case 1:
			v = v[:dim-1] // wrong dimension
		case 2:
			return "1,2,3" // not a vector literal
		}
		return "[" + strings.Join(v, ",") + "]"
	}
	var stream []string
	stream = append(stream,
		"CREATE TABLE a (id BIGINT PRIMARY KEY, c VARCHAR(4), e VECTOR(8))",
		"CREATE TABLE b (e VECTOR(5), id BIGINT PRIMARY KEY, n BIGINT)")
	next := 1
	for i := 0; i < 300; i++ {
		rows := []int{1, 2, 15, 16, 17, 33, 100}[rng.Intn(7)]
		var vals []string
		for r := 0; r < rows; r++ {
			id := next
			next++
			if rng.Intn(60) == 0 {
				id = rng.Intn(next) + 1 // possible duplicate key
			}
			c := []string{"ab", "abcd", "abcde", "你好"}[rng.Intn(4)] // abcde is too long
			if rng.Intn(3) != 0 {
				c = "ok"
			}
			switch i % 3 {
			case 0:
				vals = append(vals, fmt.Sprintf("(%d,'%s','%s')", id, c, vec(8)))
			case 1:
				vals = append(vals, fmt.Sprintf("('%s',%d,%d)", vec(5), id, rng.Intn(9)))
			default:
				vals = append(vals, fmt.Sprintf("('%s',%d,'%s')", vec(8), id, c))
			}
		}
		switch i % 3 {
		case 0:
			stream = append(stream, "INSERT INTO a VALUES "+strings.Join(vals, ","))
		case 1:
			stream = append(stream, "INSERT INTO b VALUES "+strings.Join(vals, ","))
		default:
			stream = append(stream, "INSERT INTO a (e, id, c) VALUES "+strings.Join(vals, ","))
		}
		if i%50 == 25 {
			stream = append(stream, "BEGIN", "INSERT INTO b VALUES ('"+vec(5)+"', 999999, 1)", "ROLLBACK")
		}
	}
	stream = append(stream, "SELECT id, c, e FROM a ORDER BY id", "SELECT id, n, e FROM b ORDER BY id")

	run := func(minRows int) []string {
		parallelInsertRows = minRows
		db := open(t, t.TempDir())
		defer db.Close()
		var out []string
		for _, sql := range stream {
			r, err := db.Exec(sql)
			var se *types.SQLError
			switch {
			case err == nil && r.IsQuery:
				out = append(out, fmt.Sprint(r.Columns, r.Rows))
			case err == nil:
				out = append(out, fmt.Sprint("ok ", r.Affected))
			case errors.As(err, &se):
				out = append(out, "error "+se.Name+": "+se.Error())
			default:
				t.Fatalf("%.80s: %v", sql, err)
			}
		}
		return out
	}
	serial, parallel := run(1<<30), run(1)
	failed := 0
	for i := range serial {
		if strings.HasPrefix(serial[i], "error") {
			failed++
		}
		if serial[i] != parallel[i] {
			t.Fatalf("statement %d %.100s:\n serial   %.300s\n parallel %.300s", i, stream[i], serial[i], parallel[i])
		}
	}
	if failed < 20 {
		t.Fatalf("only %d failing statements: the stream does not exercise the error paths", failed)
	}
	t.Logf("%d statements, %d failed identically", len(stream), failed)
}
