package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/shadowcut/shadowcut/internal/gate"
	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/loadgen"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/state"
	"github.com/shadowcut/shadowcut/internal/verify"
)

// scenario is one fault-injection case.
type scenario struct {
	Name       string
	Desc       string
	Inject     func(ctx context.Context) error
	Heal       func(ctx context.Context) error
	ExpectNoGo bool // replication is impaired during the fault, so the gate should say NO-GO
}

const toxiAPI = "http://localhost:18474"

func docker(args ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("docker %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return nil
	}
}

func toxi(method, path string, body any) func(context.Context) error {
	return func(ctx context.Context) error {
		var r *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			r = bytes.NewReader(b)
		} else {
			r = bytes.NewReader(nil)
		}
		req, _ := http.NewRequestWithContext(ctx, method, toxiAPI+path, r)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("toxiproxy %s %s: HTTP %d", method, path, resp.StatusCode)
		}
		return nil
	}
}

var scenarios = []scenario{
	{Name: "baseline", Desc: "no fault (control)", Inject: func(context.Context) error { return nil }, Heal: func(context.Context) error { return nil }},
	{Name: "kill_applier", Desc: "docker kill the applier mid-stream, restart after the fault window", ExpectNoGo: true,
		Inject: docker("kill", "shadowcut-applier-1"), Heal: docker("start", "shadowcut-applier-1")},
	{Name: "kafka_partition", Desc: "Toxiproxy cuts the applier <-> Kafka link", ExpectNoGo: true,
		Inject: toxi("POST", "/proxies/kafka_applier", map[string]any{"enabled": false}), Heal: toxi("POST", "/proxies/kafka_applier", map[string]any{"enabled": true})},
	{Name: "spanner_latency", Desc: "Toxiproxy adds 500 ms (+/-100) latency on applier -> Spanner",
		Inject: toxi("POST", "/proxies/spanner_applier/toxics", map[string]any{"name": "lat", "type": "latency", "stream": "downstream", "attributes": map[string]int{"latency": 500, "jitter": 100}}),
		Heal:   toxi("DELETE", "/proxies/spanner_applier/toxics/lat", nil)},
	{Name: "spanner_partition", Desc: "Toxiproxy cuts the applier <-> Spanner link", ExpectNoGo: true,
		Inject: toxi("POST", "/proxies/spanner_applier", map[string]any{"enabled": false}), Heal: toxi("POST", "/proxies/spanner_applier", map[string]any{"enabled": true})},
	{Name: "kill_connect", Desc: "docker kill Kafka Connect (Debezium), restart after the fault window", ExpectNoGo: true,
		Inject: docker("kill", "shadowcut-connect-1"), Heal: docker("start", "shadowcut-connect-1")},
}

type sample struct {
	T        float64 `json:"t_s"`
	Behind   int64   `json:"truth_rows_behind"` // probe rows in Postgres not yet in Spanner (independent ground truth)
	Phase    string  `json:"phase"`
	LagMs    float64 `json:"lag_ms"`
	HBAgeMs  float64 `json:"heartbeat_age_ms"`
	Decision string  `json:"decision"`
	Err      string  `json:"err,omitempty"`
}

type exp4Result struct {
	Scenario              string    `json:"scenario"`
	Desc                  string    `json:"desc"`
	Env                   string    `json:"env"`
	Start                 time.Time `json:"start"`
	FaultSeconds          float64   `json:"fault_seconds"`
	Acked                 int64     `json:"acked_writes"`
	FailedWrites          int64     `json:"failed_writes"`
	InSpanner             int64     `json:"rows_in_spanner"`
	Lost                  int64     `json:"lost"`
	Extra                 int64     `json:"extra"`
	ValueMismatch         int64     `json:"value_mismatch"`
	DupDeliveries         int64     `json:"duplicate_deliveries_kafka"`
	DupEffects            int64     `json:"duplicate_effects"`
	RecoverySeconds       float64   `json:"recovery_seconds"`
	DrainAfterStopSeconds float64   `json:"drain_after_writer_stop_seconds"`
	ExpectNoGo            bool      `json:"expect_nogo"`
	NoGoDuringFault       bool      `json:"nogo_during_fault"`
	GoAfterRecovery       bool      `json:"go_after_recovery"`
	GateCorrect           bool      `json:"gate_correct"`
	FalseGo               int       `json:"false_go_samples"`
	FalseNoGo             int       `json:"false_nogo_samples"`
	MaxLagMs              float64   `json:"max_lag_ms"`
	Timeline              []sample  `json:"timeline"`
	Notes                 string    `json:"notes,omitempty"`
}

