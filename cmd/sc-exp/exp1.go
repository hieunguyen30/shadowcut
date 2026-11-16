package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/shadowcut/shadowcut/internal/cdc"
	"github.com/shadowcut/shadowcut/internal/chart"
	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
	"github.com/shadowcut/shadowcut/internal/state"
	"github.com/shadowcut/shadowcut/queries"
)

// environmental categories are not hazards of the query itself; they are not scored.
var environmental = map[classify.Category]bool{classify.ReplicationLag: true, classify.MissingRow: true, classify.Isolation: true, classify.Timeout: true}

type exp1Query struct {
	Name         string              `json:"name"`
	Source       string              `json:"source"` // curated | app
	Expect       string              `json:"expect,omitempty"`
	Predicted    []classify.Category `json:"predicted"`
	Rules        []string            `json:"rules"`
	Observed     []classify.Category `json:"observed"`
	Runs         int                 `json:"runs"`
	Mismatch     int                 `json:"mismatched_runs"`
	Reasons      []string            `json:"reasons,omitempty"`
	TP, FP, FN   []classify.Category `json:"-"`
	Unobservable []classify.Category `json:"unobservable,omitempty"`
}

func exp1Cmd() *cobra.Command {
	var runs int
	var gap time.Duration
	var out string
	var quiesce, rescore bool
	c := &cobra.Command{
		Use:   "exp1",
		Short: "Experiment 1: scanner precision/recall vs mismatches observed by shadow replay",
		RunE: func(cmd *cobra.Command, args []string) error {
			if rescore {
				var qs []*exp1Query
				if err := state.ReadJSON(filepath.Join(out, "per_query.json"), &qs); err != nil {
					return err
				}
				for _, q := range qs {
					score(q)
				}
				_ = state.WriteJSON(filepath.Join(out, "per_query.json"), qs)
				return exp1Report(out, qs, qs[0].Runs)
			}
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
			schemaSQL, err := os.ReadFile("schema/postgres.sql")
			if err != nil {
				return err
			}
			if quiesce {
				if err := quiesceSource(ctx, sp); err != nil {
					return err
				}
				defer docker("unpause", "shadowcut-ingest-wiki-1")(context.Background())
			}
			// 1. predictions
			rep := hazard.New(nil).Scan([]hazard.Input{{File: "schema/postgres.sql", SQL: string(schemaSQL)}},
				[]hazard.Input{{File: "queries/hazards.sql", SQL: queries.HazardsSQL}, {File: "queries/app.sql", SQL: queries.AppSQL}})
			pred := map[string]hazard.QueryEntry{}
			for _, q := range rep.Queries {
				pred[q.File+"|"+q.Name] = q
			}
			// 2. jobs: curated (no params) + app reads with real parameter values
			params, err := appParams(ctx, pg)
			if err != nil {
				return err
			}
			type job struct {
				name, source, expect, key, sql string
				args                           []any
			}
			var jobs []job
			for _, st := range sqlscan.Split(queries.HazardsSQL) {
				if shadow.IsShadowable(sqlscan.Analyze(st.Tokens)) {
					jobs = append(jobs, job{st.Name, "curated", st.Tags["expect"], "queries/hazards.sql|" + st.Name, st.Text, nil})
				}
			}
			for _, st := range sqlscan.Split(queries.AppSQL) {
				if !shadow.IsShadowable(sqlscan.Analyze(st.Tokens)) {
					continue
				}
				a, ok := params[st.Name]
				if !ok {
					slog.Warn("no parameters for app query; skipped", "name", st.Name)
					continue
				}
				jobs = append(jobs, job{st.Name, "app", "", "queries/app.sql|" + st.Name, st.Text, a})
			}
			eng := shadow.NewEngine(pg, sp, shadow.Config{RecheckAfter: 3 * time.Second, Workers: 1}, noop.NewMeterProvider().Meter("exp1"))
			results := map[string]*exp1Query{}
			var order []string
			var outcomes []shadow.Outcome
			for r := 0; r < runs; r++ {
				for _, j := range jobs {
					o := eng.Process(ctx, shadow.Job{SQL: j.sql, Args: j.args, Source: "exp1"})
					o.Name = j.name
					outcomes = append(outcomes, o)
					q := results[j.key]
					if q == nil {
						p := pred[j.key]
						q = &exp1Query{Name: j.name, Source: j.source, Expect: j.expect, Predicted: p.Predicts, Rules: p.Rules}
						results[j.key] = q
						order = append(order, j.key)
					}
					q.Runs++
					if !o.Equal {
						q.Mismatch++
						if !environmental[o.Category] && !containsCat(q.Observed, o.Category) {
							q.Observed = append(q.Observed, o.Category)
							q.Reasons = append(q.Reasons, string(o.Category)+": "+trimReason(o.Reason))
						}
					}
				}
				slog.Info("exp1 run complete", "run", r+1, "of", runs)
				if r < runs-1 {
					time.Sleep(gap)
				}
			}
			os.MkdirAll(out, 0o755)
			_ = state.WriteJSON(filepath.Join(out, "outcomes.json"), outcomes)
			var qs []*exp1Query
			for _, k := range order {
				q := results[k]
				score(q)
				qs = append(qs, q)
			}
			_ = state.WriteJSON(filepath.Join(out, "per_query.json"), qs)
			return exp1Report(out, qs, runs)
		},
	}
	c.Flags().IntVar(&runs, "runs", 3, "replay each statement this many times")
	c.Flags().BoolVar(&rescore, "rescore", false, "only re-score an existing per_query.json in --out (no database access)")
	c.Flags().BoolVar(&quiesce, "quiesce", true, "pause live ingestion and wait for CDC to drain (static snapshot) before replaying")
	c.Flags().DurationVar(&gap, "gap", 10*time.Second, "pause between runs")
	c.Flags().StringVar(&out, "out", "results/exp1", "output directory")
	return c
}

