package main

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/chart"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/state"
	"github.com/shadowcut/shadowcut/internal/verify"
)

type exp5Round struct {
	Rate       float64 `json:"rate"`
	Trial      int     `json:"trial"`
	Round      int     `json:"round"`
	ElapsedS   float64 `json:"elapsed_s"`
	RoundMs    int64   `json:"round_ms"`
	RowsPG     int     `json:"rows_pg"`
	RowsSP     int     `json:"rows_spanner"`
	NewFound   int     `json:"new_found"`
	TotalFound int     `json:"total_found"`
	Suspects   int     `json:"suspects"`
}

type exp5Rate struct {
	Rate           float64 `json:"rate"`
	Trials         int     `json:"trials"`
	Injected       int     `json:"injected_per_trial"`
	FirstDetectS   float64 `json:"first_detection_s_median"`
	AllDetectS     float64 `json:"all_detected_s_median"`
	Rounds         float64 `json:"rounds_to_all_median"`
	RowsReadToAll  float64 `json:"rows_read_to_all_median"`
	RowsPerRound   float64 `json:"rows_read_per_round_mean"`
	RoundMs        float64 `json:"round_ms_mean"`
	ExpectedRounds float64 `json:"expected_rounds_for_one_row"`
	FalseDrift     int     `json:"false_drift_reports"`
	Complete       int     `json:"trials_all_detected"`
}

func exp5Cmd() *cobra.Command {
	var rates []float64
	var trials, inject, maxRounds int
	var table, out string
	var maxTime time.Duration
	c := &cobra.Command{
		Use:   "exp5",
		Short: "Experiment 5: verification cost vs detection speed for injected drift",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pg, err := pgutil.Connect(ctx, pgutil.DefaultPG(), 4)
			if err != nil {
				return err
			}
			defer pg.Close()
			sp, err := pgutil.Connect(ctx, pgutil.DefaultSpanner(), 4)
			if err != nil {
				return err
			}
			defer sp.Close()
			specs, err := verify.DiscoverTables(ctx, pg, sp, []string{table})
			if err != nil {
				return err
			}
			var size int64
			_ = pg.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&size)
			os.MkdirAll(out, 0o755)
			rawPath := filepath.Join(out, "rounds.jsonl")
			os.Remove(rawPath)
			var summary []exp5Rate
			for _, rate := range rates {
				agg := exp5Rate{Rate: rate, Trials: trials, Injected: inject, ExpectedRounds: 1 / (1 - math.Pow(1-rate, 2))}
				var firsts, alls, rounds, rows []float64
				var perRound, roundMs []float64
				for trial := 1; trial <= trials; trial++ {
					keys, restore, err := injectDrift(ctx, pg, sp, table, inject)
					if err != nil {
						return err
					}
					v := verify.New(pg, sp, verify.Config{Tables: specs, SampleRate: rate, LagGrace: time.Second, Rechecks: 2})
					found := map[string]bool{}
					start := time.Now()
					first := -1.0
					totalRows := 0
					var r int
					for r = 1; r <= maxRounds && len(found) < len(keys) && time.Since(start) < maxTime; r++ {
						rd, err := v.RunRound(ctx)
						if err != nil {
							restore()
							return err
						}
						pgN, spN := rd.RowsRead()
						totalRows += pgN + spN
						perRound = append(perRound, float64(pgN+spN))
						roundMs = append(roundMs, float64(rd.Duration.Milliseconds()))
						newF := 0
						for _, d := range rd.Drift {
							if keys[d.Key] {
								if !found[d.Key] {
									found[d.Key] = true
									newF++
								}
							} else {
								agg.FalseDrift++
								slog.Warn("drift outside the injected set", "key", d.Key, "kind", d.Kind, "cols", d.Columns)
							}
						}
						if newF > 0 && first < 0 {
							first = time.Since(start).Seconds()
						}
						sus := 0
						for _, t := range rd.Tables {
							sus += t.Suspects
						}
						_ = state.AppendJSONL(rawPath, exp5Round{Rate: rate, Trial: trial, Round: r, ElapsedS: time.Since(start).Seconds(), RoundMs: rd.Duration.Milliseconds(),
							RowsPG: pgN, RowsSP: spN, NewFound: newF, TotalFound: len(found), Suspects: sus})
					}
					restore()
					if len(found) == len(keys) {
						agg.Complete++
						alls = append(alls, time.Since(start).Seconds())
						rounds = append(rounds, float64(r-1))
						rows = append(rows, float64(totalRows))
					}
					if first >= 0 {
						firsts = append(firsts, first)
					}
					slog.Info("exp5 trial", "rate", rate, "trial", trial, "found", len(found), "of", len(keys), "rounds", r-1, "elapsed", time.Since(start).Round(time.Millisecond), "rows_read", totalRows)
				}
				agg.FirstDetectS, agg.AllDetectS, agg.Rounds, agg.RowsReadToAll = median(firsts), median(alls), median(rounds), median(rows)
				agg.RowsPerRound, agg.RoundMs = mean(perRound), mean(roundMs)
				summary = append(summary, agg)
			}
			if err := state.WriteJSON(filepath.Join(out, "summary.json"), map[string]any{"env": envLabel, "table": table, "table_rows": size, "rates": summary}); err != nil {
				return err
			}
			return exp5Report(out, table, size, summary)
		},
	}
	c.Flags().Float64SliceVar(&rates, "rates", []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1}, "sampling rates")
	c.Flags().IntVar(&trials, "trials", 3, "trials per rate")
	c.Flags().IntVar(&inject, "inject", 20, "drifted rows injected per trial")
	c.Flags().IntVar(&maxRounds, "max-rounds", 400, "max rounds per trial")
	c.Flags().DurationVar(&maxTime, "max-time", 5*time.Minute, "max time per trial")
	c.Flags().StringVar(&table, "table", "edits", "table to drift and verify")
	c.Flags().StringVar(&out, "out", "results/exp5", "output directory")
	return c
}