func exp4Cmd() *cobra.Command {
	var only []string
	var pre, fault, post time.Duration
	var rate float64
	var out string
	c := &cobra.Command{
		Use:   "exp4",
		Short: "Experiment 4: migration safety under faults",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			pg, err := pgutil.Connect(ctx, pgutil.DefaultPG(), 8)
			if err != nil {
				return err
			}
			defer pg.Close()
			sp, err := pgutil.Connect(ctx, pgutil.DefaultSpanner(), 8)
			if err != nil {
				return err
			}
			defer sp.Close()
			want := map[string]bool{}
			for _, o := range only {
				want[o] = true
			}
			for _, sc := range scenarios {
				if len(want) > 0 && !want[sc.Name] {
					continue
				}
				r, err := runScenario(ctx, pg, sp, sc, pre, fault, post, rate)
				if err != nil {
					return fmt.Errorf("%s: %w", sc.Name, err)
				}
				_ = state.WriteJSON(filepath.Join(out, sc.Name+".json"), r)
				slog.Info("scenario done", "scenario", sc.Name, "acked", r.Acked, "lost", r.Lost, "dup_effects", r.DupEffects, "dup_deliveries", r.DupDeliveries, "recovery_s", r.RecoverySeconds, "gate_correct", r.GateCorrect)
			}
			if len(want) == 0 {
				if crash, err := runCrashMidCutover(ctx, pg, sp); err != nil {
					slog.Error("crash_mid_cutover failed", "err", err)
				} else {
					_ = state.WriteJSON(filepath.Join(out, "crash_mid_cutover.json"), crash)
				}
			}
			return writeExp4Summary(out)
		},
	}
	c.Flags().StringSliceVar(&only, "only", nil, "run only these scenarios")
	c.Flags().DurationVar(&pre, "pre", 10*time.Second, "steady state before the fault")
	c.Flags().DurationVar(&fault, "fault", 20*time.Second, "fault duration")
	c.Flags().DurationVar(&post, "post", 15*time.Second, "writing continues this long after healing")
	c.Flags().Float64Var(&rate, "rate", 50, "probe writes per second")
	c.Flags().StringVar(&out, "out", "results/exp4", "output directory")
	return c
}

// replicationDecision evaluates the gate with every non-replication signal
// pre-satisfied, so the decision isolates what the fault does to replication.
func replicationDecision(ctx context.Context, pg, sp *pgxpool.Pool) (gate.Decision, float64, float64, string) {
	now := time.Now()
	sig := gate.Signals{
		Shadow: &shadow.Summary{UpdatedAt: now, Compared: 1_000_000},
		Verify: &verify.Summary{UpdatedAt: now, Rounds: 1},
		Scan:   &hazard.Report{},
	}
	lctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	lag, hb, err := gate.MeasureLag(lctx, pg, sp)
	var errS string
	if err != nil {
		sig.LagErr = err.Error()
		errS = err.Error()
	} else {
		sig.Lag, sig.HeartbeatAge = &lag, &hb
	}
	gate.MeasureBacklog(lctx, &sig, strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ","), sp)
	if sig.BacklogAge != nil && *sig.BacklogAge > lag {
		lag = *sig.BacklogAge // effective lag: the gate uses the larger of the two
	}
	if sig.BacklogErr != "" {
		errS += " backlog: " + sig.BacklogErr
	}
	d := gate.Evaluate(gate.DefaultPolicy(), sig, now)
	return d, float64(lag.Milliseconds()), float64(hb.Milliseconds()), errS
}

