package sqlscan

import "strings"

// Column is a table column from DDL.
type Column struct {
	Name       string
	Type       string // normalized base type, e.g. "text", "bigint", "numeric", "timestamptz"
	TypeRaw    string // as written, e.g. "numeric(14,4)", "bigserial"
	NotNull    bool
	PrimaryKey bool
	Unique     bool
	Identity   bool // serial / identity / nextval default
	Line, Col  int
}

// Table is a table from DDL.
type Table struct {
	Name    string
	Columns []*Column
	PK      []string
	Uniques [][]string
	Line    int
}

// Index is a CREATE INDEX statement.
type Index struct {
	Name    string
	Table   string
	Columns []string
	Unique  bool
	Partial bool
	Line    int
}

// Schema is the set of tables and indexes parsed from DDL.
type Schema struct {
	Tables  map[string]*Table
	Indexes []*Index
}

// Col looks up a column by table and name.
func (s *Schema) Col(table, col string) *Column {
	if s == nil {
		return nil
	}
	t := s.Tables[table]
	if t == nil {
		return nil
	}
	for _, c := range t.Columns {
		if c.Name == col {
			return c
		}
	}
	return nil
}

// Resolve finds a column by name among the given tables (first match wins).
func (s *Schema) Resolve(tables []string, col string) (*Table, *Column) {
	if s == nil {
		return nil, nil
	}
	for _, tn := range tables {
		if c := s.Col(tn, col); c != nil {
			return s.Tables[tn], c
		}
	}
	return nil, nil
}

// IsUniqueSet reports whether cols cover the PK or a unique key of table t.
func (t *Table) IsUniqueSet(cols []string) bool {
	has := map[string]bool{}
	for _, c := range cols {
		has[c] = true
	}
	covers := func(key []string) bool {
		if len(key) == 0 {
			return false
		}
		for _, k := range key {
			if !has[k] {
				return false
			}
		}
		return true
	}
	if covers(t.PK) {
		return true
	}
	for _, u := range t.Uniques {
		if covers(u) {
			return true
		}
	}
	return false
}

// NormalizeType maps PostgreSQL type spellings to a canonical base name.
func NormalizeType(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	if i := strings.IndexByte(r, '('); i >= 0 {
		j := strings.IndexByte(r, ')')
		if j > i {
			r = strings.TrimSpace(r[:i] + r[j+1:])
		} else {
			r = strings.TrimSpace(r[:i])
		}
	}
	r = strings.Join(strings.Fields(r), " ")
	arr := strings.HasSuffix(r, "[]")
	r = strings.TrimSuffix(r, "[]")
	m := map[string]string{
		"int": "integer", "int4": "integer", "integer": "integer", "smallint": "smallint", "int2": "smallint",
		"bigint": "bigint", "int8": "bigint", "serial": "serial", "serial4": "serial", "bigserial": "bigserial", "serial8": "bigserial", "smallserial": "smallserial", "serial2": "smallserial",
		"text": "text", "varchar": "varchar", "character varying": "varchar", "char": "char", "character": "char", "bpchar": "char", "name": "name", "citext": "citext",
		"numeric": "numeric", "decimal": "numeric", "real": "float4", "float4": "float4", "double precision": "float8", "float8": "float8", "float": "float8", "money": "money",
		"timestamptz": "timestamptz", "timestamp with time zone": "timestamptz", "timestamp": "timestamp", "timestamp without time zone": "timestamp",
		"date": "date", "time": "time", "time without time zone": "time", "timetz": "timetz", "time with time zone": "timetz", "interval": "interval",
		"boolean": "boolean", "bool": "boolean", "bytea": "bytea", "uuid": "uuid", "json": "json", "jsonb": "jsonb",
		"inet": "inet", "cidr": "cidr", "macaddr": "macaddr", "xml": "xml", "tsvector": "tsvector", "tsquery": "tsquery", "point": "point", "hstore": "hstore", "bit": "bit", "varbit": "varbit", "oid": "oid",
	}
	out, ok := m[r]
	if !ok {
		out = r
	}
	if arr {
		out += "[]"
	}
	return out
}

// IsTextType reports whether a normalized type compares as a collatable string.
func IsTextType(t string) bool {
	switch t {
	case "text", "varchar", "char", "name", "citext":
		return true
	}
	return false
}

// IsFloatType reports binary floating point types.
func IsFloatType(t string) bool { return t == "float4" || t == "float8" }

// ParseSchema extracts tables and indexes from a DDL script.
func ParseSchema(src string) *Schema {
	s := &Schema{Tables: map[string]*Table{}}
	for _, st := range Split(src) {
		s.AddStatement(st)
	}
	return s
}

// AddStatement folds one DDL statement into the schema (non-DDL is ignored).
func (s *Schema) AddStatement(st Statement) {
	t := st.Tokens
	if len(t) < 3 || !t[0].Is("create") {
		return
	}
	i := 1
	unique := false
	for i < len(t) && (t[i].Is("unique") || t[i].Is("temp") || t[i].Is("temporary") || t[i].Is("unlogged") || t[i].Is("or") || t[i].Is("replace") || t[i].Is("global") || t[i].Is("local")) {
		if t[i].Is("unique") {
			unique = true
		}
		i++
	}
	switch {
	case i < len(t) && t[i].Is("table"):
		s.parseTable(t[i+1:], st.Line)
	case i < len(t) && t[i].Is("index"):
		s.parseIndex(t[i+1:], unique, st.Line)
	}
}

