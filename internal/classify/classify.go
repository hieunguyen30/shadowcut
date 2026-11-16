// Package classify assigns a root cause to a shadow-read mismatch.
//
// The classifier is evidence-based: it inspects the actual rows that differ and
// the query's ORDER BY keys, and only names a cause when the data supports it.
// Anything it cannot explain is reported as "other" (never silently dropped).
package classify

import (
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shadowcut/shadowcut/internal/compare"
	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

// Category is a mismatch root cause.
type Category string

const (
	Collation      Category = "collation"
	NullOrdering   Category = "null_ordering"
	NullSemantics  Category = "null_semantics"     // e.g. concat() returns NULL on Spanner
	FuncSemantics  Category = "function_semantics" // a function returns a different value (e.g. Unicode upper())
	Precision      Category = "precision"
	Timezone       Category = "timezone"
	Nondeterminism Category = "nondeterministic_order" // ties / missing ORDER BY
	Isolation      Category = "isolation"
	ReplicationLag Category = "replication_lag"
	MissingRow     Category = "missing_row" // persistent row difference (drift)
	Unsupported    Category = "unsupported" // Spanner rejected the query
	Timeout        Category = "timeout"     // the Spanner shadow query exceeded its time limit
	Other          Category = "other"
)

// AllCategories lists categories in report order.
var AllCategories = []Category{Collation, NullOrdering, NullSemantics, FuncSemantics, Precision, Timezone, Nondeterminism, Isolation, ReplicationLag, MissingRow, Unsupported, Timeout, Other}

// Context carries execution context the proxy observed.
type Context struct {
	InTxn        bool // query ran inside an explicit transaction on Postgres
	TxnHasWrites bool // ... and that transaction had already written
	// Recheck is the diff obtained by re-running the query on both sides after the
	// replication-lag bound elapsed. Nil when no recheck was performed.
	Recheck *compare.Diff
}

// Result is the classifier's verdict.
type Result struct {
	Category   Category `json:"category"`
	Confidence string   `json:"confidence"` // high | medium | low
	Reason     string   `json:"reason"`
}

// Input bundles everything needed to classify one mismatch.
type Input struct {
	Info    sqlscan.Info
	Tokens  []sqlscan.Token // statement tokens (optional; enables predicate analysis)
	Args    []any           // bind parameters (optional)
	Primary *rowset.Result
	Shadow  *rowset.Result
	Diff    compare.Diff
	Ctx     Context
}

var nullSemanticFuncs = map[string]bool{"concat": true, "concat_ws": true, "array_cat": true, "array_to_string": true, "array_append": true, "array_prepend": true}

// Classify returns the root cause of a mismatch. It must only be called when
// in.Diff is not Same.
func Classify(in Input) Result {
	switch in.Diff.Kind {
	case compare.Same:
		return Result{Category: "", Confidence: "high", Reason: "no mismatch"}
	case compare.ErrorShadow:
		if isTimeout(in.Shadow.Err) {
			return Result{Category: Timeout, Confidence: "high", Reason: "Spanner shadow query timed out: " + truncate(in.Shadow.Err, 200)}
		}
		return Result{Category: Unsupported, Confidence: "high", Reason: "Spanner rejected the query: " + truncate(in.Shadow.Err, 300)}
	case compare.ErrorPrimary:
		return Result{Category: Other, Confidence: "medium", Reason: "only PostgreSQL failed: " + truncate(in.Primary.Err, 300)}
	case compare.ColumnsDiffer:
		return Result{Category: Other, Confidence: "medium", Reason: fmt.Sprintf("column count differs: %d vs %d", len(in.Primary.Columns), len(in.Shadow.Columns))}
	}
	keys := orderKeys(in.Info, in.Primary.Columns)

	if in.Diff.Kind == compare.OrderDiffers {
		i := in.Diff.FirstDivergence
		a, b := in.Primary.Rows[i], in.Shadow.Rows[i]
		return explainPair(in.Info, keys, a, b, "order")
	}

	// RowsDiffer
	if in.Ctx.InTxn && in.Ctx.TxnHasWrites {
		return Result{Category: Isolation, Confidence: "medium", Reason: "read ran inside a Postgres transaction that had already written; the Spanner shadow read cannot see uncommitted writes (transaction-visibility difference)"}
	}
	if in.Ctx.Recheck != nil && in.Ctx.Recheck.Equal() {
		return Result{Category: ReplicationLag, Confidence: "high", Reason: "results converged when re-run after the replication-lag bound"}
	}
	onlyP, onlyS := in.Diff.OnlyPrimary, in.Diff.OnlyShadow

	// Cell-level explanation first: pair rows that agree on most columns. This
	// catches precision/timezone/NULL-semantics differences, and must run before
	// the LIMIT-boundary check (which would otherwise read a numeric difference in
	// the sort key as an ordering difference).
	if len(onlyP) > 0 && len(onlyS) > 0 {
		if r, ok := explainCells(in, onlyP, onlyS); ok {
			return r
		}
	}
	// LIMIT boundary: the engines ordered differently, so a different set of rows
	// survived the LIMIT. Explain via the ORDER BY keys of the swapped rows.
	if in.Info.HasLimit && len(in.Info.OrderBy) > 0 && len(onlyP) > 0 && len(onlyS) > 0 && len(keys) > 0 {
		if r, ok := explainBoundary(in, keys, onlyP, onlyS); ok {
			return r
		}
	}
	if textRangePredicate(in.Tokens, in.Args) && (in.Ctx.Recheck == nil || !in.Ctx.Recheck.Equal()) {
		return Result{Category: Collation, Confidence: "medium", Reason: "the statement compares a column with a text value using <, >, or BETWEEN; the engines evaluate that range under different collations, so different rows qualify"}
	}
	if in.Info.HasLimit && len(in.Info.OrderBy) == 0 {
		return Result{Category: Nondeterminism, Confidence: "medium", Reason: "LIMIT without ORDER BY: each engine may return any qualifying rows"}
	}
	if len(onlyS) == 0 || len(onlyP) == 0 {
		if in.Ctx.Recheck != nil {
			return Result{Category: MissingRow, Confidence: "medium", Reason: fmt.Sprintf("%d row(s) only in Postgres, %d only in Spanner, still different after recheck (persistent drift)", len(onlyP), len(onlyS))}
		}
		return Result{Category: MissingRow, Confidence: "low", Reason: fmt.Sprintf("%d row(s) only in Postgres, %d only in Spanner (no recheck performed; may be replication lag)", len(onlyP), len(onlyS))}
	}
	if in.Ctx.Recheck != nil {
		return Result{Category: Other, Confidence: "low", Reason: fmt.Sprintf("%d/%d rows differ persistently (still different after recheck) and could not be attributed", len(onlyP), len(onlyS))}
	}
	return Result{Category: Other, Confidence: "low", Reason: fmt.Sprintf("%d/%d differing rows could not be attributed", len(onlyP), len(onlyS))}
}

// key maps an ORDER BY item to a result column.
type key struct {
	idx  int
	desc bool
	nf   *bool
}

func orderKeys(info sqlscan.Info, cols []rowset.Column) []key {
	var ks []key
	for _, it := range info.OrderBy {
		idx := it.ResultIndex
		if idx < 0 && len(it.Columns) == 1 {
			for ci, c := range cols {
				if strings.EqualFold(c.Name, it.Columns[0]) {
					idx = ci
					break
				}
			}
		}
		if idx < 0 || idx >= len(cols) {
			// cannot map this key; later keys are only meaningful after it, so stop
			break
		}
		ks = append(ks, key{idx: idx, desc: it.Desc, nf: it.NullsFirst})
	}
	return ks
}

// explainPair explains why the two engines disagree on the relative order of rows
// a (placed first / kept by Postgres) and b (placed first / kept by Spanner).
func explainPair(info sqlscan.Info, keys []key, a, b rowset.Row, where string) Result {
	if len(info.OrderBy) == 0 {
		return Result{Category: Nondeterminism, Confidence: "high", Reason: "query has no ORDER BY; row order is unspecified on both engines"}
	}
	if len(keys) == 0 {
		return Result{Category: Other, Confidence: "low", Reason: "ORDER BY keys could not be mapped to result columns"}
	}
	for _, k := range keys {
		va, vb := a[k.idx], b[k.idx]
		if rowset.Equal(va, vb) {
			continue
		}
		if va.IsNull() || vb.IsNull() {
			return Result{Category: NullOrdering, Confidence: "high", Reason: fmt.Sprintf("engines disagree on where NULL sorts relative to %s (%s)", nonNull(va, vb), where)}
		}
		if va.K == rowset.Text && vb.K == rowset.Text {
			conf := "medium"
			// Spanner orders by code point. If code-point order puts b first (in
			// the query's direction) while Postgres put a first, collation explains it.
			cmp := strings.Compare(va.S, vb.S)
			if k.desc {
				cmp = -cmp
			}
			if cmp > 0 {
				conf = "high"
			}
			return Result{Category: Collation, Confidence: conf, Reason: fmt.Sprintf("text sort key %q vs %q: PostgreSQL's linguistic collation and Spanner's code-point order disagree (%s)", clip(va.S), clip(vb.S), where)}
		}
		return Result{Category: Other, Confidence: "low", Reason: fmt.Sprintf("sort key differs (%s vs %s) but engines disagree on order (%s)", va, vb, where)}
	}
	return Result{Category: Nondeterminism, Confidence: "high", Reason: fmt.Sprintf("rows tie on every ORDER BY key; each engine broke the tie differently (%s)", where)}
}

func nonNull(a, b rowset.Value) string {
	if a.IsNull() {
		return clip(b.String())
	}
	return clip(a.String())
}

// explainCells pairs differing rows and explains cell-level differences.
func explainCells(in Input, onlyP, onlyS []int) (Result, bool) {
	usedS := map[int]bool{}
	votes := map[Category]int{}
	var reasons []string
	paired := 0
	for _, pi := range onlyP {
		pr := in.Primary.Rows[pi]
		best, bestEq := -1, -1
		for _, si := range onlyS {
			if usedS[si] {
				continue
			}
			eq := 0
			for c := range pr {
				if rowset.Equal(pr[c], in.Shadow.Rows[si][c]) {
					eq++
				}
			}
			if eq > bestEq {
				best, bestEq = si, eq
			}
		}
		if best < 0 {
			continue
		}
		sr := in.Shadow.Rows[best]
		// require that the rows plausibly describe the same entity
		if len(pr) > 1 && bestEq == 0 {
			continue
		}
		usedS[best] = true
		paired++
		for c := range pr {
			if rowset.Equal(pr[c], sr[c]) {
				continue
			}
			cat, why := explainCell(in, c, bestEq > 0, pr[c], sr[c])
			votes[cat]++
			if len(reasons) < 3 {
				name := ""
				if c < len(in.Primary.Columns) {
					name = in.Primary.Columns[c].Name
				}
				reasons = append(reasons, fmt.Sprintf("%s: %s", name, why))
			}
		}
	}
	if paired == 0 || len(votes) == 0 {
		return Result{}, false
	}
	// pick the most specific explained category; Other only if nothing else
	var bestCat Category
	bestN := 0
	for _, c := range AllCategories {
		if c == Other {
			continue
		}
		if votes[c] > bestN {
			bestCat, bestN = c, votes[c]
		}
	}
	if bestCat == "" {
		return Result{}, false
	}
	conf := "high"
	if votes[Other] > 0 || paired < len(onlyP) || paired < len(onlyS) {
		conf = "medium"
	}
	return Result{Category: bestCat, Confidence: conf, Reason: strings.Join(reasons, "; ")}, true
}

func explainCell(in Input, col int, anchored bool, p, s rowset.Value) (Category, string) {
	// Function-based attributions need the rows to be anchored by another equal
	// column; otherwise the pairing is arbitrary (e.g. different rows at a LIMIT
	// boundary) and the LIMIT-boundary analysis must decide instead.
	fn := ""
	if anchored {
		fn = selectFunc(in.Info, col)
	}
	if p.IsNull() != s.IsNull() {
		for f := range in.Info.Functions {
			if nullSemanticFuncs[f] {
				return NullSemantics, fmt.Sprintf("%s returns NULL on one engine (%s vs %s): NULL handling of %s() differs", f, clip(p.String()), clip(s.String()), f)
			}
		}
		return Other, fmt.Sprintf("NULL vs %s", clip(nonNull(p, s)))
	}
	if p.IsNumber() && s.IsNumber() {
		fp, _ := p.Float64()
		fs, _ := s.Float64()
		if relClose(fp, fs, 1e-9) {
			return Precision, fmt.Sprintf("%s vs %s (relative difference %.2g)", p, s, relDiff(fp, fs))
		}
		return Other, fmt.Sprintf("%s vs %s", p, s)
	}
	if p.K == rowset.Time && s.K == rowset.Time {
		d := p.T.Sub(s.T)
		if d < 0 {
			d = -d
		}
		if d < time.Millisecond {
			return Precision, fmt.Sprintf("timestamps differ by %s", d)
		}
		if isTZOffset(d) {
			return Timezone, fmt.Sprintf("timestamps differ by %s (a whole time-zone offset)", d)
		}
		return Other, fmt.Sprintf("timestamps differ by %s", d)
	}
	if p.K == rowset.Text && s.K == rowset.Text {
		switch {
		case collationAggs[fn]:
			return Collation, fmt.Sprintf("%s() over text returns %q vs %q: the engines order strings differently", fn, clip(p.S), clip(s.S))
		case caseFuncs[fn]:
			return FuncSemantics, fmt.Sprintf("%s() returns %q vs %q (Unicode case mapping differs)", fn, clip(p.S), clip(s.S))
		}
		if tp, ok1 := parseTS(p.S); ok1 {
			if ts, ok2 := parseTS(s.S); ok2 {
				d := tp.Sub(ts)
				if d < 0 {
					d = -d
				}
				if d == 0 {
					return Timezone, fmt.Sprintf("same instant rendered in different time zones (%q vs %q)", clip(p.S), clip(s.S))
				}
				if isTZOffset(d) {
					return Timezone, fmt.Sprintf("rendered timestamps differ by %s (%q vs %q)", d, clip(p.S), clip(s.S))
				}
			}
		}
		if fp, ok := parseFloat(p.S); ok {
			if fs, ok := parseFloat(s.S); ok && relClose(fp, fs, 1e-9) {
				return Precision, fmt.Sprintf("numeric text %q vs %q", clip(p.S), clip(s.S))
			}
		}
	}
	return Other, fmt.Sprintf("%s vs %s", clip(p.String()), clip(s.String()))
}

func isTZOffset(d time.Duration) bool {
	return d >= 30*time.Minute && d <= 14*time.Hour && d%(15*time.Minute) == 0
}

var tsLayouts = []string{"2006-01-02 15:04:05.999999-07", "2006-01-02 15:04:05.999999-07:00", "2006-01-02 15:04:05-07", "2006-01-02 15:04:05", "2006-01-02T15:04:05.999999Z07:00", "2006-01-02 15:04:05.999999"}

func parseTS(s string) (time.Time, bool) {
	if len(s) < 19 || s[4] != '-' {
		return time.Time{}, false
	}
	for _, l := range tsLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseFloat(s string) (float64, bool) {
	var f float64
	_, err := fmt.Sscan(s, &f)
	return f, err == nil && !strings.ContainsAny(s, " ")
}

func relDiff(a, b float64) float64 {
	den := math.Max(math.Abs(a), math.Abs(b))
	if den == 0 {
		return 0
	}
	return math.Abs(a-b) / den
}

func relClose(a, b, tol float64) bool { return relDiff(a, b) <= tol }

func clip(s string) string {
	if utf8.RuneCountInString(s) > 60 {
		r := []rune(s)
		return string(r[:57]) + "..."
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

var collationAggs = map[string]bool{"min": true, "max": true, "string_agg": true, "array_agg": true}
var caseFuncs = map[string]bool{"upper": true, "lower": true, "initcap": true}

// selectFunc returns the outermost function name of select-list column col,
// or "" when the column is not a plain function call.
func selectFunc(info sqlscan.Info, col int) string {
	if col < 0 || col >= len(info.Select) {
		return ""
	}
	t := info.Select[col].Tokens
	if len(t) >= 2 && t[0].Kind == sqlscan.TIdent && t[1].IsPunct("(") {
		return t[0].Text
	}
	return ""
}

func isTimeout(e string) bool {
	e = strings.ToLower(e)
	return strings.Contains(e, "deadline exceeded") || strings.Contains(e, "timeout") || strings.Contains(e, "canceling statement due to statement timeout")
}

// explainBoundary explains row-set differences of an ORDER BY ... LIMIT query.
// Rows describing the same entity (all text cells equal) but with different
// values are stale values (replication); the remaining unmatched rows were
// selected differently at the LIMIT boundary and are explained through the sort
// keys. The most code-relevant cause wins.
func explainBoundary(in Input, keys []key, onlyP, onlyS []int) (Result, bool) {
	usedS := map[int]bool{}
	var restP []int
	stale := 0
	for _, pi := range onlyP {
		matched := false
		for _, si := range onlyS {
			if !usedS[si] && sameEntity(in.Primary.Rows[pi], in.Shadow.Rows[si]) {
				usedS[si] = true
				matched = true
				stale++
				break
			}
		}
		if !matched {
			restP = append(restP, pi)
		}
	}
	var restS []int
	for _, si := range onlyS {
		if !usedS[si] {
			restS = append(restS, si)
		}
	}
	priority := map[Category]int{Collation: 4, NullOrdering: 3, Nondeterminism: 2, Other: 0}
	var best Result
	bestP := -1
	for i := 0; i < len(restP) && i < len(restS); i++ {
		r := explainPair(in.Info, keys, in.Primary.Rows[restP[i]], in.Shadow.Rows[restS[i]], "limit")
		if p := priority[r.Category]; p > bestP {
			best, bestP = r, p
		}
	}
	switch {
	case bestP > 0:
		if stale > 0 {
			best.Reason += fmt.Sprintf("; also %d row(s) with stale values (replication)", stale)
		}
		return best, true
	case stale > 0 && len(restP) == 0 && len(restS) == 0:
		return Result{Category: ReplicationLag, Confidence: "medium", Reason: fmt.Sprintf("%d row(s) differ only in values updated at the source moments earlier (stale replica values)", stale)}, true
	}
	return Result{}, false
}

// sameEntity: every text cell is equal (numbers/times may differ).
func sameEntity(a, b rowset.Row) bool {
	texts := 0
	for i := range a {
		if a[i].K == rowset.Text || b[i].K == rowset.Text {
			if !rowset.Equal(a[i], b[i]) {
				return false
			}
			texts++
		}
	}
	return texts > 0
}

// textRangePredicate detects "col <op> 'text'" / "col <op> $n" (string argument)
// range comparisons, whose result depends on the collation.
func textRangePredicate(toks []sqlscan.Token, args []any) bool {
	isText := func(t sqlscan.Token) bool {
		if t.Kind == sqlscan.TString {
			return true
		}
		if t.Kind == sqlscan.TParam {
			var n int
			if _, err := fmt.Sscanf(t.Text, "$%d", &n); err == nil && n >= 1 && n <= len(args) {
				_, ok := args[n-1].(string)
				return ok
			}
		}
		return false
	}
	for i := 1; i+1 < len(toks); i++ {
		t := toks[i]
		isRange := t.Kind == sqlscan.TOp && (t.Text == "<" || t.Text == ">" || t.Text == "<=" || t.Text == ">=")
		if isRange && ((toks[i-1].IsIdent() && isText(toks[i+1])) || (isText(toks[i-1]) && toks[i+1].IsIdent())) {
			return true
		}
		if t.Is("between") && toks[i-1].IsIdent() && isText(toks[i+1]) {
			return true
		}
	}
	return false
}
