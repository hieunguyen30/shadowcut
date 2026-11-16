package main

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/shadowcut/shadowcut/internal/app"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/state"
)

type isoResult struct {
	Variant      string  `json:"variant"`
	Workers      int     `json:"workers"`
	OpsPerWorker int     `json:"ops_per_worker"`
	Succeeded    int64   `json:"succeeded"`
	Failed       int64   `json:"failed"`
	Retries      int64   `json:"retries"`
	Expected     int64   `json:"expected_increment"`
	Actual       int64   `json:"actual_increment"`
	LostUpdates  int64   `json:"lost_updates"`
	Seconds      float64 `json:"seconds"`
	ErrorSample  string  `json:"error_sample,omitempty"`
}

func isolationCmd() *cobra.Command {
	var workers, ops int
	var spDSN string
	var out string
	c := &cobra.Command{
		Use:   "isolation",
		Short: "Lost updates from read-modify-write transactions: Postgres READ COMMITTED vs Spanner serializable",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pg, err := pgutil.Connect(ctx, pgutil.DefaultPG(), int32(workers+4))
			if err != nil {
				return err
			}
			defer pg.Close()
			sp, err := pgutil.Connect(ctx, spDSN, int32(workers+4))
			if err != nil {
				return err
			}
			defer sp.Close()
			title := fmt.Sprintf("Isolation probe %d", time.Now().Unix())
			// The Spanner variants use a separate emulator database (PGAdapter creates it)
			// so they never compete with the CDC applier for the emulator's single
			// read-write transaction slot (docs/BUGS.md BUG-009). Both sides are seeded directly.
			for _, ddl := range []string{
				`CREATE TABLE pages (wiki varchar(64) NOT NULL, title text NOT NULL, edit_count bigint NOT NULL, bytes bigint, avg_delta numeric, last_editor text, last_edit_ts timestamptz NOT NULL, PRIMARY KEY (wiki, title))`,
				`CREATE TABLE sc_reverse_journal (id varchar(36) NOT NULL, tbl varchar(64) NOT NULL, row_data jsonb NOT NULL, created_at spanner.commit_timestamp NOT NULL, PRIMARY KEY (id))`,
			} {
				if _, err := sp.Exec(ctx, ddl); err != nil && !strings.Contains(err.Error(), "exists") && !strings.Contains(err.Error(), "uplicate") {
					return fmt.Errorf("isolation db: %w", err)
				}
			}
			for _, p := range []*pgxpool.Pool{pg, sp} {
				if _, err := p.Exec(ctx, `INSERT INTO pages (wiki, title, edit_count, last_edit_ts) VALUES ('isowiki', $1, 0, now())`, title); err != nil {
					return fmt.Errorf("seed: %w", err)
				}
			}
			var results []isoResult
			for _, v := range []struct {
				name    string
				mode    app.Mode
				noRetry bool
			}{{"postgres_read_committed", app.ModePostgres, false}, {"spanner_with_retry", app.ModeSpanner, false}, {"spanner_no_retry", app.ModeSpanner, true}} {
				srv := app.New(pg, pg, sp, noop.NewMeterProvider().Meter("iso"))
				srv.SetMode(v.mode)
				srv.NoRetry = v.noRetry
				pool := pg
				if v.mode == app.ModeSpanner {
					pool = sp
				}
				var before int64
				if err := pool.QueryRow(ctx, `SELECT edit_count FROM pages WHERE wiki = 'isowiki' AND title = $1`, title).Scan(&before); err != nil {
					return err
				}
				var ok, failed atomic.Int64
				var errSample atomic.Value
				start := time.Now()
				var wg sync.WaitGroup
				for w := 0; w < workers; w++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						for i := 0; i < ops; i++ {
							if _, err := srv.TouchPage(ctx, "isowiki", title); err != nil {
								failed.Add(1)
								errSample.Store(shadow.ErrText(err))
							} else {
								ok.Add(1)
							}
						}
					}()
				}
				wg.Wait()
				var after int64
				if err := pool.QueryRow(ctx, `SELECT edit_count FROM pages WHERE wiki = 'isowiki' AND title = $1`, title).Scan(&after); err != nil {
					return err
				}
				r := isoResult{Variant: v.name, Workers: workers, OpsPerWorker: ops, Succeeded: ok.Load(), Failed: failed.Load(), Retries: srv.RetryCount.Load(),
					Expected: ok.Load(), Actual: after - before, Seconds: time.Since(start).Seconds()}
				r.LostUpdates = r.Expected - r.Actual
				if s, ok := errSample.Load().(string); ok {
					r.ErrorSample = s
				}
				results = append(results, r)
				slog.Info("isolation", "variant", r.Variant, "succeeded", r.Succeeded, "failed", r.Failed, "retries", r.Retries, "lost_updates", r.LostUpdates, "seconds", fmt.Sprintf("%.1f", r.Seconds))
			}
			// Spanner-mode TouchPage journals rows; clean the probe rows from the journal
			// so a later rollback does not resurrect them into Postgres needlessly.
			_, _ = sp.Exec(ctx, `DELETE FROM sc_reverse_journal WHERE tbl = 'pages' AND row_data->>'wiki' = 'isowiki'`)
			os.MkdirAll(out, 0o755)
			if err := state.WriteJSON(filepath.Join(out, "results.json"), map[string]any{"env": envLabel, "results": results}); err != nil {
				return err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%d concurrent workers x %d read-modify-write increments of one row (`touch_page`). %s.\n\n", workers, ops, envLabel)
			b.WriteString("| variant | succeeded | failed | retries | expected increment | actual increment | lost updates | seconds |\n|---|---|---|---|---|---|---|---|\n")
			for _, r := range results {
				fmt.Fprintf(&b, "| %s | %d | %d | %d | %d | %d | %d | %.1f |\n", r.Variant, r.Succeeded, r.Failed, r.Retries, r.Expected, r.Actual, r.LostUpdates, r.Seconds)
			}
			for _, r := range results {
				if r.ErrorSample != "" {
					fmt.Fprintf(&b, "\nError sample (%s): `%s`\n", r.Variant, r.ErrorSample)
				}
			}
			return os.WriteFile(filepath.Join(out, "summary.md"), []byte(b.String()), 0o644)
		},
	}
	c.Flags().IntVar(&workers, "workers", 16, "concurrent workers")
	c.Flags().StringVar(&spDSN, "spanner", "postgres://localhost:15433/isolation?sslmode=disable", "Spanner (PGAdapter) DSN; a separate database avoids contention with the CDC applier")
	c.Flags().IntVar(&ops, "ops", 25, "increments per worker")
	c.Flags().StringVar(&out, "out", "results/isolation", "output directory")
	return c
}
