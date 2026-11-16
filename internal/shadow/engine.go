// Package shadow implements Layer 2: shadow reads. A statement whose primary
// (Postgres) result is known is also executed on Spanner; the two results are
// compared and every mismatch is classified by root cause and logged with
// enough context to reproduce it.
package shadow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/compare"
	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
	"github.com/shadowcut/shadowcut/internal/state"
	"github.com/shadowcut/shadowcut/internal/telemetry"
)

// Job is one statement to shadow.
type Job struct {
	SQL       string
	Args      []any
	Primary   *rowset.Result // result the app received; nil = engine runs it on Postgres
	InTxn     bool
	TxnWrites bool
	Source    string // "proxy" | "replay"
	At        time.Time
}

// Outcome is the comparison result for one job.
type Outcome struct {
	At          time.Time         `json:"at"`
	Source      string            `json:"source"`
	Name        string            `json:"name,omitempty"`
	Fingerprint string            `json:"fingerprint"`
	SQL         string            `json:"sql"`
	Args        []string          `json:"args,omitempty"`
	Mode        compare.Mode      `json:"mode"`
	Equal       bool              `json:"equal"`
	DiffKind    compare.Kind      `json:"diff_kind"`
	Category    classify.Category `json:"category,omitempty"`
	Confidence  string            `json:"confidence,omitempty"`
	Reason      string            `json:"reason,omitempty"`
	Rechecked   bool              `json:"rechecked,omitempty"`
	InTxn       bool              `json:"in_txn,omitempty"`
	PrimaryRows int               `json:"primary_rows"`
	ShadowRows  int               `json:"shadow_rows"`
	PrimaryErr  string            `json:"primary_err,omitempty"`
	ShadowErr   string            `json:"shadow_err,omitempty"`
	PrimaryMs   float64           `json:"primary_ms"`
	ShadowMs    float64           `json:"shadow_ms"`
	Columns     []string          `json:"columns,omitempty"`
	// Up to 3 differing rows per side, for reproduction.
	SamplePrimary []rowset.Row `json:"sample_primary,omitempty"`
	SampleShadow  []rowset.Row `json:"sample_shadow,omitempty"`
}

// Config configures an Engine.
type Config struct {
	Mode         compare.Mode  // auto (default) | ordered | unordered
	RecheckAfter time.Duration // replication-lag bound; 0 disables rechecks
	Workers      int
	QueueSize    int
	OutDir       string            // mismatches.jsonl + shadow-summary.json; "" = no files
	Names        map[string]string // fingerprint -> query name (from annotated SQL files)
	// ShadowTimeout bounds each Spanner execution.
	ShadowTimeout time.Duration
}

// Engine runs shadow comparisons.
type Engine struct {
	PG, SP *pgxpool.Pool
	cfg    Config
	queue  chan Job

	mu    sync.Mutex
	stats *Summary

	compared   metric.Int64Counter
	mismatches metric.Int64Counter
	dropped    metric.Int64Counter
	shadowLat  metric.Float64Histogram
}

// NewEngine creates an engine. PG may be nil when every job carries a Primary result
// and rechecks are disabled.
func NewEngine(pg, sp *pgxpool.Pool, cfg Config, meter metric.Meter) *Engine {
	if cfg.Mode == "" {
		cfg.Mode = compare.Auto
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 4
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 1000
	}
	if cfg.ShadowTimeout <= 0 {
		cfg.ShadowTimeout = 30 * time.Second
	}
	e := &Engine{PG: pg, SP: sp, cfg: cfg, queue: make(chan Job, cfg.QueueSize), stats: NewSummary()}
	e.compared = telemetry.Must(meter.Int64Counter("shadowcut_shadow_compared_total"))
	e.mismatches = telemetry.Must(meter.Int64Counter("shadowcut_shadow_mismatches_total"))
	e.dropped = telemetry.Must(meter.Int64Counter("shadowcut_shadow_dropped_total", metric.WithDescription("jobs dropped because the shadow queue was full")))
	e.shadowLat = telemetry.Must(meter.Float64Histogram("shadowcut_shadow_query_seconds", metric.WithUnit("s")))
	return e
}

