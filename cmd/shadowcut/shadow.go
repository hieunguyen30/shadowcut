package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/compare"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
	"github.com/shadowcut/shadowcut/internal/state"
	"github.com/shadowcut/shadowcut/internal/telemetry"
	"github.com/shadowcut/shadowcut/queries"
)

func shadowCmd() *cobra.Command {
	c := &cobra.Command{Use: "shadow", Short: "Layer 2: shadow reads against Spanner with root-cause classification"}
	var pgDSN, spDSN, out, mode string
	var recheck time.Duration
	var workers int
	c.PersistentFlags().StringVar(&pgDSN, "pg", pgutil.DefaultPG(), "Postgres DSN ($PG_DSN)")
	c.PersistentFlags().StringVar(&spDSN, "spanner", pgutil.DefaultSpanner(), "Spanner PGAdapter DSN ($SPANNER_DSN)")
	c.PersistentFlags().StringVar(&out, "out", "state/shadow", "output dir for mismatches.jsonl and shadow-summary.json")
	c.PersistentFlags().StringVar(&mode, "order", "auto", "comparison mode: auto | ordered | unordered")
	c.PersistentFlags().DurationVar(&recheck, "recheck", 3*time.Second, "replication-lag bound used to recheck row differences (0 = off)")
	c.PersistentFlags().IntVar(&workers, "workers", 4, "concurrent shadow executions")

	newEngine := func(cmd *cobra.Command) (*shadow.Engine, func(), error) {
		ctx := cmd.Context()
		m := compare.Mode(mode)
		if m != compare.Auto && m != compare.Ordered && m != compare.Unordered {
			return nil, nil, fmt.Errorf("bad --order %q", mode)
		}
		meter, shutdown := telemetry.Setup("shadowcut-shadow", "")
		pg, err := pgutil.Connect(ctx, pgDSN, int32(workers*2))
		if err != nil {
			return nil, nil, err
		}
		sp, err := pgutil.Connect(ctx, spDSN, int32(workers*2))
		if err != nil {
			return nil, nil, err
		}
		e := shadow.NewEngine(pg, sp, shadow.Config{Mode: m, RecheckAfter: recheck, Workers: workers, OutDir: out,
			Names: shadow.NamesFromSQL(queries.AppSQL, queries.HazardsSQL)}, meter)
		return e, func() { pg.Close(); sp.Close(); shutdown(ctx) }, nil
	}

	var listen, upstream string
	var maxRows int
	proxy := &cobra.Command{
		Use:   "proxy",
		Short: "PostgreSQL wire proxy: serve reads from Postgres, shadow them on Spanner",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, closeFn, err := newEngine(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			if upstream == "" {
				cfg, err := pgx.ParseConfig(pgDSN)
				if err != nil {
					return err
				}
				upstream = net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port)))
			}
			go e.Run(cmd.Context())
			p := &shadow.Proxy{Listen: listen, Upstream: upstream, Engine: e, MaxRows: maxRows}
			return p.Serve(cmd.Context())
		},
	}
	proxy.Flags().StringVar(&listen, "listen", ":15435", "listen address")
	proxy.Flags().StringVar(&upstream, "upstream", "", "Postgres host:port (default: from --pg)")
	proxy.Flags().IntVar(&maxRows, "max-rows", 10000, "results larger than this are not shadowed")

	var files []string
	var repeat int
	var interval time.Duration
	var only []string
	replay := &cobra.Command{
		Use:   "replay",
		Short: "Run annotated SQL files against both databases and classify differences",
		RunE: func(cmd *cobra.Command, args []string) error {
			e, closeFn, err := newEngine(cmd)
			if err != nil {
				return err
			}
			defer closeFn()
			want := map[string]bool{}
			for _, o := range only {
				want[o] = true
			}
			var jobs []sqlscan.Statement
			for _, f := range files {
				src, err := readQueries(f)
				if err != nil {
					return err
				}
				for _, st := range sqlscan.Split(src) {
					if len(want) > 0 && !want[st.Name] {
						continue
					}
					if shadow.IsShadowable(sqlscan.Analyze(st.Tokens)) {
						jobs = append(jobs, st)
					}
				}
			}
			var outcomes []shadow.Outcome
			for r := 0; r < repeat; r++ {
				for _, st := range jobs {
					o := e.Process(cmd.Context(), shadow.Job{SQL: st.Text, Source: "replay", At: time.Now().UTC()})
					outcomes = append(outcomes, o)
					status := "match"
					if !o.Equal {
						status = fmt.Sprintf("MISMATCH %-22s %s", o.Category, trunc(o.Reason, 110))
					}
					fmt.Fprintf(os.Stderr, "%-32s %s\n", label(o, st), status)
				}
				if r < repeat-1 {
					time.Sleep(interval)
				}
			}
			e.Flush()
			if out != "" {
				if err := state.WriteJSON(filepath.Join(out, "replay-outcomes.json"), outcomes); err != nil {
					return err
				}
			}
			s := e.Summary()
			b, _ := json.MarshalIndent(map[string]any{"compared": s.Compared, "mismatches": s.Mismatches, "by_category": s.ByCategory}, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	replay.Flags().StringSliceVar(&files, "queries", []string{"queries/hazards.sql"}, "annotated SQL files (\"embedded:hazards\"/\"embedded:app\" use the built-in copies)")
	replay.Flags().IntVar(&repeat, "repeat", 1, "repeat the whole set N times")
	replay.Flags().DurationVar(&interval, "interval", 2*time.Second, "pause between repeats")
	replay.Flags().StringSliceVar(&only, "only", nil, "only run statements with these names")
	c.AddCommand(proxy, replay)
	return c
}

func readQueries(f string) (string, error) {
	switch f {
	case "embedded:hazards":
		return queries.HazardsSQL, nil
	case "embedded:app":
		return queries.AppSQL, nil
	}
	b, err := os.ReadFile(f)
	return string(b), err
}

func label(o shadow.Outcome, st sqlscan.Statement) string {
	if st.Name != "" {
		return st.Name
	}
	return trunc(o.Fingerprint, 32)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-3]) + "..."
	}
	return s
}
