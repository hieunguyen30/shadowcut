// sc-demoapp is the demo read/write API (see internal/app).
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shadowcut/shadowcut/internal/app"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/telemetry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	meter, shutdown := telemetry.Setup("sc-demoapp", "")
	defer shutdown(context.Background())

	pg, err := pgutil.Connect(ctx, pgutil.DefaultPG(), 16)
	if err != nil {
		slog.Error("postgres", "err", err)
		os.Exit(1)
	}
	read := pg
	if dsn := os.Getenv("READ_DSN"); dsn != "" {
		if read, err = pgutil.Connect(ctx, dsn, 16); err != nil {
			slog.Error("read path", "err", err)
			os.Exit(1)
		}
	}
	sp, err := pgutil.Connect(ctx, pgutil.DefaultSpanner(), 16)
	if err != nil {
		slog.Error("spanner", "err", err)
		os.Exit(1)
	}
	s := app.New(pg, read, sp, meter)
	go s.PollControl(ctx, 500*time.Millisecond)

	addr := pgutil.Env("LISTEN", ":18080")
	srv := &http.Server{Addr: addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	slog.Info("demo app listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
}