// Submit enqueues a job without blocking; it returns false (and counts a drop)
// when the queue is full. The app's request is never delayed by Spanner.
func (e *Engine) Submit(j Job) bool {
	select {
	case e.queue <- j:
		return true
	default:
		e.dropped.Add(context.Background(), 1)
		e.mu.Lock()
		e.stats.Dropped++
		e.mu.Unlock()
		return false
	}
}

// Run starts workers and periodic summary flushes; it returns when ctx ends.
func (e *Engine) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for i := 0; i < e.cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-e.queue:
					e.Process(ctx, j)
				}
			}
		}()
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			e.Flush()
			return
		case <-t.C:
			e.Flush()
		}
	}
}

// Flush writes the summary file.
func (e *Engine) Flush() {
	if e.cfg.OutDir == "" {
		return
	}
	e.mu.Lock()
	e.stats.UpdatedAt = time.Now().UTC()
	b := e.stats.clone()
	e.mu.Unlock()
	if err := state.WriteJSON(e.cfg.OutDir+"/shadow-summary.json", b); err != nil {
		slog.Warn("shadow: writing summary failed", "err", err)
	}
}

// Summary returns a snapshot of the statistics.
func (e *Engine) Summary() *Summary {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats.clone()
}

// IsShadowable reports whether a statement is a read we should shadow.
func IsShadowable(info sqlscan.Info) bool {
	return info.Kind == sqlscan.KSelect
}

// Process runs one job synchronously and records the outcome.
func (e *Engine) Process(ctx context.Context, j Job) Outcome {
	toks := sqlscan.Lex(j.SQL)
	info := sqlscan.Analyze(toks)
	mode := e.cfg.Mode
	if mode == compare.Auto {
		mode = compare.Unordered
		if len(info.OrderBy) > 0 {
			mode = compare.Ordered
		}
	}
	o := Outcome{At: j.At, Source: j.Source, Fingerprint: info.Fingerprint, SQL: j.SQL, Mode: mode, InTxn: j.InTxn}
	if o.At.IsZero() {
		o.At = time.Now().UTC()
	}
	o.Name = e.cfg.Names[info.Fingerprint]
	for _, a := range j.Args {
		o.Args = append(o.Args, rowset.Normalize(a).String())
	}
	primary := j.Primary
	if primary == nil {
		primary = Exec(ctx, e.PG, j.SQL, j.Args, false, e.cfg.ShadowTimeout)
	}
	shadow := Exec(ctx, e.SP, j.SQL, spannerArgs(j.Args), j.InTxn && info.ForUpdate, e.cfg.ShadowTimeout)
	e.shadowLat.Record(ctx, shadow.Elapsed.Seconds())
	o.PrimaryMs = float64(primary.Elapsed.Microseconds()) / 1000
	o.ShadowMs = float64(shadow.Elapsed.Microseconds()) / 1000
	o.PrimaryRows, o.ShadowRows = len(primary.Rows), len(shadow.Rows)
	o.PrimaryErr, o.ShadowErr = primary.Err, shadow.Err
	for _, c := range primary.Columns {
		o.Columns = append(o.Columns, c.Name)
	}
	d := compare.Compare(primary, shadow, mode)
	o.DiffKind = d.Kind
	o.Equal = d.Equal()
	if !o.Equal {
		cctx := classify.Context{InTxn: j.InTxn, TxnHasWrites: j.TxnWrites}
		c := classify.Classify(classify.Input{Info: info, Tokens: toks, Args: j.Args, Primary: primary, Shadow: shadow, Diff: d, Ctx: cctx})
		// Recheck (re-read both sides after the lag bound, bracketed) only when the
		// rows themselves do not prove the cause. Cell-level evidence (a NULL from
		// concat, a whole-hour shift, last-digit float noise, a Unicode case
		// difference) cannot be produced by replication lag; ordering-based causes can.
		if d.Kind == compare.RowsDiffer && e.cfg.RecheckAfter > 0 && e.PG != nil && !(j.InTxn && j.TxnWrites) && !conclusive(c) {
			rd := e.recheck(ctx, j, mode)
			cctx.Recheck = &rd
			o.Rechecked = true
			c = classify.Classify(classify.Input{Info: info, Tokens: toks, Args: j.Args, Primary: primary, Shadow: shadow, Diff: d, Ctx: cctx})
		}
		// Counterfactual: does Postgres reproduce Spanner's result when evaluated in
		// Spanner's default time zone? If so, the time zone is the root cause.
		if (c.Category == classify.Other || c.Category == classify.MissingRow) && e.PG != nil && d.Kind != compare.ErrorShadow {
			if e.tzCounterfactual(ctx, j, shadow, mode) {
				c = classify.Result{Category: classify.Timezone, Confidence: "high", Reason: "Postgres reproduces Spanner's result when the session time zone is " + SpannerDefaultTZ + " (counterfactual re-execution)"}
			}
		}
		o.Category, o.Confidence, o.Reason = c.Category, c.Confidence, c.Reason
		o.SamplePrimary, o.SampleShadow = samples(primary, shadow, d)
	}
	e.record(ctx, o)
	return o
}

