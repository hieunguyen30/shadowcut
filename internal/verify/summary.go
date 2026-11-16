package verify

import "time"

// Summary is the verifier's persisted state, read by `shadowcut gate`.
type Summary struct {
	UpdatedAt      time.Time `json:"updated_at"`
	SampleRate     float64   `json:"sample_rate"`
	Rounds         int       `json:"rounds"`
	RowsCompared   int64     `json:"rows_compared"`
	DriftTotal     int       `json:"drift_total"`
	OpenDrift      []Drift   `json:"open_drift"` // confirmed drift not yet seen resolved
	LastRoundStart time.Time `json:"last_round_start"`
	LastRoundMs    int64     `json:"last_round_ms"`
}

// Fold adds a round to the summary. Drift found in a later round for the same
// key replaces earlier entries; drift for a key that a later round re-checked
// and found consistent is not tracked here (it simply ages out of OpenDrift
// after maxAge).
func (s *Summary) Fold(r Round, maxAge time.Duration) {
	s.UpdatedAt = time.Now().UTC()
	s.SampleRate = r.Rate
	s.Rounds++
	pg, sp := r.RowsRead()
	s.RowsCompared += int64(pg + sp)
	s.DriftTotal += len(r.Drift)
	s.LastRoundStart = r.Start
	s.LastRoundMs = r.Duration.Milliseconds()
	byKey := map[string]Drift{}
	for _, d := range s.OpenDrift {
		if time.Since(d.Found) < maxAge {
			byKey[d.Table+"|"+d.Key] = d
		}
	}
	for _, d := range r.Drift {
		byKey[d.Table+"|"+d.Key] = d
	}
	s.OpenDrift = s.OpenDrift[:0]
	for _, d := range byKey {
		s.OpenDrift = append(s.OpenDrift, d)
	}
}
