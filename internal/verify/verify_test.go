package verify

import (
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// BUG-001 regression: lookup statements must stay under Spanner's limit of 1000
// functions (each "=", AND and OR counts).
func TestFetchChunkWithinFunctionLimit(t *testing.T) {
	for npk := 1; npk <= 4; npk++ {
		pk := []string{"a", "b", "c", "d"}[:npk]
		n := ChunkSize(npk)
		pred := KeyPredicate(pk, n)
		funcs := strings.Count(pred, " = ") + strings.Count(pred, " AND ") + strings.Count(pred, " OR ")
		if funcs >= 1000 {
			t.Fatalf("pk width %d: %d keys -> %d functions", npk, n, funcs)
		}
		if n < 100 {
			t.Fatalf("pk width %d: chunk %d is needlessly small", npk, n)
		}
	}
}

// Property: Fold never loses drift and OpenDrift has one entry per key.
func TestPropSummaryFold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var s Summary
		keys := map[string]bool{}
		rounds := rapid.IntRange(1, 10).Draw(t, "rounds")
		total := 0
		for i := 0; i < rounds; i++ {
			n := rapid.IntRange(0, 5).Draw(t, "n")
			var r Round
			for j := 0; j < n; j++ {
				k := rapid.SampledFrom([]string{"1", "2", "3", "4"}).Draw(t, "key")
				r.Drift = append(r.Drift, Drift{Table: "t", Key: k, Kind: "value_diff", Found: time.Now()})
				keys[k] = true
			}
			total += n
			s.Fold(r, time.Hour)
		}
		if s.DriftTotal != total {
			t.Fatalf("DriftTotal %d != %d", s.DriftTotal, total)
		}
		if len(s.OpenDrift) != len(keys) {
			t.Fatalf("OpenDrift %d entries, %d distinct keys", len(s.OpenDrift), len(keys))
		}
		if s.Rounds != rounds {
			t.Fatalf("rounds %d != %d", s.Rounds, rounds)
		}
	})
}
