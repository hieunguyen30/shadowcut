package sqlscan

import (
	"strconv"
	"strings"
)

// Kind is the statement kind.
type Kind string

const (
	KSelect  Kind = "select"
	KInsert  Kind = "insert"
	KUpdate  Kind = "update"
	KDelete  Kind = "delete"
	KDDL     Kind = "ddl"
	KTxn     Kind = "txn" // BEGIN/COMMIT/ROLLBACK/SAVEPOINT/SET TRANSACTION
	KSet     Kind = "set"
	KOther   Kind = "other"
	KUnknown Kind = "unknown"
)

// OrderItem is one ORDER BY item.
type OrderItem struct {
	Tokens     []Token
	Desc       bool
	NullsFirst *bool // explicit NULLS FIRST/LAST, nil when defaulted
	// Columns are the (unqualified, lower-case) identifiers referenced by the item
	// after resolving select-list aliases and positional references.
	Columns []string
	// ResultIndex is the index of the matching select-list column, or -1.
	ResultIndex int
	// Ordinal is true for "ORDER BY 2" style items.
	Ordinal bool
}

// SelectItem is one select-list entry.
type SelectItem struct {
	Tokens []Token
	Alias  string   // explicit AS alias or inferred column name
	Cols   []string // identifiers referenced
	Star   bool
}

// Info is the structural summary of a statement.
type Info struct {
	Kind        Kind
	Tables      []string          // referenced table names (lower-case, unqualified)
	Aliases     map[string]string // alias -> table
	Select      []SelectItem      // outermost select list
	OrderBy     []OrderItem       // outermost ORDER BY
	HasLimit    bool              // outermost LIMIT / FETCH FIRST
	HasOffset   bool
	Functions   map[string]int // function name -> call count (lower-case)
	ForUpdate   bool
	HasGroupBy  bool
	Aggregates  bool
	Fingerprint string
}

var aggregateFuncs = map[string]bool{"count": true, "sum": true, "avg": true, "min": true, "max": true, "string_agg": true, "array_agg": true, "bool_and": true, "bool_or": true, "stddev": true, "variance": true}

// Analyze computes Info for a statement's tokens.
func Analyze(toks []Token) Info {
	in := Info{Kind: KUnknown, Aliases: map[string]string{}, Functions: map[string]int{}}
	if len(toks) == 0 {
		return in
	}
	in.Fingerprint = Fingerprint(toks)
	first := toks[0]
	switch {
	case first.Is("select") || first.Is("with") || first.Is("values") || first.Is("table") || first.IsPunct("("):
		in.Kind = KSelect
		// WITH ... INSERT/UPDATE/DELETE
		if first.Is("with") {
			for _, t := range toks {
				if t.Depth == 0 && (t.Is("insert") || t.Is("update") || t.Is("delete")) {
					in.Kind = Kind(t.Text)
					break
				}
			}
		}
	case first.Is("insert"):
		in.Kind = KInsert
	case first.Is("update"):
		in.Kind = KUpdate
	case first.Is("delete"):
		in.Kind = KDelete
	case first.Is("create") || first.Is("alter") || first.Is("drop") || first.Is("truncate") || first.Is("comment"):
		in.Kind = KDDL
	case first.Is("begin") || first.Is("commit") || first.Is("rollback") || first.Is("savepoint") || first.Is("release") || first.Is("start") || first.Is("end") || first.Is("abort"):
		in.Kind = KTxn
	case first.Is("set"):
		in.Kind = KSet
		if len(toks) > 1 && (toks[1].Is("transaction") || (len(toks) > 2 && toks[1].Is("session") && toks[2].Is("characteristics"))) {
			in.Kind = KTxn
		}
	default:
		in.Kind = KOther
	}

	// functions, tables
	for i, t := range toks {
		if t.Kind == TIdent && i+1 < len(toks) && toks[i+1].IsPunct("(") {
			if !isKeywordNotFunc(t.Text) {
				in.Functions[t.Text]++
				if aggregateFuncs[t.Text] {
					in.Aggregates = true
				}
			}
		}
		if (t.Is("from") || t.Is("join") || t.Is("update") || t.Is("into")) && i+1 < len(toks) && !inFuncArgs(toks, i) {
			j := i + 1
			if toks[j].Is("only") || toks[j].Is("lateral") {
				j++
			}
			if j < len(toks) && toks[j].IsIdent() {
				name, next := qualifiedName(toks, j)
				isTableFunc := (t.Is("from") || t.Is("join")) && next < len(toks) && toks[next].IsPunct("(")
				if name != "" && !isKeywordNotFunc(name) && !isTableFunc && !(t.Is("update") && i > 0 && toks[i-1].Is("for")) && !(t.Is("from") && i > 0 && toks[i-1].Kind == TIdent && (toks[i-1].Text == "distinct" || toks[i-1].Text == "is")) {
					in.Tables = appendUnique(in.Tables, name)
					// alias
					k := next
					if k < len(toks) && toks[k].Is("as") {
						k++
					}
					if k < len(toks) && toks[k].IsIdent() && !isClauseKeyword(toks[k].Text) {
						in.Aliases[toks[k].Text] = name
					}
				}
			}
		}
		if t.Is("for") && t.Depth == 0 && i+1 < len(toks) && (toks[i+1].Is("update") || toks[i+1].Is("share") || toks[i+1].Is("no") || toks[i+1].Is("key")) {
			in.ForUpdate = true
		}
		if t.Is("group") && t.Depth == 0 && i+1 < len(toks) && toks[i+1].Is("by") {
			in.HasGroupBy = true
		}
	}
	for name := range in.Aliases {
		_ = name
	}

	if in.Kind == KSelect {
		in.Select = selectList(toks)
		in.OrderBy, in.HasLimit, in.HasOffset = orderBy(toks, in.Select)
	}
	return in
}

