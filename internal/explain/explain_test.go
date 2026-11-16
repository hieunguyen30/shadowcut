package explain

import (
	"context"
	"strings"
	"testing"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

func TestRewrites(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT id, concat(user_name, ' @ ', wiki) AS label FROM edits", "concat(coalesce(user_name::text, ''), ' @ ', coalesce(wiki::text, ''))"},
		{"SELECT date_trunc('day', edit_ts) AS d FROM edits", "date_trunc('day', edit_ts, 'UTC')"},
		{"SELECT count(*) FROM edits WHERE wiki = ANY('{enwiki,dewiki}')", "ANY(ARRAY['enwiki', 'dewiki'])"},
		{"SELECT id FROM edits WHERE (edit_ts, id) < ($1, $2) ORDER BY edit_ts DESC", "(edit_ts < $1 OR (edit_ts = $1 AND id < $2))"},
	}
	for _, c := range cases {
		var got string
		for _, f := range []func(string) string{rewriteConcat, rewriteDateTrunc, rewriteAnyLiteral, rewriteRowCompare} {
			if s := f(c.in); s != c.in {
				got = s
			}
		}
		if !strings.Contains(got, c.want) {
			t.Errorf("%q:\n got %q\nwant substring %q", c.in, got, c.want)
		}
	}
	schema := sqlscan.ParseSchema("CREATE TABLE pages (wiki text NOT NULL, title text NOT NULL, edit_count bigint, PRIMARY KEY (wiki, title));")
	got := rewriteTieBreak("SELECT title, edit_count FROM pages ORDER BY edit_count DESC LIMIT 10", schema)
	if !strings.Contains(got, "ORDER BY edit_count DESC, wiki, title\nLIMIT 10") {
		t.Errorf("tie-break: %q", got)
	}
	got = rewriteTieBreak("SELECT id, wiki FROM pages LIMIT 10", schema)
	if !strings.Contains(got, "ORDER BY wiki, title\nLIMIT 10") {
		t.Errorf("tie-break no order: %q", got)
	}
}

func TestMockExplains(t *testing.T) {
	e, err := Mock{}.Explain(context.Background(), Input{SQL: "SELECT concat(a, b) FROM t", Category: classify.NullSemantics, Reason: "label NULL"})
	if err != nil || e.Summary == "" || e.Fix == "" || e.FixedSQL == "" {
		t.Fatalf("%+v %v", e, err)
	}
}
