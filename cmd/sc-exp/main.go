// sc-exp runs Shadowcut's experiments and writes raw results under results/.
//
//	sc-exp probe       feature probe (evidence for the hazard catalog)
//	sc-exp exp1        scanner accuracy (precision/recall vs observed mismatches)
//	sc-exp exp2        behaviour parity on the live Wikipedia workload
//	sc-exp exp4        migration safety under faults
//	sc-exp exp5        verification cost vs detection speed
//	sc-exp isolation   lost updates under concurrent read-modify-write
package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/probe"
)

var envLabel = "local Docker: PostgreSQL 16 vs Cloud Spanner emulator + PGAdapter (EMULATOR, not real Spanner)"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	root := &cobra.Command{Use: "sc-exp", Short: "Shadowcut experiments", SilenceUsage: true}
	root.AddCommand(probeCmd(), exp1Cmd(), exp2Cmd(), exp3Cmd(), exp4Cmd(), exp5Cmd(), isolationCmd())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func probeCmd() *cobra.Command {
	var out, evidence string
	c := &cobra.Command{
		Use:   "probe",
		Short: "Run the feature probe on both engines",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pg, err := pgutil.Connect(ctx, pgutil.DefaultPG(), 2)
			if err != nil {
				return err
			}
			defer pg.Close()
			sp, err := pgutil.Connect(ctx, pgutil.DefaultSpanner(), 2)
			if err != nil {
				return err
			}
			defer sp.Close()
			rs, err := probe.Run(ctx, pg, sp)
			if err != nil {
				return err
			}
			if err := probe.Write(out, rs, envLabel); err != nil {
				return err
			}
			if evidence != "" {
				if err := probe.WriteEvidence(evidence, rs); err != nil {
					return err
				}
			}
			counts := map[string]int{}
			for _, r := range rs {
				counts[r.Status]++
			}
			slog.Info("probe done", "probes", len(rs), "status", counts, "out", out)
			return nil
		},
	}
	c.Flags().StringVar(&out, "out", "results/probe", "output directory")
	c.Flags().StringVar(&evidence, "evidence", "internal/hazard/evidence.csv", "also write the scanner evidence table here (\"\" to skip)")
	return c
}