func trimReason(s string) string {
	if r := []rune(s); len(r) > 140 {
		return string(r[:137]) + "..."
	}
	return s
}

func containsCat(xs []classify.Category, c classify.Category) bool {
	for _, x := range xs {
		if x == c {
			return true
		}
	}
	return false
}

func score(q *exp1Query) {
	q.TP, q.FP, q.FN = nil, nil, nil
	// When Spanner rejects the statement, no other behaviour can be observed for it:
	// predictions besides "unsupported" are unobservable, not false positives.
	rejected := containsCat(q.Observed, classify.Unsupported)
	q.Unobservable = nil
	for _, p := range q.Predicted {
		if environmental[p] {
			continue
		}
		if rejected && p != classify.Unsupported {
			q.Unobservable = append(q.Unobservable, p)
			continue
		}
		if containsCat(q.Observed, p) {
			q.TP = append(q.TP, p)
		} else {
			q.FP = append(q.FP, p)
		}
	}
	for _, o := range q.Observed {
		if environmental[o] {
			continue
		}
		if !containsCat(q.Predicted, o) {
			q.FN = append(q.FN, o)
		}
	}
}

// appParams picks realistic parameter values from the live data.
func appParams(ctx context.Context, pg *pgxpool.Pool) (map[string][]any, error) {
	var wiki, user, title, prefix string
	var tsHi time.Time
	var idHi int64
	if err := pg.QueryRow(ctx, `SELECT wiki FROM edits GROUP BY wiki ORDER BY count(*) DESC LIMIT 1 OFFSET 2`).Scan(&wiki); err != nil {
		return nil, err
	}
	if err := pg.QueryRow(ctx, `SELECT user_name FROM edits WHERE user_name IS NOT NULL GROUP BY user_name ORDER BY count(*) DESC LIMIT 1`).Scan(&user); err != nil {
		return nil, err
	}
	if err := pg.QueryRow(ctx, `SELECT title FROM pages WHERE wiki = 'enwiki' ORDER BY edit_count DESC, title LIMIT 1`).Scan(&title); err != nil {
		return nil, err
	}
	prefix = string([]rune(title)[:1])
	if err := pg.QueryRow(ctx, `SELECT edit_ts, id FROM edits ORDER BY id DESC LIMIT 1 OFFSET 200`).Scan(&tsHi, &idHi); err != nil {
		return nil, err
	}
	var repo string
	_ = pg.QueryRow(ctx, `SELECT repo_name FROM gh_events GROUP BY repo_name ORDER BY count(*) DESC, repo_name LIMIT 1`).Scan(&repo)
	return map[string][]any{
		"top_pages":            {"enwiki", int64(20)},
		"recent_edits_by_user": {user},
		"edits_in_range":       {tsHi.Add(-30 * time.Second), tsHi},
		"search_titles":        {prefix},
		"feed_page":            {tsHi, idHi},
		"feed_first_page":      {},
		"feed_offset":          {int64(50)},
		"pages_alpha":          {"enwiki", "M"},
		"wiki_stats":           {},
		"daily_counts":         {wiki},
		"editor_labels":        {},
		"top_editors":          {wiki},
		"page_detail":          {"enwiki", title},
		"gh_top_repos":         {},
		"gh_repo_events":       {repo},
		"touch_page_read":      {"enwiki", title},
	}, nil
}

