package engine

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"strings"

	sp "minivectordb/internal/sqlparse"
	"minivectordb/internal/types"
)

// coerce converts a value to the storage type of column c.
func coerce(c types.Column, v types.Value) (types.Value, *types.SQLError) {
	return coerceVectorInto(c, v, nil)
}

func coerceVectorInto(c types.Column, v types.Value, scratch []float32) (types.Value, *types.SQLError) {
	switch c.Type {
	case types.TBigint:
		switch v.K {
		case types.KInt:
			return v, nil
		case types.KFloat:
			r := math.Round(v.F)
			if math.IsNaN(r) || r < -9.223372036854775808e18 || r >= 9.223372036854775808e18 {
				return v, types.ErrOutOfRange(c.Name)
			}
			return types.Int(int64(r)), nil
		case types.KStr:
			s := strings.TrimSpace(v.S)
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return types.Int(i), nil
			} else if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
				return v, types.ErrOutOfRange(c.Name)
			}
			return v, types.ErrBadValue("integer", v.S, c.Name)
		}
		return v, types.ErrBadValue("integer", types.FormatVector(v.V), c.Name)
	case types.TVarchar:
		var s string
		switch v.K {
		case types.KStr:
			s = v.S
		case types.KInt:
			s = strconv.FormatInt(v.I, 10)
		case types.KFloat:
			s = strconv.FormatFloat(v.F, 'g', -1, 64)
		default:
			return v, types.ErrBadValue("string", types.FormatVector(v.V), c.Name)
		}
		if types.CharLen(s) > c.Param {
			return v, types.ErrDataTooLong(c.Name)
		}
		return types.Str(s), nil
	default:
		var vec []float32
		switch v.K {
		case types.KVec:
			vec = v.V
		case types.KStr:
			var err *types.SQLError
			if vec, err = types.ParseVectorInto(v.S, scratch); err != nil {
				return v, err
			}
		default:
			return v, types.ErrBadValue("vector", valueText(v), c.Name)
		}
		if len(vec) != c.Param {
			return v, types.ErrVectorDim(c.Param, len(vec))
		}
		return types.Vec(vec), nil
	}
}

func valueText(v types.Value) string {
	switch v.K {
	case types.KInt:
		return strconv.FormatInt(v.I, 10)
	case types.KFloat:
		return strconv.FormatFloat(v.F, 'g', -1, 64)
	case types.KVec:
		return types.FormatVector(v.V)
	}
	return v.S
}

