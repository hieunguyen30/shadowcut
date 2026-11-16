package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/app"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/state"
)

// Experiment 3: write throughput/latency with sequential ids vs bit-reversed
// sequences vs UUIDv4 under identical load, plus "what breaks when keys change".
// Only meaningful on REAL Spanner (the emulator has no splits or hotspots); the
// local run is a functional smoke test of the harness.
type exp3Result struct {
	Strategy      string  `json:"strategy"`
	Env           string  `json:"env"`
	Writers       int     `json:"writers"`
	Seconds       float64 `json:"seconds"`
	Commits       int64   `json:"commits"`
	Errors        int64   `json:"errors"`
	Retries       int64   `json:"retries"`
	CommitsPerSec float64 `json:"commits_per_sec"`
	P50Ms         float64 `json:"p50_ms"`
	P95Ms         float64 `json:"p95_ms"`
	P99Ms         float64 `json:"p99_ms"`
	RecencyByIDOK float64 `json:"recency_by_id_correct"` // share of ORDER BY id DESC LIMIT 20 that are truly the newest rows
	ErrSample     string  `json:"error_sample,omitempty"`
}

func exp3Cmd() *cobra.Command {
	var dsn, out, env string
	var writers int
	var dur time.Duration
	c := &cobra.Command{
		Use:   "exp3",
		Short: "Experiment 3 (real Spanner): hotspotting with sequential vs bit-reversed vs UUID keys",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pool, err := pgutil.Connect(ctx, dsn, int32(writers+4))
			if err != nil {
				return err
			}
			defer pool.Close()
			suffix := fmt.Sprint(time.Now().Unix() % 100000)
			ddl := []string{
				fmt.Sprintf(`CREATE TABLE hs_seq_%s (id bigint NOT NULL, payload text, created_at timestamptz NOT NULL, PRIMARY KEY (id))`, suffix),
				fmt.Sprintf(`CREATE SEQUENCE hs_bitrev_seq_%s bit_reversed_positive`, suffix),
				fmt.Sprintf(`CREATE TABLE hs_bitrev_%s (id bigint NOT NULL DEFAULT nextval('hs_bitrev_seq_%s'), payload text, created_at timestamptz NOT NULL, PRIMARY KEY (id))`, suffix, suffix),
				fmt.Sprintf(`CREATE TABLE hs_uuid_%s (id varchar(36) NOT NULL, payload text, created_at timestamptz NOT NULL, PRIMARY KEY (id))`, suffix),
			}
			for _, d := range ddl {
				if _, err := pool.Exec(ctx, d); err != nil {
					return fmt.Errorf("%s: %w", d, err)
				}
			}
			var seq atomic.Int64
			payload := strings.Repeat("x", 200)
			strategies := []struct {
				name, table string
				insert      func(ctx context.Context, tx pgx.Tx) error
			}{
				{"sequential", "hs_seq_" + suffix, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO hs_seq_%s (id, payload, created_at) VALUES ($1, $2, now())`, suffix), seq.Add(1), payload)
					return err
				}},
				{"bit_reversed_sequence", "hs_bitrev_" + suffix, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO hs_bitrev_%s (payload, created_at) VALUES ($1, now())`, suffix), payload)
					return err
				}},
				{"uuid_v4", "hs_uuid_" + suffix, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO hs_uuid_%s (id, payload, created_at) VALUES ($1, $2, now())`, suffix), uuid.NewString(), payload)
					return err
				}},
			}
			var results []exp3Result
			for _, s := range strategies {
				r := runHotspot(ctx, pool, s.name, s.insert, writers, dur)
				r.Env = env
				r.RecencyByIDOK = recencyByID(ctx, pool, s.table)
				results = append(results, r)
				slog.Info("exp3", "strategy", r.Strategy, "commits_per_sec", fmt.Sprintf("%.1f", r.CommitsPerSec), "p99_ms", fmt.Sprintf("%.1f", r.P99Ms), "recency_by_id_correct", r.RecencyByIDOK)
			}
			os.MkdirAll(out, 0o755)
			if err := state.WriteJSON(filepath.Join(out, "results.json"), results); err != nil {
				return err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "%s; %d writers x %s per strategy, one row per transaction.\n\n", env, writers, dur)
			b.WriteString("| key strategy | commits/s | p50 ms | p95 ms | p99 ms | errors | retries | ORDER BY id DESC returns newest rows |\n|---|---|---|---|---|---|---|---|\n")
			for _, r := range results {
				fmt.Fprintf(&b, "| %s | %.1f | %.1f | %.1f | %.1f | %d | %d | %.0f%% |\n", r.Strategy, r.CommitsPerSec, r.P50Ms, r.P95Ms, r.P99Ms, r.Errors, r.Retries, 100*r.RecencyByIDOK)
			}
			return os.WriteFile(filepath.Join(out, "summary.md"), []byte(b.String()), 0o644)
		},
	}
	c.Flags().StringVar(&dsn, "dsn", pgutil.DefaultSpanner(), "PGAdapter DSN of the target Spanner database")
	c.Flags().IntVar(&writers, "writers", 32, "concurrent writers")
	c.Flags().DurationVar(&dur, "duration", 2*time.Minute, "duration per strategy")
	c.Flags().StringVar(&out, "out", "results/exp3", "output dir")
	c.Flags().StringVar(&env, "env", "REAL Spanner (set by the cloud job)", "environment label recorded with the results")
	return c
}

func runHotspot(ctx context.Context, pool *pgxpool.Pool, name string, insert func(context.Context, pgx.Tx) error, writers int, dur time.Duration) exp3Result {
	r := exp3Result{Strategy: name, Writers: writers}
	var commits, errs, retries atomic.Int64
	var mu sync.Mutex
	var lat []float64
	var errSample atomic.Value
	ctx2, cancel := context.WithTimeout(ctx, dur)
	defer cancel()
	start := time.Now()
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx2.Err() == nil {
				t0 := time.Now()
				var err error
				for attempt := 0; attempt < 5; attempt++ {
					err = pgx.BeginFunc(ctx2, pool, func(tx pgx.Tx) error { return insert(ctx2, tx) })
					if err == nil || !app.IsRetryable(err) {
						break
					}
					retries.Add(1)
				}
				if ctx2.Err() != nil {
					return
				}
				if err != nil {
					errs.Add(1)
					errSample.Store(err.Error())
					continue
				}
				commits.Add(1)
				mu.Lock()
				lat = append(lat, float64(time.Since(t0).Microseconds())/1000)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	r.Seconds = time.Since(start).Seconds()
	r.Commits, r.Errors, r.Retries = commits.Load(), errs.Load(), retries.Load()
	r.CommitsPerSec = float64(r.Commits) / r.Seconds
	sort.Float64s(lat)
	pct := func(p float64) float64 {
		if len(lat) == 0 {
			return 0
		}
		return lat[int(p*float64(len(lat)-1))]
	}
	r.P50Ms, r.P95Ms, r.P99Ms = pct(0.5), pct(0.95), pct(0.99)
	if s, ok := errSample.Load().(string); ok {
		r.ErrSample = s
	}
	return r
}

// recencyByID measures whether "newest rows by id" still means newest rows.
func recencyByID(ctx context.Context, pool *pgxpool.Pool, table string) float64 {
	byID := map[string]bool{}
	rows, err := pool.Query(ctx, fmt.Sprintf(`SELECT id::text FROM %s ORDER BY id DESC LIMIT 20`, table))
	if err != nil {
		return -1
	}
	for rows.Next() {
		var id string
		rows.Scan(&id)
		byID[id] = true
	}
	rows.Close()
	rows, err = pool.Query(ctx, fmt.Sprintf(`SELECT id::text FROM %s ORDER BY created_at DESC LIMIT 20`, table))
	if err != nil {
		return -1
	}
	hit, n := 0, 0
	for rows.Next() {
		var id string
		rows.Scan(&id)
		n++
		if byID[id] {
			hit++
		}
	}
	rows.Close()
	if n == 0 {
		return -1
	}
	return float64(hit) / float64(n)
}