// inFuncArgs reports whether toks[i] sits inside parentheses that are not a
// subquery (e.g. "extract(epoch FROM ts)", "substring(x from 2)").
func inFuncArgs(toks []Token, i int) bool {
	d := toks[i].Depth
	if d == 0 {
		return false
	}
	for k := i - 1; k >= 0; k-- {
		if toks[k].Depth < d || (toks[k].IsPunct("(") && toks[k].Depth == d-1) {
			return true
		}
		if toks[k].Depth == d && (toks[k].Is("select") || toks[k].Is("delete") || toks[k].Is("update") || toks[k].Is("insert")) {
			return false
		}
	}
	return true
}

func qualifiedName(toks []Token, j int) (string, int) {
	name := toks[j].Text
	k := j + 1
	for k+1 < len(toks) && toks[k].IsPunct(".") && toks[k+1].IsIdent() {
		name = toks[k+1].Text
		k += 2
	}
	return name, k
}

var clauseKeywords = map[string]bool{"where": true, "join": true, "inner": true, "left": true, "right": true, "full": true, "cross": true, "on": true, "using": true, "group": true, "order": true, "limit": true, "offset": true, "having": true, "union": true, "except": true, "intersect": true, "set": true, "values": true, "returning": true, "for": true, "natural": true, "window": true, "fetch": true, "default": true, "select": true, "tablesample": true, "outer": true, "lateral": true}

func isClauseKeyword(s string) bool { return clauseKeywords[s] }

// tokens that look like f( but are not function calls
var notFuncs = map[string]bool{"in": true, "exists": true, "values": true, "as": true, "and": true, "or": true, "not": true, "over": true, "filter": true, "using": true, "on": true, "from": true, "select": true, "where": true, "any": true, "all": true, "some": true, "array": true, "row": true, "into": true, "when": true, "then": true, "else": true, "by": true, "is": true, "with": true, "join": true, "table": true, "key": true, "primary": true, "unique": true, "references": true, "check": true, "index": true, "constraint": true, "foreign": true, "partition": true, "include": true, "lateral": true, "returns": true, "interleave": true, "parent": true}

func isKeywordNotFunc(s string) bool { return notFuncs[s] }

func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

