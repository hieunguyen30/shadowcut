package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/shadowcut/shadowcut/internal/app"
	"github.com/shadowcut/shadowcut/internal/cutover"
	"github.com/shadowcut/shadowcut/internal/pgutil"
)

type cutoverCrashResult struct {
	Scenario         string             `json:"scenario"`
	Env              string             `json:"env"`
	Start            time.Time          `json:"start"`
	Steps            []string           `json:"steps"`
	AckedWrites      int                `json:"acked_writes"`
	RejectedFrozen   int                `json:"rejected_while_frozen"`
	OtherErrors      int                `json:"other_errors"`
	AckedByMode      map[string]int     `json:"acked_by_mode"`
	LostInPostgres   int                `json:"lost_in_postgres"`
	MissingInSpanner int                `json:"missing_in_spanner_after_cdc"`
	DuplicateRows    int                `json:"duplicate_rows"`
	FreezeSeconds    map[string]float64 `json:"freeze_seconds"`
	JournalReplayed  int                `json:"journal_entries_replayed"`
	ErrorsSample     []string           `json:"errors_sample,omitempty"`
}

var errSimulatedCrash = errors.New("simulated crash")

// runCrashMidCutover crashes the cutover after freezing writes and the rollback
// after its freeze, resumes both, and verifies that every write the app
// acknowledged survives in Postgres (the final system of record) and reaches
// Spanner through CDC.
func runCrashMidCutover(ctx context.Context, pg, sp *pgxpool.Pool) (cutoverCrashResult, error) {
	r := cutoverCrashResult{Scenario: "crash_mid_cutover", Env: envLabel, Start: time.Now().UTC(), AckedByMode: map[string]int{}, FreezeSeconds: map[string]float64{}}
	step := func(s string) {
		r.Steps = append(r.Steps, fmt.Sprintf("%6.1fs %s", time.Since(r.Start).Seconds(), s))
		slog.Info("crash_mid_cutover", "step", s)
	}

	// Every writer must honour the freeze; the live ingestor writes to Postgres
	// directly, so it is paused for the duration (see README "Where it fails").
	_ = docker("pause", "shadowcut-ingest-wiki-1")(ctx)
	defer docker("unpause", "shadowcut-ingest-wiki-1")(context.Background())

	ctl := &cutover.Controller{PG: pg, SP: sp, KafkaBrokers: strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ","),
		TopicPrefix: "sc.public.", SettleTime: 700 * time.Millisecond, DrainTimeout: 2 * time.Minute,
		Tables: []string{"edits", "pages", "gh_events", "probe_events"}}
	if _, err := ctl.Rollback(ctx); err != nil {
		return r, err
	}
	srv := app.New(pg, pg, sp, noop.NewMeterProvider().Meter("exp4"))
	pctx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go srv.PollControl(pctx, 100*time.Millisecond)

	// app writer
	var mu sync.Mutex
	var acked []string
	wctx, stopWriter := context.WithCancel(ctx)
	defer stopWriter()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		user := "exp4-writer"
		for i := 0; ; i++ {
			select {
			case <-wctx.Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			mode := srv.Mode()
			old, nw := int64(100+i), int64(110+i)
			res, err := srv.CreateEdit(wctx, app.EditInput{Wiki: "exp4wiki", Title: fmt.Sprintf("Cutover page %d", i%20), User: &user, OldLen: &old, NewLen: &nw})
			mu.Lock()
			switch {
			case err == nil:
				acked = append(acked, res["event_id"].(string))
				r.AckedByMode[string(mode)]++
			case strings.Contains(err.Error(), "frozen"):
				r.RejectedFrozen++
			case wctx.Err() != nil:
			default:
				r.OtherErrors++
				if len(r.ErrorsSample) < 5 {
					r.ErrorsSample = append(r.ErrorsSample, err.Error())
				}
			}
			mu.Unlock()
		}
	}()
	time.Sleep(3 * time.Second)

	// 1. cutover crashes right after freezing writes
	step("cutover (crash injected after freeze)")
	ctl.Hook = func(s string) error {
		if s == cutover.StepFrozen {
			return errSimulatedCrash
		}
		return nil
	}
	t0 := time.Now()
	if err := ctl.Cutover(ctx, nil); !errors.Is(err, errSimulatedCrash) {
		return r, fmt.Errorf("expected simulated crash, got %v", err)
	}
	st, _ := ctl.GetStatus(ctx)
	step("crashed; control state = " + st.Mode + "/" + st.Step)
	time.Sleep(3 * time.Second) // operator notices
	// 2. resume
	ctl.Hook = nil
	step("cutover resumed")
	if err := ctl.Cutover(ctx, nil); err != nil {
		return r, fmt.Errorf("resume cutover: %w", err)
	}
	r.FreezeSeconds["cutover"] = time.Since(t0).Seconds()
	st, _ = ctl.GetStatus(ctx)
	step("cutover complete; state = " + st.Mode + "/" + st.Step)
	// Emulator-only workaround: the emulator runs one read-write transaction at a
	// time and the applier's per-second heartbeat transactions starve app writes
	// (results/emulator-limits). Postgres writes are frozen/switched, so pausing the
	// applier while the app is on Spanner loses nothing; it is resumed before rollback.
	_ = docker("stop", "shadowcut-applier-1")(ctx)
	step("applier stopped gracefully (emulator single-RW-transaction workaround)")
	time.Sleep(spannerWindow)
	_ = docker("start", "shadowcut-applier-1")(ctx)

	// 3. rollback crashes after freezing
	step("rollback (crash injected after freeze)")
	ctl.Hook = func(s string) error {
		if s == cutover.StepRBFrozen {
			return errSimulatedCrash
		}
		return nil
	}
	t1 := time.Now()
	if _, err := ctl.Rollback(ctx); !errors.Is(err, errSimulatedCrash) {
		return r, fmt.Errorf("expected simulated rollback crash, got %v", err)
	}
	st, _ = ctl.GetStatus(ctx)
	step("crashed; control state = " + st.Mode + "/" + st.Step)
	time.Sleep(2 * time.Second)
	ctl.Hook = nil
	n, err := ctl.Rollback(ctx)
	if err != nil {
		return r, fmt.Errorf("resume rollback: %w", err)
	}
	r.JournalReplayed = n
	r.FreezeSeconds["rollback"] = time.Since(t1).Seconds()
	step(fmt.Sprintf("rollback complete; %d journal entries replayed", n))
	time.Sleep(3 * time.Second) // writes on Postgres again
	stopWriter()
	wg.Wait()
	r.AckedWrites = len(acked)

	// 4. verify: every acknowledged write is in Postgres exactly once ...
	pgIDs, dupes, err := eventIDs(ctx, pg, "exp4wiki")
	if err != nil {
		return r, err
	}
	r.DuplicateRows = dupes
	for _, id := range acked {
		if !pgIDs[id] {
			r.LostInPostgres++
		}
	}
	// ... and reaches Spanner through CDC
	deadline := time.Now().Add(2 * time.Minute)
	for {
		spIDs, _, err := eventIDs(ctx, sp, "exp4wiki")
		if err != nil {
			return r, err
		}
		missing := 0
		for _, id := range acked {
			if !spIDs[id] {
				missing++
			}
		}
		r.MissingInSpanner = missing
		if missing == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	step(fmt.Sprintf("verified: acked=%d lost=%d missing_in_spanner=%d dupes=%d", r.AckedWrites, r.LostInPostgres, r.MissingInSpanner, r.DuplicateRows))
	return r, nil
}

func eventIDs(ctx context.Context, q *pgxpool.Pool, wiki string) (map[string]bool, int, error) {
	rows, err := q.Query(ctx, `SELECT event_id::text FROM edits WHERE wiki = $1`, wiki)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := map[string]bool{}
	dupes := 0
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, 0, err
		}
		if out[id] {
			dupes++
		}
		out[id] = true
	}
	return out, dupes, rows.Err()
}

// spannerWindow is how long the crash scenario writes in Spanner mode. On the
// emulator each app write takes ~2 s and contends with the applier's heartbeat
// transactions (results/emulator-limits), so the window is generous.
var spannerWindow = 30 * time.Second
