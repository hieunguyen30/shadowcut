package compare

import (
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/shadowcut/shadowcut/internal/rowset"
)

func rs(rows ...rowset.Row) *rowset.Result {
	r := &rowset.Result{Rows: rows}
	if len(rows) > 0 {
		r.Columns = make([]rowset.Column, len(rows[0]))
	} else {
		r.Columns = []rowset.Column{{}, {}}
	}
	return r
}

func n(i int64) rowset.Value  { return rowset.Value{K: rowset.Int, I: i} }
func s(x string) rowset.Value { return rowset.Value{K: rowset.Text, S: x} }

func TestBracketCountsGrow(t *testing.T) {
	p1 := rs(rowset.Row{s("en"), n(100)}, rowset.Row{s("de"), n(50)})
	sh := rs(rowset.Row{s("en"), n(103)}, rowset.Row{s("de"), n(50)})
	p2 := rs(rowset.Row{s("en"), n(105)}, rowset.Row{s("de"), n(51)}, rowset.Row{s("fr"), n(1)})
	if !Bracketed(p1, sh, p2) {
		t.Fatal("growing counts should be bracketed")
	}
	bad := rs(rowset.Row{s("en"), n(120)}, rowset.Row{s("de"), n(50)}) // beyond one bracket-width of slack
	if Bracketed(p1, bad, p2) {
		t.Fatal("count above both reads is not churn")
	}
}

func TestBracketTimezoneNotChurn(t *testing.T) {
	day := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	tv := func(t time.Time) rowset.Value { return rowset.Value{K: rowset.Time, T: t} }
	p := rs(rowset.Row{tv(day), n(10)})
	sh := rs(rowset.Row{tv(day.Add(8 * time.Hour)), n(10)})
	if Bracketed(p, sh, p) {
		t.Fatal("a time-zone shift is not churn")
	}
}

// Property: if the shadow equals either Postgres read it is always bracketed.
func TestPropBracketEndpoints(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.IntRange(1, 6).Draw(t, "k")
		var a, b []rowset.Row
		for i := 0; i < k; i++ {
			key := s(string(rune('a' + i)))
			x := rapid.Int64Range(0, 100).Draw(t, "x")
			a = append(a, rowset.Row{key, n(x)})
			b = append(b, rowset.Row{key, n(x + rapid.Int64Range(0, 5).Draw(t, "d"))})
		}
		p1, p2 := rs(a...), rs(b...)
		if !BracketedStable(p1, rs(a...), p2) || !BracketedStable(p1, rs(b...), p2) {
			t.Fatal("endpoint not bracketed")
		}
	})
}

func TestWindowShift(t *testing.T) {
	// newest-3 window by id under inserts: p1 = 10,9,8; shadow = 11,10,9; p2 = 13,12,11
	row := func(i int64) rowset.Row { return rowset.Row{n(i), s("x")} }
	p1 := rs(row(10), row(9), row(8))
	sh := rs(row(11), row(10), row(9))
	p2 := rs(row(13), row(12), row(11))
	if !WindowShift(p1, sh, p2, 0) {
		t.Fatal("sliding window should be accepted")
	}
	if WindowShift(p1, rs(row(42), row(10), row(9)), p2, 0) {
		t.Fatal("row outside the envelope is not churn")
	}
}