// recheck re-runs the statement: Postgres now, then Spanner after the lag bound,
// so that Spanner has had time to receive everything Postgres had committed.
func (e *Engine) recheck(ctx context.Context, j Job, mode compare.Mode) compare.Diff {
	p := Exec(ctx, e.PG, j.SQL, j.Args, false, e.cfg.ShadowTimeout)
	select {
	case <-ctx.Done():
		return compare.Diff{Kind: compare.RowsDiffer}
	case <-time.After(e.cfg.RecheckAfter):
	}
	s := Exec(ctx, e.SP, j.SQL, spannerArgs(j.Args), false, e.cfg.ShadowTimeout)
	if d := compare.Compare(p, s, mode); d.Equal() {
		return d
	}
	// Bracket: read Postgres again. If the Spanner result lies between the two
	// Postgres reads, the source simply changed during the comparison (churn).
	p2 := Exec(ctx, e.PG, j.SQL, j.Args, false, e.cfg.ShadowTimeout)
	d := compare.Compare(p2, s, mode)
	if d.Equal() {
		return d
	}
	info := sqlscan.Analyze(sqlscan.Lex(j.SQL))
	bracketed := compare.BracketedStable(p, s, p2)
	if info.HasLimit {
		bracketed = compare.Bracketed(p, s, p2)
		// "newest N" windows under continuous inserts slide between the reads
		if !bracketed && len(info.OrderBy) > 0 {
			bracketed = compare.WindowShift(p, s, p2, firstSortColumn(info, p.Columns))
		}
	}
	if bracketed && !(compare.Compare(p, p2, compare.Unordered).Equal()) {
		return compare.Diff{Kind: compare.Same}
	}
	return d
}

func samples(p, s *rowset.Result, d compare.Diff) (sp, ss []rowset.Row) {
	switch d.Kind {
	case compare.RowsDiffer:
		for i, idx := range d.OnlyPrimary {
			if i == 3 {
				break
			}
			sp = append(sp, p.Rows[idx])
		}
		for i, idx := range d.OnlyShadow {
			if i == 3 {
				break
			}
			ss = append(ss, s.Rows[idx])
		}
	case compare.OrderDiffers:
		i := d.FirstDivergence
		end := i + 3
		if end > len(p.Rows) {
			end = len(p.Rows)
		}
		sp = append(sp, p.Rows[i:end]...)
		ss = append(ss, s.Rows[i:end]...)
	}
	return sp, ss
}

func (e *Engine) record(ctx context.Context, o Outcome) {
	cat := string(o.Category)
	e.compared.Add(ctx, 1, metric.WithAttributes(attribute.String("query", nameOr(o))))
	if !o.Equal {
		e.mismatches.Add(ctx, 1, metric.WithAttributes(attribute.String("category", cat), attribute.String("query", nameOr(o))))
		if e.cfg.OutDir != "" {
			if err := state.AppendJSONL(e.cfg.OutDir+"/mismatches.jsonl", o); err != nil {
				slog.Warn("shadow: writing mismatch failed", "err", err)
			}
		}
	}
	e.mu.Lock()
	e.stats.add(o)
	e.mu.Unlock()
}

