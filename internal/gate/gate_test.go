package gate

import (
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/verify"
)

func healthy(now time.Time) Signals {
	lag, hb, zero := 500*time.Millisecond, 300*time.Millisecond, time.Duration(0)
	var recs int64
	return Signals{
		Shadow: &shadow.Summary{UpdatedAt: now, Compared: 1000, ByCategory: map[classify.Category]int64{}},
		Verify: &verify.Summary{UpdatedAt: now, Rounds: 3},
		Scan:   &hazard.Report{},
		Lag:    &lag, HeartbeatAge: &hb, BacklogRecords: &recs, BacklogAge: &zero,
	}
}

func TestGateGoWhenHealthy(t *testing.T) {
	now := time.Now()
	if d := Evaluate(DefaultPolicy(), healthy(now), now); !d.Go {
		t.Fatalf("expected GO: %+v", d.Checks)
	}
}

func TestGateFailsClosed(t *testing.T) {
	now := time.Now()
	for name, mut := range map[string]func(*Signals){
		"no shadow":     func(s *Signals) { s.Shadow = nil },
		"no verify":     func(s *Signals) { s.Verify = nil },
		"no scan":       func(s *Signals) { s.Scan = nil },
		"no lag":        func(s *Signals) { s.Lag = nil },
		"lag error":     func(s *Signals) { s.LagErr = "boom" },
		"stale verify":  func(s *Signals) { s.Verify.UpdatedAt = now.Add(-time.Hour) },
		"few compared":  func(s *Signals) { s.Shadow.Compared = 10 },
		"stalled CDC":   func(s *Signals) { d := time.Minute; s.HeartbeatAge = &d },
		"collation":     func(s *Signals) { s.Shadow.ByCategory[classify.Collation] = 1 },
		"open drift":    func(s *Signals) { s.Verify.OpenDrift = []verify.Drift{{Table: "t", Key: "1"}} },
		"unwaived high": func(s *Signals) { s.Scan.Findings = []hazard.Finding{{Rule: "Q-CONCAT-NULL", Severity: hazard.High}} },
		"backlog error": func(s *Signals) { s.BacklogErr = "kafka down" },
	} {
		s := healthy(now)
		mut(&s)
		if d := Evaluate(DefaultPolicy(), s, now); d.Go {
			t.Errorf("%s: expected NO-GO", name)
		}
	}
}

// BUG-005 regression: a fresh heartbeat must not hide an old CDC backlog.
func TestGateUsesBacklogAge(t *testing.T) {
	now := time.Now()
	s := healthy(now)
	age, recs := 12*time.Second, int64(600)
	s.BacklogAge, s.BacklogRecords = &age, &recs
	if d := Evaluate(DefaultPolicy(), s, now); d.Go {
		t.Fatalf("expected NO-GO with a 12s backlog behind a fresh heartbeat")
	}
}

func TestGateAcceptedCategoryWarns(t *testing.T) {
	now := time.Now()
	s := healthy(now)
	s.Shadow.ByCategory[classify.Collation] = 5
	p := DefaultPolicy()
	p.Accept = []classify.Category{classify.Collation}
	d := Evaluate(p, s, now)
	if !d.Go {
		t.Fatalf("accepted category must not block: %+v", d.Checks)
	}
	warned := false
	for _, c := range d.Checks {
		if c.Name == "shadow.collation" && c.Status == "warn" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("accepted category should still be reported as WARN")
	}
}

// Property: the gate is monotone; making any signal worse never turns NO-GO into GO.
func TestPropGateMonotone(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		now := time.Now()
		s := healthy(now)
		lag := time.Duration(rapid.Int64Range(0, int64(20*time.Second)).Draw(t, "lag"))
		s.Lag = &lag
		mis := rapid.Int64Range(0, 5).Draw(t, "mis")
		s.Shadow.ByCategory[classify.Timezone] = mis
		d1 := Evaluate(DefaultPolicy(), s, now)
		worse := lag + time.Duration(rapid.Int64Range(0, int64(10*time.Second)).Draw(t, "more"))
		s.Lag = &worse
		s.Shadow.ByCategory[classify.Timezone] = mis + rapid.Int64Range(0, 3).Draw(t, "moremis")
		d2 := Evaluate(DefaultPolicy(), s, now)
		if !d1.Go && d2.Go {
			t.Fatalf("worse signals flipped NO-GO to GO")
		}
	})
}

// BUG-005 (second part): while Debezium is down nothing reaches Kafka, so the
// backlog is empty; the stale source heartbeat must still block.
func TestGateUsesHeartbeatAgeAsLag(t *testing.T) {
	now := time.Now()
	s := healthy(now)
	hb := 8 * time.Second // below MaxHeartbeatAge (15s) but above MaxLag (5s)
	s.HeartbeatAge = &hb
	if d := Evaluate(DefaultPolicy(), s, now); d.Go {
		t.Fatalf("expected NO-GO: connector silent for 8s")
	}
}
