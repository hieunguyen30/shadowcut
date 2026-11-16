// Package verify performs continuous, sampled row-level drift checking between
// the Postgres source and the Spanner target (`shadowcut verify`).
//
// Each round samples a fraction of rows on BOTH sides (TABLESAMPLE BERNOULLI),
// fetches the same primary keys from the other side, and compares normalized
// rows. A difference is first a *suspect*; it becomes *confirmed drift* only if
// it is still present after a lag grace period AND the source row did not change
// in between (otherwise it is in-flight replication, not drift).
package verify

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shadowcut/shadowcut/internal/rowset"
)

// Querier is the subset of pgx used (pool or conn).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Table describes a table to verify.
type Table struct {
	Name    string
	PK      []string
	Columns []string // compared columns (PK first)
}

// Config controls a verifier.
type Config struct {
	Tables     []Table
	SampleRate float64       // fraction of rows per round, 0 < r <= 1
	MaxRows    int           // cap per table per side per round (0 = no cap)
	LagGrace   time.Duration // wait before re-checking suspects
	Rechecks   int           // max re-checks for in-flight rows
	Seed       int64
}

// Drift is one confirmed difference.
type Drift struct {
	Table   string   `json:"table"`
	Key     string   `json:"key"`
	Kind    string   `json:"kind"` // missing_in_spanner | extra_in_spanner | value_diff
	Columns []string `json:"columns,omitempty"`
	Found   time.Time `json:"found"`
}

// TableStats is per-table round accounting.
type TableStats struct {
	Table        string `json:"table"`
	SampledPG    int    `json:"sampled_pg"`
	SampledSP    int    `json:"sampled_spanner"`
	RowsReadPG   int    `json:"rows_read_pg"`
	RowsReadSP   int    `json:"rows_read_spanner"`
	Suspects     int    `json:"suspects"`
	InFlight     int    `json:"in_flight"`
	Confirmed    int    `json:"confirmed"`
}

// Round is the result of one verification round.
type Round struct {
	Start    time.Time     `json:"start"`
	Duration time.Duration `json:"duration_ns"`
	Rate     float64       `json:"sample_rate"`
	Tables   []TableStats  `json:"tables"`
	Drift    []Drift       `json:"drift"`
}

// RowsRead totals rows read on both sides.
func (r Round) RowsRead() (pg, sp int) {
	for _, t := range r.Tables {
		pg += t.RowsReadPG
		sp += t.RowsReadSP
	}
	return
}

// Verifier compares the two databases.
type Verifier struct {
	PG, SP Querier
	Cfg    Config
	rnd    *rand.Rand
}

// New creates a verifier.
func New(pg, sp Querier, cfg Config) *Verifier {
	if cfg.Rechecks <= 0 {
		cfg.Rechecks = 3
	}
	if cfg.Seed == 0 {
		cfg.Seed = time.Now().UnixNano()
	}
	return &Verifier{PG: pg, SP: sp, Cfg: cfg, rnd: rand.New(rand.NewSource(cfg.Seed))}
}

// DiscoverTables builds Table specs from Postgres metadata for the given names,
// keeping only columns that also exist in Spanner.
func DiscoverTables(ctx context.Context, pg, sp *pgxpool.Pool, names []string) ([]Table, error) {
	var out []Table
	for _, n := range names {
		pgCols, err := columns(ctx, pg, n)
		if err != nil {
			return nil, err
		}
		spCols, err := columns(ctx, sp, n)
		if err != nil {
			return nil, err
		}
		has := map[string]bool{}
		for _, c := range spCols {
			has[c] = true
		}
		var pk []string
		rows, err := pg.Query(ctx, `SELECT a.attname FROM pg_index i JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
			WHERE i.indrelid = $1::regclass AND i.indisprimary ORDER BY array_position(i.indkey, a.attnum)`, n)
		if err != nil {
			return nil, err
		}
		pk, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		t := Table{Name: n, PK: pk}
		t.Columns = append(t.Columns, pk...)
		for _, c := range pgCols {
			if has[c] && !contains(pk, c) {
				t.Columns = append(t.Columns, c)
			}
		}
		out = append(out, t)
	}
	return out, nil
}

