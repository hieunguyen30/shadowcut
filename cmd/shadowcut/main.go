// shadowcut finds where Spanner's PostgreSQL interface behaves differently from
// PostgreSQL for a specific application.
//
//	shadowcut scan      Layer 1: static hazard scan of schema + queries
//	shadowcut shadow    Layer 2: shadow-read proxy / replay with root-cause classification
//	shadowcut verify    continuous sampled row-level drift checking
//	shadowcut gate      Layer 3: GO / NO-GO cutover decision
//	shadowcut cutover   switch the app to Spanner (gated)
//	shadowcut rollback  switch back to Postgres, replaying the reverse journal
//	shadowcut report    render reports from saved results
//	shadowcut schema    apply the Spanner schema through PGAdapter
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

var version = "0.1.0"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	root := &cobra.Command{
		Use:           "shadowcut",
		Short:         "Find where Spanner's PostgreSQL interface behaves differently from PostgreSQL for your app",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(scanCmd(), shadowCmd(), verifyCmd(), gateCmd(), cutoverCmd(), rollbackCmd(), reportCmd(), schemaCmd(), explainCmd())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		if ec, ok := err.(exitCode); ok {
			os.Exit(int(ec))
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(2)
	}
}

// exitCode lets commands request a specific exit status without printing an error
// (scan --fail-on, gate NO-GO).
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit %d", int(e)) }
