package cdc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/api/iterator"

	"github.com/shadowcut/shadowcut/internal/telemetry"
)

// UnavailableValue is Debezium's default unavailable.value.placeholder.
const UnavailableValue = "__debezium_unavailable_value"

// TableMeta describes a Spanner table for applying row images.
type TableMeta struct {
	Name    string
	Columns map[string]ColType
	Order   []string // column order
	PK      []string
}

// LoadSchema reads table metadata from Spanner's information_schema.
func LoadSchema(ctx context.Context, c *spanner.Client) (map[string]*TableMeta, error) {
	out := map[string]*TableMeta{}
	ro := c.Single()
	defer ro.Close()
	it := ro.Query(ctx, spanner.Statement{SQL: `SELECT table_name, column_name, spanner_type FROM information_schema.columns WHERE table_schema = 'public' ORDER BY table_name, ordinal_position`})
	err := it.Do(func(r *spanner.Row) error {
		var t, col, typ string
		if err := r.Columns(&t, &col, &typ); err != nil {
			return err
		}
		m := out[t]
		if m == nil {
			m = &TableMeta{Name: t, Columns: map[string]ColType{}}
			out[t] = m
		}
		m.Columns[col] = ColType(typ)
		m.Order = append(m.Order, col)
		return nil
	})
	if err != nil {
		return nil, err
	}
	ro2 := c.Single()
	defer ro2.Close()
	it = ro2.Query(ctx, spanner.Statement{SQL: `SELECT table_name, column_name FROM information_schema.index_columns WHERE table_schema = 'public' AND index_type = 'PRIMARY_KEY' ORDER BY table_name, ordinal_position`})
	err = it.Do(func(r *spanner.Row) error {
		var t, col string
		if err := r.Columns(&t, &col); err != nil {
			return err
		}
		if m := out[t]; m != nil {
			m.PK = append(m.PK, col)
		}
		return nil
	})
	return out, err
}

// Config configures the applier.
type Config struct {
	// Guard enables the exactly-once offset guard (default true). Disabling it is
	// only for fault experiments that demonstrate why it exists.
	Guard bool
	// Audit records per-record apply counts in sc_apply_audit (experiments only).
	Audit bool
	// MaxBatch caps records per Spanner transaction.
	MaxBatch int
	// TopicPrefix strips "<prefix>.<schema>." from topic names to get tables.
	TopicPrefix string
}

// Applier consumes Debezium topics and applies them to Spanner.
type Applier struct {
	cfg    Config
	kc     *kgo.Client
	sp     *spanner.Client
	tables map[string]*TableMeta

	applied   metric.Int64Counter
	skipped   metric.Int64Counter
	batches   metric.Int64Counter
	errs      metric.Int64Counter
	batchSecs metric.Float64Histogram
	lagMu     sync.Mutex
	lastLag   time.Duration
	lastSrc   time.Time
}

// New creates an applier. kc must be a consumer-group client created with
// kgo.DisableAutoCommit() and kgo.BlockRebalanceOnPoll().
func New(cfg Config, kc *kgo.Client, sp *spanner.Client, tables map[string]*TableMeta, meter metric.Meter) *Applier {
	if cfg.MaxBatch <= 0 {
		cfg.MaxBatch = 500
	}
	a := &Applier{cfg: cfg, kc: kc, sp: sp, tables: tables}
	a.applied = telemetry.Must(meter.Int64Counter("shadowcut_applier_records_applied_total"))
	a.skipped = telemetry.Must(meter.Int64Counter("shadowcut_applier_records_skipped_total", metric.WithDescription("redelivered records skipped by the offset guard")))
	a.batches = telemetry.Must(meter.Int64Counter("shadowcut_applier_batches_total"))
	a.errs = telemetry.Must(meter.Int64Counter("shadowcut_applier_errors_total"))
	a.batchSecs = telemetry.Must(meter.Float64Histogram("shadowcut_applier_batch_seconds", metric.WithUnit("s")))
	_ = telemetry.Must(meter.Float64ObservableGauge("shadowcut_replication_lag_seconds",
		metric.WithDescription("now - source commit time of the last applied change"),
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			a.lagMu.Lock()
			defer a.lagMu.Unlock()
			if !a.lastSrc.IsZero() {
				o.Observe(time.Since(a.lastSrc).Seconds())
			}
			return nil
		})))
	return a
}

