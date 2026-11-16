// Package explain is the optional Layer 4: plain-language explanations and
// suggested fixes for shadow-read mismatches. It is behind an interface with a
// deterministic, rule-based mock (used for every local result) and a Vertex AI
// Gemini implementation that needs cloud credentials (evaluation pending).
package explain

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

// Input is one mismatch to explain.
type Input struct {
	SQL      string
	Category classify.Category
	Reason   string // classifier evidence
	Schema   *sqlscan.Schema
}

// Explanation is the explainer's answer.
type Explanation struct {
	Summary  string `json:"summary"`
	Fix      string `json:"fix"`
	FixedSQL string `json:"fixed_sql,omitempty"` // concrete rewrite, when one exists
	Model    string `json:"model"`
}

// Explainer explains mismatches.
type Explainer interface {
	Explain(ctx context.Context, in Input) (Explanation, error)
	Name() string
}

// Mock is a deterministic explainer built from the hazard catalog plus
// rule-based rewrites.
type Mock struct{}

// Name implements Explainer.
func (Mock) Name() string { return "mock-rules-v1" }

var catRule = map[classify.Category]string{
	classify.Collation: "Q-COLLATION-ORDER", classify.NullSemantics: "Q-CONCAT-NULL", classify.FuncSemantics: "Q-FUNC-SEMANTICS",
	classify.Timezone: "Q-TZ-DEFAULT", classify.Precision: "Q-FLOAT-AGG", classify.Nondeterminism: "Q-ORDER-TIES",
	classify.Unsupported: "Q-UNSUP-SYNTAX", classify.NullOrdering: "Q-NULL-ORDER", classify.Isolation: "Q-ISOLATION-RMW",
}

// Explain implements Explainer.
func (Mock) Explain(_ context.Context, in Input) (Explanation, error) {
	e := Explanation{Model: "mock-rules-v1"}
	r, ok := hazard.RuleByID[catRule[in.Category]]
	if ok {
		e.Summary = fmt.Sprintf("%s. %s Evidence: %s", r.Title, firstSentence(r.Explain), in.Reason)
		e.Fix = r.Fix
	} else {
		e.Summary = fmt.Sprintf("Mismatch classified as %s. Evidence: %s", in.Category, in.Reason)
		e.Fix = "Inspect the differing rows in the mismatch log; no automatic rewrite is known."
	}
	switch in.Category {
	case classify.NullSemantics:
		e.FixedSQL = rewriteConcat(in.SQL)
	case classify.Timezone:
		e.FixedSQL = rewriteDateTrunc(in.SQL)
	case classify.Nondeterminism:
		e.FixedSQL = rewriteTieBreak(in.SQL, in.Schema)
	case classify.Unsupported:
		if s := rewriteAnyLiteral(in.SQL); s != in.SQL {
			e.FixedSQL = s
		} else if s := rewriteRowCompare(in.SQL); s != in.SQL {
			e.FixedSQL = s
		}
	}
	if e.FixedSQL == in.SQL {
		e.FixedSQL = ""
	}
	return e, nil
}

func firstSentence(s string) string {
	if i := strings.Index(s, ". "); i > 0 {
		return s[:i+1]
	}
	return s
}

