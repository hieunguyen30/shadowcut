package classify

import (
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/shadowcut/shadowcut/internal/compare"
	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

func info(sql string) sqlscan.Info { return sqlscan.Analyze(sqlscan.Lex(sql)) }

func txt(s string) rowset.Value  { return rowset.Value{K: rowset.Text, S: s} }
func num(i int64) rowset.Value   { return rowset.Value{K: rowset.Int, I: i} }
func flt(f float64) rowset.Value { return rowset.Value{K: rowset.Float, F: f} }
func null() rowset.Value         { return rowset.Value{K: rowset.Null} }

func res(cols []string, rows ...rowset.Row) *rowset.Result {
	r := &rowset.Result{}
	for _, c := range cols {
		r.Columns = append(r.Columns, rowset.Column{Name: c})
	}
	r.Rows = rows
	return r
}

func run(t *testing.T, sql string, p, s *rowset.Result, ctx Context) Result {
	t.Helper()
	in := info(sql)
	mode := compare.Unordered
	if len(in.OrderBy) > 0 {
		mode = compare.Ordered
	}
	d := compare.Compare(p, s, mode)
	if d.Equal() {
		t.Fatalf("expected a mismatch")
	}
	return Classify(Input{Info: in, Primary: p, Shadow: s, Diff: d, Ctx: ctx})
}

// pgLinguistic approximates en_US ordering for ASCII test data: case-insensitive,
// punctuation ignored first. Good enough to construct realistic disagreements.
func pgLinguistic(a, b string) bool {
	k := func(s string) string { return strings.ToLower(strings.TrimLeft(s, "_-. ")) }
	if k(a) != k(b) {
		return k(a) < k(b)
	}
	return a < b
}

func TestCollationOrder(t *testing.T) {
	p := res([]string{"title"}, rowset.Row{txt("apple")}, rowset.Row{txt("Banana")}, rowset.Row{txt("_zeta")})
	s := res([]string{"title"}, rowset.Row{txt("Banana")}, rowset.Row{txt("_zeta")}, rowset.Row{txt("apple")})
	r := run(t, "SELECT title FROM pages ORDER BY title", p, s, Context{})
	if r.Category != Collation || r.Confidence != "high" {
		t.Fatalf("got %+v", r)
	}
}

func TestCollationLimitBoundary(t *testing.T) {
	// Postgres keeps "apple", Spanner keeps "Banana" at the LIMIT boundary
	p := res([]string{"title", "n"}, rowset.Row{txt("apple"), num(1)})
	s := res([]string{"title", "n"}, rowset.Row{txt("Banana"), num(7)})
	r := run(t, "SELECT title, n FROM pages ORDER BY title LIMIT 1", p, s, Context{})
	if r.Category != Collation {
		t.Fatalf("got %+v", r)
	}
}

func TestNullOrdering(t *testing.T) {
	p := res([]string{"u"}, rowset.Row{txt("a")}, rowset.Row{null()})
	s := res([]string{"u"}, rowset.Row{null()}, rowset.Row{txt("a")})
	r := run(t, "SELECT user_name AS u FROM edits ORDER BY u", p, s, Context{})
	if r.Category != NullOrdering {
		t.Fatalf("got %+v", r)
	}
}

func TestTies(t *testing.T) {
	p := res([]string{"title", "n"}, rowset.Row{txt("a"), num(5)}, rowset.Row{txt("b"), num(5)})
	s := res([]string{"title", "n"}, rowset.Row{txt("b"), num(5)}, rowset.Row{txt("a"), num(5)})
	r := run(t, "SELECT title, edit_count AS n FROM pages ORDER BY n DESC", p, s, Context{})
	if r.Category != Nondeterminism {
		t.Fatalf("got %+v", r)
	}
	// ties at a LIMIT boundary select different rows
	p = res([]string{"title", "n"}, rowset.Row{txt("a"), num(5)})
	s = res([]string{"title", "n"}, rowset.Row{txt("b"), num(5)})
	r = run(t, "SELECT title, edit_count AS n FROM pages ORDER BY n DESC LIMIT 1", p, s, Context{})
	if r.Category != Nondeterminism {
		t.Fatalf("limit ties: got %+v", r)
	}
}

func TestConcatNull(t *testing.T) {
	p := res([]string{"label"}, rowset.Row{txt("enwiki")})
	s := res([]string{"label"}, rowset.Row{null()})
	r := run(t, "SELECT concat(user_name, wiki) AS label FROM edits", p, s, Context{})
	if r.Category != NullSemantics {
		t.Fatalf("got %+v", r)
	}
}

func TestPrecision(t *testing.T) {
	p := res([]string{"wiki", "avg"}, rowset.Row{txt("en"), flt(0.15000000000000002)})
	s := res([]string{"wiki", "avg"}, rowset.Row{txt("en"), flt(0.15)})
	r := run(t, "SELECT wiki, avg(growth_ratio) FROM edits GROUP BY wiki", p, s, Context{})
	if r.Category != Precision {
		t.Fatalf("got %+v", r)
	}
}

func TestTimezone(t *testing.T) {
	base := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	p := res([]string{"day", "n"}, rowset.Row{rowset.Value{K: rowset.Time, T: base}, num(3)})
	s := res([]string{"day", "n"}, rowset.Row{rowset.Value{K: rowset.Time, T: base.Add(8 * time.Hour)}, num(3)})
	r := run(t, "SELECT date_trunc('day', edit_ts) AS day, count(*) n FROM edits GROUP BY 1", p, s, Context{})
	if r.Category != Timezone {
		t.Fatalf("got %+v", r)
	}
	pt := res([]string{"ts"}, rowset.Row{txt("2024-01-01 00:00:00.123457+00")})
	st := res([]string{"ts"}, rowset.Row{txt("2023-12-31 16:00:00.123457-08")})
	if r := run(t, "SELECT edit_ts::text AS ts FROM edits", pt, st, Context{}); r.Category != Timezone {
		t.Fatalf("text tz: got %+v", r)
	}
}

func TestUnsupported(t *testing.T) {
	p := res([]string{"x"}, rowset.Row{num(1)})
	s := &rowset.Result{Err: "Statements with DISTINCT ON clauses are not supported"}
	r := run(t, "SELECT DISTINCT ON (wiki) wiki FROM edits", p, s, Context{})
	if r.Category != Unsupported {
		t.Fatalf("got %+v", r)
	}
}

func TestLagAndMissing(t *testing.T) {
	p := res([]string{"id"}, rowset.Row{num(1)}, rowset.Row{num(2)})
	s := res([]string{"id"}, rowset.Row{num(1)})
	same := compare.Diff{Kind: compare.Same}
	if r := run(t, "SELECT id FROM edits", p, s, Context{Recheck: &same}); r.Category != ReplicationLag {
		t.Fatalf("lag: got %+v", r)
	}
	still := compare.Diff{Kind: compare.RowsDiffer, OnlyPrimary: []int{1}}
	if r := run(t, "SELECT id FROM edits", p, s, Context{Recheck: &still}); r.Category != MissingRow {
		t.Fatalf("missing: got %+v", r)
	}
	if r := run(t, "SELECT id FROM edits", p, s, Context{InTxn: true, TxnHasWrites: true}); r.Category != Isolation {
		t.Fatalf("isolation: got %+v", r)
	}
}

// Property: any permutation of an unordered result never produces a mismatch in
// unordered mode, and identical results never mismatch in ordered mode.
func TestPropUnorderedPermutationEqual(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 30).Draw(t, "n")
		var rows []rowset.Row
		for i := 0; i < n; i++ {
			rows = append(rows, rowset.Row{
				txt(rapid.StringMatching(`[a-zA-Z_é]{0,4}`).Draw(t, "s")),
				num(rapid.Int64Range(-3, 3).Draw(t, "i")),
				rapid.SampledFrom([]rowset.Value{null(), flt(0.1), flt(-0), num(0)}).Draw(t, "v"),
			})
		}
		perm := rapid.Permutation(rows).Draw(t, "perm")
		p := res([]string{"a", "b", "c"}, rows...)
		s := res([]string{"a", "b", "c"}, perm...)
		if d := compare.Compare(p, s, compare.Unordered); !d.Equal() {
			t.Fatalf("permutation mismatched: %+v", d)
		}
		if d := compare.Compare(p, p, compare.Ordered); !d.Equal() {
			t.Fatalf("identity mismatched: %+v", d)
		}
	})
}

