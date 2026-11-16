//go:build integration

// Integration test for the cutover -> write on Spanner -> rollback path. It needs
// the running local stack (Postgres, Spanner emulator + PGAdapter, Kafka,
// Debezium, sc-applier): `make up` / scripts/up.sh, then
//
//	SHADOWCUT_STACK=1 go test -tags integration ./internal/cutover/ -run TestCutoverRollback -v
package cutover

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/shadowcut/shadowcut/internal/app"
	"github.com/shadowcut/shadowcut/internal/pgutil"
)

func TestCutoverRollback(t *testing.T) {
	if os.Getenv("SHADOWCUT_STACK") != "1" {
		t.Skip("set SHADOWCUT_STACK=1 with the local stack running")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pg, err := pgutil.Connect(ctx, pgutil.DefaultPG(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	sp, err := pgutil.Connect(ctx, pgutil.DefaultSpanner(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()

	ctl := &Controller{PG: pg, SP: sp, KafkaBrokers: strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ","),
		TopicPrefix: "sc.public.", SettleTime: 700 * time.Millisecond, DrainTimeout: 2 * time.Minute,
		Tables: []string{"edits", "pages", "gh_events", "probe_events"}}
	if err := ctl.EnsureControl(ctx); err != nil {
		t.Fatal(err)
	}
	// start from a clean postgres state
	if _, err := ctl.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	srv := app.New(pg, pg, sp, noop.NewMeterProvider().Meter("test"))
	pctx, stopPoll := context.WithCancel(ctx)
	defer stopPoll()
	go srv.PollControl(pctx, 100*time.Millisecond)

	user := "rollback-tester"
	title := "Shadowcut rollback " + time.Now().Format("150405.000")
	one, two := int64(100), int64(150)
	if _, err := srv.CreateEdit(ctx, app.EditInput{Wiki: "testwiki", Title: title, User: &user, OldLen: &one, NewLen: &two}); err != nil {
		t.Fatalf("postgres-mode write: %v", err)
	}

	// cutover (gate skipped: this test exercises the mechanics)
	start := time.Now()
	if err := ctl.Cutover(ctx, nil); err != nil {
		t.Fatalf("cutover: %v", err)
	}
	// BUG-003 regression: the write freeze must not wait for Connect's 60s offset flush.
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("cutover took %s; drain should finish within seconds", d)
	} else {
		t.Logf("cutover (freeze -> switch) took %s", d.Round(time.Millisecond))
	}
	waitMode(t, srv, app.ModeSpanner)

	// writes on Spanner, journaled
	three := int64(175)
	res, err := srv.CreateEdit(ctx, app.EditInput{Wiki: "testwiki", Title: title, User: &user, OldLen: &two, NewLen: &three})
	if err != nil {
		t.Fatalf("spanner-mode write: %v", err)
	}
	spannerEditID := res["id"].(int64)
	if spannerEditID < 1<<40 {
		t.Fatalf("expected a bit-reversed sequence id, got %d", spannerEditID)
	}
	n, err := srv.TouchPage(ctx, "testwiki", title)
	if err != nil {
		t.Fatalf("touch on spanner: %v", err)
	}
	if n != 3 {
		t.Fatalf("edit_count after touch = %d, want 3", n)
	}

	// rollback
	applied, err := ctl.Rollback(ctx)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if applied < 3 {
		t.Fatalf("expected >= 3 journal entries applied, got %d", applied)
	}
	waitMode(t, srv, app.ModePostgres)

	// Postgres now has the Spanner-era writes
	var cnt int64
	if err := pg.QueryRow(ctx, `SELECT count(*) FROM edits WHERE id = $1`, spannerEditID).Scan(&cnt); err != nil || cnt != 1 {
		t.Fatalf("spanner-era edit %d not in postgres (count=%d, err=%v)", spannerEditID, cnt, err)
	}
	var ec int64
	if err := pg.QueryRow(ctx, `SELECT edit_count FROM pages WHERE wiki = 'testwiki' AND title = $1`, title).Scan(&ec); err != nil || ec != 3 {
		t.Fatalf("pages.edit_count in postgres = %d (err %v), want 3", ec, err)
	}
	// rollback is idempotent
	again, err := ctl.Rollback(ctx)
	if err != nil || again != 0 {
		t.Fatalf("second rollback applied %d (err %v), want 0", again, err)
	}
	// writes work again on postgres
	if _, err := srv.CreateEdit(ctx, app.EditInput{Wiki: "testwiki", Title: title, User: &user}); err != nil {
		t.Fatalf("postgres write after rollback: %v", err)
	}
}

func waitMode(t *testing.T, s *app.Server, m app.Mode) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for s.Mode() != m {
		if time.Now().After(deadline) {
			t.Fatalf("app did not switch to %s (still %s)", m, s.Mode())
		}
		time.Sleep(50 * time.Millisecond)
	}
}
