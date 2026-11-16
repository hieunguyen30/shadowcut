// Package gate implements Layer 3: a GO / NO-GO cutover decision that combines
// shadow-read mismatch rates by category, replication lag, verifier drift, and
// unresolved high-severity hazards from the scanner.
package gate

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"github.com/shadowcut/shadowcut/internal/cdc"
	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/verify"
)

// Policy holds the thresholds. Zero values mean "use default".
type Policy struct {
	// MinCompared is the minimum number of shadow comparisons required as evidence.
	MinCompared int64 `yaml:"min_compared"`
	// MaxMismatchRate per category (fraction of all comparisons). Categories not
	// listed use DefaultMaxRate. Categories in Accept are ignored.
	MaxMismatchRate map[classify.Category]float64 `yaml:"max_mismatch_rate"`
	DefaultMaxRate  float64                       `yaml:"default_max_rate"`
	// Accept lists categories the team has reviewed and accepted (e.g. a
	// documented sort-order change). They are reported but do not block.
	Accept []classify.Category `yaml:"accept"`
	// MaxLag is the maximum replication lag (heartbeat-based).
	MaxLag Duration `yaml:"max_lag"`
	// MaxHeartbeatAge: the source heartbeat must be fresher than this (CDC alive).
	MaxHeartbeatAge Duration `yaml:"max_heartbeat_age"`
	// MaxOpenDrift is the maximum number of confirmed, unresolved drift rows.
	MaxOpenDrift int `yaml:"max_open_drift"`
	// MaxVerifyAge: the verifier must have completed a round within this window.
	MaxVerifyAge Duration `yaml:"max_verify_age"`
	// MaxShadowAge: the shadow summary must be this fresh.
	MaxShadowAge Duration `yaml:"max_shadow_age"`
	// MaxUnresolvedHigh is the maximum number of unwaived high-severity hazards.
	MaxUnresolvedHigh int `yaml:"max_unresolved_high"`
}

// Duration is a YAML-friendly time.Duration ("5s").
type Duration struct{ time.Duration }

// UnmarshalYAML parses "5s".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalYAML renders "5s".
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// DefaultPolicy is conservative: any unaccepted behavioural mismatch blocks.
func DefaultPolicy() Policy {
	return Policy{
		MinCompared:       200,
		DefaultMaxRate:    0,
		MaxMismatchRate:   map[classify.Category]float64{classify.ReplicationLag: 0.05},
		MaxLag:            Duration{5 * time.Second},
		MaxHeartbeatAge:   Duration{15 * time.Second},
		MaxOpenDrift:      0,
		MaxVerifyAge:      Duration{15 * time.Minute},
		MaxShadowAge:      Duration{15 * time.Minute},
		MaxUnresolvedHigh: 0,
	}
}

// LoadPolicy reads YAML over the defaults.
func LoadPolicy(path string) (Policy, error) {
	p := DefaultPolicy()
	if path == "" {
		return p, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return p, err
	}
	if err := yaml.Unmarshal(b, &p); err != nil {
		return p, err
	}
	return p, nil
}

// Signals are the inputs to a decision. Nil/zero fields are "missing".
type Signals struct {
	Shadow       *shadow.Summary
	Verify       *verify.Summary
	Scan         *hazard.Report
	Lag          *time.Duration // replication lag measured from heartbeats
	HeartbeatAge *time.Duration // age of the source heartbeat (CDC liveness)
	// Backlog: unapplied CDC records and the age of the oldest one (per-partition
	// measurement; the heartbeat alone can run ahead of a data backlog, BUG-005).
	BacklogRecords *int64
	BacklogAge     *time.Duration
	BacklogErr     string
	LagErr         string
}

// Check is one evaluated condition.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | fail | warn
	Detail string `json:"detail"`
}

// Decision is the gate output.
type Decision struct {
	Go     bool      `json:"go"`
	At     time.Time `json:"at"`
	Checks []Check   `json:"checks"`
}

// Verdict renders GO / NO-GO.
func (d Decision) Verdict() string {
	if d.Go {
		return "GO"
	}
	return "NO-GO"
}