func columns(ctx context.Context, q Querier, table string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

type keyed struct {
	key  string
	pk   []any
	row  rowset.Row
}

// RunRound performs one verification round.
func (v *Verifier) RunRound(ctx context.Context) (Round, error) {
	r := Round{Start: time.Now().UTC(), Rate: v.Cfg.SampleRate}
	for _, t := range v.Cfg.Tables {
		st, drift, err := v.verifyTable(ctx, t)
		if err != nil {
			return r, fmt.Errorf("%s: %w", t.Name, err)
		}
		r.Tables = append(r.Tables, st)
		r.Drift = append(r.Drift, drift...)
	}
	r.Duration = time.Since(r.Start)
	return r, nil
}

func (v *Verifier) verifyTable(ctx context.Context, t Table) (TableStats, []Drift, error) {
	st := TableStats{Table: t.Name}
	pct := v.Cfg.SampleRate * 100
	if pct > 100 {
		pct = 100
	}
	// 1. samples on both sides
	pgSample, err := v.sample(ctx, v.PG, t, pct)
	if err != nil {
		return st, nil, fmt.Errorf("sample postgres: %w", err)
	}
	spSample, err := v.sample(ctx, v.SP, t, pct)
	if err != nil {
		return st, nil, fmt.Errorf("sample spanner: %w", err)
	}
	st.SampledPG, st.SampledSP = len(pgSample), len(spSample)
	st.RowsReadPG += len(pgSample)
	st.RowsReadSP += len(spSample)

	// 2. fetch counterparts
	spRows, err := v.fetch(ctx, v.SP, t, pgSample)
	if err != nil {
		return st, nil, fmt.Errorf("fetch spanner: %w", err)
	}
	pgRows, err := v.fetch(ctx, v.PG, t, spSample)
	if err != nil {
		return st, nil, fmt.Errorf("fetch postgres: %w", err)
	}
	st.RowsReadSP += len(spRows)
	st.RowsReadPG += len(pgRows)

	suspects := map[string]keyed{}
	// last observed Postgres image per key (absent = row missing in Postgres)
	prevPG := map[string]keyed{}
	for _, p := range pgSample {
		prevPG[p.key] = p
	}
	for k, p := range pgRows {
		prevPG[k] = p
	}
	for _, p := range pgSample {
		if s, ok := spRows[p.key]; !ok || !rowset.RowsEqual(p.row, s.row) {
			suspects[p.key] = p
		}
	}
	for _, s := range spSample {
		if p, ok := pgRows[s.key]; !ok || !rowset.RowsEqual(p.row, s.row) {
			if _, dup := suspects[s.key]; !dup {
				suspects[s.key] = s
			}
		}
	}
	st.Suspects = len(suspects)
	if len(suspects) == 0 {
		return st, nil, nil
	}

	// 3. recheck suspects after the lag grace period; a suspect whose source row
	// keeps changing is in flight, not drift.
	pending := make([]keyed, 0, len(suspects))
	for _, s := range suspects {
		pending = append(pending, s)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].key < pending[j].key })
	var drift []Drift
	for attempt := 0; attempt <= v.Cfg.Rechecks && len(pending) > 0; attempt++ {
		select {
		case <-ctx.Done():
			return st, drift, ctx.Err()
		case <-time.After(v.Cfg.LagGrace):
		}
		pgNow, err := v.fetch(ctx, v.PG, t, pending)
		if err != nil {
			return st, nil, err
		}
		spNow, err := v.fetch(ctx, v.SP, t, pending)
		if err != nil {
			return st, nil, err
		}
		st.RowsReadPG += len(pgNow)
		st.RowsReadSP += len(spNow)
		var still []keyed
		for _, s := range pending {
			p, pok := pgNow[s.key]
			q, sok := spNow[s.key]
			if pok == sok && (!pok || rowset.RowsEqual(p.row, q.row)) {
				continue // converged: it was replication lag
			}
			// drift only if the source row is unchanged since the previous observation
			pp, had := prevPG[s.key]
			stable := (had == pok) && (!pok || rowset.RowsEqual(pp.row, p.row))
			if pok {
				prevPG[s.key] = p
			} else {
				delete(prevPG, s.key)
			}
			if !stable && attempt < v.Cfg.Rechecks {
				still = append(still, s)
				continue
			}
			d := Drift{Table: t.Name, Key: s.key, Found: time.Now().UTC()}
			switch {
			case pok && !sok:
				d.Kind = "missing_in_spanner"
			case !pok && sok:
				d.Kind = "extra_in_spanner"
			default:
				d.Kind = "value_diff"
				for i := range p.row {
					if !rowset.Equal(p.row[i], q.row[i]) {
						d.Columns = append(d.Columns, t.Columns[i])
					}
				}
			}
			drift = append(drift, d)
		}
		pending = still
	}
	st.InFlight = len(suspects) - len(drift)
	st.Confirmed = len(drift)
	return st, drift, nil
}

