// sc-load writes uniquely keyed probe rows into Postgres at a fixed rate. Fault
// experiments compare the acknowledged rows with what reached Spanner to count
// lost and duplicated writes.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shadowcut/shadowcut/internal/loadgen"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/state"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	writer := flag.String("writer", fmt.Sprintf("w%d", time.Now().Unix()), "writer id (probe_events.writer)")
	rate := flag.Float64("rate", 50, "rows per second")
	dur := flag.Duration("duration", time.Minute, "how long to write")
	out := flag.String("out", "", "write a JSON summary here")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgutil.Connect(ctx, pgutil.DefaultPG(), 4)
	if err != nil {
		slog.Error("connect", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(ctx, *dur)
	defer cancel()
	res := loadgen.ProbeWriter{Pool: pool, Writer: *writer, Rate: *rate}.Run(ctx)
	slog.Info("load done", "writer", res.Writer, "attempted", res.Attempted, "acked", res.Acked, "failed", res.Failed)
	if *out != "" {
		_ = state.WriteJSON(*out, res)
	}
}
