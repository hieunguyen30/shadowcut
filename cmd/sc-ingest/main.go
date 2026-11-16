// sc-ingest loads real data into the source Postgres:
//
//	sc-ingest wiki       live Wikipedia recent changes (EventStreams SSE), or a recorded replay
//	sc-ingest gharchive  one or more hourly GH Archive files
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/metric"

	"github.com/shadowcut/shadowcut/internal/ingest/gharchive"
	"github.com/shadowcut/shadowcut/internal/ingest/wiki"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/telemetry"
)

func userAgent() string {
	return "Shadowcut/0.1 (migration behaviour-parity research tool; contact: " + pgutil.Env("WIKI_UA_CONTACT", "unset") + ") Go-http-client"
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	root := &cobra.Command{Use: "sc-ingest", Short: "Load real datasets into the source Postgres"}
	root.AddCommand(wikiCmd(), ghCmd())
	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func wikiCmd() *cobra.Command {
	var record, replay string
	var rate float64
	var loop bool
	var max int64
	var duration time.Duration
	var batch int
	var flushEvery time.Duration
	var dsn string
	c := &cobra.Command{
		Use:   "wiki",
		Short: "Ingest Wikipedia recent changes (live SSE, falling back to --replay)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if duration > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, duration)
				defer cancel()
			}
			meter, shutdown := telemetry.Setup("sc-ingest", "")
			defer shutdown(context.Background())
			events := telemetry.Must(meter.Int64Counter("shadowcut_ingest_events_total", metric.WithDescription("events received")))
			written := telemetry.Must(meter.Int64Counter("shadowcut_ingest_rows_written_total", metric.WithDescription("edit rows inserted")))
			reconnects := telemetry.Must(meter.Int64Counter("shadowcut_ingest_reconnects_total"))

			pool, err := pgutil.Connect(ctx, dsn, 4)
			if err != nil {
				return err
			}
			defer pool.Close()
			store := &wiki.Store{Pool: pool}

			var src wiki.Source
			if replay != "" {
				src = &wiki.Replay{Path: replay, Rate: rate, Loop: loop}
				slog.Info("replaying recorded events", "path", replay, "rate", rate, "loop", loop)
			} else {
				src = &wiki.SSE{URL: wiki.StreamURL, UserAgent: userAgent(), OnReconnect: func(error, time.Duration) { reconnects.Add(context.Background(), 1) }}
				slog.Info("streaming live", "url", wiki.StreamURL)
			}
			var rec *os.File
			if record != "" {
				rec, err = os.OpenFile(record, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					return err
				}
				defer rec.Close()
			}

			raw := make(chan []byte, 1024)
			srcErr := make(chan error, 1)
			go func() { srcErr <- src.Run(ctx, raw); close(raw) }()

			var pending []wiki.Edit
			var total, seen int64
			tick := time.NewTicker(flushEvery)
			defer tick.Stop()
			lastLog := time.Now()
			flush := func() error {
				if len(pending) == 0 {
					return nil
				}
				wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				n, err := store.WriteBatch(wctx, pending)
				if err != nil {
					return err
				}
				written.Add(wctx, n)
				total += n
				pending = pending[:0]
				if time.Since(lastLog) > 30*time.Second {
					slog.Info("ingest progress", "events_seen", seen, "rows_written", total)
					lastLog = time.Now()
				}
				return nil
			}
			for {
				select {
				case line, ok := <-raw:
					if !ok {
						if err := flush(); err != nil {
							return err
						}
						err := <-srcErr
						slog.Info("ingest finished", "events_seen", seen, "rows_written", total)
						if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
							return nil
						}
						return err
					}
					seen++
					events.Add(ctx, 1)
					if rec != nil {
						rec.Write(append(line, '\n'))
					}
					var ev wiki.Event
					if json.Unmarshal(line, &ev) != nil {
						continue
					}
					if e, ok := ev.ToEdit(); ok {
						pending = append(pending, e)
					}
					if len(pending) >= batch {
						if err := flush(); err != nil {
							return err
						}
					}
					if max > 0 && total+int64(len(pending)) >= max {
						if err := flush(); err != nil {
							return err
						}
						slog.Info("reached --max", "rows_written", total)
						return nil
					}
				case <-tick.C:
					if err := flush(); err != nil {
						slog.Error("write failed; retrying next tick", "err", err)
					}
				}
			}
		},
	}
	c.Flags().StringVar(&dsn, "dsn", pgutil.DefaultPG(), "Postgres DSN")
	c.Flags().StringVar(&record, "record", "", "append raw events (JSON lines) to this file")
	c.Flags().StringVar(&replay, "replay", "", "replay recorded events from this file instead of the live stream")
	c.Flags().Float64Var(&rate, "rate", 0, "replay rate in events/s (0 = unthrottled)")
	c.Flags().BoolVar(&loop, "loop", false, "loop the replay file (event ids rewritten to stay unique)")
	c.Flags().Int64Var(&max, "max", 0, "stop after writing this many rows (0 = unbounded)")
	c.Flags().DurationVar(&duration, "duration", 0, "stop after this long (0 = unbounded)")
	c.Flags().IntVar(&batch, "batch", 200, "rows per write transaction")
	c.Flags().DurationVar(&flushEvery, "flush", 500*time.Millisecond, "max time before a partial batch is written")
	return c
}

func ghCmd() *cobra.Command {
	var hours []string
	var max int
	var dsn string
	c := &cobra.Command{
		Use:   "gharchive",
		Short: "Load hourly GH Archive files (e.g. --hour 2024-01-01-15) or local .json.gz paths",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			pool, err := pgutil.Connect(ctx, dsn, 4)
			if err != nil {
				return err
			}
			defer pool.Close()
			for _, h := range hours {
				r, err := gharchive.Open(ctx, h, userAgent())
				if err != nil {
					return err
				}
				start := time.Now()
				n, err := gharchive.Load(ctx, pool, r, max, 500)
				r.Close()
				if err != nil {
					return fmt.Errorf("%s: %w", h, err)
				}
				slog.Info("loaded GH Archive hour", "hour", h, "rows", n, "elapsed", time.Since(start).Round(time.Millisecond))
			}
			return nil
		},
	}
	c.Flags().StringVar(&dsn, "dsn", pgutil.DefaultPG(), "Postgres DSN")
	c.Flags().StringSliceVar(&hours, "hour", []string{"2024-01-01-15"}, "GH Archive hour(s) or local .json.gz files")
	c.Flags().IntVar(&max, "max", 0, "max events per hour (0 = all)")
	return c
}
