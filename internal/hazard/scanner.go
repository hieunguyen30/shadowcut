package hazard

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

// Finding is one hazard occurrence.
type Finding struct {
	Rule        string            `json:"rule"`
	Title       string            `json:"title"`
	Severity    Severity          `json:"severity"`
	Area        string            `json:"area"`
	Predicts    classify.Category `json:"predicts,omitempty"`
	File        string            `json:"file"`
	Line        int               `json:"line"`
	Col         int               `json:"col"`
	Query       string            `json:"query,omitempty"` // statement name (annotation) if any
	Fingerprint string            `json:"fingerprint,omitempty"`
	Message     string            `json:"message"`
	Fix         string            `json:"fix"`
	Sources     []string          `json:"sources"`
	Waived      string            `json:"waived,omitempty"` // waiver reason
}

// QueryEntry lists what the scanner predicts for one statement (Experiment 1).
type QueryEntry struct {
	Name        string              `json:"name,omitempty"`
	Fingerprint string              `json:"fingerprint"`
	File        string              `json:"file"`
	Line        int                 `json:"line"`
	Kind        sqlscan.Kind        `json:"kind"`
	Predicts    []classify.Category `json:"predicts"`
	Rules       []string            `json:"rules"`
}

// Report is the scanner output.
type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Inputs      []string         `json:"inputs"`
	Findings    []Finding        `json:"findings"`
	Queries     []QueryEntry     `json:"queries"`
	BySeverity  map[Severity]int `json:"by_severity"`
	ByRule      map[string]int   `json:"by_rule"`
}

// Unresolved counts non-waived findings at or above sev.
func (r *Report) Unresolved(sev Severity) int {
	n := 0
	for _, f := range r.Findings {
		if f.Waived == "" && f.Severity.Rank() >= sev.Rank() {
			n++
		}
	}
	return n
}

// Input is one SQL source.
type Input struct {
	File string
	SQL  string
}

// Scanner runs the rules.
type Scanner struct {
	Schema   *sqlscan.Schema
	Evidence *Evidence
}

// New creates a scanner over a schema (may be empty).
func New(schema *sqlscan.Schema) *Scanner {
	if schema == nil {
		schema = &sqlscan.Schema{Tables: map[string]*sqlscan.Table{}}
	}
	return &Scanner{Schema: schema, Evidence: LoadEvidence()}
}

// Scan scans schema and query inputs. Schema inputs are folded into the scanner's
// schema first so that query rules can resolve column types.
func (s *Scanner) Scan(schemaInputs, queryInputs []Input) *Report {
	rep := &Report{GeneratedAt: time.Now().UTC(), BySeverity: map[Severity]int{}, ByRule: map[string]int{}}
	type parsed struct {
		file string
		sts  []sqlscan.Statement
	}
	var schemas, qs []parsed
	for _, in := range schemaInputs {
		sts := sqlscan.Split(in.SQL)
		for _, st := range sts {
			s.Schema.AddStatement(st)
		}
		schemas = append(schemas, parsed{in.File, sts})
		rep.Inputs = append(rep.Inputs, in.File)
	}
	for _, in := range queryInputs {
		qs = append(qs, parsed{in.File, sqlscan.Split(in.SQL)})
		rep.Inputs = append(rep.Inputs, in.File)
	}
	for _, p := range schemas {
		for _, st := range p.sts {
			for _, f := range s.schemaRules(st) {
				f.File = p.file
				rep.add(f)
			}
		}
	}
	for _, p := range qs {
		txn := newTxnTracker()
		for _, st := range p.sts {
			info := sqlscan.Analyze(st.Tokens)
			var fs []Finding
			if info.Kind == sqlscan.KDDL {
				fs = s.schemaRules(st)
			} else {
				fs = s.queryRules(st, info)
				fs = append(fs, txn.observe(s, st, info)...)
			}
			qe := QueryEntry{Name: st.Name, Fingerprint: info.Fingerprint, File: p.file, Line: st.Line, Kind: info.Kind, Predicts: []classify.Category{}, Rules: []string{}}
			seenCat := map[classify.Category]bool{}
			seenRule := map[string]bool{}
			for i := range fs {
				fs[i].File = p.file
				if fs[i].Query == "" {
					fs[i].Query = st.Name
				}
				if fs[i].Fingerprint == "" {
					fs[i].Fingerprint = info.Fingerprint
				}
				rep.add(fs[i])
				if fs[i].Predicts != "" && !seenCat[fs[i].Predicts] {
					seenCat[fs[i].Predicts] = true
					qe.Predicts = append(qe.Predicts, fs[i].Predicts)
				}
				if !seenRule[fs[i].Rule] {
					seenRule[fs[i].Rule] = true
					qe.Rules = append(qe.Rules, fs[i].Rule)
				}
			}
			if info.Kind != sqlscan.KDDL {
				rep.Queries = append(rep.Queries, qe)
			}
		}
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.Severity.Rank() != b.Severity.Rank() {
			return a.Severity.Rank() > b.Severity.Rank()
		}
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})
	return rep
}

