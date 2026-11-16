// Package compare diffs two query results (primary = PostgreSQL, shadow = Spanner).
package compare

import (
	"github.com/shadowcut/shadowcut/internal/rowset"
)

// Mode selects order sensitivity.
type Mode string

const (
	// Ordered requires identical row sequences.
	Ordered Mode = "ordered"
	// Unordered compares rows as multisets.
	Unordered Mode = "unordered"
	// Auto is Ordered when the query has a top-level ORDER BY, else Unordered.
	Auto Mode = "auto"
)

// Kind is the coarse shape of a difference.
type Kind string

const (
	Same          Kind = "same"
	ErrorShadow   Kind = "error_shadow"  // only the shadow (Spanner) failed
	ErrorPrimary  Kind = "error_primary" // only the primary (Postgres) failed
	ColumnsDiffer Kind = "columns_differ"
	OrderDiffers  Kind = "order_differs" // same multiset, different order
	RowsDiffer    Kind = "rows_differ"   // different multisets
)

// Diff describes how two results differ.
type Diff struct {
	Kind Kind
	// For RowsDiffer: indexes of rows present only on one side (multiset difference).
	OnlyPrimary []int
	OnlyShadow  []int
	// For OrderDiffers: first position where the sequences diverge.
	FirstDivergence int
}

// Equal reports whether the diff is a match.
func (d Diff) Equal() bool { return d.Kind == Same }

// Compare diffs primary vs shadow under mode (Auto must be resolved by the caller).
func Compare(p, s *rowset.Result, mode Mode) Diff {
	switch {
	case p.Err != "" && s.Err != "":
		return Diff{Kind: Same} // both failed: not a behavioural divergence we can act on
	case p.Err == "" && s.Err != "":
		return Diff{Kind: ErrorShadow}
	case p.Err != "" && s.Err == "":
		return Diff{Kind: ErrorPrimary}
	}
	if len(p.Columns) != len(s.Columns) {
		return Diff{Kind: ColumnsDiffer}
	}
	onlyP, onlyS := multisetDiff(p.Rows, s.Rows)
	if len(onlyP) > 0 || len(onlyS) > 0 {
		return Diff{Kind: RowsDiffer, OnlyPrimary: onlyP, OnlyShadow: onlyS}
	}
	if mode == Ordered {
		for i := range p.Rows {
			if !rowset.RowsEqual(p.Rows[i], s.Rows[i]) {
				return Diff{Kind: OrderDiffers, FirstDivergence: i}
			}
		}
	}
	return Diff{Kind: Same}
}

// multisetDiff returns indexes of rows that have no partner on the other side.
// Rows are matched by exact equality; floats use exact bit equality via keys,
// with a fallback pairwise pass so that Equal() semantics (NaN==NaN, int vs
// integral numeric) are honoured.
func multisetDiff(a, b []rowset.Row) (onlyA, onlyB []int) {
	buckets := map[string][]int{}
	for j, r := range b {
		k := rowset.RowKey(r)
		buckets[k] = append(buckets[k], j)
	}
	used := make([]bool, len(b))
	var pendingA []int
	for i, r := range a {
		k := rowset.RowKey(r)
		if js := buckets[k]; len(js) > 0 {
			used[js[0]] = true
			buckets[k] = js[1:]
			continue
		}
		pendingA = append(pendingA, i)
	}
	// slow path for rows whose keys differ but values are Equal (rare)
	for _, i := range pendingA {
		found := false
		for j := range b {
			if !used[j] && rowset.RowsEqual(a[i], b[j]) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			onlyA = append(onlyA, i)
		}
	}
	for j := range b {
		if !used[j] {
			onlyB = append(onlyB, j)
		}
	}
	return onlyA, onlyB
}