func skipIfNotExists(t []Token) []Token {
	if len(t) >= 3 && t[0].Is("if") && t[1].Is("not") && t[2].Is("exists") {
		return t[3:]
	}
	return t
}

func (s *Schema) parseTable(t []Token, line int) {
	t = skipIfNotExists(t)
	if len(t) == 0 || !t[0].IsIdent() {
		return
	}
	name, k := qualifiedName(t, 0)
	tbl := &Table{Name: name, Line: t[0].Line}
	if k >= len(t) || !t[k].IsPunct("(") {
		return
	}
	d := t[k].Depth
	end := k + 1
	for end < len(t) && !(t[end].IsPunct(")") && t[end].Depth == d) {
		end++
	}
	for _, def := range splitTop(t[k+1:end], 0) {
		if len(def) == 0 {
			continue
		}
		switch {
		case def[0].Is("primary") && len(def) > 1 && def[1].Is("key"):
			tbl.PK = parenList(def)
		case def[0].Is("unique"):
			tbl.Uniques = append(tbl.Uniques, parenList(def))
		case def[0].Is("constraint"):
			// CONSTRAINT name PRIMARY KEY (...) | UNIQUE (...)
			if len(def) > 3 && def[2].Is("primary") {
				tbl.PK = parenList(def)
			} else if len(def) > 2 && def[2].Is("unique") {
				tbl.Uniques = append(tbl.Uniques, parenList(def))
			}
		case def[0].Is("foreign") || def[0].Is("check") || def[0].Is("exclude") || def[0].Is("like"):
		default:
			if !def[0].IsIdent() {
				continue
			}
			c := &Column{Name: def[0].Text, Line: def[0].Line, Col: def[0].Col}
			// type: tokens until a constraint keyword at the column's depth
			j := 1
			var typ []string
			for j < len(def) {
				tk := def[j]
				if tk.Depth == def[0].Depth && tk.Kind == TIdent && isColConstraintKw(tk.Text) {
					break
				}
				typ = append(typ, tk.Raw)
				j++
			}
			c.TypeRaw = strings.ToLower(strings.Join(typ, " "))
			c.TypeRaw = tightPunct(c.TypeRaw)
			c.Type = NormalizeType(c.TypeRaw)
			if strings.Contains(c.Type, "serial") {
				c.Identity = true
			}
			rest := def[j:]
			for x := 0; x < len(rest); x++ {
				switch {
				case rest[x].Is("not") && x+1 < len(rest) && rest[x+1].Is("null"):
					c.NotNull = true
				case rest[x].Is("primary"):
					c.PrimaryKey = true
					c.NotNull = true
					tbl.PK = []string{c.Name}
				case rest[x].Is("unique"):
					c.Unique = true
					tbl.Uniques = append(tbl.Uniques, []string{c.Name})
				case rest[x].Is("identity"), rest[x].Is("nextval"):
					c.Identity = true
				}
			}
			tbl.Columns = append(tbl.Columns, c)
		}
	}
	for _, pk := range tbl.PK {
		for _, c := range tbl.Columns {
			if c.Name == pk {
				c.NotNull = true
			}
		}
	}
	s.Tables[name] = tbl
}

func isColConstraintKw(s string) bool {
	switch s {
	case "not", "null", "primary", "unique", "default", "references", "check", "constraint", "generated", "collate":
		return true
	}
	return false
}

func parenList(def []Token) []string {
	var out []string
	in := false
	d := -1
	for _, t := range def {
		if t.IsPunct("(") && !in {
			in = true
			d = t.Depth + 1
			continue
		}
		if in && t.IsPunct(")") && t.Depth == d-1 {
			break
		}
		if in && t.IsIdent() && t.Depth == d {
			out = append(out, t.Text)
		}
	}
	return out
}

func (s *Schema) parseIndex(t []Token, unique bool, line int) {
	t = skipIfNotExists(t)
	idx := &Index{Unique: unique, Line: line}
	if len(t) > 0 && t[0].Is("concurrently") {
		t = t[1:]
	}
	k := 0
	if k < len(t) && t[k].IsIdent() && !t[k].Is("on") {
		idx.Name = t[k].Text
		k++
	}
	if k < len(t) && t[k].Is("on") {
		k++
	}
	if k < len(t) && t[k].Is("only") {
		k++
	}
	if k < len(t) {
		idx.Table, k = qualifiedName(t, k)
	}
	for k < len(t) && !t[k].IsPunct("(") {
		k++
	}
	if k < len(t) {
		d := t[k].Depth
		end := k + 1
		for end < len(t) && !(t[end].IsPunct(")") && t[end].Depth == d) {
			end++
		}
		for _, part := range splitTop(t[k+1:end], 0) {
			if len(part) > 0 && part[0].IsIdent() {
				idx.Columns = append(idx.Columns, part[0].Text)
			}
		}
		for _, x := range t[end:] {
			if x.Is("where") && x.Depth == 0 {
				idx.Partial = true
			}
		}
	}
	if unique && idx.Table != "" {
		if tbl := s.Tables[idx.Table]; tbl != nil {
			tbl.Uniques = append(tbl.Uniques, idx.Columns)
		}
	}
	s.Indexes = append(s.Indexes, idx)
}

// tightPunct removes spaces adjacent to ( ) , [ ] in a type spelling.
func tightPunct(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			prev := byte(0)
			if b.Len() > 0 {
				prev = b.String()[b.Len()-1]
			}
			next := byte(0)
			if i+1 < len(s) {
				next = s[i+1]
			}
			if strings.IndexByte("(),[]", prev) >= 0 || strings.IndexByte("(),[]", next) >= 0 {
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
