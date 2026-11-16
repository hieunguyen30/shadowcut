// Package cutover switches the demo app from Postgres to Spanner (gated, with a
// write freeze and a drained CDC pipeline) and back (rollback replays the
// reverse journal into Postgres). Every step is recorded in shadowcut.control so
// a crashed cutover can be resumed or rolled back from any point.
package cutover

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Steps recorded in shadowcut.control.step.
const (
	StepIdle       = "idle"
	StepFrozen     = "frozen"       // writes stopped, waiting for drain
	StepDrained    = "drained"      // CDC fully applied, final checks pending
	StepDone       = "cutover_done" // app on Spanner
	StepRBFrozen   = "rollback_frozen"
	StepRBReplayed = "rollback_replayed"
	StepRolledBack = "rolled_back"
)

// Controller runs cutover and rollback.
type Controller struct {
	PG, SP       *pgxpool.Pool
	KafkaBrokers []string
	// TopicPrefix of Debezium topics (e.g. "sc.public.").
	TopicPrefix string
	// Group is the applier's consumer group.
	Group string
	// SettleTime is how long to wait after changing the mode so every app
	// instance has observed it (apps poll every 500ms).
	SettleTime time.Duration
	// DrainTimeout bounds the wait for CDC to drain.
	DrainTimeout time.Duration
	// Tables whose row counts must match before switching.
	Tables []string
	// Hook is called after each step (tests/fault injection use it to crash).
	Hook func(step string) error
	Log  *slog.Logger
}

// Status is the control-plane state.
type Status struct {
	Mode, Step string
	UpdatedAt  time.Time
	Note       string
}

// EnsureControl creates control-plane tables if missing.
func (c *Controller) EnsureControl(ctx context.Context) error {
	_, err := c.PG.Exec(ctx, `
CREATE SCHEMA IF NOT EXISTS shadowcut;
CREATE TABLE IF NOT EXISTS shadowcut.control (
  id integer PRIMARY KEY DEFAULT 1 CHECK (id = 1), mode text NOT NULL DEFAULT 'postgres',
  step text NOT NULL DEFAULT 'idle', updated_at timestamptz NOT NULL DEFAULT now(), note text);
INSERT INTO shadowcut.control (id) VALUES (1) ON CONFLICT DO NOTHING;
CREATE TABLE IF NOT EXISTS shadowcut.journal_applied (id text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now());
CREATE TABLE IF NOT EXISTS shadowcut.events (at timestamptz NOT NULL DEFAULT clock_timestamp(), action text NOT NULL, step text NOT NULL, note text);`)
	return err
}

// GetStatus reads the control row.
func (c *Controller) GetStatus(ctx context.Context) (Status, error) {
	var s Status
	var note *string
	err := c.PG.QueryRow(ctx, `SELECT mode, step, updated_at, note FROM shadowcut.control WHERE id = 1`).Scan(&s.Mode, &s.Step, &s.UpdatedAt, &note)
	if note != nil {
		s.Note = *note
	}
	return s, err
}

func (c *Controller) set(ctx context.Context, action, mode, step, note string) error {
	_, err := c.PG.Exec(ctx, `UPDATE shadowcut.control SET mode = $1, step = $2, updated_at = now(), note = $3 WHERE id = 1`, mode, step, note)
	if err != nil {
		return err
	}
	_, _ = c.PG.Exec(ctx, `INSERT INTO shadowcut.events (action, step, note) VALUES ($1, $2, $3)`, action, step, note)
	c.log().Info(action, "mode", mode, "step", step, "note", note)
	if c.Hook != nil {
		return c.Hook(step)
	}
	return nil
}

func (c *Controller) log() *slog.Logger {
	if c.Log != nil {
		return c.Log
	}
	return slog.Default()
}

// ErrAborted is returned when a precondition fails.
var ErrAborted = errors.New("cutover aborted")