// Property: if Postgres orders by a linguistic collation and Spanner by code
// point, every resulting mismatch is classified as collation (distinct keys) or
// nondeterministic_order (never "other").
func TestPropCollationClassified(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 12).Draw(t, "n")
		seen := map[string]bool{}
		var titles []string
		for len(titles) < n {
			s := rapid.StringMatching(`[_]?[a-cA-C]{1,3}`).Draw(t, "title")
			if !seen[s] {
				seen[s] = true
				titles = append(titles, s)
			}
		}
		pg := append([]string(nil), titles...)
		sort.Slice(pg, func(i, j int) bool { return pgLinguistic(pg[i], pg[j]) })
		sp := append([]string(nil), titles...)
		sort.Strings(sp)
		limit := rapid.IntRange(1, n).Draw(t, "limit")
		mk := func(xs []string) *rowset.Result {
			r := res([]string{"title"})
			for _, x := range xs[:limit] {
				r.Rows = append(r.Rows, rowset.Row{txt(x)})
			}
			return r
		}
		p, s := mk(pg), mk(sp)
		sql := "SELECT title FROM pages ORDER BY title LIMIT 5"
		in := info(sql)
		d := compare.Compare(p, s, compare.Ordered)
		if d.Equal() {
			return
		}
		r := Classify(Input{Info: in, Primary: p, Shadow: s, Diff: d})
		if r.Category != Collation {
			t.Fatalf("pg=%v sp=%v limit=%d classified %+v", pg, sp, limit, r)
		}
	})
}