func runScenario(ctx context.Context, pg, sp *pgxpool.Pool, sc scenario, pre, fault, post time.Duration, rate float64) (exp4Result, error) {
	r := exp4Result{Scenario: sc.Name, Desc: sc.Desc, Env: envLabel, Start: time.Now().UTC(), FaultSeconds: fault.Seconds(), ExpectNoGo: sc.ExpectNoGo}
	writer := fmt.Sprintf("x4-%s-%d", sc.Name, time.Now().Unix()%100000)
	if len(writer) > 32 {
		writer = writer[:32]
	}
	dupBefore, err := auditDupes(ctx, sp)
	if err != nil {
		return r, err
	}
	// writer
	wctx, wcancel := context.WithTimeout(ctx, pre+fault+post)
	defer wcancel()
	var res loadgen.Result
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		res = loadgen.ProbeWriter{Pool: pg, Writer: writer, Rate: rate}.Run(wctx)
	}()
	start := time.Now()
	phase := "pre"
	var mu sync.Mutex
	stopSampling := make(chan struct{})
	samplingDone := make(chan struct{})
	go func() {
		defer close(samplingDone)
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopSampling:
				return
			case <-t.C:
			}
			d, lag, hb, e := replicationDecision(ctx, pg, sp)
			var np, ns int64
			_ = pg.QueryRow(ctx, `SELECT count(*) FROM probe_events WHERE writer = $1`, writer).Scan(&np)
			_ = sp.QueryRow(ctx, `SELECT count(*) FROM probe_events WHERE writer = $1`, writer).Scan(&ns)
			mu.Lock()
			r.Timeline = append(r.Timeline, sample{T: time.Since(start).Seconds(), Behind: np - ns, Phase: phase, LagMs: lag, HBAgeMs: hb, Decision: d.Verdict(), Err: e})
			mu.Unlock()
		}
	}()
	time.Sleep(pre)
	slog.Info("injecting fault", "scenario", sc.Name)
	if err := sc.Inject(ctx); err != nil {
		close(stopSampling)
		return r, err
	}
	mu.Lock()
	phase = "fault"
	mu.Unlock()
	time.Sleep(fault)
	if err := sc.Heal(ctx); err != nil {
		close(stopSampling)
		return r, err
	}
	healed := time.Now()
	healT := time.Since(start).Seconds()
	mu.Lock()
	phase = "recovery"
	mu.Unlock()
	slog.Info("healed", "scenario", sc.Name)
	wg.Wait()
	r.Acked, r.FailedWrites = res.Acked, res.Failed

	// recovery: all acknowledged rows in Spanner and lag back under 2s
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var n int64
		_ = sp.QueryRow(ctx, `SELECT count(*) FROM probe_events WHERE writer = $1`, writer).Scan(&n)
		d, lag, _, _ := replicationDecision(ctx, pg, sp)
		if n >= res.Acked && lag < 2000 && d.Go {
			r.DrainAfterStopSeconds = time.Since(healed).Seconds() - post.Seconds()
			break
		}
		if time.Now().After(deadline) {
			r.DrainAfterStopSeconds = -1
			r.Notes = "did not drain within 5 minutes"
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	time.Sleep(3 * time.Second) // a few post-recovery gate samples
	close(stopSampling)
	<-samplingDone

	// correctness
	pgRows, err := probeRows(ctx, pg, writer)
	if err != nil {
		return r, err
	}
	spRows, err := probeRows(ctx, sp, writer)
	if err != nil {
		return r, err
	}
	r.InSpanner = int64(len(spRows))
	for _, seq := range res.AckedSeqs {
		if _, ok := spRows[seq]; !ok {
			r.Lost++
		}
	}
	for seq, payload := range spRows {
		p, ok := pgRows[seq]
		if !ok {
			r.Extra++
		} else if p != payload {
			r.ValueMismatch++
		}
	}
	dupAfter, err := auditDupes(ctx, sp)
	if err != nil {
		return r, err
	}
	r.DupEffects = dupAfter - dupBefore
	r.DupDeliveries, err = kafkaDuplicates(ctx, writer)
	if err != nil {
		r.Notes += " kafka duplicate count failed: " + err.Error()
	}
	for _, s := range r.Timeline {
		if s.LagMs > r.MaxLagMs {
			r.MaxLagMs = s.LagMs
		}
		if s.Phase == "fault" && s.Decision == "NO-GO" {
			r.NoGoDuringFault = true
		}
	}
	// recovery: first sample after healing where replication is healthy again
	r.RecoverySeconds = -1
	for _, s := range r.Timeline {
		if s.T >= healT && s.LagMs < 2000 && s.Decision == "GO" {
			r.RecoverySeconds = s.T - healT
			break
		}
	}
	if n := len(r.Timeline); n > 0 {
		r.GoAfterRecovery = r.Timeline[n-1].Decision == "GO"
	}
	// Ground truth: rows Spanner is missing, converted to seconds of writes. The
	// gate is unsafe if it says GO while truly more than MaxLag (+1s) behind, and
	// over-conservative if it says NO-GO while truly less than MaxLag (-1s) behind.
	maxLag := gate.DefaultPolicy().MaxLag.Seconds()
	for _, s := range r.Timeline {
		behindS := float64(s.Behind) / rate
		if s.Decision == "GO" && behindS > maxLag+1 {
			r.FalseGo++
		}
		if s.Decision == "NO-GO" && behindS < maxLag-1 {
			r.FalseNoGo++
		}
	}
	r.GateCorrect = r.FalseGo == 0 && r.GoAfterRecovery
	return r, nil
}