// Cutover moves the app to Spanner. gateOK is evaluated first unless the
// controller is resuming from a later step. It is safe to re-run after a crash.
func (c *Controller) Cutover(ctx context.Context, gateOK func(context.Context) (bool, string, error)) error {
	if err := c.EnsureControl(ctx); err != nil {
		return err
	}
	st, err := c.GetStatus(ctx)
	if err != nil {
		return err
	}
	if st.Mode == "spanner" && st.Step == StepDone {
		c.log().Info("already cut over")
		return nil
	}
	if strings.HasPrefix(st.Step, "rollback") {
		return fmt.Errorf("%w: a rollback is in progress (step %s); finish it with `shadowcut rollback` first", ErrAborted, st.Step)
	}
	// 1. gate (only when starting fresh)
	if st.Step == StepIdle || st.Step == StepRolledBack {
		if gateOK != nil {
			ok, why, err := gateOK(ctx)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("%w: gate says NO-GO: %s", ErrAborted, why)
			}
		}
		if err := c.set(ctx, "cutover", "frozen", StepFrozen, "writes frozen"); err != nil {
			return err
		}
		st.Step = StepFrozen
	}
	// 2. drain
	if st.Step == StepFrozen {
		sleepCtx(ctx, c.SettleTime)
		if err := c.WaitDrained(ctx); err != nil {
			return fmt.Errorf("drain: %w (app stays frozen; rerun cutover or run rollback)", err)
		}
		if err := c.set(ctx, "cutover", "frozen", StepDrained, "CDC drained"); err != nil {
			return err
		}
		st.Step = StepDrained
	}
	// 3. final consistency check, then switch
	if st.Step == StepDrained {
		if err := c.CheckCounts(ctx); err != nil {
			return fmt.Errorf("%w: final check failed: %v (app stays frozen; run rollback)", ErrAborted, err)
		}
		if err := c.set(ctx, "cutover", "spanner", StepDone, "app on Spanner"); err != nil {
			return err
		}
	}
	return nil
}

// Rollback moves the app back to Postgres, replaying every journaled Spanner
// write first. Safe to re-run (journal entries are applied exactly once).
func (c *Controller) Rollback(ctx context.Context) (int, error) {
	if err := c.EnsureControl(ctx); err != nil {
		return 0, err
	}
	st, err := c.GetStatus(ctx)
	if err != nil {
		return 0, err
	}
	if st.Mode == "postgres" && (st.Step == StepIdle || st.Step == StepRolledBack) {
		// Already on Postgres; still apply any outstanding journal entries (e.g. from
		// writes that reached Spanner outside a cutover). Replay is idempotent.
		n, err := c.ReplayJournal(ctx)
		c.log().Info("already on postgres", "journal_entries_replayed", n)
		return n, err
	}
	if st.Step != StepRBReplayed {
		if err := c.set(ctx, "rollback", "frozen", StepRBFrozen, "writes frozen for rollback"); err != nil {
			return 0, err
		}
		sleepCtx(ctx, c.SettleTime)
	}
	n, err := c.ReplayJournal(ctx)
	if err != nil {
		return n, fmt.Errorf("replay journal: %w (app stays frozen; rerun rollback)", err)
	}
	if err := c.set(ctx, "rollback", "frozen", StepRBReplayed, fmt.Sprintf("%d journal entries applied", n)); err != nil {
		return n, err
	}
	if err := c.set(ctx, "rollback", "postgres", StepRolledBack, "app on Postgres"); err != nil {
		return n, err
	}
	return n, nil
}

func sleepCtx(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// WaitDrained waits until Debezium has published everything up to the current
// WAL position and the applier has applied every Kafka record.
func (c *Controller) WaitDrained(ctx context.Context) error {
	var target string
	if err := c.PG.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&target); err != nil {
		return err
	}
	deadline := time.Now().Add(c.DrainTimeout)
	kc, err := kgo.NewClient(kgo.SeedBrokers(c.KafkaBrokers...))
	if err != nil {
		return err
	}
	defer kc.Close()
	adm := kadm.NewClient(kc)
	for {
		slotOK, kafkaOK, detail, err := c.drainState(ctx, adm, target)
		if err == nil && slotOK && kafkaOK {
			c.log().Info("CDC drained", "wal_target", target)
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return err
			}
			return fmt.Errorf("not drained after %s: %s", c.DrainTimeout, detail)
		}
		sleepCtx(ctx, 500*time.Millisecond)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (c *Controller) drainState(ctx context.Context, adm *kadm.Client, target string) (slotOK, kafkaOK bool, detail string, err error) {
	// Debezium confirms LSNs only after Kafka has acknowledged the records.
	err = c.PG.QueryRow(ctx, `SELECT coalesce(bool_and(confirmed_flush_lsn >= $1::pg_lsn), false) FROM pg_replication_slots WHERE slot_type = 'logical'`, target).Scan(&slotOK)
	if err != nil {
		return
	}
	topics, err := adm.ListTopics(ctx)
	if err != nil {
		return
	}
	var names []string
	for _, t := range topics.Sorted() {
		if strings.HasPrefix(t.Topic, c.TopicPrefix) {
			names = append(names, t.Topic)
		}
	}
	ends, err := adm.ListEndOffsets(ctx, names...)
	if err != nil {
		return
	}
	applied := map[string]int64{}
	rows, err := c.SP.Query(ctx, `SELECT topic, kpartition, koffset FROM sc_offsets`)
	if err != nil {
		return
	}
	for rows.Next() {
		var t string
		var p, o int64
		if err = rows.Scan(&t, &p, &o); err != nil {
			rows.Close()
			return
		}
		applied[fmt.Sprintf("%s/%d", t, p)] = o
	}
	rows.Close()
	kafkaOK = true
	var behind []string
	ends.Each(func(o kadm.ListedOffset) {
		if o.Offset <= 0 {
			return // empty partition
		}
		a, ok := applied[fmt.Sprintf("%s/%d", o.Topic, o.Partition)]
		if !ok || a+1 < o.Offset {
			kafkaOK = false
			behind = append(behind, fmt.Sprintf("%s/%d applied=%d end=%d", o.Topic, o.Partition, a, o.Offset))
		}
	})
	detail = fmt.Sprintf("slot_confirmed=%v kafka_applied=%v %s", slotOK, kafkaOK, strings.Join(behind, " "))
	return
}

// CheckCounts compares row counts of the configured tables.
func (c *Controller) CheckCounts(ctx context.Context) error {
	var diffs []string
	for _, t := range c.Tables {
		var a, b int64
		if err := c.PG.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t}.Sanitize()).Scan(&a); err != nil {
			return err
		}
		if err := c.SP.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{t}.Sanitize()).Scan(&b); err != nil {
			return err
		}
		if a != b {
			diffs = append(diffs, fmt.Sprintf("%s: postgres=%d spanner=%d", t, a, b))
		}
	}
	if len(diffs) > 0 {
		return errors.New(strings.Join(diffs, "; "))
	}
	return nil
}