// outermost SELECT list: tokens between the first depth-0 SELECT [DISTINCT] and the
// following depth-0 FROM (or end of the select-list clause).
func selectList(toks []Token) []SelectItem {
	start := -1
	for i, t := range toks {
		if t.Depth == 0 && t.Is("select") {
			start = i + 1
			// keep the LAST depth-0 select before the top-level FROM? use first after any CTE
		}
		if start >= 0 {
			break
		}
	}
	if start < 0 {
		return nil
	}
	if start < len(toks) && (toks[start].Is("distinct") || toks[start].Is("all")) {
		start++
		if start < len(toks) && toks[start].Is("on") && start+1 < len(toks) && toks[start+1].IsPunct("(") {
			// skip DISTINCT ON (...)
			d := toks[start+1].Depth
			k := start + 2
			for k < len(toks) && !(toks[k].IsPunct(")") && toks[k].Depth == d) {
				k++
			}
			start = k + 1
		}
	}
	end := len(toks)
	for i := start; i < len(toks); i++ {
		t := toks[i]
		if t.Depth == 0 && (t.Is("from") || t.Is("where") || t.Is("group") || t.Is("order") || t.Is("limit") || t.Is("union") || t.Is("except") || t.Is("intersect") || t.Is("into") || t.Is("having") || t.Is("offset") || t.Is("fetch") || t.Is("for") || t.Is("window")) {
			end = i
			break
		}
	}
	var items []SelectItem
	for _, part := range splitTop(toks[start:end], 0) {
		if len(part) == 0 {
			continue
		}
		it := SelectItem{Tokens: part}
		if len(part) == 1 && part[0].Kind == TOp && part[0].Text == "*" {
			it.Star = true
		}
		if len(part) == 3 && part[1].IsPunct(".") && part[2].Kind == TOp && part[2].Text == "*" {
			it.Star = true
		}
		n := len(part)
		switch {
		case n >= 2 && part[n-2].Is("as") && part[n-1].IsIdent():
			it.Alias = part[n-1].Text
			part = part[:n-2]
		case n >= 2 && part[n-1].IsIdent() && part[n-1].Depth == part[0].Depth && (part[n-2].IsIdent() || part[n-2].IsPunct(")") || part[n-2].Kind == TString || part[n-2].Kind == TNumber) && !isOperatorKeyword(part[n-1].Text):
			it.Alias = part[n-1].Text
			part = part[:n-1]
		}
		it.Cols = identifiers(part)
		if it.Alias == "" {
			// inferred output name: last identifier for a bare column ref, else function name
			if len(part) >= 1 && part[len(part)-1].IsIdent() && (len(part) == 1 || part[len(part)-2].IsPunct(".")) {
				it.Alias = part[len(part)-1].Text
			} else if len(part) >= 2 && part[0].Kind == TIdent && part[1].IsPunct("(") {
				it.Alias = part[0].Text
			} else if len(part) >= 3 && part[len(part)-2].Kind == TOp && part[len(part)-2].Text == "::" {
				// expr::type -> name of expr if bare column
				if len(part) == 3 && part[0].IsIdent() {
					it.Alias = part[0].Text
				}
			}
		}
		items = append(items, it)
	}
	return items
}

func isOperatorKeyword(s string) bool {
	switch s {
	case "and", "or", "not", "is", "null", "like", "ilike", "in", "between", "desc", "asc", "end", "true", "false":
		return true
	}
	return false
}

// splitTop splits tokens on commas at the given relative depth.
func splitTop(toks []Token, rel int) [][]Token {
	if len(toks) == 0 {
		return nil
	}
	base := toks[0].Depth
	for _, t := range toks {
		if t.Depth < base {
			base = t.Depth
		}
	}
	var out [][]Token
	var cur []Token
	for _, t := range toks {
		if t.IsPunct(",") && t.Depth == base+rel {
			out = append(out, cur)
			cur = nil
			continue
		}
		cur = append(cur, t)
	}
	out = append(out, cur)
	return out
}

// identifiers returns column-like identifiers referenced in toks: bare identifiers
// that are not keywords, not function names, and not type names after '::'.
func identifiers(toks []Token) []string {
	var out []string
	for i, t := range toks {
		if !t.IsIdent() {
			continue
		}
		if t.Kind == TIdent && (isReserved(t.Text) || (i+1 < len(toks) && toks[i+1].IsPunct("("))) {
			continue
		}
		if i > 0 && toks[i-1].Kind == TOp && toks[i-1].Text == "::" {
			continue
		}
		// qualified: alias.col -> keep only the last part
		if i+1 < len(toks) && toks[i+1].IsPunct(".") {
			continue
		}
		out = appendUnique(out, t.Text)
	}
	return out
}

var reserved = map[string]bool{"select": true, "from": true, "where": true, "and": true, "or": true, "not": true, "null": true, "is": true, "as": true, "asc": true, "desc": true, "nulls": true, "first": true, "last": true, "case": true, "when": true, "then": true, "else": true, "end": true, "true": true, "false": true, "in": true, "like": true, "ilike": true, "between": true, "distinct": true, "on": true, "join": true, "left": true, "right": true, "inner": true, "outer": true, "limit": true, "offset": true, "order": true, "by": true, "group": true, "having": true, "interval": true, "current_timestamp": true, "current_date": true, "localtimestamp": true, "all": true, "any": true, "exists": true, "with": true, "using": true, "union": true, "collate": true, "similar": true, "to": true, "escape": true, "over": true, "partition": true, "filter": true, "within": true, "array": true, "row": true, "rows": true, "only": true, "fetch": true, "next": true, "cast": true, "at": true, "time": true, "zone": true, "timestamp": true, "date": true, "for": true, "update": true, "unknown": true, "some": true, "lateral": true, "symmetric": true}

func isReserved(s string) bool { return reserved[s] }