// Run polls and applies until ctx is cancelled.
func (a *Applier) Run(ctx context.Context) error {
	for {
		fetches := a.kc.PollRecords(ctx, a.cfg.MaxBatch)
		if ctx.Err() != nil {
			a.kc.AllowRebalance()
			return ctx.Err()
		}
		if errs := fetches.Errors(); len(errs) > 0 {
			for _, e := range errs {
				if errors.Is(e.Err, context.Canceled) {
					return ctx.Err()
				}
				slog.Warn("kafka fetch error", "topic", e.Topic, "partition", e.Partition, "err", e.Err)
			}
		}
		recs := fetches.Records()
		if len(recs) == 0 {
			a.kc.AllowRebalance()
			continue
		}
		// Apply with retry; never commit Kafka offsets before Spanner commits.
		backoff := 200 * time.Millisecond
		for {
			err := a.ApplyBatch(ctx, recs)
			if err == nil {
				break
			}
			a.errs.Add(ctx, 1)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Warn("apply failed; retrying", "records", len(recs), "err", err, "backoff", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			if backoff < 10*time.Second {
				backoff *= 2
			}
		}
		if err := a.kc.CommitRecords(ctx, recs...); err != nil {
			// Not fatal: the records will be redelivered and skipped by the guard.
			slog.Warn("kafka commit failed (records will be redelivered and deduplicated)", "err", err)
		}
		a.kc.AllowRebalance()
	}
}

type tp struct {
	topic string
	part  int32
}

// ApplyBatch applies records in one Spanner read-write transaction.
func (a *Applier) ApplyBatch(ctx context.Context, recs []*kgo.Record) error {
	start := time.Now()
	var appliedN, skippedN int64
	var newest time.Time
	_, err := a.sp.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		appliedN, skippedN, newest = 0, 0, time.Time{}
		// 1. current offsets for every (topic, partition) in the batch
		maxOff := map[tp]int64{}
		var keys []spanner.Key
		seen := map[tp]bool{}
		for _, r := range recs {
			k := tp{r.Topic, r.Partition}
			if !seen[k] {
				seen[k] = true
				keys = append(keys, spanner.Key{r.Topic, int64(r.Partition)})
			}
		}
		stored := map[tp]int64{}
		if a.cfg.Guard {
			it := txn.Read(ctx, "sc_offsets", spanner.KeySetFromKeys(keys...), []string{"topic", "kpartition", "koffset"})
			if err := it.Do(func(row *spanner.Row) error {
				var t string
				var p, o int64
				if err := row.Columns(&t, &p, &o); err != nil {
					return err
				}
				stored[tp{t, int32(p)}] = o
				return nil
			}); err != nil {
				return fmt.Errorf("read offsets: %w", err)
			}
		}
		// 2. mutations
		var muts []*spanner.Mutation
		var auditKeys []spanner.Key
		for _, r := range recs {
			k := tp{r.Topic, r.Partition}
			if a.cfg.Guard {
				if off, ok := stored[k]; ok && r.Offset <= off {
					skippedN++
					continue
				}
			}
			if r.Offset > maxOff[k] || maxOff[k] == 0 {
				maxOff[k] = r.Offset
			}
			env, ok, err := Decode(r.Value)
			if err != nil {
				return fmt.Errorf("decode %s/%d@%d: %w", r.Topic, r.Partition, r.Offset, err)
			}
			if !ok {
				continue
			}
			m, err := a.mutation(r.Topic, env)
			if err != nil {
				return fmt.Errorf("%s/%d@%d: %w", r.Topic, r.Partition, r.Offset, err)
			}
			if m != nil {
				muts = append(muts, m)
			}
			appliedN++
			if st := env.SourceTime(); st.After(newest) {
				newest = st
			}
			if a.cfg.Audit {
				auditKeys = append(auditKeys, spanner.Key{r.Topic, int64(r.Partition), r.Offset})
			}
		}
		now := time.Now().UTC()
		for k, off := range maxOff {
			var src spanner.NullTime
			if !newest.IsZero() {
				src = spanner.NullTime{Time: newest, Valid: true}
			}
			muts = append(muts, spanner.InsertOrUpdate("sc_offsets",
				[]string{"topic", "kpartition", "koffset", "source_ts", "applied_at"},
				[]any{k.topic, int64(k.part), off, src, now}))
		}
		if len(auditKeys) > 0 {
			times := map[string]int64{}
			it := txn.Read(ctx, "sc_apply_audit", spanner.KeySetFromKeys(auditKeys...), []string{"topic", "kpartition", "koffset", "times"})
			if err := it.Do(func(row *spanner.Row) error {
				var t string
				var p, o, n int64
				if err := row.Columns(&t, &p, &o, &n); err != nil {
					return err
				}
				times[fmt.Sprintf("%s/%d/%d", t, p, o)] = n
				return nil
			}); err != nil {
				return fmt.Errorf("read audit: %w", err)
			}
			for _, k := range auditKeys {
				key := fmt.Sprintf("%s/%d/%d", k[0], k[1], k[2])
				muts = append(muts, spanner.InsertOrUpdate("sc_apply_audit", []string{"topic", "kpartition", "koffset", "times"},
					[]any{k[0], k[1], k[2], times[key] + 1}))
			}
		}
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return err
	}
	a.batches.Add(ctx, 1)
	a.applied.Add(ctx, appliedN)
	a.skipped.Add(ctx, skippedN)
	a.batchSecs.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attribute.Int("records", len(recs))))
	if !newest.IsZero() {
		a.lagMu.Lock()
		if newest.After(a.lastSrc) {
			a.lastSrc = newest
		}
		a.lastLag = time.Since(newest)
		a.lagMu.Unlock()
	}
	return nil
}

