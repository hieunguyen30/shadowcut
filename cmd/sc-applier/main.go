// sc-applier consumes Debezium change topics from Kafka and applies them to
// Spanner with an exactly-once effect (offset guard in the same transaction).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/shadowcut/shadowcut/internal/cdc"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/telemetry"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	guard := flag.Bool("guard", true, "exactly-once offset guard (disable only for fault experiments)")
	audit := flag.Bool("audit", pgutil.Env("APPLIER_AUDIT", "") == "1", "record per-record apply counts in sc_apply_audit")
	group := flag.String("group", pgutil.Env("APPLIER_GROUP", "sc-applier"), "Kafka consumer group")
	instance := flag.String("instance-id", pgutil.Env("APPLIER_INSTANCE_ID", "sc-applier-1"), "static group membership id (unique per applier replica)")
	topics := flag.String("topics", `^sc\.public\..*`, "topic regex")
	batch := flag.Int("batch", 500, "max records per Spanner transaction")
	flag.Parse()
	if os.Getenv("SPANNER_EMULATOR_HOST") == "" {
		os.Setenv("SPANNER_EMULATOR_HOST", "localhost:19010")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	meter, shutdown := telemetry.Setup("sc-applier", "")
	defer shutdown(context.Background())

	db := fmt.Sprintf("projects/%s/instances/%s/databases/%s",
		pgutil.Env("SPANNER_PROJECT", "emulator-project"), pgutil.Env("SPANNER_INSTANCE", "test-instance"), pgutil.Env("SPANNER_DATABASE", "wiki"))
	sp, err := spanner.NewClientWithConfig(ctx, db, spanner.ClientConfig{DisableNativeMetrics: true})
	if err != nil {
		slog.Error("spanner client", "err", err)
		os.Exit(1)
	}
	defer sp.Close()
	var tables map[string]*cdc.TableMeta
	for {
		tables, err = cdc.LoadSchema(ctx, sp)
		if err == nil && tables["edits"] != nil {
			break
		}
		if ctx.Err() != nil {
			os.Exit(1)
		}
		slog.Info("waiting for Spanner schema", "err", err)
		time.Sleep(2 * time.Second)
	}
	if _, err := regexp.Compile(*topics); err != nil {
		slog.Error("bad --topics", "err", err)
		os.Exit(2)
	}
	brokers := strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ",")
	kc, err := kgo.NewClient(
		kgo.SeedBrokers(brokers...),
		kgo.ConsumerGroup(*group),
		// Static membership: a restarted applier reclaims its partitions without
		// waiting for the session timeout of its previous incarnation.
		kgo.InstanceID(*instance),
		kgo.ConsumeRegex(),
		kgo.ConsumeTopics(*topics),
		kgo.DisableAutoCommit(),
		kgo.BlockRebalanceOnPoll(),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		kgo.FetchMaxWait(250*time.Millisecond),
		kgo.MetadataMaxAge(10*time.Second),
	)
	if err != nil {
		slog.Error("kafka client", "err", err)
		os.Exit(1)
	}
	defer kc.Close()
	a := cdc.New(cdc.Config{Guard: *guard, Audit: *audit, MaxBatch: *batch}, kc, sp, tables, meter)
	slog.Info("applier running", "brokers", brokers, "db", db, "guard", *guard, "audit", *audit, "tables", len(tables))
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Error("applier stopped", "err", err)
		os.Exit(1)
	}
}