// Evaluate applies the policy to the signals. Missing evidence fails closed.
func Evaluate(p Policy, s Signals, now time.Time) Decision {
	d := Decision{At: now.UTC()}
	add := func(name string, ok bool, detail string) {
		st := "pass"
		if !ok {
			st = "fail"
		}
		d.Checks = append(d.Checks, Check{Name: name, Status: st, Detail: detail})
	}
	warn := func(name, detail string) {
		d.Checks = append(d.Checks, Check{Name: name, Status: "warn", Detail: detail})
	}

	// 1. shadow evidence
	if s.Shadow == nil {
		add("shadow.evidence", false, "no shadow-read summary found (run `shadowcut shadow proxy` or `replay`)")
	} else {
		age := now.Sub(s.Shadow.UpdatedAt)
		add("shadow.fresh", age <= p.MaxShadowAge.Duration, fmt.Sprintf("summary age %s (max %s)", age.Round(time.Second), p.MaxShadowAge))
		add("shadow.evidence", s.Shadow.Compared >= p.MinCompared, fmt.Sprintf("%d comparisons (min %d)", s.Shadow.Compared, p.MinCompared))
		accepted := map[classify.Category]bool{}
		for _, c := range p.Accept {
			accepted[c] = true
		}
		cats := make([]string, 0)
		for c := range s.Shadow.ByCategory {
			cats = append(cats, string(c))
		}
		sort.Strings(cats)
		for _, cs := range cats {
			c := classify.Category(cs)
			n := s.Shadow.ByCategory[c]
			rate := float64(n) / float64(max64(s.Shadow.Compared, 1))
			limit, ok := p.MaxMismatchRate[c]
			if !ok {
				limit = p.DefaultMaxRate
			}
			detail := fmt.Sprintf("%d mismatches = %.2f%% of reads (max %.2f%%)%s", n, 100*rate, 100*limit, topQueries(s.Shadow, c))
			if accepted[c] {
				warn("shadow."+cs, detail+" [accepted by policy]")
				continue
			}
			add("shadow."+cs, rate <= limit, detail)
		}
		if s.Shadow.Dropped > 0 {
			warn("shadow.dropped", fmt.Sprintf("%d reads were not shadowed (queue full); evidence is a sample", s.Shadow.Dropped))
		}
	}

	// 2. replication lag and CDC liveness
	switch {
	case s.LagErr != "":
		add("replication.lag", false, "could not measure lag: "+s.LagErr)
	case s.Lag == nil:
		add("replication.lag", false, "lag not measured")
	default:
		lag := *s.Lag
		src := "heartbeat"
		if s.BacklogAge != nil && *s.BacklogAge > lag {
			lag, src = *s.BacklogAge, "oldest unapplied CDC record"
		}
		add("replication.lag", lag <= p.MaxLag.Duration, fmt.Sprintf("lag %s from %s (max %s)", lag.Round(time.Millisecond), src, p.MaxLag))
	}
	switch {
	case s.BacklogErr != "":
		add("replication.backlog", false, "could not measure the CDC backlog: "+s.BacklogErr)
	case s.BacklogRecords != nil:
		detail := fmt.Sprintf("%d unapplied CDC records", *s.BacklogRecords)
		if s.BacklogAge != nil && *s.BacklogAge > 0 {
			detail += fmt.Sprintf(", oldest %s old", s.BacklogAge.Round(time.Millisecond))
		}
		add("replication.backlog", s.BacklogAge == nil || *s.BacklogAge <= p.MaxLag.Duration, detail)
	}
	if s.HeartbeatAge != nil {
		add("replication.heartbeat", *s.HeartbeatAge <= p.MaxHeartbeatAge.Duration, fmt.Sprintf("source heartbeat age %s (max %s): CDC connector %s", s.HeartbeatAge.Round(time.Millisecond), p.MaxHeartbeatAge, alive(*s.HeartbeatAge <= p.MaxHeartbeatAge.Duration)))
	}

	// 3. verifier
	if s.Verify == nil {
		add("verify.evidence", false, "no verifier summary found (run `shadowcut verify`)")
	} else {
		age := now.Sub(s.Verify.UpdatedAt)
		add("verify.fresh", age <= p.MaxVerifyAge.Duration, fmt.Sprintf("last round %s ago (max %s), %d rounds, %d rows compared", age.Round(time.Second), p.MaxVerifyAge, s.Verify.Rounds, s.Verify.RowsCompared))
		var keys []string
		for i, dr := range s.Verify.OpenDrift {
			if i == 3 {
				keys = append(keys, "...")
				break
			}
			keys = append(keys, dr.Table+":"+dr.Key+"("+dr.Kind+")")
		}
		add("verify.drift", len(s.Verify.OpenDrift) <= p.MaxOpenDrift, fmt.Sprintf("%d open drift rows (max %d) %s", len(s.Verify.OpenDrift), p.MaxOpenDrift, strings.Join(keys, " ")))
	}

	// 4. static hazards
	if s.Scan == nil {
		add("scan.evidence", false, "no scan report found (run `shadowcut scan --json`)")
	} else {
		n := s.Scan.Unresolved(hazard.High)
		var ids []string
		seen := map[string]bool{}
		for _, f := range s.Scan.Findings {
			if f.Waived == "" && f.Severity == hazard.High && !seen[f.Rule] {
				seen[f.Rule] = true
				ids = append(ids, f.Rule)
			}
		}
		add("scan.high", n <= p.MaxUnresolvedHigh, fmt.Sprintf("%d unresolved high-severity hazards (max %d) %s", n, p.MaxUnresolvedHigh, strings.Join(ids, ", ")))
	}

	d.Go = true
	for _, c := range d.Checks {
		if c.Status == "fail" {
			d.Go = false
		}
	}
	return d
}