func (r *Report) add(f Finding) {
	// de-duplicate identical rule+location
	for _, g := range r.Findings {
		if g.Rule == f.Rule && g.File == f.File && g.Line == f.Line && g.Col == f.Col && g.Message == f.Message {
			return
		}
	}
	r.Findings = append(r.Findings, f)
	r.BySeverity[f.Severity]++
	r.ByRule[f.Rule]++
}

func finding(ruleID string, at sqlscan.Token, msg string) Finding {
	r := RuleByID[ruleID]
	return Finding{Rule: r.ID, Title: r.Title, Severity: r.Severity, Area: r.Area, Predicts: r.Predicts, Line: at.Line, Col: at.Col, Message: msg, Fix: r.Fix, Sources: r.Sources}
}

// ---- schema rules -----------------------------------------------------------

var unsupportedTypes = map[string]string{
	"timestamp": "timestamp without time zone", "char": "char(n)", "money": "money", "inet": "inet", "cidr": "cidr",
	"macaddr": "macaddr", "xml": "xml", "json": "json (use jsonb)", "tsvector": "tsvector", "tsquery": "tsquery", "hstore": "hstore",
	"point": "point", "interval": "interval (query-only on Spanner)", "time": "time", "timetz": "time with time zone",
	"bit": "bit", "varbit": "bit varying", "citext": "citext (extension)", "oid": "oid", "name": "name",
}

func (s *Scanner) schemaRules(st sqlscan.Statement) []Finding {
	t := st.Tokens
	var out []Finding
	if len(t) < 2 {
		return out
	}
	first := t[0]
	// unsupported DDL statements
	switch {
	case first.Is("create"):
		kw := ""
		for _, x := range t[1:] {
			if x.Kind == sqlscan.TIdent && (x.Text == "or" || x.Text == "replace" || x.Text == "unique" || x.Text == "temp" || x.Text == "temporary" || x.Text == "unlogged" || x.Text == "constraint" || x.Text == "materialized" || x.Text == "recursive") {
				if x.Text == "temp" || x.Text == "temporary" {
					out = append(out, finding("Q-UNSUP-TXN", x, "temporary tables are not supported"))
				}
				if x.Text == "materialized" {
					out = append(out, finding("SC-UNSUP-DDL", x, "materialized views are not supported"))
				}
				continue
			}
			kw = x.Text
			break
		}
		switch kw {
		case "trigger", "function", "procedure", "extension", "type", "domain", "operator", "rule", "aggregate", "foreign", "server", "policy", "publication", "subscription":
			if kw == "extension" && hasIdent(t, "pg_stat_statements") {
				out = append(out, finding("SC-UNSUP-DDL", first, "CREATE EXTENSION pg_stat_statements: extensions are not supported (observability must come from Spanner's query statistics instead)"))
			} else if kw == "publication" {
				out = append(out, finding("SC-UNSUP-DDL", first, "CREATE PUBLICATION: logical replication publications do not exist on Spanner (use change streams)"))
			} else {
				out = append(out, finding("SC-UNSUP-DDL", first, "CREATE "+strings.ToUpper(kw)+" is not supported"))
			}
		case "index":
			out = append(out, s.indexRules(st)...)
		case "table":
			out = append(out, s.tableRules(st)...)
		case "schema":
			// named schemas are supported
		}
		// identifiers starting with '_'
		for i, x := range t {
			if x.IsIdent() && strings.HasPrefix(x.Text, "_") && i > 0 && (t[i-1].Is("table") || t[i-1].Is("index") || t[i-1].Is("sequence") || t[i-1].Is("view")) {
				out = append(out, finding("SC-IDENT-UNDERSCORE", x, fmt.Sprintf("%q starts with an underscore", x.Text)))
			}
		}
	case first.Is("alter"):
		if hasIdent(t, "trigger") {
			out = append(out, finding("SC-UNSUP-DDL", first, "triggers are not supported"))
		}
	}
	return out
}