// JournalEntry is one reverse-journal row.
type JournalEntry struct {
	ID, Table, Row string
}

// ReplayJournal applies every unapplied journal entry to Postgres in commit
// order. Each entry is applied together with its journal_applied marker in one
// Postgres transaction, so a crash mid-replay never applies an entry twice.
func (c *Controller) ReplayJournal(ctx context.Context) (int, error) {
	rows, err := c.SP.Query(ctx, `SELECT id, tbl, row_data::text FROM sc_reverse_journal ORDER BY created_at, id`)
	if err != nil {
		return 0, err
	}
	var entries []JournalEntry
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.ID, &e.Table, &e.Row); err != nil {
			rows.Close()
			return 0, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	pkCache := map[string][]string{}
	colCache := map[string][]string{}
	applied := 0
	for _, e := range entries {
		if !validTable(e.Table) {
			return applied, fmt.Errorf("journal entry %s: unexpected table %q", e.ID, e.Table)
		}
		pk, cols, err := c.tableMeta(ctx, e.Table, pkCache, colCache)
		if err != nil {
			return applied, err
		}
		err = pgx.BeginFunc(ctx, c.PG, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `INSERT INTO shadowcut.journal_applied (id) VALUES ($1) ON CONFLICT DO NOTHING`, e.ID)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				return nil // already applied
			}
			var sets []string
			for _, col := range cols {
				if !contains(pk, col) {
					q := pgx.Identifier{col}.Sanitize()
					sets = append(sets, q+" = EXCLUDED."+q)
				}
			}
			tbl := pgx.Identifier{e.Table}.Sanitize()
			sql := fmt.Sprintf(`INSERT INTO %s SELECT * FROM jsonb_populate_record(NULL::%s, $1::jsonb) ON CONFLICT (%s) DO UPDATE SET %s`,
				tbl, tbl, quoteList(pk), strings.Join(sets, ", "))
			if _, err := tx.Exec(ctx, sql, e.Row); err != nil {
				return fmt.Errorf("apply %s to %s: %w", e.ID, e.Table, err)
			}
			applied++
			return nil
		})
		if err != nil {
			return applied, err
		}
	}
	return applied, nil
}

func (c *Controller) tableMeta(ctx context.Context, t string, pkCache, colCache map[string][]string) ([]string, []string, error) {
	if pk, ok := pkCache[t]; ok {
		return pk, colCache[t], nil
	}
	rows, err := c.PG.Query(ctx, `SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = $1::regclass AND i.indisprimary ORDER BY array_position(i.indkey, a.attnum)`, t)
	if err != nil {
		return nil, nil, err
	}
	pk, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	rows, err = c.PG.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`, t)
	if err != nil {
		return nil, nil, err
	}
	cols, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, err
	}
	pkCache[t], colCache[t] = pk, cols
	return pk, cols, nil
}

func validTable(t string) bool {
	if t == "" || len(t) > 63 {
		return false
	}
	for _, r := range t {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func quoteList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = pgx.Identifier{x}.Sanitize()
	}
	return strings.Join(q, ", ")
}