// injectDrift corrupts n random rows directly in Spanner (bypassing CDC) and
// returns their verifier keys plus a restore function.
func injectDrift(ctx context.Context, pg, sp *pgxpool.Pool, table string, n int) (map[string]bool, func(), error) {
	rows, err := pg.Query(ctx, fmt.Sprintf(`SELECT id, comment FROM %s TABLESAMPLE BERNOULLI (5) WHERE ingested_at < now() - interval '2 minutes' LIMIT %d`, pgx.Identifier{table}.Sanitize(), n*5))
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		id      int64
		comment *string
	}
	var cands []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.comment); err != nil {
			rows.Close()
			return nil, nil, err
		}
		cands = append(cands, r)
	}
	rows.Close()
	rand.Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	if len(cands) > n {
		cands = cands[:n]
	}
	keys := map[string]bool{}
	for _, c := range cands {
		if _, err := sp.Exec(ctx, fmt.Sprintf(`UPDATE %s SET comment = $1 WHERE id = $2`, pgx.Identifier{table}.Sanitize()), "drift-"+strconv.FormatInt(c.id, 10), c.id); err != nil {
			return nil, nil, err
		}
		keys[rowset.RowKey(rowset.Row{rowset.Normalize(c.id)})] = true
	}
	restore := func() {
		for _, c := range cands {
			_, _ = sp.Exec(context.Background(), fmt.Sprintf(`UPDATE %s SET comment = $1 WHERE id = $2`, pgx.Identifier{table}.Sanitize()), c.comment, c.id)
		}
	}
	return keys, restore, nil
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t / float64(len(xs))
}

func exp5Report(out, table string, size int64, rs []exp5Rate) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Table `%s` (%d rows at start), %d injected drifted rows per trial, %d trials per rate. %s.\n\n", table, size, rs[0].Injected, rs[0].Trials, envLabel)
	b.WriteString("| sample rate | first detection (s) | all detected (s) | rounds to all | expected rounds (1 row) | rows read to all | rows read / round | round time (ms) | trials complete | false drift |\n|---|---|---|---|---|---|---|---|---|---|\n")
	var x, tAll, tFirst, cost []float64
	for _, r := range rs {
		fmt.Fprintf(&b, "| %.0f%% | %.1f | %.1f | %.0f | %.1f | %.0f | %.0f | %.0f | %d/%d | %d |\n", r.Rate*100, r.FirstDetectS, r.AllDetectS, r.Rounds, r.ExpectedRounds, r.RowsReadToAll, r.RowsPerRound, r.RoundMs, r.Complete, r.Trials, r.FalseDrift)
		x = append(x, r.Rate*100)
		tAll = append(tAll, nz(r.AllDetectS))
		tFirst = append(tFirst, nz(r.FirstDetectS))
		cost = append(cost, nz(r.RowsReadToAll))
	}
	if err := os.WriteFile(filepath.Join(out, "summary.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	if err := (chart.Chart{Title: "Exp 5: time to detect injected drift vs sampling rate (emulator)", XLabel: "sampling rate per round (%, log)", YLabel: "seconds (log)", LogX: true, LogY: true,
		Series: []chart.Series{{Name: "all 20 rows detected", X: x, Y: tAll}, {Name: "first row detected", X: x, Y: tFirst}}}).Save(filepath.Join(out, "detection_time.svg")); err != nil {
		return err
	}
	return chart.Chart{Title: "Exp 5: rows read until all drift detected (emulator)", XLabel: "sampling rate per round (%, log)", YLabel: "rows read, both sides (log)", LogX: true, LogY: true,
		Series: []chart.Series{{Name: "rows read to detect all", X: x, Y: cost}}}.Save(filepath.Join(out, "read_cost.svg"))
}

func nz(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}