// orderBy extracts the outermost ORDER BY and LIMIT/OFFSET.
func orderBy(toks []Token, sel []SelectItem) (items []OrderItem, hasLimit, hasOffset bool) {
	ob := -1
	for i, t := range toks {
		if t.Depth != 0 {
			continue
		}
		if t.Is("order") && i+1 < len(toks) && toks[i+1].Is("by") {
			ob = i + 2
		}
		if t.Is("limit") {
			hasLimit = true
		}
		if t.Is("fetch") && i+1 < len(toks) && (toks[i+1].Is("first") || toks[i+1].Is("next")) {
			hasLimit = true
		}
		if t.Is("offset") {
			hasOffset = true
		}
	}
	if ob < 0 {
		return nil, hasLimit, hasOffset
	}
	end := len(toks)
	for i := ob; i < len(toks); i++ {
		t := toks[i]
		if t.Depth == 0 && (t.Is("limit") || t.Is("offset") || t.Is("fetch") || t.Is("for") || t.Is("union") || t.Is("except") || t.Is("intersect")) {
			end = i
			break
		}
	}
	for _, part := range splitTop(toks[ob:end], 0) {
		if len(part) == 0 {
			continue
		}
		it := OrderItem{ResultIndex: -1}
		// trailing modifiers
		for {
			n := len(part)
			if n >= 2 && part[n-2].Is("nulls") && (part[n-1].Is("first") || part[n-1].Is("last")) {
				v := part[n-1].Is("first")
				it.NullsFirst = &v
				part = part[:n-2]
				continue
			}
			if n >= 1 && (part[n-1].Is("asc") || part[n-1].Is("desc")) && n > 1 {
				it.Desc = part[n-1].Is("desc")
				part = part[:n-1]
				continue
			}
			break
		}
		it.Tokens = part
		if len(part) == 1 && part[0].Kind == TNumber {
			if n, err := strconv.Atoi(part[0].Text); err == nil && n >= 1 && n <= len(sel) {
				it.Ordinal = true
				it.ResultIndex = n - 1
				it.Columns = sel[n-1].Cols
			}
		} else {
			it.Columns = identifiers(part)
			// alias reference or same expression as a select item
			for si, s := range sel {
				if len(part) == 1 && part[0].IsIdent() && s.Alias == part[0].Text {
					it.ResultIndex = si
					if len(s.Cols) > 0 {
						it.Columns = s.Cols
					}
					break
				}
				if sameTokens(stripAlias(s), part) {
					it.ResultIndex = si
					break
				}
			}
		}
		items = append(items, it)
	}
	return items, hasLimit, hasOffset
}

func stripAlias(s SelectItem) []Token {
	toks := s.Tokens
	n := len(toks)
	if n >= 2 && toks[n-2].Is("as") {
		return toks[:n-2]
	}
	if s.Alias != "" && n >= 2 && toks[n-1].IsIdent() && toks[n-1].Text == s.Alias && !(n >= 2 && toks[n-2].IsPunct(".")) && n > 1 {
		return toks[:n-1]
	}
	return toks
}

func sameTokens(a, b []Token) bool {
	// compare ignoring qualifiers: strip "x." prefixes
	na, nb := unqualify(a), unqualify(b)
	if len(na) != len(nb) {
		return false
	}
	for i := range na {
		if na[i].Kind != nb[i].Kind || na[i].Text != nb[i].Text {
			return false
		}
	}
	return true
}

func unqualify(ts []Token) []Token {
	var out []Token
	for i := 0; i < len(ts); i++ {
		if ts[i].IsIdent() && i+1 < len(ts) && ts[i+1].IsPunct(".") {
			i++
			continue
		}
		out = append(out, ts[i])
	}
	return out
}

// Fingerprint normalizes literals and params so that structurally identical
// queries share a fingerprint (like pg_stat_statements' queryid, but textual).
func Fingerprint(toks []Token) string {
	var b strings.Builder
	for i, t := range toks {
		if i > 0 {
			b.WriteByte(' ')
		}
		switch t.Kind {
		case TString, TNumber, TParam:
			b.WriteByte('?')
		case TQIdent:
			b.WriteString(strings.ToLower(t.Text))
		default:
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// TokensText renders tokens back to compact SQL (for messages).
func TokensText(toks []Token) string {
	var b strings.Builder
	for i, t := range toks {
		if i > 0 && !(t.IsPunct(")") || t.IsPunct(",") || t.IsPunct(".") || (i > 0 && (toks[i-1].IsPunct("(") || toks[i-1].IsPunct(".")))) {
			b.WriteByte(' ')
		}
		b.WriteString(t.Raw)
	}
	return b.String()
}