func colList(t Table) string {
	q := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		q[i] = quoteIdent(c)
	}
	return strings.Join(q, ", ")
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

func (v *Verifier) sample(ctx context.Context, q Querier, t Table, pct float64) ([]keyed, error) {
	var sql string
	if pct >= 100 {
		sql = fmt.Sprintf(`SELECT %s FROM %s`, colList(t), quoteIdent(t.Name))
	} else {
		sql = fmt.Sprintf(`SELECT %s FROM %s TABLESAMPLE BERNOULLI (%g)`, colList(t), quoteIdent(t.Name), pct)
	}
	if v.Cfg.MaxRows > 0 {
		sql += fmt.Sprintf(" LIMIT %d", v.Cfg.MaxRows)
	}
	rows, err := q.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	return collect(rows, len(t.PK))
}

func collect(rows pgx.Rows, npk int) ([]keyed, error) {
	defer rows.Close()
	var out []keyed
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		r := make(rowset.Row, len(vals))
		for i, x := range vals {
			r[i] = rowset.Normalize(x)
		}
		k := keyed{row: r, pk: vals[:npk]}
		k.key = rowset.RowKey(r[:npk])
		out = append(out, k)
	}
	return out, rows.Err()
}

// fetch loads rows by primary key in chunks using OR-of-AND predicates (row
// comparisons and array-literal ANY are not supported by Spanner's PG
// interface, see docs/HAZARDS.md).
func (v *Verifier) fetch(ctx context.Context, q Querier, t Table, keys []keyed) (map[string]keyed, error) {
	out := map[string]keyed{}
	// Spanner limits a statement to 1000 functions; each key costs one "=" per PK
	// column plus one AND/OR per column (see docs/BUGS.md BUG-001).
	chunk := 900 / (2 * len(t.PK))
	for i := 0; i < len(keys); i += chunk {
		end := i + chunk
		if end > len(keys) {
			end = len(keys)
		}
		var preds []string
		var args []any
		for _, k := range keys[i:end] {
			var parts []string
			for j, c := range t.PK {
				args = append(args, k.pk[j])
				parts = append(parts, fmt.Sprintf("%s = $%d", quoteIdent(c), len(args)))
			}
			preds = append(preds, "("+strings.Join(parts, " AND ")+")")
		}
		sql := fmt.Sprintf(`SELECT %s FROM %s WHERE %s`, colList(t), quoteIdent(t.Name), strings.Join(preds, " OR "))
		rows, err := q.Query(ctx, sql, args...)
		if err != nil {
			return nil, shortErr(err)
		}
		got, err := collect(rows, len(t.PK))
		if err != nil {
			return nil, err
		}
		for _, g := range got {
			out[g.key] = g
		}
	}
	return out, nil
}

// shortErr drops the echoed statement PGAdapter appends to errors (it can be
// hundreds of KB for chunked key lookups).
func shortErr(err error) error {
	msg := err.Error()
	if i := strings.Index(msg, " - Statement: "); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300] + "..."
	}
	return fmt.Errorf("%s", msg)
}
