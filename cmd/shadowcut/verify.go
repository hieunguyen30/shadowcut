package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/state"
	"github.com/shadowcut/shadowcut/internal/telemetry"
	"github.com/shadowcut/shadowcut/internal/verify"
)

func verifyCmd() *cobra.Command {
	var pgDSN, spDSN, out string
	var tables []string
	var rate float64
	var interval, grace, maxAge time.Duration
	var rounds, maxRows int
	c := &cobra.Command{
		Use:   "verify",
		Short: "Continuous sampled row-level drift check between Postgres and Spanner",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if rate <= 0 || rate > 1 {
				return fmt.Errorf("--sample must be in (0, 1]")
			}
			meter, shutdown := telemetry.Setup("shadowcut-verify", "")
			defer shutdown(ctx)
			rowsC := telemetry.Must(meter.Int64Counter("shadowcut_verify_rows_read_total"))
			driftC := telemetry.Must(meter.Int64Counter("shadowcut_verify_drift_total"))
			roundH := telemetry.Must(meter.Float64Histogram("shadowcut_verify_round_seconds", metric.WithUnit("s")))

			pg, err := pgutil.Connect(ctx, pgDSN, 4)
			if err != nil {
				return err
			}
			defer pg.Close()
			sp, err := pgutil.Connect(ctx, spDSN, 4)
			if err != nil {
				return err
			}
			defer sp.Close()
			specs, err := verify.DiscoverTables(ctx, pg, sp, tables)
			if err != nil {
				return err
			}
			v := verify.New(pg, sp, verify.Config{Tables: specs, SampleRate: rate, MaxRows: maxRows, LagGrace: grace})
			var sum verify.Summary
			_ = state.ReadJSON(filepath.Join(out, "verify-summary.json"), &sum)
			for i := 0; rounds == 0 || i < rounds; i++ {
				r, err := v.RunRound(ctx)
				if err != nil {
					if ctx.Err() != nil {
						return nil
					}
					slog.Error("verify round failed", "err", err)
				} else {
					sum.Fold(r, maxAge)
					pgN, spN := r.RowsRead()
					rowsC.Add(ctx, int64(pgN), metric.WithAttributes(attribute.String("side", "postgres")))
					rowsC.Add(ctx, int64(spN), metric.WithAttributes(attribute.String("side", "spanner")))
					driftC.Add(ctx, int64(len(r.Drift)))
					roundH.Record(ctx, r.Duration.Seconds())
					if out != "" {
						_ = state.AppendJSONL(filepath.Join(out, "verify-rounds.jsonl"), r)
						_ = state.WriteJSON(filepath.Join(out, "verify-summary.json"), sum)
					}
					var parts []string
					for _, t := range r.Tables {
						parts = append(parts, fmt.Sprintf("%s:%d/%d", t.Table, t.Confirmed, t.SampledPG+t.SampledSP))
					}
					slog.Info("verify round", "drift", len(r.Drift), "rows_pg", pgN, "rows_spanner", spN, "ms", r.Duration.Milliseconds(), "tables(drift/sampled)", strings.Join(parts, " "))
					for _, d := range r.Drift {
						b, _ := json.Marshal(d)
						fmt.Fprintln(os.Stdout, string(b))
					}
				}
				if rounds != 0 && i == rounds-1 {
					break
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(interval):
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&pgDSN, "pg", pgutil.DefaultPG(), "Postgres DSN ($PG_DSN)")
	c.Flags().StringVar(&spDSN, "spanner", pgutil.DefaultSpanner(), "Spanner PGAdapter DSN ($SPANNER_DSN)")
	c.Flags().StringSliceVar(&tables, "tables", []string{"edits", "pages", "gh_events", "probe_events"}, "tables to verify")
	c.Flags().Float64Var(&rate, "sample", 0.05, "fraction of rows sampled per side per round (0,1]")
	c.Flags().IntVar(&maxRows, "max-rows", 0, "cap sampled rows per table per side (0 = none)")
	c.Flags().DurationVar(&interval, "interval", 10*time.Second, "pause between rounds")
	c.Flags().DurationVar(&grace, "lag-grace", 3*time.Second, "wait before re-checking suspects (replication lag bound)")
	c.Flags().DurationVar(&maxAge, "drift-max-age", 30*time.Minute, "how long confirmed drift stays open in the summary")
	c.Flags().IntVar(&rounds, "rounds", 0, "number of rounds (0 = run forever)")
	c.Flags().StringVar(&out, "out", "state", "directory for verify-summary.json / verify-rounds.jsonl")
	return c
}