func nameOr(o Outcome) string {
	if o.Name != "" {
		return o.Name
	}
	if len(o.Fingerprint) > 60 {
		return o.Fingerprint[:60]
	}
	return o.Fingerprint
}

// spannerArgs adapts decoded wire values for re-execution (uuid bytes -> text).
func spannerArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case [16]byte:
			out[i] = rowset.Normalize(v).S
		default:
			out[i] = a
		}
	}
	return out
}

// Exec runs a statement and captures a normalized result. inTx wraps it in a
// read-write transaction that is rolled back (for FOR UPDATE inside app txns).
func Exec(ctx context.Context, pool *pgxpool.Pool, sql string, args []any, inTx bool, timeout time.Duration) *rowset.Result {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	res := &rowset.Result{}
	run := func(q interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	}) error {
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for _, f := range rows.FieldDescriptions() {
			res.Columns = append(res.Columns, rowset.Column{Name: f.Name, OID: f.DataTypeOID})
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				return err
			}
			r := make(rowset.Row, len(vals))
			for i, v := range vals {
				r[i] = rowset.Normalize(v)
			}
			res.Rows = append(res.Rows, r)
		}
		return rows.Err()
	}
	var err error
	if inTx {
		var tx pgx.Tx
		tx, err = pool.Begin(ctx)
		if err == nil {
			err = run(tx)
			_ = tx.Rollback(ctx)
		}
	} else {
		err = run(pool)
	}
	res.Elapsed = time.Since(start)
	if err != nil {
		res.Err = ErrText(err)
		res.Rows = nil
	}
	return res
}

// ErrText renders an error compactly (without PGAdapter's echoed statement).
func ErrText(err error) string {
	var pe *pgconn.PgError
	msg := err.Error()
	if errors.As(err, &pe) {
		msg = fmt.Sprintf("%s (SQLSTATE %s)", pe.Message, pe.Code)
	}
	if i := strings.Index(msg, " - Statement: "); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 400 {
		msg = msg[:400] + "..."
	}
	return msg
}

// QueryStats aggregates outcomes per statement fingerprint.
type QueryStats struct {
	Name        string                      `json:"name,omitempty"`
	Fingerprint string                      `json:"fingerprint"`
	Example     string                      `json:"example"`
	Compared    int64                       `json:"compared"`
	Mismatches  int64                       `json:"mismatches"`
	ByCategory  map[classify.Category]int64 `json:"by_category,omitempty"`
	LastReason  string                      `json:"last_reason,omitempty"`
}

// Summary is the shadow layer's persisted state, read by `shadowcut gate`.
type Summary struct {
	StartedAt  time.Time                   `json:"started_at"`
	UpdatedAt  time.Time                   `json:"updated_at"`
	Compared   int64                       `json:"compared"`
	Mismatches int64                       `json:"mismatches"`
	Dropped    int64                       `json:"dropped"`
	ByCategory map[classify.Category]int64 `json:"by_category"`
	Queries    map[string]*QueryStats      `json:"queries"`
}

// NewSummary returns an empty summary.
func NewSummary() *Summary {
	return &Summary{StartedAt: time.Now().UTC(), ByCategory: map[classify.Category]int64{}, Queries: map[string]*QueryStats{}}
}

func (s *Summary) add(o Outcome) {
	s.Compared++
	q := s.Queries[o.Fingerprint]
	if q == nil {
		q = &QueryStats{Name: o.Name, Fingerprint: o.Fingerprint, Example: o.SQL, ByCategory: map[classify.Category]int64{}}
		s.Queries[o.Fingerprint] = q
	}
	q.Compared++
	if !o.Equal {
		s.Mismatches++
		s.ByCategory[o.Category]++
		q.Mismatches++
		q.ByCategory[o.Category]++
		q.LastReason = o.Reason
	}
}