func hasIdent(t []sqlscan.Token, s string) bool {
	for _, x := range t {
		if x.Kind == sqlscan.TIdent && x.Text == s {
			return true
		}
	}
	return false
}

func (s *Scanner) tableRules(st sqlscan.Statement) []Finding {
	var out []Finding
	// re-parse to get this table
	tmp := &sqlscan.Schema{Tables: map[string]*sqlscan.Table{}}
	tmp.AddStatement(st)
	for _, tbl := range tmp.Tables {
		at := func(c *sqlscan.Column) sqlscan.Token { return sqlscan.Token{Line: c.Line, Col: c.Col} }
		colByName := map[string]*sqlscan.Column{}
		for _, c := range tbl.Columns {
			colByName[c.Name] = c
			if strings.HasPrefix(c.Name, "_") {
				out = append(out, finding("SC-IDENT-UNDERSCORE", at(c), fmt.Sprintf("column %s.%s starts with an underscore", tbl.Name, c.Name)))
			}
			base := strings.TrimSuffix(c.Type, "[]")
			if why, bad := unsupportedTypes[base]; bad {
				out = append(out, finding("SC-UNSUP-TYPE", at(c), fmt.Sprintf("%s.%s has type %s, which Spanner does not support", tbl.Name, c.Name, why)))
			}
			if c.Type == "numeric" && strings.Contains(c.TypeRaw, "(") {
				out = append(out, finding("SC-NUMERIC-TYPMOD", at(c), fmt.Sprintf("%s.%s is %s; Spanner stores plain numeric, so the scale is no longer enforced on writes", tbl.Name, c.Name, c.TypeRaw)))
			}
			if c.Type == "integer" || c.Type == "smallint" || c.Type == "serial" || c.Type == "smallserial" {
				out = append(out, finding("SC-INT4", at(c), fmt.Sprintf("%s.%s (%s) becomes int8", tbl.Name, c.Name, c.TypeRaw)))
			}
			if strings.Contains(c.TypeRaw, "collate") {
				out = append(out, finding("Q-UNSUP-SYNTAX", at(c), "column-level COLLATE is not supported"))
			}
		}
		if len(tbl.PK) > 0 {
			first := colByName[tbl.PK[0]]
			if first != nil {
				if first.Identity {
					out = append(out, finding("SC-SEQ-KEY", at(first), fmt.Sprintf("%s primary key %s is %s: sequential values hotspot one split", tbl.Name, first.Name, first.TypeRaw)))
				}
				if first.Type == "timestamptz" || first.Type == "timestamp" || first.Type == "date" {
					out = append(out, finding("SC-MONO-TS-KEY", at(first), fmt.Sprintf("%s primary key starts with %s (%s)", tbl.Name, first.Name, first.Type)))
				}
			}
			for _, k := range tbl.PK {
				if c := colByName[k]; c != nil && (c.Type == "numeric" || c.Type == "jsonb" || c.Type == "float4") {
					out = append(out, finding("SC-KEY-TYPE", at(c), fmt.Sprintf("%s.%s (%s) is part of the primary key", tbl.Name, c.Name, c.Type)))
				}
			}
		}
	}
	return out
}

func (s *Scanner) indexRules(st sqlscan.Statement) []Finding {
	var out []Finding
	tmp := &sqlscan.Schema{Tables: map[string]*sqlscan.Table{}}
	tmp.AddStatement(st)
	for _, idx := range tmp.Indexes {
		at := sqlscan.Token{Line: st.Line, Col: 1}
		if idx.Partial {
			out = append(out, finding("SC-UNSUP-DDL", at, fmt.Sprintf("partial index %s (WHERE clause) is not supported", idx.Name)))
		}
		if len(idx.Columns) == 0 {
			continue
		}
		c := s.Schema.Col(idx.Table, idx.Columns[0])
		if c == nil {
			continue
		}
		if c.Type == "timestamptz" || c.Type == "timestamp" || c.Type == "date" || c.Identity {
			out = append(out, finding("SC-MONO-INDEX", at, fmt.Sprintf("index %s on %s(%s): first column is monotonically increasing (%s)", idx.Name, idx.Table, strings.Join(idx.Columns, ", "), c.TypeRaw)))
		}
		for _, col := range idx.Columns {
			if cc := s.Schema.Col(idx.Table, col); cc != nil && (cc.Type == "numeric" || cc.Type == "jsonb" || cc.Type == "float4") {
				out = append(out, finding("SC-KEY-TYPE", at, fmt.Sprintf("index %s includes %s (%s)", idx.Name, col, cc.Type)))
			}
		}
	}
	return out
}