func exp1Report(out string, qs []*exp1Query, runs int) error {
	type agg struct{ tp, fp, fn int }
	byCat := map[classify.Category]*agg{}
	total := agg{}
	expTotal := agg{}
	get := func(c classify.Category) *agg {
		if byCat[c] == nil {
			byCat[c] = &agg{}
		}
		return byCat[c]
	}
	for _, q := range qs {
		for _, c := range q.TP {
			get(c).tp++
			total.tp++
		}
		for _, c := range q.FP {
			get(c).fp++
			total.fp++
		}
		for _, c := range q.FN {
			get(c).fn++
			total.fn++
		}
		// author's expectation as a baseline "scanner"
		if q.Source == "curated" {
			var e []classify.Category
			if q.Expect != "" && q.Expect != "none" {
				e = []classify.Category{classify.Category(q.Expect)}
			}
			for _, c := range e {
				if containsCat(q.Observed, c) {
					expTotal.tp++
				} else {
					expTotal.fp++
				}
			}
			for _, o := range q.Observed {
				if !containsCat(e, o) {
					expTotal.fn++
				}
			}
		}
	}
	pr := func(a agg) (float64, float64) {
		p, r := 0.0, 0.0
		if a.tp+a.fp > 0 {
			p = float64(a.tp) / float64(a.tp+a.fp)
		}
		if a.tp+a.fn > 0 {
			r = float64(a.tp) / float64(a.tp+a.fn)
		}
		return p, r
	}
	var b strings.Builder
	p, r := pr(total)
	fmt.Fprintf(&b, "Statements scored: %d (%d runs each). %s.\n\n", len(qs), runs, envLabel)
	fmt.Fprintf(&b, "**Overall (query × category): precision %.2f, recall %.2f** (TP %d, FP %d, FN %d).\n\n", p, r, total.tp, total.fp, total.fn)
	ep, er := pr(expTotal)
	fmt.Fprintf(&b, "Baseline: the curated set's hand-written `expect` labels as predictions: precision %.2f, recall %.2f (TP %d, FP %d, FN %d).\n\n", ep, er, expTotal.tp, expTotal.fp, expTotal.fn)
	b.WriteString("| category | TP | FP | FN | precision | recall |\n|---|---|---|---|---|---|\n")
	cats := make([]string, 0)
	for c := range byCat {
		cats = append(cats, string(c))
	}
	sort.Strings(cats)
	var cx []string
	var ps, rs []float64
	for _, cs := range cats {
		a := byCat[classify.Category(cs)]
		p, r := pr(*a)
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %.2f | %.2f |\n", cs, a.tp, a.fp, a.fn, p, r)
		cx = append(cx, cs)
		ps = append(ps, p)
		rs = append(rs, r)
	}
	b.WriteString("\n### Per statement\n\n| statement | source | predicted | observed | result |\n|---|---|---|---|---|\n")
	for _, q := range qs {
		res := "✔"
		var parts []string
		if len(q.FP) > 0 {
			parts = append(parts, fmt.Sprintf("FP %v", q.FP))
		}
		if len(q.FN) > 0 {
			parts = append(parts, fmt.Sprintf("FN %v", q.FN))
		}
		if len(q.Unobservable) > 0 {
			parts = append(parts, fmt.Sprintf("unobservable (rejected) %v", q.Unobservable))
		}
		if len(parts) > 0 {
			res = strings.Join(parts, "; ")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", q.Name, q.Source, cats2s(q.Predicted), cats2s(q.Observed), res)
	}
	b.WriteString("\n### Misses and false alarms, with evidence\n\n")
	for _, q := range qs {
		if len(q.FP) == 0 && len(q.FN) == 0 {
			continue
		}
		fmt.Fprintf(&b, "- **%s** (rules %s): ", q.Name, strings.Join(q.Rules, ", "))
		if len(q.FN) > 0 {
			fmt.Fprintf(&b, "missed %v. Observed: %s. ", q.FN, strings.Join(q.Reasons, " | "))
		}
		if len(q.FP) > 0 {
			fmt.Fprintf(&b, "predicted %v but never observed in %d runs.", q.FP, q.Runs)
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(out, "summary.md"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	return chart.Chart{Title: "Exp 1: scanner precision / recall by category (emulator)", Bars: true, Categories: cx, YLabel: "score",
		Series: []chart.Series{{Name: "precision", Y: ps}, {Name: "recall", Y: rs}}}.Save(filepath.Join(out, "precision_recall.svg"))
}

func cats2s(cs []classify.Category) string {
	if len(cs) == 0 {
		return "—"
	}
	var s []string
	for _, c := range cs {
		s = append(s, string(c))
	}
	return strings.Join(s, ", ")
}

// quiesceSource pauses the live ingestor and waits until every CDC record has
// been applied, so Postgres and Spanner hold the same static snapshot.
func quiesceSource(ctx context.Context, sp *pgxpool.Pool) error {
	if err := docker("pause", "shadowcut-ingest-wiki-1")(ctx); err != nil {
		slog.Warn("could not pause the ingestor (already paused?)", "err", err)
	}
	brokers := strings.Split(pgutil.Env("KAFKA_BROKERS", "localhost:19094"), ",")
	deadline := time.Now().Add(3 * time.Minute)
	for {
		b, err := cdc.MeasureBacklog(ctx, brokers, "sc.public.", sp)
		if err == nil && b.Records == 0 {
			time.Sleep(3 * time.Second) // let the last heartbeat-driven batches settle
			slog.Info("source quiesced: CDC backlog empty")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("CDC did not drain: %+v %v", b, err)
		}
		time.Sleep(time.Second)
	}
}
