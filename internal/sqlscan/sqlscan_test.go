package sqlscan

import (
	"os"
	"reflect"
	"testing"
)

func TestLexBasics(t *testing.T) {
	toks := Lex(`SELECT "Mixed", 'it''s', E'a\'b', $tag$ x;y $tag$, $1 -- c
	/* nested /* c */ */ FROM t WHERE a::text <> 'x';`)
	var kinds []TokKind
	var texts []string
	for _, tk := range toks {
		kinds = append(kinds, tk.Kind)
		texts = append(texts, tk.Text)
	}
	want := []string{"select", "Mixed", ",", "it's", ",", "a'b", ",", " x;y ", ",", "$1", "from", "t", "where", "a", "::", "text", "<>", "x", ";"}
	if !reflect.DeepEqual(texts, want) {
		t.Fatalf("texts:\n got %q\nwant %q", texts, want)
	}
	if kinds[1] != TQIdent || kinds[3] != TString || kinds[9] != TParam {
		t.Fatalf("unexpected kinds %v", kinds)
	}
}

func TestSplitAnnotations(t *testing.T) {
	src := "-- name: a\n-- expect: collation\nSELECT 1;\n\n-- name: b\nSELECT ';' ;"
	sts := Split(src)
	if len(sts) != 2 {
		t.Fatalf("got %d statements", len(sts))
	}
	if sts[0].Name != "a" || sts[0].Tags["expect"] != "collation" || sts[1].Name != "b" {
		t.Fatalf("bad annotations: %+v %+v", sts[0].Tags, sts[1].Tags)
	}
	if sts[1].Text != "SELECT ';'" {
		t.Fatalf("text = %q", sts[1].Text)
	}
}

func TestAnalyzeOrderBy(t *testing.T) {
	st := Split(`SELECT e.page_title AS title, count(*) n, user_name
	FROM edits e JOIN pages p ON p.title = e.page_title
	WHERE extract(epoch FROM e.edit_ts) > 0
	ORDER BY n DESC, title NULLS FIRST, 3 LIMIT 10`)[0]
	in := Analyze(st.Tokens)
	if in.Kind != KSelect || !in.HasLimit || !in.Aggregates {
		t.Fatalf("info = %+v", in)
	}
	if !reflect.DeepEqual(in.Tables, []string{"edits", "pages"}) {
		t.Fatalf("tables = %v", in.Tables)
	}
	if in.Aliases["e"] != "edits" || in.Aliases["p"] != "pages" {
		t.Fatalf("aliases = %v", in.Aliases)
	}
	if len(in.OrderBy) != 3 {
		t.Fatalf("order by = %+v", in.OrderBy)
	}
	o := in.OrderBy
	if !o[0].Desc || o[0].ResultIndex != 1 {
		t.Fatalf("item0 = %+v", o[0])
	}
	if o[1].NullsFirst == nil || !*o[1].NullsFirst || o[1].ResultIndex != 0 || !reflect.DeepEqual(o[1].Columns, []string{"page_title"}) {
		t.Fatalf("item1 = %+v", o[1])
	}
	if !o[2].Ordinal || o[2].ResultIndex != 2 || !reflect.DeepEqual(o[2].Columns, []string{"user_name"}) {
		t.Fatalf("item2 = %+v", o[2])
	}
}

func TestAnalyzeKinds(t *testing.T) {
	cases := map[string]Kind{
		"BEGIN":                                    KTxn,
		"SET TRANSACTION ISOLATION LEVEL READ COMMITTED": KTxn,
		"WITH x AS (SELECT 1) UPDATE t SET a=1":    KUpdate,
		"INSERT INTO t (a) VALUES (1)":             KInsert,
		"CREATE INDEX i ON t (a)":                  KDDL,
		"(SELECT 1) UNION (SELECT 2)":              KSelect,
	}
	for sql, want := range cases {
		if got := Analyze(Lex(sql)).Kind; got != want {
			t.Errorf("%q: got %s want %s", sql, got, want)
		}
	}
	in := Analyze(Lex("INSERT INTO pages (wiki, title) VALUES ($1,$2)"))
	if !reflect.DeepEqual(in.Tables, []string{"pages"}) {
		t.Errorf("insert tables = %v", in.Tables)
	}
	in = Analyze(Lex("SELECT * FROM generate_series(1,3) g"))
	if len(in.Tables) != 0 {
		t.Errorf("table func treated as table: %v", in.Tables)
	}
}

func TestFingerprint(t *testing.T) {
	a := Fingerprint(Lex("SELECT * FROM t WHERE id = 42 AND s = 'x'"))
	b := Fingerprint(Lex("select *  from T where ID = $1 and S = 'yy'"))
	if a != b {
		t.Fatalf("%q != %q", a, b)
	}
}

func TestParseSchemaFile(t *testing.T) {
	src, err := os.ReadFile("../../schema/postgres.sql")
	if err != nil {
		t.Fatal(err)
	}
	s := ParseSchema(string(src))
	e := s.Tables["edits"]
	if e == nil {
		t.Fatalf("no edits table; tables=%v", s.Tables)
	}
	if !reflect.DeepEqual(e.PK, []string{"id"}) {
		t.Fatalf("pk = %v", e.PK)
	}
	id := s.Col("edits", "id")
	if id.Type != "bigserial" || !id.Identity {
		t.Fatalf("id = %+v", id)
	}
	if c := s.Col("edits", "user_name"); c.Type != "text" || c.NotNull {
		t.Fatalf("user_name = %+v", c)
	}
	if c := s.Col("pages", "avg_delta"); c.Type != "numeric" || c.TypeRaw != "numeric(14,4)" {
		t.Fatalf("avg_delta = %+v", c)
	}
	if c := s.Col("edits", "growth_ratio"); c.Type != "float8" {
		t.Fatalf("growth_ratio = %+v", c)
	}
	if p := s.Tables["pages"]; !reflect.DeepEqual(p.PK, []string{"wiki", "title"}) {
		t.Fatalf("pages pk = %v", p.PK)
	}
	if !e.IsUniqueSet([]string{"event_id"}) || e.IsUniqueSet([]string{"edit_ts"}) {
		t.Fatal("unique set detection wrong")
	}
	if len(s.Indexes) != 4 {
		t.Fatalf("indexes = %d", len(s.Indexes))
	}
}
