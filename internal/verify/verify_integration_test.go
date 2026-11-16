package verify

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/shadowcut/shadowcut/internal/testenv"
)

// TestVerifyIntegration injects drift of each kind into Spanner and checks that a
// full-sample round finds exactly those rows, including on a two-column key with
// more rows than one lookup chunk (BUG-001).
func TestVerifyIntegration(t *testing.T) {
	env := testenv.Start(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	tbl := "vit_" + testenv.Unique()
	ddl := fmt.Sprintf(`CREATE TABLE %s (a varchar(20) NOT NULL, b bigint NOT NULL, v text, n numeric, PRIMARY KEY (a, b))`, tbl)
	if _, err := env.PG.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	if _, err := env.SP.Exec(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		env.PG.Exec(context.Background(), "DROP TABLE "+tbl)
		env.SP.Exec(context.Background(), "DROP TABLE "+tbl)
	})
	const rows = 600 // > ChunkSize(2) = 225
	for _, p := range []string{"pg", "sp"} {
		pool := env.PG
		if p == "sp" {
			pool = env.SP
		}
		for i := 0; i < rows; i += 100 {
			sql := fmt.Sprintf("INSERT INTO %s (a, b, v, n) VALUES ", tbl)
			var args []any
			for j := i; j < i+100; j++ {
				if j > i {
					sql += ", "
				}
				sql += fmt.Sprintf("($%d, $%d, $%d, $%d)", len(args)+1, len(args)+2, len(args)+3, len(args)+4)
				args = append(args, fmt.Sprintf("k%d", j%7), int64(j), fmt.Sprintf("value %d", j), fmt.Sprintf("%d.50", j))
			}
			if _, err := pool.Exec(ctx, sql, args...); err != nil {
				t.Fatalf("%s insert: %v", p, err)
			}
		}
	}
	// inject drift on the Spanner side
	mustExec(t, env.SP.Exec, ctx, fmt.Sprintf("UPDATE %s SET v = 'drifted' WHERE a = 'k3' AND b = 10", tbl))
	mustExec(t, env.SP.Exec, ctx, fmt.Sprintf("DELETE FROM %s WHERE a = 'k5' AND b = 12", tbl))
	mustExec(t, env.SP.Exec, ctx, fmt.Sprintf("INSERT INTO %s (a, b, v, n) VALUES ('zz', 999999, 'extra', 1)", tbl))
	mustExec(t, env.SP.Exec, ctx, fmt.Sprintf("UPDATE %s SET n = 100.5 WHERE a = 'k0' AND b = 14", tbl))

	tables, err := DiscoverTables(ctx, env.PG, env.SP, []string{tbl})
	if err != nil {
		t.Fatal(err)
	}
	v := New(env.PG, env.SP, Config{Tables: tables, SampleRate: 1, LagGrace: 200 * time.Millisecond, Rechecks: 1})
	r, err := v.RunRound(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range r.Drift {
		got = append(got, d.Kind+":"+d.Key)
	}
	sort.Strings(got)
	if len(got) != 4 {
		t.Fatalf("want 4 drift rows, got %v", got)
	}
	kinds := map[string]int{}
	for _, d := range r.Drift {
		kinds[d.Kind]++
	}
	if kinds["value_diff"] != 2 || kinds["missing_in_spanner"] != 1 || kinds["extra_in_spanner"] != 1 {
		t.Fatalf("kinds = %v (%v)", kinds, got)
	}
	pgN, spN := r.RowsRead()
	t.Logf("drift=%v rows_read pg=%d spanner=%d", got, pgN, spN)
}

func mustExec[T any](t *testing.T, f func(context.Context, string, ...any) (T, error), ctx context.Context, sql string) {
	t.Helper()
	if _, err := f(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}