// TableForTopic maps "sc.public.edits" -> "edits".
func TableForTopic(topic string) string {
	if i := strings.LastIndexByte(topic, '.'); i >= 0 {
		return topic[i+1:]
	}
	return topic
}

func (a *Applier) mutation(topic string, env Envelope) (*spanner.Mutation, error) {
	table := TableForTopic(topic)
	meta := a.tables[table]
	if meta == nil {
		return nil, fmt.Errorf("table %q not in Spanner schema", table)
	}
	switch env.Op {
	case "c", "u", "r":
		if env.After == nil {
			return nil, fmt.Errorf("op %s without after image", env.Op)
		}
		return rowMutation(meta, env.After)
	case "d":
		img := env.Before
		if img == nil {
			return nil, errors.New("delete without before image (REPLICA IDENTITY?)")
		}
		key := spanner.Key{}
		for _, pk := range meta.PK {
			v, err := Convert(meta.Columns[pk], img[pk])
			if err != nil {
				return nil, fmt.Errorf("key %s: %w", pk, err)
			}
			key = append(key, v)
		}
		return spanner.Delete(table, key), nil
	case "t":
		return spanner.Delete(table, spanner.AllKeys()), nil
	}
	return nil, nil
}

func rowMutation(meta *TableMeta, img map[string]any) (*spanner.Mutation, error) {
	cols := make([]string, 0, len(img))
	for c, v := range img {
		// Debezium emits this placeholder for unchanged TOASTed columns in UPDATEs
		// (REPLICA IDENTITY DEFAULT); omitting the column keeps Spanner's value.
		if s, ok := v.(string); ok && s == UnavailableValue {
			continue
		}
		if _, ok := meta.Columns[c]; ok {
			cols = append(cols, c)
		}
	}
	sort.Strings(cols)
	vals := make([]any, len(cols))
	for i, c := range cols {
		v, err := Convert(meta.Columns[c], img[c])
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", c, err)
		}
		vals[i] = v
	}
	return spanner.InsertOrUpdate(meta.Name, cols, vals), nil
}

// DrainCheck returns true when every partition's committed offset equals its end
// offset (no pending CDC work). Used by cutover to wait for a drained pipeline.
func DrainCheck(ctx context.Context, sp *spanner.Client) (map[string]int64, error) {
	ro := sp.Single()
	defer ro.Close()
	it := ro.Query(ctx, spanner.Statement{SQL: `SELECT topic, kpartition, koffset FROM sc_offsets`})
	out := map[string]int64{}
	for {
		row, err := it.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		var t string
		var p, o int64
		if err := row.Columns(&t, &p, &o); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%s/%d", t, p)] = o
	}
}