func probeRows(ctx context.Context, q *pgxpool.Pool, writer string) (map[int64]string, error) {
	rows, err := q.Query(ctx, `SELECT seq, payload FROM probe_events WHERE writer = $1`, writer)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var s int64
		var p string
		if err := rows.Scan(&s, &p); err != nil {
			return nil, err
		}
		out[s] = p
	}
	return out, rows.Err()
}

// auditDupes counts Kafka records whose effect the applier applied more than once.
func auditDupes(ctx context.Context, sp *pgxpool.Pool) (int64, error) {
	var n int64
	err := sp.QueryRow(ctx, `SELECT count(*) FROM sc_apply_audit WHERE times > 1`).Scan(&n)
	return n, err
}

// kafkaDuplicates counts probe rows of writer delivered more than once to Kafka
// (Debezium is at-least-once; the applier must absorb these).
func kafkaDuplicates(ctx context.Context, writer string) (int64, error) {
	brokers := strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ",")
	topic := "sc.public.probe_events"
	adm, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return 0, err
	}
	ends, err := kadm.NewClient(adm).ListEndOffsets(ctx, topic)
	adm.Close()
	if err != nil {
		return 0, err
	}
	parts := map[string]map[int32]kgo.Offset{topic: {}}
	remaining := map[int32]int64{}
	ends.Each(func(o kadm.ListedOffset) {
		if o.Offset > 0 {
			parts[topic][o.Partition] = kgo.NewOffset().AtStart()
			remaining[o.Partition] = o.Offset
		}
	})
	if len(remaining) == 0 {
		return 0, nil
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumePartitions(parts))
	if err != nil {
		return 0, err
	}
	defer cl.Close()
	counts := map[string]int{}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for len(remaining) > 0 {
		fs := cl.PollFetches(cctx)
		if cctx.Err() != nil {
			return 0, fmt.Errorf("timed out reading %s", topic)
		}
		fs.EachRecord(func(rec *kgo.Record) {
			if end, ok := remaining[rec.Partition]; ok && rec.Offset >= end-1 {
				delete(remaining, rec.Partition)
			}
			if bytes.Contains(rec.Key, []byte(`"`+writer+`"`)) {
				counts[string(rec.Key)]++
			}
		})
	}
	var d int64
	for _, c := range counts {
		if c > 1 {
			d += int64(c - 1)
		}
	}
	return d, nil
}

func writeExp4Summary(dir string) error {
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	var b strings.Builder
	b.WriteString("| scenario | fault | acked writes | lost | extra | value mismatch | dup deliveries (Kafka) | dup effects | recovery (s) | max lag (s) | NO-GO during fault | false GO samples | false NO-GO samples | gate safe |\n|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, f := range files {
		var r exp4Result
		if state.ReadJSON(f, &r) != nil || r.Desc == "" {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %d | %d | %.1f | %.1f | %v | %d | %d | %v |\n", r.Scenario, r.Desc, r.Acked, r.Lost, r.Extra, r.ValueMismatch, r.DupDeliveries, r.DupEffects, r.RecoverySeconds, r.MaxLagMs/1000, r.NoGoDuringFault, r.FalseGo, r.FalseNoGo, r.GateCorrect)
	}
	for _, f := range summaryExtras {
		f(dir, &b)
	}
	return os.WriteFile(filepath.Join(dir, "summary.md"), []byte(b.String()), 0o644)
}

func init() {
	// crash_mid_cutover has its own table
	summaryExtras = append(summaryExtras, func(dir string, b *strings.Builder) {
		var c cutoverCrashResult
		if state.ReadJSON(filepath.Join(dir, "crash_mid_cutover.json"), &c) != nil {
			return
		}
		fmt.Fprintf(b, "\n**crash_mid_cutover**: app writes acknowledged %d (by mode %v), rejected while frozen %d, other errors %d; lost in Postgres %d; missing in Spanner after CDC %d; duplicate rows %d; freeze %.1fs (cutover incl. crash + 3s operator delay) / %.1fs (rollback); journal entries replayed %d.\n\nSteps:\n\n", c.AckedWrites, c.AckedByMode, c.RejectedFrozen, c.OtherErrors, c.LostInPostgres, c.MissingInSpanner, c.DuplicateRows, c.FreezeSeconds["cutover"], c.FreezeSeconds["rollback"], c.JournalReplayed)
		for _, s := range c.Steps {
			fmt.Fprintf(b, "    %s\n", s)
		}
	})
}

var summaryExtras []func(string, *strings.Builder)
