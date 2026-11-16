// Package loadgen generates write load for fault experiments.
package loadgen

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ProbeWriter inserts probe_events rows (writer, seq) one per transaction.
type ProbeWriter struct {
	Pool   *pgxpool.Pool
	Writer string
	Rate   float64
}

// Result summarizes a run. Acked holds every seq Postgres acknowledged.
type Result struct {
	Writer    string    `json:"writer"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Attempted int64     `json:"attempted"`
	Acked     int64     `json:"acked"`
	Failed    int64     `json:"failed"`
	AckedSeqs []int64   `json:"-"`
}

// Run writes until ctx ends.
func (w ProbeWriter) Run(ctx context.Context) Result {
	r := Result{Writer: w.Writer, Start: time.Now().UTC()}
	t := time.NewTicker(time.Duration(float64(time.Second) / w.Rate))
	defer t.Stop()
	var seq int64
	for {
		select {
		case <-ctx.Done():
			r.End = time.Now().UTC()
			return r
		case <-t.C:
		}
		seq++
		r.Attempted++
		wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := w.Pool.Exec(wctx, `INSERT INTO probe_events (writer, seq, payload) VALUES ($1, $2, $3)`, w.Writer, seq, fmt.Sprintf("probe %s/%d", w.Writer, seq))
		cancel()
		if err != nil {
			r.Failed++
			continue
		}
		r.Acked++
		r.AckedSeqs = append(r.AckedSeqs, seq)
	}
}