// rewriteConcat wraps every non-literal concat() argument in coalesce(x, ”).
func rewriteConcat(sql string) string {
	toks := sqlscan.Lex(sql)
	var b strings.Builder
	last := 0
	for i := 0; i+1 < len(toks); i++ {
		if !(toks[i].Is("concat") && toks[i+1].IsPunct("(")) {
			continue
		}
		d := toks[i+1].Depth + 1
		var args [][]sqlscan.Token
		var cur []sqlscan.Token
		j := i + 2
		for ; j < len(toks); j++ {
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
		args = append(args, cur)
		start, end := offset(sql, toks[i]), offset(sql, toks[j])+1
		b.WriteString(sql[last:start])
		var parts []string
		for _, a := range args {
			txt := strings.TrimSpace(sql[offset(sql, a[0]) : offset(sql, a[len(a)-1])+len(a[len(a)-1].Raw)])
			if len(a) == 1 && a[0].Kind == sqlscan.TString {
				parts = append(parts, txt)
			} else {
				parts = append(parts, "coalesce("+txt+"::text, '')")
			}
		}
		b.WriteString("concat(" + strings.Join(parts, ", ") + ")")
		last = end
		i = j
	}
	b.WriteString(sql[last:])
	return b.String()
}

var dateTrunc2 = regexp.MustCompile(`(?i)date_trunc\(\s*('[^']*')\s*,\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\)`)

// rewriteDateTrunc pins 2-argument date_trunc to UTC (the Postgres session zone here).
func rewriteDateTrunc(sql string) string {
	return dateTrunc2.ReplaceAllString(sql, "date_trunc($1, $2, 'UTC')")
}

// rewriteTieBreak appends the table's primary key to a top-level ORDER BY (or adds
// ORDER BY pk before LIMIT when there is none).
func rewriteTieBreak(sql string, schema *sqlscan.Schema) string {
	toks := sqlscan.Lex(sql)
	info := sqlscan.Analyze(toks)
	if schema == nil || len(info.Tables) != 1 || info.HasGroupBy || info.Aggregates {
		return ""
	}
	t := schema.Tables[info.Tables[0]]
	if t == nil || len(t.PK) == 0 {
		return ""
	}
	pk := strings.Join(t.PK, ", ")
	insertAt := -1
	for _, tk := range toks {
		if tk.Depth == 0 && (tk.Is("limit") || tk.Is("offset") || tk.Is("fetch") || tk.Is("for")) {
			insertAt = offset(sql, tk)
			break
		}
	}
	add := ", " + pk
	if len(info.OrderBy) == 0 {
		add = " ORDER BY " + pk
	}
	if insertAt < 0 {
		return strings.TrimRight(sql, " \n;") + " " + strings.TrimPrefix(add, " ")
	}
	return strings.TrimRight(sql[:insertAt], " \n") + add + "\n" + sql[insertAt:]
}

var anyLiteral = regexp.MustCompile(`(?i)\b(ANY|SOME)\(\s*'\{([^}']*)\}'\s*\)`)

// rewriteAnyLiteral turns = ANY('{a,b}') into = ANY(ARRAY['a','b']).
func rewriteAnyLiteral(sql string) string {
	return anyLiteral.ReplaceAllStringFunc(sql, func(m string) string {
		sub := anyLiteral.FindStringSubmatch(m)
		var items []string
		for _, x := range strings.Split(sub[2], ",") {
			x = strings.TrimSpace(x)
			if _, err := fmt.Sscan(x, new(float64)); err == nil {
				items = append(items, x)
			} else {
				items = append(items, "'"+strings.ReplaceAll(x, "'", "''")+"'")
			}
		}
		return sub[1] + "(ARRAY[" + strings.Join(items, ", ") + "])"
	})
}

var rowCmp = regexp.MustCompile(`\(\s*([A-Za-z_][A-Za-z0-9_.]*)\s*,\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\)\s*(<|>)\s*\(\s*([^,()]+?)\s*,\s*([^,()]+?)\s*\)`)

// rewriteRowCompare expands (a, b) < (x, y) into a < x OR (a = x AND b < y).
func rewriteRowCompare(sql string) string {
	return rowCmp.ReplaceAllString(sql, "($1 $3 $4 OR ($1 = $4 AND $2 $3 $5))")
}

// offset returns the byte offset of token t in sql (line/col are 1-based bytes).
func offset(sql string, t sqlscan.Token) int {
	line, i := 1, 0
	for i < len(sql) && line < t.Line {
		if sql[i] == '\n' {
			line++
		}
		i++
	}
	return i + t.Col - 1
}