func (s *Summary) clone() *Summary {
	c := *s
	c.ByCategory = map[classify.Category]int64{}
	for k, v := range s.ByCategory {
		c.ByCategory[k] = v
	}
	c.Queries = map[string]*QueryStats{}
	for k, v := range s.Queries {
		q := *v
		q.ByCategory = map[classify.Category]int64{}
		for a, b := range v.ByCategory {
			q.ByCategory[a] = b
		}
		c.Queries[k] = &q
	}
	return &c
}

// SortedQueries returns per-query stats, most mismatches first.
func (s *Summary) SortedQueries() []*QueryStats {
	var out []*QueryStats
	for _, q := range s.Queries {
		out = append(out, q)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Mismatches != out[j].Mismatches {
			return out[i].Mismatches > out[j].Mismatches
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

// NamesFromSQL maps fingerprints of annotated statements to their names.
func NamesFromSQL(srcs ...string) map[string]string {
	m := map[string]string{}
	for _, src := range srcs {
		for _, st := range sqlscan.Split(src) {
			if st.Name != "" {
				fp := sqlscan.Fingerprint(st.Tokens)
				if _, taken := m[fp]; !taken { // first source wins (app before curated set)
					m[fp] = st.Name
				}
			}
		}
	}
	return m
}

// SpannerDefaultTZ is the time zone Spanner's PostgreSQL interface uses for
// date_trunc/extract/text rendering when none is given (observed on the
// emulator; documented for 2-argument date_trunc).
const SpannerDefaultTZ = "America/Los_Angeles"

func (e *Engine) tzCounterfactual(ctx context.Context, j Job, shadow *rowset.Result, mode compare.Mode) bool {
	q1 := e.execInTZ(ctx, j)
	if q1 == nil {
		return false
	}
	if compare.Compare(q1, shadow, mode).Equal() {
		return true
	}
	// live data: bracket a fresh Spanner read between two counterfactual reads
	s2 := Exec(ctx, e.SP, j.SQL, spannerArgs(j.Args), false, e.cfg.ShadowTimeout)
	q2 := e.execInTZ(ctx, j)
	if q2 == nil {
		return false
	}
	return compare.Compare(q2, s2, mode).Equal() || (!compare.Compare(q1, q2, compare.Unordered).Equal() && compare.Bracketed(q1, s2, q2))
}

// execInTZ runs the job on Postgres with the session time zone set to Spanner's default.
func (e *Engine) execInTZ(ctx context.Context, j Job) *rowset.Result {
	cctx, cancel := context.WithTimeout(ctx, e.cfg.ShadowTimeout)
	defer cancel()
	tx, err := e.PG.Begin(cctx)
	if err != nil {
		return nil
	}
	defer tx.Rollback(cctx)
	if _, err := tx.Exec(cctx, "SET LOCAL TIME ZONE '"+SpannerDefaultTZ+"'"); err != nil {
		return nil
	}
	rows, err := tx.Query(cctx, j.SQL, j.Args...)
	if err != nil {
		return nil
	}
	res := &rowset.Result{}
	for _, f := range rows.FieldDescriptions() {
		res.Columns = append(res.Columns, rowset.Column{Name: f.Name})
	}
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			rows.Close()
			return nil
		}
		r := make(rowset.Row, len(vals))
		for i, v := range vals {
			r[i] = rowset.Normalize(v)
		}
		res.Rows = append(res.Rows, r)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil
	}
	return res
}

// firstSortColumn maps the first ORDER BY item to a result column index (-1 if unknown).
func firstSortColumn(info sqlscan.Info, cols []rowset.Column) int {
	if len(info.OrderBy) == 0 {
		return -1
	}
	it := info.OrderBy[0]
	if it.ResultIndex >= 0 {
		return it.ResultIndex
	}
	if len(it.Columns) == 1 {
		for i, c := range cols {
			if strings.EqualFold(c.Name, it.Columns[0]) {
				return i
			}
		}
	}
	return -1
}

// conclusive reports classifications backed by cell-level evidence that
// replication lag cannot produce, so no recheck is needed.
func conclusive(c classify.Result) bool {
	if c.Confidence != "high" {
		return false
	}
	switch c.Category {
	case classify.NullSemantics, classify.FuncSemantics, classify.Precision, classify.Timezone:
		return true
	}
	return false
}