func (db *DB) execInsert(s *sp.Insert) (*Result, error) {
	t, err := db.table(s.Table)
	if err != nil {
		return nil, err
	}
	// target[i] is the definition index for the i-th supplied value.
	target := make([]int, 0, len(t.Cols))
	if s.Cols == nil {
		for i := range t.Cols {
			target = append(target, i)
		}
	} else {
		seen := make([]bool, len(t.Cols))
		for _, name := range s.Cols {
			i := t.colIndex(name)
			if i < 0 {
				return nil, types.ErrBadField(name)
			}
			if seen[i] {
				return nil, types.ErrDupField(name)
			}
			seen[i] = true
			target = append(target, i)
		}
		// No NULLs and no defaults: every column must be supplied.
		for i, ok := range seen {
			if !ok {
				return nil, types.ErrNoDefault(t.Cols[i].Name)
			}
		}
	}
	sc := &scope{}
	chs := make([]change, 0, len(s.Rows))
	fresh := make(map[int64]struct{}, len(s.Rows))
	row := make([]types.Value, len(t.Cols))
	// A table has at most one vector column, and encode copies it before the
	// next row. This buffer belongs only to this statement; expressions that
	// already return a vector keep ownership of their own backing arrays.
	var vectorScratch []float32
	var enc []byte
	// Reserve the fixed-width payload plus a small VARCHAR allowance. Rows
	// in chs retain earlier backing arrays when enc grows, so avoiding that
	// growth also reduces the live heap of a multi-row INSERT. Cap the eager
	// reservation; large/variable-width statements still grow normally.
	perRow := t.varBase + 8*len(t.Cols)
	if len(s.Rows) <= (1<<20)/perRow {
		enc = make([]byte, 0, len(s.Rows)*perRow)
	}
	for _, exprs := range s.Rows {
		if len(exprs) != len(target) {
			return nil, types.ErrColCount()
		}
		for j, e := range exprs {
			// Literal VALUES need no compiled expression or captured closure.
			// Keep the general evaluator for functions and other expressions.
			var value types.Value
			switch e := e.(type) {
			case *sp.IntLit:
				value = types.Int(e.V)
			case *sp.FloatLit:
				value = types.Float(e.V)
			case *sp.StrLit:
				value = types.Str(e.V)
			default:
				c, err := sc.compile(e)
				if err != nil {
					return nil, err
				}
				value = c.fn(nil)
			}
			col := target[j]
			v, err := coerceVectorInto(t.Cols[col], value, vectorScratch)
			if err != nil {
				return nil, err
			}
			if value.K == types.KStr && v.K == types.KVec {
				vectorScratch = v.V
			}
			row[col] = v
		}
		pk := row[t.PK].I
		if _, dup := fresh[pk]; dup {
			return nil, types.ErrDupKey(pk)
		}
		if _, exists := db.lookup(t, pk); exists {
			return nil, types.ErrDupKey(pk)
		}
		fresh[pk] = struct{}{}
		start := len(enc)
		enc = t.encode(enc, row)
		chs = append(chs, change{t: t, pk: pk, row: enc[start:len(enc):len(enc)]})
	}
	if err := db.write(chs); err != nil {
		return nil, ioErr(err)
	}
	return &Result{Affected: int64(len(chs)), HasAffected: true}, nil
}

// match returns the visible (pk, offset) pairs satisfying where. A conjunct
// of the form pk = constant turns the scan into a point lookup.
func (db *DB) match(t *Table, where sp.Expr, sc *scope) ([]int64, []int64, *types.SQLError) {
	var pred func([]byte) bool
	if where != nil {
		p, err := sc.pred(where)
		if err != nil {
			return nil, nil, err
		}
		pred = p
	}
	var pks, offs []int64
	if pk, ok := pointKey(t, where); ok {
		if off, found := db.lookup(t, pk); found && (pred == nil || pred(db.rowBytes(off))) {
			pks, offs = append(pks, pk), append(offs, off)
		}
		return pks, offs, nil
	}
	db.forEach(t, func(pk, off int64) bool {
		if pred == nil || pred(db.rowBytes(off)) {
			pks, offs = append(pks, pk), append(offs, off)
		}
		return true
	})
	return pks, offs, nil
}

// pointKey finds "pk = integer constant" among the AND-ed conjuncts.
func pointKey(t *Table, e sp.Expr) (int64, bool) {
	switch x := e.(type) {
	case *sp.And:
		if k, ok := pointKey(t, x.L); ok {
			return k, true
		}
		return pointKey(t, x.R)
	case *sp.Cmp:
		if x.Op != "=" {
			return 0, false
		}
		l, r := x.L, x.R
		if _, ok := l.(*sp.ColRef); !ok {
			l, r = r, l
		}
		c, ok := l.(*sp.ColRef)
		if !ok || !strings.EqualFold(c.Name, t.Cols[t.PK].Name) {
			return 0, false
		}
		if c.Table != "" && !strings.EqualFold(c.Table, t.Name) {
			return 0, false
		}
		if k, ok := r.(*sp.IntLit); ok {
			return k.V, true
		}
	}
	return 0, false
}

type updateSet struct {
	col int
	c   *compiled
}

// Keep statement planning bounded when an UPDATE touches a large part of a
// table. Tests lower this threshold to exercise the same path on small rows.
var updateStreamBytes = 8 << 20

