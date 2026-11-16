package hazard

import (
	"fmt"
	"strings"

	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

// scope resolves identifiers to columns for one statement.
type scope struct {
	s      *Scanner
	tables []string
}

func (sc scope) col(name string) *sqlscan.Column {
	_, c := sc.s.Schema.Resolve(sc.tables, name)
	return c
}

func (sc scope) isText(name string) bool {
	c := sc.col(name)
	return c != nil && sqlscan.IsTextType(c.Type)
}

func (sc scope) isTimestamp(name string) bool {
	c := sc.col(name)
	return c != nil && (c.Type == "timestamptz" || c.Type == "timestamp")
}

// non-text-returning functions: ORDER BY length(title) is not a collation hazard
var numericFuncs = map[string]bool{"count": true, "sum": true, "avg": true, "length": true, "char_length": true, "octet_length": true,
	"extract": true, "date_part": true, "strpos": true, "position": true, "ascii": true, "abs": true, "row_number": true, "rank": true, "array_length": true}

var textCaseFuncs = map[string]bool{"lower": true, "upper": true, "coalesce": true, "trim": true, "btrim": true, "concat": true, "substr": true, "substring": true, "left": true, "replace": true}

// itemIsText decides whether an ORDER BY / comparison expression is textual.
func (sc scope) exprIsText(toks []sqlscan.Token, info sqlscan.Info) bool {
	if len(toks) == 0 {
		return false
	}
	if toks[0].Kind == sqlscan.TIdent && len(toks) > 1 && toks[1].IsPunct("(") {
		fn := toks[0].Text
		if numericFuncs[fn] {
			return false
		}
		if fn == "min" || fn == "max" || textCaseFuncs[fn] {
			// textual if a text column is inside
			for _, id := range identsIn(toks) {
				if sc.isText(id) {
					return true
				}
			}
			return false
		}
	}
	// cast to non-text
	for i, t := range toks {
		if t.Kind == sqlscan.TOp && t.Text == "::" && i+1 < len(toks) {
			ty := sqlscan.NormalizeType(toks[i+1].Text)
			return sqlscan.IsTextType(ty)
		}
	}
	for _, id := range identsIn(toks) {
		if sc.isText(id) {
			return true
		}
		// alias of a select item
		for _, si := range info.Select {
			if si.Alias == id && len(si.Tokens) > 0 && !(len(si.Tokens) == 1 && si.Tokens[0].Text == id) {
				if sc.exprIsText(stripAlias(si), info) {
					return true
				}
			}
		}
	}
	return false
}

func stripAlias(si sqlscan.SelectItem) []sqlscan.Token {
	t := si.Tokens
	n := len(t)
	if n >= 2 && t[n-2].Is("as") {
		return t[:n-2]
	}
	if n >= 2 && t[n-1].IsIdent() && t[n-1].Text == si.Alias && !t[n-2].IsPunct(".") {
		return t[:n-1]
	}
	return t
}

func identsIn(toks []sqlscan.Token) []string {
	var out []string
	for i, t := range toks {
		if !t.IsIdent() {
			continue
		}
		if t.Kind == sqlscan.TIdent && i+1 < len(toks) && toks[i+1].IsPunct("(") {
			continue
		}
		if i > 0 && toks[i-1].Kind == sqlscan.TOp && toks[i-1].Text == "::" {
			continue
		}
		if i+1 < len(toks) && toks[i+1].IsPunct(".") {
			continue
		}
		out = append(out, t.Text)
	}
	return out
}

// orderByClauses finds every ORDER BY (top-level, subqueries, aggregates, windows).
func orderByClauses(toks []sqlscan.Token) [][][]sqlscan.Token {
	var out [][][]sqlscan.Token
	for i := 0; i+1 < len(toks); i++ {
		if !(toks[i].Is("order") && toks[i+1].Is("by")) {
			continue
		}
		d := toks[i].Depth
		j := i + 2
		var items [][]sqlscan.Token
		var cur []sqlscan.Token
		for ; j < len(toks); j++ {
			t := toks[j]
			if t.Depth < d || (t.IsPunct(")") && t.Depth == d-1) {
				break
			}
			if t.Depth == d && (t.Is("limit") || t.Is("offset") || t.Is("fetch") || t.Is("for") || t.Is("union") || t.Is("except") || t.Is("intersect") || t.Is("rows") || t.Is("range") || t.Is("groups") || t.IsPunct(";")) {
				break
			}
			if t.Depth == d && t.IsPunct(",") {
				items = append(items, cur)
				cur = nil
				continue
			}
			cur = append(cur, t)
		}
		items = append(items, cur)
		out = append(out, items)
	}
	return out
}

func trimOrderModifiers(item []sqlscan.Token) (expr []sqlscan.Token, explicitNulls bool) {
	for {
		n := len(item)
		if n >= 2 && item[n-2].Is("nulls") && (item[n-1].Is("first") || item[n-1].Is("last")) {
			explicitNulls = true
			item = item[:n-2]
			continue
		}
		if n >= 2 && (item[n-1].Is("asc") || item[n-1].Is("desc")) {
			item = item[:n-1]
			continue
		}
		return item, explicitNulls
	}
}

var setReturning = map[string]bool{"generate_series": true, "unnest": true, "jsonb_array_elements": true, "jsonb_array_elements_text": true, "jsonb_each": true, "jsonb_object_keys": true, "regexp_split_to_table": true}

var tzFuncs = map[string]bool{"extract": true, "date_part": true, "to_char": true}

func (s *Scanner) queryRules(st sqlscan.Statement, info sqlscan.Info) []Finding {
	toks := st.Tokens
	var out []Finding
	tables := append([]string(nil), info.Tables...)
	sc := scope{s: s, tables: tables}

	// ---- ORDER BY: collation, NULL ordering ---------------------------------
	for _, items := range orderByClauses(toks) {
		for ii, raw := range items {
			expr, explicitNulls := trimOrderModifiers(raw)
			if len(expr) == 0 {
				continue
			}
			// ordinal: ORDER BY 2
			if len(expr) == 1 && expr[0].Kind == sqlscan.TNumber {
				var n int
				fmt.Sscan(expr[0].Text, &n)
				if n >= 1 && n <= len(info.Select) {
					expr = stripAlias(info.Select[n-1])
				}
			}
			if ii == 0 && len(expr) == 1 && expr[0].IsIdent() {
				if c := sc.col(expr[0].Text); c != nil && c.Identity {
					out = append(out, finding("Q-ORDER-BY-SEQ-KEY", raw[0], fmt.Sprintf("ORDER BY %s: %s is a %s key; recency order breaks once keys are bit-reversed or UUIDs", sqlscan.TokensText(raw), c.Name, c.TypeRaw)))
				}
			}
			if sc.exprIsText(expr, info) {
				out = append(out, finding("Q-COLLATION-ORDER", raw[0], fmt.Sprintf("ORDER BY %s sorts text", sqlscan.TokensText(raw))))
			}
			if !explicitNulls {
				for _, id := range identsIn(expr) {
					if c := sc.col(id); c != nil && !c.NotNull {
						out = append(out, finding("Q-NULL-ORDER", raw[0], fmt.Sprintf("ORDER BY %s: %s is nullable and NULL placement is implicit", sqlscan.TokensText(raw), id)))
						break
					}
				}
			}
		}
	}

	// ---- ties / missing ORDER BY --------------------------------------------
	if info.Kind == sqlscan.KSelect {
		if info.HasLimit || info.HasOffset {
			if len(info.OrderBy) == 0 {
				out = append(out, finding("Q-LIMIT-NO-ORDER", toks[0], "LIMIT/OFFSET without ORDER BY returns an arbitrary subset"))
			}
		}
		if len(info.OrderBy) > 0 && !s.orderIsUnique(info, sc, toks) {
			sev := "ORDER BY keys can tie"
			if info.HasLimit {
				sev += "; with LIMIT the engines can return different rows"
			}
			f := finding("Q-ORDER-TIES", info.OrderBy[0].Tokens[0], sev)
			if !info.HasLimit && !info.HasOffset {
				f.Severity = Low
			}
			out = append(out, f)
		}
	}

	// ---- comparisons on text ------------------------------------------------
	for i, t := range toks {
		if t.Kind == sqlscan.TOp && (t.Text == "<" || t.Text == ">" || t.Text == "<=" || t.Text == ">=") || t.Is("between") {
			var left, right string
			if i > 0 && toks[i-1].IsIdent() {
				left = toks[i-1].Text
			}
			if i+1 < len(toks) && toks[i+1].IsIdent() {
				right = toks[i+1].Text
			}
			if (left != "" && sc.isText(left)) || (right != "" && sc.isText(right)) {
				out = append(out, finding("Q-COLLATION-COMPARE", t, fmt.Sprintf("range comparison %q on a text column follows the collation", t.Raw)))
			}
		}
	}
	for i, t := range toks {
		if t.Kind != sqlscan.TIdent || i+1 >= len(toks) || !toks[i+1].IsPunct("(") {
			continue
		}
		args := callArgs(toks, i+1)
		switch t.Text {
		case "min", "max", "greatest", "least":
			for _, id := range identsIn(flatten(args)) {
				if sc.isText(id) {
					out = append(out, finding("Q-COLLATION-COMPARE", t, fmt.Sprintf("%s() over text column %s follows the collation", t.Text, id)))
					break
				}
			}
		case "concat", "concat_ws", "array_cat", "array_to_string":
			if s.Evidence.Funcs[t.Text] != nil && s.Evidence.Funcs[t.Text].Unsupported() {
				break // reported as unsupported below
			}
			nullable := false
			for _, a := range args {
				for _, id := range identsIn(a) {
					if c := sc.col(id); c == nil || !c.NotNull {
						nullable = true
					}
				}
			}
			if nullable {
				out = append(out, finding("Q-CONCAT-NULL", t, fmt.Sprintf("%s() with a nullable argument returns NULL on Spanner but skips NULLs on Postgres", t.Text)))
			}
		case "date_trunc":
			if len(args) == 2 {
				out = append(out, finding("Q-TZ-DEFAULT", t, "2-argument date_trunc() uses America/Los_Angeles on Spanner and the session time zone on Postgres"))
			}
		case "sum", "avg":
			for _, id := range identsIn(flatten(args)) {
				if c := sc.col(id); c != nil && sqlscan.IsFloatType(c.Type) {
					out = append(out, finding("Q-FLOAT-AGG", t, fmt.Sprintf("%s() over floating-point column %s", t.Text, id)))
					break
				}
			}
		case "upper", "lower", "initcap", "quote_ident", "round", "power":
			if ev := s.Evidence.Funcs[t.Text]; ev != nil && ev.Differs() {
				out = append(out, finding("Q-FUNC-SEMANTICS", t, fmt.Sprintf("%s() returns different results on Spanner (probe %s)", t.Text, strings.Join(ev.ProbeIDs, ","))))
			}
		}
		if tzFuncs[t.Text] {
			for _, id := range identsIn(flatten(args)) {
				if sc.isTimestamp(id) {
					out = append(out, finding("Q-TZ-DEFAULT", t, fmt.Sprintf("%s() on timestamptz %s is evaluated in America/Los_Angeles on Spanner", t.Text, id)))
					break
				}
			}
		}
		// unsupported functions (evidence-driven)
		if ev := s.Evidence.Funcs[t.Text]; ev != nil && (ev.Unsupported() || ev.Partial()) && !(t.Text == "round" && !ev.Unsupported() && roundOnFloat(args, sc)) {
			msg := fmt.Sprintf("%s() is not supported by Spanner (probe %s)", t.Text, strings.Join(ev.ProbeIDs, ","))
			if ev.Partial() {
				msg = fmt.Sprintf("%s() is not supported for some argument types on Spanner (probe %s)", t.Text, strings.Join(ev.ProbeIDs, ","))
			}
			out = append(out, finding("Q-UNSUP-FUNC", t, msg))
		}
	}
	// casts of timestamptz to text/date, and timestamptz +/- interval
	for i, t := range toks {
		if t.Kind == sqlscan.TOp && t.Text == "::" && i > 0 && i+1 < len(toks) {
			ty := sqlscan.NormalizeType(toks[i+1].Text)
			if (ty == "text" || ty == "date" || ty == "varchar") && toks[i-1].IsIdent() && sc.isTimestamp(toks[i-1].Text) {
				out = append(out, finding("Q-TZ-DEFAULT", t, fmt.Sprintf("%s::%s renders/truncates in America/Los_Angeles on Spanner", toks[i-1].Text, ty)))
			}
			if ty == "timestamp" && !(i+3 < len(toks) && toks[i+2].Is("with")) {
				out = append(out, finding("Q-UNSUP-SYNTAX", t, "cast to timestamp without time zone is not supported"))
			}
		}
		if t.Kind == sqlscan.TOp && (t.Text == "+" || t.Text == "-") && i > 0 && i+1 < len(toks) && toks[i-1].IsIdent() && sc.isTimestamp(toks[i-1].Text) && toks[i+1].Is("interval") {
			out = append(out, finding("Q-TZ-DEFAULT", t, "timestamptz +/- interval is computed in America/Los_Angeles on Spanner (DST-dependent)"))
		}
	}

	// ---- unsupported syntax -----------------------------------------------
	out = append(out, s.syntaxRules(toks, info)...)
	return out
}

func roundOnFloat(args [][]sqlscan.Token, sc scope) bool {
	if len(args) != 1 {
		return false
	}
	for _, id := range identsIn(args[0]) {
		if c := sc.col(id); c != nil && sqlscan.IsFloatType(c.Type) {
			return true
		}
	}
	return false
}

// callArgs returns the comma-separated arguments of the call whose '(' is at open.
func callArgs(toks []sqlscan.Token, open int) [][]sqlscan.Token {
	d := toks[open].Depth + 1
	var args [][]sqlscan.Token
	var cur []sqlscan.Token
	for j := open + 1; j < len(toks); j++ {
		t := toks[j]
		if t.IsPunct(")") && t.Depth == d-1 {
			break
		}
		if t.IsPunct(",") && t.Depth == d {
			args = append(args, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 || len(args) > 0 {
		args = append(args, cur)
	}
	return args
}

func flatten(a [][]sqlscan.Token) []sqlscan.Token {
	var out []sqlscan.Token
	for _, x := range a {
		out = append(out, x...)
	}
	return out
}

// orderIsUnique reports whether the top-level ORDER BY covers a unique key
// (PK/unique of a single referenced table, or every GROUP BY column).
func (s *Scanner) orderIsUnique(info sqlscan.Info, sc scope, toks []sqlscan.Token) bool {
	var cols []string
	for _, it := range info.OrderBy {
		if len(it.Tokens) == 1 && it.Tokens[0].IsIdent() || it.Ordinal || it.ResultIndex >= 0 {
			cols = append(cols, it.Columns...)
			if it.ResultIndex >= 0 && info.Select[it.ResultIndex].Alias != "" {
				cols = append(cols, info.Select[it.ResultIndex].Alias)
			}
			if len(it.Tokens) == 1 && it.Tokens[0].IsIdent() {
				cols = append(cols, it.Tokens[0].Text)
			}
		} else if len(it.Columns) == 1 && len(it.Tokens) <= 3 {
			cols = append(cols, it.Columns...) // qualified e.alias.col
		}
	}
	cols = append(cols, equalityBound(toks)...)
	if info.HasGroupBy {
		// unique if ORDER BY covers all GROUP BY expressions' columns
		gb := groupByColumns(toks, info)
		if len(gb) == 0 {
			return false
		}
		has := map[string]bool{}
		for _, c := range cols {
			has[c] = true
		}
		for _, g := range gb {
			if !has[g] {
				return false
			}
		}
		return true
	}
	if info.Aggregates && !info.HasGroupBy {
		return true // single-row aggregate
	}
	if len(info.Tables) != 1 {
		// joins: unique only if it covers a key of every table; be conservative
		for _, tn := range info.Tables {
			t := s.Schema.Tables[tn]
			if t == nil || !t.IsUniqueSet(cols) {
				return false
			}
		}
		return len(info.Tables) > 0
	}
	t := s.Schema.Tables[info.Tables[0]]
	if t == nil {
		return true // unknown table: don't guess
	}
	if hasDistinct(info) {
		// SELECT DISTINCT a, b ORDER BY a, b: rows are unique on the select list
		var sel []string
		for _, si := range info.Select {
			sel = append(sel, si.Cols...)
		}
		if subset(sel, cols) {
			return true
		}
	}
	return t.IsUniqueSet(cols)
}

func hasDistinct(info sqlscan.Info) bool {
	return strings.HasPrefix(info.Fingerprint, "select distinct ")
}

func subset(a, b []string) bool {
	has := map[string]bool{}
	for _, x := range b {
		has[x] = true
	}
	for _, x := range a {
		if !has[x] {
			return false
		}
	}
	return len(a) > 0
}

func groupByColumns(toks []sqlscan.Token, info sqlscan.Info) []string {
	start := -1
	for i := 0; i+1 < len(toks); i++ {
		if toks[i].Depth == 0 && toks[i].Is("group") && toks[i+1].Is("by") {
			start = i + 2
			break
		}
	}
	if start < 0 {
		return nil
	}
	var out []string
	var cur []sqlscan.Token
	flush := func() {
		if len(cur) == 1 && cur[0].Kind == sqlscan.TNumber {
			var n int
			fmt.Sscan(cur[0].Text, &n)
			if n >= 1 && n <= len(info.Select) {
				if a := info.Select[n-1].Alias; a != "" {
					out = append(out, a)
				} else {
					out = append(out, info.Select[n-1].Cols...)
				}
			}
		} else {
			out = append(out, identsIn(cur)...)
		}
		cur = nil
	}
	for _, t := range toks[start:] {
		if t.Depth == 0 && (t.Is("having") || t.Is("order") || t.Is("limit") || t.Is("offset") || t.Is("window") || t.Is("union") || t.Is("except") || t.Is("intersect") || t.Is("for")) {
			break
		}
		if t.Depth == 0 && t.IsPunct(",") {
			flush()
			continue
		}
		cur = append(cur, t)
	}
	flush()
	return out
}

func (s *Scanner) syntaxRules(toks []sqlscan.Token, info sqlscan.Info) []Finding {
	var out []Finding
	add := func(t sqlscan.Token, what string) {
		out = append(out, finding("Q-UNSUP-SYNTAX", t, what+" is not supported"))
	}
	for i, t := range toks {
		next := func(k int) sqlscan.Token {
			if i+k < len(toks) {
				return toks[i+k]
			}
			return sqlscan.Token{}
		}
		switch {
		case t.Is("distinct") && next(1).Is("on"):
			add(t, "DISTINCT ON")
		case t.Is("collate"):
			add(t, "COLLATE")
		case t.Is("recursive") && i > 0 && toks[i-1].Is("with"):
			add(t, "WITH RECURSIVE")
		case t.Is("over") && (next(1).IsPunct("(") || next(1).IsIdent()):
			add(t, "window function (OVER)")
		case t.Is("lateral"):
			add(t, "LATERAL")
		case t.Is("similar") && next(1).Is("to"):
			add(t, "SIMILAR TO")
		case (t.Is("rollup") || t.Is("cube")) && next(1).IsPunct("("), t.Is("grouping") && next(1).Is("sets"):
			add(t, "GROUPING SETS / ROLLUP / CUBE")
		case t.Is("skip") && next(1).Is("locked"), t.Is("nowait"):
			add(t, "lock wait policy ("+strings.ToUpper(t.Text)+")")
		case (t.Is("any") || t.Is("some") || t.Is("all")) && next(1).IsPunct("(") && next(2).Kind == sqlscan.TString:
			add(t, "= ANY('{...}') with an array literal")
		case t.IsPunct("(") && isRowCompare(toks, i):
			add(t, "row-value comparison (a, b) < (x, y)")
		case t.Is("listen") && i == 0, t.Is("notify") && i == 0, t.Is("unlisten") && i == 0:
			out = append(out, finding("Q-UNSUP-TXN", t, "LISTEN/NOTIFY is not supported"))
		case t.Is("savepoint") || (t.Is("rollback") && next(1).Is("to")):
			out = append(out, finding("Q-UNSUP-TXN", t, "SAVEPOINT is not supported"))
		case t.Is("ilike") && t.Depth >= 0:
			out = append(out, finding("Q-ILIKE", t, "ILIKE operator: supported by the emulator, documented as pg.ilike(); confirm on real Spanner"))
		}
	}
	// set-returning functions in the select list
	for _, si := range info.Select {
		if len(si.Tokens) >= 2 && si.Tokens[0].Kind == sqlscan.TIdent && si.Tokens[1].IsPunct("(") && setReturning[si.Tokens[0].Text] {
			add(si.Tokens[0], si.Tokens[0].Text+"() in the select list (set-returning functions are only allowed in FROM)")
		}
	}
	// WITH inside DML
	if (info.Kind == sqlscan.KInsert || info.Kind == sqlscan.KUpdate || info.Kind == sqlscan.KDelete) && len(toks) > 0 && toks[0].Is("with") {
		add(toks[0], "WITH in a DML statement")
	}
	return out
}

// isRowCompare detects "(a, b) <op> (" at position i (the opening paren).
func isRowCompare(toks []sqlscan.Token, i int) bool {
	d := toks[i].Depth
	j := i + 1
	commas := 0
	for ; j < len(toks); j++ {
		if toks[j].IsPunct(")") && toks[j].Depth == d {
			break
		}
		if toks[j].Depth == d+1 && toks[j].IsPunct(",") {
			commas++
		}
		if toks[j].Is("select") {
			return false
		}
	}
	if commas == 0 || j+2 >= len(toks) {
		return false
	}
	op := toks[j+1]
	if op.Kind != sqlscan.TOp || !(op.Text == "<" || op.Text == ">" || op.Text == "<=" || op.Text == ">=" || op.Text == "=" || op.Text == "<>") {
		return false
	}
	// not a function call's argument list
	if i > 0 && toks[i-1].Kind == sqlscan.TIdent && !isKw(toks[i-1].Text) {
		return false
	}
	return toks[j+2].IsPunct("(") || toks[j+2].Is("row")
}

func isKw(s string) bool {
	switch s {
	case "where", "and", "or", "not", "on", "when", "having":
		return true
	}
	return false
}

// ---- transactions -----------------------------------------------------------

// txnTracker groups statements into transactions: explicit BEGIN..COMMIT blocks
// or statements sharing a "-- txn: <id>" annotation.
type txnTracker struct {
	inBlock  bool
	tag      string
	reads    map[string]sqlscan.Token // table -> SELECT without FOR UPDATE
	reported map[string]bool
}

func newTxnTracker() *txnTracker {
	return &txnTracker{reads: map[string]sqlscan.Token{}, reported: map[string]bool{}}
}

func (t *txnTracker) reset() {
	t.reads = map[string]sqlscan.Token{}
}

func (t *txnTracker) observe(s *Scanner, st sqlscan.Statement, info sqlscan.Info) []Finding {
	var out []Finding
	toks := st.Tokens
	if info.Kind == sqlscan.KTxn && len(toks) > 0 {
		switch {
		case toks[0].Is("begin") || toks[0].Is("start"):
			t.inBlock = true
			t.reset()
		case toks[0].Is("commit") || toks[0].Is("end") || toks[0].Is("rollback") && !(len(toks) > 1 && toks[1].Is("to")):
			t.inBlock = false
			t.reset()
		}
		for i, x := range toks {
			if x.Is("isolation") && i+2 < len(toks) && toks[i+1].Is("level") && toks[i+2].Is("read") && i+3 < len(toks) && (toks[i+3].Is("committed") || toks[i+3].Is("uncommitted")) {
				out = append(out, finding("Q-ISOLATION-LEVEL", x, "READ "+strings.ToUpper(toks[i+3].Text)+" is not available on Spanner"))
			}
		}
		return out
	}
	tag := st.Tags["txn"]
	if tag != t.tag && !t.inBlock {
		t.reset()
	}
	t.tag = tag
	inTxn := t.inBlock || tag != ""
	if !inTxn {
		if info.ForUpdate {
			out = append(out, finding("Q-UNSUP-SYNTAX", toks[0], "FOR UPDATE outside a read-write transaction is not supported"))
		}
		return out
	}
	switch info.Kind {
	case sqlscan.KSelect:
		if !info.ForUpdate {
			for _, tb := range info.Tables {
				t.reads[tb] = toks[0]
			}
		}
	case sqlscan.KUpdate, sqlscan.KInsert, sqlscan.KDelete:
		for _, tb := range info.Tables {
			if at, ok := t.reads[tb]; ok && !t.reported[tag+tb] {
				t.reported[tag+tb] = true
				f := finding("Q-ISOLATION-RMW", at, fmt.Sprintf("transaction reads %s without FOR UPDATE and then writes it", tb))
				out = append(out, f)
			}
		}
	}
	return out
}

// equalityBound returns columns pinned to a single value by the top-level WHERE
// (col = $1 / literal), which therefore cannot produce ties. WHERE clauses with a
// top-level OR are ignored (the binding is not guaranteed).
func equalityBound(toks []sqlscan.Token) []string {
	start := -1
	for i, t := range toks {
		if t.Depth == 0 && t.Is("where") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	end := len(toks)
	for i := start; i < len(toks); i++ {
		t := toks[i]
		if t.Depth == 0 && (t.Is("group") || t.Is("order") || t.Is("limit") || t.Is("offset") || t.Is("having") || t.Is("union") || t.Is("for")) {
			end = i
			break
		}
		if t.Depth == 0 && t.Is("or") {
			return nil
		}
	}
	isConst := func(t sqlscan.Token) bool {
		return t.Kind == sqlscan.TParam || t.Kind == sqlscan.TString || t.Kind == sqlscan.TNumber
	}
	var out []string
	w := toks[start:end]
	for i := 1; i+1 < len(w); i++ {
		if w[i].Kind == sqlscan.TOp && w[i].Text == "=" {
			if w[i-1].IsIdent() && isConst(w[i+1]) {
				out = append(out, w[i-1].Text)
			} else if isConst(w[i-1]) && w[i+1].IsIdent() {
				out = append(out, w[i+1].Text)
			}
		}
	}
	return out
}
