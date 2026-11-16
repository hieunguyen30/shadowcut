package shadow

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/testenv"
)

// TestProxyIntegration runs real client traffic through the proxy (extended and
// simple protocol, text and binary formats, parameters, NULLs, transactions)
// and checks that (1) the client receives exactly what Postgres returns and
// (2) the shadow comparisons are classified correctly.
func TestProxyIntegration(t *testing.T) {
	env := testenv.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tbl := "pit_" + testenv.Unique()
	ddl := fmt.Sprintf(`CREATE TABLE %s (id bigint NOT NULL, title text NOT NULL, who text, ts timestamptz NOT NULL, PRIMARY KEY (id))`, tbl)
	ins := fmt.Sprintf(`INSERT INTO %s (id, title, who, ts) VALUES
	 (1, 'apple', 'ann', '2024-01-01T10:00:00Z'), (2, 'Banana', NULL, '2024-01-01T23:30:00Z'),
	 (3, '_under', 'bob', '2024-01-02T05:00:00Z'), (4, 'Zebra', NULL, '2024-01-02T12:00:00Z'),
	 (5, 'éclair', 'cy', '2024-01-03T01:00:00Z')`, tbl)
	for _, p := range []func(context.Context, string, ...any) error{
		func(c context.Context, s string, a ...any) error { _, err := env.PG.Exec(c, s, a...); return err },
		func(c context.Context, s string, a ...any) error { _, err := env.SP.Exec(c, s, a...); return err },
	} {
		if err := p(ctx, ddl); err != nil {
			t.Fatal(err)
		}
		if err := p(ctx, ins); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		env.PG.Exec(context.Background(), "DROP TABLE "+tbl)
		env.SP.Exec(context.Background(), "DROP TABLE "+tbl)
	})

	eng := NewEngine(env.PG, env.SP, Config{Workers: 2}, noop.NewMeterProvider().Meter("t"))
	ectx, stopEng := context.WithCancel(ctx)
	defer stopEng()
	go eng.Run(ectx)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(env.PGDSN)
	p := &Proxy{Upstream: u.Host, Engine: eng}
	go p.ServeListener(ectx, ln)
	u.Host = ln.Addr().String()
	proxyDSN := u.String()

	queries := []struct {
		sql  string
		args []any
		want classify.Category // "" = should match
	}{
		{fmt.Sprintf("SELECT id, title FROM %s ORDER BY title", tbl), nil, classify.Collation},
		{fmt.Sprintf("SELECT id, concat(who, '@x') AS label FROM %s ORDER BY id", tbl), nil, classify.NullSemantics},
		{fmt.Sprintf("SELECT id, title, who, ts FROM %s WHERE id > $1 ORDER BY id", tbl), []any{int64(1)}, ""},
		{fmt.Sprintf("SELECT date_trunc('day', ts) AS d, count(*) AS n FROM %s GROUP BY 1 ORDER BY 1", tbl), nil, classify.Timezone},
		{fmt.Sprintf("SELECT count(*) FROM %s WHERE who = $1", tbl), []any{"ann"}, ""},
	}
	for _, mode := range []pgx.QueryExecMode{pgx.QueryExecModeCacheStatement, pgx.QueryExecModeSimpleProtocol, pgx.QueryExecModeExec} {
		cfg, err := pgx.ParseConfig(proxyDSN)
		if err != nil {
			t.Fatal(err)
		}
		cfg.DefaultQueryExecMode = mode
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect via proxy (%v): %v", mode, err)
		}
		for _, q := range queries {
			got := collectConn(t, ctx, conn, q.sql, q.args)
			want := collectConn(t, ctx, mustConn(t, ctx, env.PGDSN), q.sql, q.args)
			if got != want {
				t.Fatalf("mode %v: proxy changed the result of %q:\n got %s\nwant %s", mode, q.sql, got, want)
			}
		}
		// inside a transaction that has written: classified as isolation, never blocks
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf("INSERT INTO %s (id, title, ts) VALUES (100, 'tx row', now())", tbl)); err != nil {
			t.Fatal(err)
		}
		var n int64
		if err := tx.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %s", tbl)).Scan(&n); err != nil || n != 6 {
			t.Fatalf("in-txn read through proxy: n=%d err=%v", n, err)
		}
		tx.Rollback(ctx)
		conn.Close(ctx)
	}

	// wait for the shadow comparisons
	deadline := time.Now().Add(60 * time.Second)
	want := int64(3 * (len(queries) + 1))
	for eng.Summary().Compared < want && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	s := eng.Summary()
	if s.Compared < want {
		t.Fatalf("compared %d, want %d", s.Compared, want)
	}
	for _, q := range s.Queries {
		t.Logf("%-70.70s compared=%d mismatches=%v", q.Example, q.Compared, q.ByCategory)
	}
	for _, q := range queries {
		found := false
		for _, qs := range s.Queries {
			if qs.Example != q.sql {
				continue
			}
			found = true
			if q.want == "" && qs.Mismatches != 0 {
				t.Errorf("%q: unexpected mismatches %v", q.sql, qs.ByCategory)
			}
			if q.want != "" && qs.ByCategory[q.want] != qs.Compared {
				t.Errorf("%q: want every comparison classified %s, got %v of %d", q.sql, q.want, qs.ByCategory, qs.Compared)
			}
		}
		if !found {
			t.Errorf("%q was never shadowed", q.sql)
		}
	}
	if s.ByCategory[classify.Isolation] != 3 {
		t.Errorf("in-transaction read after a write: want 3 isolation, got %v", s.ByCategory)
	}
}

func mustConn(t *testing.T, ctx context.Context, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close(context.Background()) })
	return c
}

func collectConn(t *testing.T, ctx context.Context, c *pgx.Conn, sql string, args []any) string {
	t.Helper()
	rows, err := c.Query(ctx, sql, args...)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatal(err)
		}
		var r rowset.Row
		for _, v := range vals {
			r = append(r, rowset.Normalize(v))
		}
		out = append(out, rowset.RowKey(r))
	}
	return fmt.Sprint(out)
}