func (db *DB) execUpdate(s *sp.Update) (*Result, error) {
	t, err := db.table(s.Table)
	if err != nil {
		return nil, err
	}
	sc := &scope{t: t}
	sets := make([]updateSet, 0, len(s.Sets))
	for _, a := range s.Sets {
		i := t.colIndex(a.Col)
		if i < 0 {
			return nil, types.ErrBadField(a.Col)
		}
		c, err := sc.compile(a.Expr)
		if err != nil {
			return nil, err
		}
		if i == t.PK {
			return nil, types.ErrPKUpdate()
		}
		sets = append(sets, updateSet{i, c})
	}
	pks, offs, serr := db.match(t, s.Where, sc)
	if serr != nil {
		return nil, serr
	}
	if int64(len(offs))*int64(t.varBase) > int64(updateStreamBytes) {
		return db.streamUpdate(t, offs, sets)
	}
	chs := make([]change, 0, len(pks))
	var enc []byte
	for k, pk := range pks {
		old := db.rowBytes(offs[k])
		row := t.decode(old)
		next := make([]types.Value, len(row))
		copy(next, row)
		for _, st := range sets {
			v, err := coerce(t.Cols[st.col], st.c.fn(old))
			if err != nil {
				return nil, err
			}
			next[st.col] = v
		}
		start := len(enc)
		enc = t.encode(enc, next)
		// Variable-width columns can make the actual payload much larger
		// than the fixed-width estimate. Switch before retaining more rows.
		if len(enc) > updateStreamBytes {
			return db.streamUpdate(t, offs, sets)
		}
		nb := enc[start:len(enc):len(enc)]
		if bytes.Equal(nb, db.rowBytes(offs[k])) {
			continue // unchanged row: counted as matched, nothing to write
		}
		chs = append(chs, change{t: t, pk: pk, row: nb})
	}
	if err := db.write(chs); err != nil {
		return nil, ioErr(err)
	}
	return &Result{Affected: int64(len(pks)), HasAffected: true}, nil
}

func (db *DB) streamUpdate(t *Table, offs []int64, sets []updateSet) (*Result, error) {
	// Keep the pre-statement offsets fixed across both passes, including
	// an explicit transaction's earlier writes. Sorting avoids random page
	// reads when the working set is larger than memory.
	sort.Slice(offs, func(i, j int) bool { return offs[i] < offs[j] })
	for _, off := range offs {
		old := db.rowBytes(off)
		for _, st := range sets {
			if _, err := coerce(t.Cols[st.col], st.c.fn(old)); err != nil {
				return nil, err
			}
		}
	}
	// All supported expressions are deterministic functions of this row
	// (constants, columns, comparisons, vector construction and L2). The
	// validation pass therefore catches every SQL error before any write.
	// Storage failures are fatal, exactly as on the planned write path.
	tx := db.txn
	autocommit := tx == nil
	if autocommit {
		tx = db.newTxn()
	}
	var enc []byte
	for _, off := range offs {
		old := db.rowBytes(off)
		pk := rowPK(old)
		row := t.decode(old)
		for _, st := range sets {
			v, err := coerce(t.Cols[st.col], st.c.fn(old))
			if err != nil {
				// A second-pass error would violate the determinism invariant;
				// never let a partially written statement be committed later.
				return nil, ioErr(err)
			}
			row[st.col] = v
		}
		enc = t.encode(enc[:0], row)
		if bytes.Equal(enc, old) {
			continue
		}
		if err := db.apply(tx, []change{{t: t, pk: pk, row: enc}}); err != nil {
			return nil, ioErr(err)
		}
	}
	if autocommit {
		if err := db.commitTxn(tx); err != nil {
			return nil, ioErr(err)
		}
	}
	return &Result{Affected: int64(len(offs)), HasAffected: true}, nil
}

func (db *DB) execDelete(s *sp.Delete) (*Result, error) {
	t, err := db.table(s.Table)
	if err != nil {
		return nil, err
	}
	pks, _, serr := db.match(t, s.Where, &scope{t: t})
	if serr != nil {
		return nil, serr
	}
	chs := make([]change, len(pks))
	for i, pk := range pks {
		chs[i] = change{t: t, pk: pk}
	}
	if err := db.write(chs); err != nil {
		return nil, ioErr(err)
	}
	return &Result{Affected: int64(len(pks)), HasAffected: true}, nil
}
