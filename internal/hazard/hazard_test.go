package hazard

import (
	"os"
	"strings"
	"testing"

	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

const testSchema = `
CREATE TABLE edits (
  id bigserial PRIMARY KEY,
  event_id uuid NOT NULL UNIQUE,
  wiki varchar(64) NOT NULL,
  page_title text NOT NULL,
  user_name text,
  growth_ratio double precision,
  edit_ts timestamptz NOT NULL
);
CREATE TABLE pages (wiki varchar(64) NOT NULL, title text NOT NULL, edit_count bigint NOT NULL, avg_delta numeric(14,4), PRIMARY KEY (wiki, title));
`

func scanOne(t *testing.T, sql string) []Finding {
	t.Helper()
	s := New(sqlscan.ParseSchema(testSchema))
	rep := s.Scan(nil, []Input{{File: "q.sql", SQL: sql}})
	return rep.Findings
}

func rules(fs []Finding) map[string]bool {
	m := map[string]bool{}
	for _, f := range fs {
		m[f.Rule] = true
	}
	return m
}

func TestQueryRules(t *testing.T) {
	cases := []struct {
		sql     string
		want    []string
		wantNot []string
	}{
		{"SELECT title FROM pages WHERE wiki = $1 ORDER BY title LIMIT 10", []string{"Q-COLLATION-ORDER"}, []string{"Q-ORDER-TIES", "Q-NULL-ORDER"}},
		{"SELECT lower(title) AS t FROM pages ORDER BY t", []string{"Q-COLLATION-ORDER"}, nil},
		{"SELECT length(title) AS n FROM pages ORDER BY n", nil, []string{"Q-COLLATION-ORDER"}},
		{"SELECT title, edit_count FROM pages ORDER BY edit_count DESC LIMIT 5", []string{"Q-ORDER-TIES"}, []string{"Q-COLLATION-ORDER"}},
		{"SELECT id FROM edits ORDER BY edit_ts DESC, id DESC LIMIT 5", nil, []string{"Q-ORDER-TIES"}},
		{"SELECT id FROM edits LIMIT 5", []string{"Q-LIMIT-NO-ORDER"}, nil},
		{"SELECT user_name FROM edits ORDER BY user_name NULLS LAST, id", []string{"Q-COLLATION-ORDER"}, []string{"Q-NULL-ORDER"}},
		{"SELECT id FROM edits ORDER BY user_name, id", []string{"Q-NULL-ORDER"}, nil},
		{"SELECT concat(user_name, wiki) FROM edits", []string{"Q-CONCAT-NULL"}, nil},
		{"SELECT concat(wiki, page_title) FROM edits", nil, []string{"Q-CONCAT-NULL"}},
		{"SELECT date_trunc('day', edit_ts), count(*) FROM edits GROUP BY 1 ORDER BY 1", []string{"Q-TZ-DEFAULT"}, []string{"Q-ORDER-TIES"}},
		{"SELECT date_trunc('day', edit_ts, 'UTC') FROM edits", nil, []string{"Q-TZ-DEFAULT"}},
		{"SELECT extract(hour FROM edit_ts) FROM edits", []string{"Q-TZ-DEFAULT"}, nil},
		{"SELECT edit_ts::text FROM edits", []string{"Q-TZ-DEFAULT"}, nil},
		{"SELECT edit_ts + interval '1 day' FROM edits", []string{"Q-TZ-DEFAULT"}, nil},
		{"SELECT avg(growth_ratio) FROM edits", []string{"Q-FLOAT-AGG"}, nil},
		{"SELECT max(title) FROM pages", []string{"Q-COLLATION-COMPARE"}, nil},
		{"SELECT count(*) FROM pages WHERE title > 'M'", []string{"Q-COLLATION-COMPARE"}, nil},
		{"SELECT count(*) FROM pages WHERE edit_count > 3", nil, []string{"Q-COLLATION-COMPARE"}},
		{"SELECT DISTINCT ON (wiki) wiki FROM edits ORDER BY wiki", []string{"Q-UNSUP-SYNTAX"}, nil},
		{"SELECT id FROM edits WHERE (edit_ts, id) < ($1, $2)", []string{"Q-UNSUP-SYNTAX"}, nil},
		{"SELECT id FROM edits WHERE wiki = ANY('{a,b}')", []string{"Q-UNSUP-SYNTAX"}, nil},
		{"SELECT id FROM edits WHERE wiki = ANY(ARRAY['a'])", nil, []string{"Q-UNSUP-SYNTAX"}},
		{"SELECT row_number() OVER (ORDER BY id) FROM edits", []string{"Q-UNSUP-SYNTAX"}, nil},
		{"WITH RECURSIVE r(i) AS (SELECT 1) SELECT * FROM r", []string{"Q-UNSUP-SYNTAX"}, nil},
		{"SELECT generate_series(1, 3)", []string{"Q-UNSUP-SYNTAX"}, nil},
		{"SELECT * FROM generate_series(1, 3) g", nil, []string{"Q-UNSUP-SYNTAX"}},
		{"SELECT concat_ws('-', wiki, page_title) FROM edits", []string{"Q-UNSUP-FUNC"}, nil},
		{"SELECT round(avg_delta, 1) FROM pages", []string{"Q-UNSUP-FUNC"}, nil},
		{"SELECT round(growth_ratio) FROM edits", []string{"Q-FUNC-SEMANTICS"}, []string{"Q-UNSUP-FUNC"}},
		{"SELECT upper(page_title) FROM edits", []string{"Q-FUNC-SEMANTICS"}, nil},
		{"SELECT id FROM edits WHERE page_title ILIKE 'a%'", []string{"Q-ILIKE"}, nil},
		{"SELECT id FROM edits FOR UPDATE", []string{"Q-UNSUP-SYNTAX"}, nil},
		{"SAVEPOINT a", []string{"Q-UNSUP-TXN"}, nil},
	}
	for _, c := range cases {
		got := rules(scanOne(t, c.sql))
		for _, w := range c.want {
			if !got[w] {
				t.Errorf("%q: missing %s (got %v)", c.sql, w, got)
			}
		}
		for _, w := range c.wantNot {
			if got[w] {
				t.Errorf("%q: unexpected %s", c.sql, w)
			}
		}
	}
}

func TestIsolationRules(t *testing.T) {
	got := rules(scanOne(t, "BEGIN; SELECT edit_count FROM pages WHERE wiki = $1; UPDATE pages SET edit_count = $2 WHERE wiki = $1; COMMIT;"))
	if !got["Q-ISOLATION-RMW"] {
		t.Fatalf("missing RMW: %v", got)
	}
	got = rules(scanOne(t, "BEGIN; SELECT edit_count FROM pages WHERE wiki = $1 FOR UPDATE; UPDATE pages SET edit_count = $2 WHERE wiki = $1; COMMIT;"))
	if got["Q-ISOLATION-RMW"] {
		t.Fatalf("FOR UPDATE should not trigger RMW")
	}
	got = rules(scanOne(t, "-- txn: x\nSELECT edit_count FROM pages;\n-- txn: x\nUPDATE pages SET edit_count = 1;"))
	if !got["Q-ISOLATION-RMW"] {
		t.Fatalf("txn annotation: missing RMW: %v", got)
	}
	got = rules(scanOne(t, "SET TRANSACTION ISOLATION LEVEL READ COMMITTED"))
	if !got["Q-ISOLATION-LEVEL"] {
		t.Fatalf("missing isolation level: %v", got)
	}
}

func TestSchemaRules(t *testing.T) {
	s := New(nil)
	rep := s.Scan([]Input{{File: "s.sql", SQL: testSchema + `
CREATE INDEX edits_ts ON edits (edit_ts);
CREATE INDEX edits_p ON edits (page_title) WHERE user_name IS NULL;
CREATE TABLE ev (ts timestamptz PRIMARY KEY, n int, at timestamp, m money);
CREATE TRIGGER t AFTER INSERT ON ev FOR EACH ROW EXECUTE FUNCTION f();
CREATE TABLE k (n numeric PRIMARY KEY);`}}, nil)
	got := rules(rep.Findings)
	for _, w := range []string{"SC-SEQ-KEY", "SC-MONO-INDEX", "SC-NUMERIC-TYPMOD", "SC-UNSUP-DDL", "SC-MONO-TS-KEY", "SC-INT4", "SC-UNSUP-TYPE", "SC-KEY-TYPE"} {
		if !got[w] {
			t.Errorf("missing %s; got %v", w, got)
		}
	}
}

// BUG-002 regression: Spanner rejects identifiers starting with "_".
func TestRuleIdentifierUnderscore(t *testing.T) {
	rep := New(nil).Scan([]Input{{File: "s.sql", SQL: "CREATE TABLE _sc_offsets (id bigint PRIMARY KEY, _x text);"}}, nil)
	n := 0
	for _, f := range rep.Findings {
		if f.Rule == "SC-IDENT-UNDERSCORE" {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 underscore findings, got %d: %+v", n, rep.Findings)
	}
}

func TestWaivers(t *testing.T) {
	rep := New(sqlscan.ParseSchema(testSchema)).Scan(nil, []Input{{File: "q.sql", SQL: "-- name: a\nSELECT concat(user_name, wiki) FROM edits;"}})
	if rep.Unresolved(High) != 1 {
		t.Fatalf("want 1 unresolved high, got %d", rep.Unresolved(High))
	}
	rep.ApplyWaivers([]Waiver{{Rule: "Q-CONCAT-NULL", Query: "a", Reason: "fixed in app"}})
	if rep.Unresolved(High) != 0 {
		t.Fatalf("waiver not applied")
	}
}

func TestCatalogHasSources(t *testing.T) {
	for _, r := range Catalog {
		if len(r.Sources) == 0 || r.Explain == "" || r.Fix == "" {
			t.Errorf("rule %s lacks source/explain/fix", r.ID)
		}
	}
}

// TestCatalogDocumented keeps docs/HAZARDS.md in sync with the catalog.
func TestCatalogDocumented(t *testing.T) {
	b, err := os.ReadFile("../../docs/HAZARDS.md")
	if err != nil {
		t.Skip("docs/HAZARDS.md not present")
	}
	for _, r := range Catalog {
		if !strings.Contains(string(b), r.ID) {
			t.Errorf("docs/HAZARDS.md does not document %s", r.ID)
		}
	}
}

func TestEvidenceLoaded(t *testing.T) {
	ev := LoadEvidence()
	if len(ev.Funcs) < 50 {
		t.Fatalf("evidence has %d functions", len(ev.Funcs))
	}
	if !ev.Funcs["concat_ws"].Unsupported() || !ev.Funcs["concat"].Differs() {
		t.Fatalf("unexpected evidence for concat/concat_ws")
	}
}

func TestOrderBySequentialKey(t *testing.T) {
	got := rules(scanOne(t, "SELECT id, wiki FROM edits ORDER BY id DESC LIMIT 50"))
	if !got["Q-ORDER-BY-SEQ-KEY"] {
		t.Fatalf("missing Q-ORDER-BY-SEQ-KEY: %v", got)
	}
	got = rules(scanOne(t, "SELECT id FROM edits ORDER BY edit_ts DESC, id DESC LIMIT 50"))
	if got["Q-ORDER-BY-SEQ-KEY"] {
		t.Fatal("tiebreaker use of the key should not trigger")
	}
}
