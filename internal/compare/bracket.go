package compare

import (
	"strings"

	"github.com/shadowcut/shadowcut/internal/rowset"
)

// Bracketed reports whether shadow result s is explainable as Postgres state at
// some moment between p1 (read before s) and p2 (read after s). Under continuous
// writes, whole-table aggregates never match exactly, but a shadow result whose
// rows each come from p1 or p2, and whose changing numeric/time cells lie between
// the p1 and p2 values for the same group, is replication churn, not a behaviour
// difference.
//
// Rows are matched by their "group key": every cell that is not a number or a
// timestamp. Order is ignored.
func Bracketed(p1, s, p2 *rowset.Result) bool {
	if p1.Err != "" || s.Err != "" || p2.Err != "" || len(p1.Columns) != len(s.Columns) || len(p2.Columns) != len(s.Columns) {
		return false
	}
	if len(p1.Rows) == 1 && len(s.Rows) == 1 && len(p2.Rows) == 1 {
		return between(p1.Rows[0], s.Rows[0], p2.Rows[0])
	}
	g1, g2 := groups(p1.Rows), groups(p2.Rows)
	if len(g1) != len(p1.Rows) || len(g2) != len(p2.Rows) || len(groups(s.Rows)) != len(s.Rows) {
		return false // rows are not identifiable by their non-numeric cells
	}
	for _, r := range s.Rows {
		k := groupKey(r)
		a, inA := g1[k]
		b, inB := g2[k]
		switch {
		case inA && inB:
			if !between(a, r, b) {
				return false
			}
		case inA:
			if !rowset.RowsEqual(a, r) {
				return false
			}
		case inB:
			if !rowset.RowsEqual(b, r) {
				return false
			}
		default:
			return false // a group neither Postgres read had
		}
	}
	// groups present in both Postgres reads must be present in the shadow read,
	// unless the result is LIMIT-truncated (callers pass hasLimit separately).
	return true
}

// BracketedStable is Bracketed plus: every group present in both p1 and p2 is
// also present in s (use for results without LIMIT).
func BracketedStable(p1, s, p2 *rowset.Result) bool {
	if !Bracketed(p1, s, p2) {
		return false
	}
	gs := groups(s.Rows)
	g2 := groups(p2.Rows)
	for k := range groups(p1.Rows) {
		if _, ok := g2[k]; ok {
			if _, ok := gs[k]; !ok {
				return false
			}
		}
	}
	return true
}

func groups(rows []rowset.Row) map[string]rowset.Row {
	m := map[string]rowset.Row{}
	for _, r := range rows {
		m[groupKey(r)] = r
	}
	return m
}

func groupKey(r rowset.Row) string {
	var b strings.Builder
	for i, v := range r {
		if i > 0 {
			b.WriteByte('\x1f')
		}
		if v.IsNumber() || v.K == rowset.Time {
			b.WriteString("#")
			continue
		}
		b.WriteString(v.Key())
	}
	return b.String()
}

// between checks each cell of s lies within [a, b] (either order) or equals one.
func between(a, s, b rowset.Row) bool {
	for i := range s {
		if rowset.Equal(a[i], s[i]) || rowset.Equal(b[i], s[i]) {
			continue
		}
		switch {
		case s[i].IsNumber() && a[i].IsNumber() && b[i].IsNumber():
			x, _ := s[i].Float64()
			lo, _ := a[i].Float64()
			hi, _ := b[i].Float64()
			if lo > hi {
				lo, hi = hi, lo
			}
			// non-monotonic aggregates (avg) may overshoot slightly between reads:
			// allow one bracket-width of slack (zero when Postgres did not change)
			w := hi - lo
			lo, hi = lo-w, hi+w
			if x < lo || x > hi {
				return false
			}
		case s[i].K == rowset.Time && a[i].K == rowset.Time && b[i].K == rowset.Time:
			lo, hi := a[i].T, b[i].T
			if lo.After(hi) {
				lo, hi = hi, lo
			}
			if s[i].T.Before(lo) || s[i].T.After(hi) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// WindowShift reports whether s is a "sliding window" between p1 and p2 for an
// ordered LIMIT query whose first sort key (column key) is a number or time,
// e.g. "newest 25 edits" under continuous inserts. Every shadow row must either
// appear in p1 or p2, or have a sort key inside the envelope spanned by p1 and p2.
func WindowShift(p1, s, p2 *rowset.Result, key int) bool {
	if key < 0 || p1.Err != "" || s.Err != "" || p2.Err != "" || len(p1.Rows) == 0 || len(p2.Rows) == 0 {
		return false
	}
	lo, hi, ok := envelope(append(append([]rowset.Row{}, p1.Rows...), p2.Rows...), key)
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, r := range p1.Rows {
		seen[rowset.RowKey(r)] = true
	}
	for _, r := range p2.Rows {
		seen[rowset.RowKey(r)] = true
	}
	for _, r := range s.Rows {
		if seen[rowset.RowKey(r)] {
			continue
		}
		if key >= len(r) {
			return false
		}
		x, ok := sortVal(r[key])
		if !ok || x < lo || x > hi {
			return false
		}
	}
	return true
}

func envelope(rows []rowset.Row, key int) (lo, hi float64, ok bool) {
	first := true
	for _, r := range rows {
		if key >= len(r) {
			return 0, 0, false
		}
		x, ok := sortVal(r[key])
		if !ok {
			return 0, 0, false
		}
		if first || x < lo {
			lo = x
		}
		if first || x > hi {
			hi = x
		}
		first = false
	}
	return lo, hi, !first
}

func sortVal(v rowset.Value) (float64, bool) {
	if v.K == rowset.Time {
		return float64(v.T.UnixMicro()), true
	}
	return v.Float64()
}
