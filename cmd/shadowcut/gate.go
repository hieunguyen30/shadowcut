package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/cutover"
	"github.com/shadowcut/shadowcut/internal/gate"
	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/state"
	"github.com/shadowcut/shadowcut/internal/verify"
)

type gateFlags struct {
	pgDSN, spDSN, policy, shadowSum, verifySum, scanRep string
}

func (g *gateFlags) register(c *cobra.Command) {
	c.Flags().StringVar(&g.pgDSN, "pg", pgutil.DefaultPG(), "Postgres DSN")
	c.Flags().StringVar(&g.spDSN, "spanner", pgutil.DefaultSpanner(), "Spanner PGAdapter DSN")
	c.Flags().StringVar(&g.policy, "policy", "", "gate policy YAML (default: built-in conservative policy)")
	c.Flags().StringVar(&g.shadowSum, "shadow-summary", "state/shadow/shadow-summary.json", "shadow summary")
	c.Flags().StringVar(&g.verifySum, "verify-summary", "state/verify-summary.json", "verifier summary")
	c.Flags().StringVar(&g.scanRep, "scan-report", "state/scan.json", "scan report (shadowcut scan --json)")
}

// evaluate collects signals and evaluates the policy.
func (g *gateFlags) evaluate(ctx context.Context, pg, sp *pgxpool.Pool) (gate.Decision, error) {
	p, err := gate.LoadPolicy(g.policy)
	if err != nil {
		return gate.Decision{}, err
	}
	var sig gate.Signals
	var ss shadow.Summary
	if state.ReadJSON(g.shadowSum, &ss) == nil {
		sig.Shadow = &ss
	}
	var vs verify.Summary
	if state.ReadJSON(g.verifySum, &vs) == nil {
		sig.Verify = &vs
	}
	var sr hazard.Report
	if state.ReadJSON(g.scanRep, &sr) == nil {
		sig.Scan = &sr
	}
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lag, hb, err := gate.MeasureLag(lctx, pg, sp)
	if err != nil {
		sig.LagErr = err.Error()
	} else {
		sig.Lag, sig.HeartbeatAge = &lag, &hb
	}
	gate.MeasureBacklog(lctx, &sig, strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ","), sp)
	return gate.Evaluate(p, sig, time.Now()), nil
}

func printDecision(w io.Writer, d gate.Decision) {
	fmt.Fprintf(w, "\n  %s\n\n", map[bool]string{true: "GO   ✔  cutover may proceed", false: "NO-GO ✘  do not cut over"}[d.Go])
	for _, c := range d.Checks {
		mark := map[string]string{"pass": "PASS", "fail": "FAIL", "warn": "WARN"}[c.Status]
		fmt.Fprintf(w, "  %-5s %-28s %s\n", mark, c.Name, c.Detail)
	}
	fmt.Fprintln(w)
}

func gateCmd() *cobra.Command {
	var g gateFlags
	var jsonOut string
	c := &cobra.Command{
		Use:   "gate",
		Short: "Layer 3: GO / NO-GO cutover decision (exit 0 = GO, 1 = NO-GO)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pg, err := pgutil.Connect(ctx, g.pgDSN, 2)
			if err != nil {
				return err
			}
			defer pg.Close()
			sp, err := pgutil.Connect(ctx, g.spDSN, 2)
			if err != nil {
				return err
			}
			defer sp.Close()
			d, err := g.evaluate(ctx, pg, sp)
			if err != nil {
				return err
			}
			printDecision(os.Stdout, d)
			if jsonOut != "" {
				if err := state.WriteJSON(jsonOut, d); err != nil {
					return err
				}
			}
			if !d.Go {
				return exitCode(1)
			}
			return nil
		},
	}
	g.register(c)
	c.Flags().StringVar(&jsonOut, "json", "", "write the decision as JSON")
	return c
}

func newController(pg, sp *pgxpool.Pool) *cutover.Controller {
	return &cutover.Controller{
		PG: pg, SP: sp,
		KafkaBrokers: strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ","),
		TopicPrefix:  "sc.public.",
		SettleTime:   2 * time.Second,
		DrainTimeout: 2 * time.Minute,
		Tables:       []string{"edits", "pages", "gh_events", "probe_events"},
	}
}

func cutoverCmd() *cobra.Command {
	var g gateFlags
	var force bool
	var drain time.Duration
	c := &cobra.Command{
		Use:   "cutover",
		Short: "Switch the app to Spanner: gate -> freeze writes -> drain CDC -> verify counts -> switch",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pg, err := pgutil.Connect(ctx, g.pgDSN, 4)
			if err != nil {
				return err
			}
			defer pg.Close()
			sp, err := pgutil.Connect(ctx, g.spDSN, 4)
			if err != nil {
				return err
			}
			defer sp.Close()
			ctl := newController(pg, sp)
			ctl.DrainTimeout = drain
			gateFn := func(ctx context.Context) (bool, string, error) {
				d, err := g.evaluate(ctx, pg, sp)
				if err != nil {
					return false, "", err
				}
				printDecision(os.Stdout, d)
				if force && !d.Go {
					fmt.Println("  --force given: proceeding despite NO-GO")
					return true, "", nil
				}
				var fails []string
				for _, c := range d.Checks {
					if c.Status == "fail" {
						fails = append(fails, c.Name)
					}
				}
				return d.Go, strings.Join(fails, ", "), nil
			}
			if err := ctl.Cutover(ctx, gateFn); err != nil {
				return err
			}
			st, _ := ctl.GetStatus(ctx)
			b, _ := json.Marshal(st)
			fmt.Println(string(b))
			return nil
		},
	}
	g.register(c)
	c.Flags().BoolVar(&force, "force", false, "proceed even if the gate says NO-GO (recorded in shadowcut.events)")
	c.Flags().DurationVar(&drain, "drain-timeout", 2*time.Minute, "max wait for CDC to drain")
	return c
}

func rollbackCmd() *cobra.Command {
	var pgDSN, spDSN string
	c := &cobra.Command{
		Use:   "rollback",
		Short: "Switch the app back to Postgres, replaying Spanner-side writes from the reverse journal",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
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
			n, err := newController(pg, sp).Rollback(ctx)
			if err != nil {
				return err
			}
			fmt.Printf("rolled back to postgres; %d journal entries replayed\n", n)
			return nil
		},
	}
	c.Flags().StringVar(&pgDSN, "pg", pgutil.DefaultPG(), "Postgres DSN")
	c.Flags().StringVar(&spDSN, "spanner", pgutil.DefaultSpanner(), "Spanner PGAdapter DSN")
	return c
}