// Property: float results perturbed by a few ULPs are classified as precision.
func TestPropFloatPrecision(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		f := rapid.Float64Range(-1e12, 1e12).Draw(t, "f")
		if f == 0 {
			f = 1
		}
		ulps := rapid.IntRange(1, 64).Draw(t, "ulps")
		g := f
		for i := 0; i < ulps; i++ {
			g = nextafter(g)
		}
		p := res([]string{"k", "v"}, rowset.Row{txt("x"), flt(f)})
		s := res([]string{"k", "v"}, rowset.Row{txt("x"), flt(g)})
		d := compare.Compare(p, s, compare.Unordered)
		if d.Equal() {
			return
		}
		r := Classify(Input{Info: info("SELECT k, sum(v) FROM t GROUP BY k"), Primary: p, Shadow: s, Diff: d})
		if r.Category != Precision {
			t.Fatalf("%v vs %v -> %+v", f, g, r)
		}
	})
}

func nextafter(f float64) float64 {
	return f + f*2.2e-16*float64(1+rand.Intn(2))
}

func TestFunctionSemanticsNeedsAnchor(t *testing.T) {
	// anchored by id: upper() differs -> function_semantics
	p := res([]string{"id", "t"}, rowset.Row{num(1), txt("STRAßE")})
	s := res([]string{"id", "t"}, rowset.Row{num(1), txt("STRASSE")})
	if r := run(t, "SELECT id, upper(page_title) AS t FROM edits ORDER BY id", p, s, Context{}); r.Category != FuncSemantics {
		t.Fatalf("anchored: got %+v", r)
	}
	// unanchored single column at a LIMIT boundary: ordering decides -> collation
	p = res([]string{"t"}, rowset.Row{txt("ági")})
	s = res([]string{"t"}, rowset.Row{txt("\"dragons\"")})
	if r := run(t, "SELECT lower(title) AS t FROM pages ORDER BY t LIMIT 1", p, s, Context{}); r.Category != Collation {
		t.Fatalf("unanchored: got %+v", r)
	}
}

func TestMinMaxCollation(t *testing.T) {
	p := res([]string{"wiki", "last"}, rowset.Row{txt("an"), txt("Torre d'os")})
	s := res([]string{"wiki", "last"}, rowset.Row{txt("an"), txt("Torre de Sant")})
	if r := run(t, "SELECT wiki, max(title) AS last FROM pages GROUP BY wiki ORDER BY wiki", p, s, Context{}); r.Category != Collation {
		t.Fatalf("got %+v", r)
	}
}

func TestBoundaryMixedStaleAndTies(t *testing.T) {
	// one row has a stale count (lag), and a tie at the LIMIT boundary resolved differently
	p := res([]string{"wiki", "title", "n"},
		rowset.Row{txt("en"), txt("Reports"), num(97)}, rowset.Row{txt("en"), txt("Garbage"), num(12)})
	s := res([]string{"wiki", "title", "n"},
		rowset.Row{txt("en"), txt("Reports"), num(96)}, rowset.Row{txt("en"), txt("Anikó"), num(12)})
	r := run(t, "SELECT wiki, title, edit_count AS n FROM pages WHERE wiki = $1 ORDER BY n DESC LIMIT 2", p, s, Context{})
	if r.Category != Nondeterminism {
		t.Fatalf("got %+v", r)
	}
	// only stale values -> replication lag
	p = res([]string{"wiki", "title", "n"}, rowset.Row{txt("en"), txt("Reports"), num(97)})
	s = res([]string{"wiki", "title", "n"}, rowset.Row{txt("en"), txt("Reports"), num(96)})
	if r := run(t, "SELECT wiki, title, edit_count AS n FROM pages ORDER BY n DESC LIMIT 1", p, s, Context{}); r.Category != ReplicationLag {
		t.Fatalf("stale only: got %+v", r)
	}
}

func TestTimeoutIsNotUnsupported(t *testing.T) {
	p := res([]string{"x"}, rowset.Row{num(1)})
	s := &rowset.Result{Err: "timeout: context deadline exceeded"}
	if r := run(t, "SELECT x FROM t", p, s, Context{}); r.Category != Timeout {
		t.Fatalf("got %+v", r)
	}
}

func TestTextRangePredicateIsCollation(t *testing.T) {
	sql := "SELECT title FROM pages WHERE wiki = $1 AND title > $2 ORDER BY title LIMIT 50"
	in := info(sql)
	p := res([]string{"title"}, rowset.Row{txt("Property:P1")}, rowset.Row{txt("Property:P2")})
	s := res([]string{"title"})
	d := compare.Compare(p, s, compare.Ordered)
	r := Classify(Input{Info: in, Tokens: sqlscan.Lex(sql), Args: []any{"wikidatawiki", "Ö"}, Primary: p, Shadow: s, Diff: d})
	if r.Category != Collation {
		t.Fatalf("got %+v", r)
	}
	// numeric range: not collation
	sql = "SELECT id FROM edits WHERE id > $1"
	r = Classify(Input{Info: info(sql), Tokens: sqlscan.Lex(sql), Args: []any{int64(5)}, Primary: p, Shadow: s, Diff: d})
	if r.Category == Collation {
		t.Fatalf("numeric range classified as collation")
	}
}