func alive(ok bool) string {
	if ok {
		return "alive"
	}
	return "STALLED"
}

func topQueries(s *shadow.Summary, c classify.Category) string {
	type qc struct {
		name string
		n    int64
	}
	var qs []qc
	for _, q := range s.Queries {
		if n := q.ByCategory[c]; n > 0 {
			name := q.Name
			if name == "" {
				name = q.Fingerprint
				if len(name) > 40 {
					name = name[:40] + "..."
				}
			}
			qs = append(qs, qc{name, n})
		}
	}
	sort.Slice(qs, func(i, j int) bool { return qs[i].n > qs[j].n || (qs[i].n == qs[j].n && qs[i].name < qs[j].name) })
	var parts []string
	for i, q := range qs {
		if i == 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s×%d", q.name, q.n))
	}
	if len(parts) == 0 {
		return ""
	}
	return "; top: " + strings.Join(parts, ", ")
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// MeasureLag reads the heartbeat row on both sides. lag = source ts - target ts;
// heartbeat age = now (per the source clock) - source ts.
func MeasureLag(ctx context.Context, pg, sp *pgxpool.Pool) (lag, hbAge time.Duration, err error) {
	var pts, now time.Time
	if err = pg.QueryRow(ctx, `SELECT ts, now() FROM sc_heartbeat WHERE id = 1`).Scan(&pts, &now); err != nil {
		return 0, 0, fmt.Errorf("postgres heartbeat: %w", err)
	}
	var sts time.Time
	if err = sp.QueryRow(ctx, `SELECT ts FROM sc_heartbeat WHERE id = 1`).Scan(&sts); err != nil {
		return 0, 0, fmt.Errorf("spanner heartbeat: %w", err)
	}
	lag = pts.Sub(sts)
	if lag < 0 {
		lag = 0
	}
	return lag, now.Sub(pts), nil
}

// MeasureBacklog fills the backlog signals (Kafka end offsets vs applied offsets).
func MeasureBacklog(ctx context.Context, s *Signals, brokers []string, sp *pgxpool.Pool) {
	b, err := cdc.MeasureBacklog(ctx, brokers, "sc.public.", sp)
	if err != nil {
		s.BacklogErr = err.Error()
		return
	}
	s.BacklogRecords = &b.Records
	var age time.Duration
	if !b.OldestPending.IsZero() {
		age = time.Since(b.OldestPending)
	}
	s.BacklogAge = &age
}
