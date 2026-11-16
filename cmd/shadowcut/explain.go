package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/compare"
	"github.com/shadowcut/shadowcut/internal/explain"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
	"github.com/shadowcut/shadowcut/internal/state"
)

type evalRow struct {
	Name      string              `json:"name"`
	Category  classify.Category   `json:"category"`
	SQL       string              `json:"sql"`
	Expl      explain.Explanation `json:"explanation"`
	Evaluated bool                `json:"evaluated"`
	Parity    bool                `json:"parity"`    // fixed query: Postgres == Spanner
	Preserved *bool               `json:"preserved"` // fixed query on Postgres == original on Postgres (nil: not applicable)
	FixErr    string              `json:"fix_error,omitempty"`
}

func explainCmd() *cobra.Command {
	var in, out, which, project, location, model, schemaFile string
	var eval bool
	c := &cobra.Command{
		Use:   "explain",
		Short: "Layer 4 (optional): explain mismatches and suggest fixes; --eval re-runs suggested SQL on both engines",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var ex explain.Explainer = explain.Mock{}
			if which == "vertex" {
				if project == "" {
					return fmt.Errorf("--project (or $VERTEX_PROJECT) is required for --explainer vertex")
				}
				ex = &explain.Vertex{Project: project, Location: location, Model: model}
			}
			outcomes, err := loadOutcomes(in)
			if err != nil {
				return err
			}
			var schema *sqlscan.Schema
			if b, err := os.ReadFile(schemaFile); err == nil {
				schema = sqlscan.ParseSchema(string(b))
			}
			var pgp, spp *pgxpool.Pool
			if eval {
				pg, err := pgutil.Connect(ctx, pgutil.DefaultPG(), 4)
				if err != nil {
					return err
				}
				defer pg.Close()
				sp, err := pgutil.Connect(ctx, pgutil.DefaultSpanner(), 4)
				if err != nil {
					return err
				}
				defer sp.Close()
				pgp, spp = pg, sp
			}
			seen := map[string]bool{}
			var rows []evalRow
			for _, o := range outcomes {
				if o.Equal || o.Category == "" || seen[o.Fingerprint+string(o.Category)] || o.Category == classify.ReplicationLag || o.Category == classify.MissingRow {
					continue
				}
				seen[o.Fingerprint+string(o.Category)] = true
				e, err := ex.Explain(ctx, explain.Input{SQL: o.SQL, Category: o.Category, Reason: o.Reason, Schema: schema})
				if err != nil {
					return err
				}
				r := evalRow{Name: o.Name, Category: o.Category, SQL: o.SQL, Expl: e}
				if eval && e.FixedSQL != "" {
					r.Evaluated = true
					info := sqlscan.Analyze(sqlscan.Lex(e.FixedSQL))
					mode := compare.Unordered
					if len(info.OrderBy) > 0 {
						mode = compare.Ordered
					}
					args := parseArgs(o.Args)
					orig := shadow.Exec(ctx, pgp, o.SQL, args, false, 30*time.Second)
					fp := shadow.Exec(ctx, pgp, e.FixedSQL, args, false, 30*time.Second)
					fs := shadow.Exec(ctx, spp, e.FixedSQL, args, false, 30*time.Second)
					if fp.Err != "" || fs.Err != "" {
						r.FixErr = strings.TrimSpace(fp.Err + " " + fs.Err)
					}
					r.Parity = fp.Err == "" && fs.Err == "" && compare.Compare(fp, fs, mode).Equal()
					if o.Category != classify.Nondeterminism && o.Category != classify.Timezone {
						p := fp.Err == "" && compare.Compare(orig, fp, mode).Equal()
						r.Preserved = &p
					}
				}
				rows = append(rows, r)
				fmt.Printf("%-28s %-22s %s\n  fix: %s\n", o.Name, o.Category, e.Summary, e.Fix)
				if e.FixedSQL != "" {
					fmt.Printf("  rewrite: %s\n", strings.ReplaceAll(e.FixedSQL, "\n", " "))
				}
				if r.Evaluated {
					fmt.Printf("  eval: parity=%v preserved=%v %s\n", r.Parity, deref(r.Preserved), r.FixErr)
				}
			}
			if out != "" {
				os.MkdirAll(out, 0o755)
				if err := state.WriteJSON(filepath.Join(out, "explanations.json"), rows); err != nil {
					return err
				}
				return writeEvalSummary(filepath.Join(out, "summary.md"), ex.Name(), rows, eval)
			}
			return nil
		},
	}
	c.Flags().StringVar(&in, "mismatches", "results/exp1/outcomes.json", "mismatch outcomes (JSON array or JSONL)")
	c.Flags().StringVar(&out, "out", "results/layer4", "output dir")
	c.Flags().StringVar(&which, "explainer", "mock", "mock | vertex")
	c.Flags().StringVar(&project, "project", os.Getenv("VERTEX_PROJECT"), "Vertex AI project")
	c.Flags().StringVar(&location, "location", pgutil.Env("VERTEX_LOCATION", "us-central1"), "Vertex AI location")
	c.Flags().StringVar(&model, "model", "gemini-2.5-flash", "Vertex AI model id")
	c.Flags().StringVar(&schemaFile, "schema", "schema/postgres.sql", "schema for tie-break rewrites")
	c.Flags().BoolVar(&eval, "eval", false, "re-run each suggested rewrite on both engines and score it")
	return c
}

func deref(b *bool) string {
	if b == nil {
		return "n/a"
	}
	return fmt.Sprint(*b)
}

// parseArgs converts logged argument strings back into values (strings, ints).
func parseArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		var n int64
		if _, err := fmt.Sscan(s, &n); err == nil && fmt.Sprint(n) == s {
			out[i] = n
			continue
		}
		if t, err := time.Parse("2006-01-02T15:04:05.000000Z07:00", s); err == nil {
			out[i] = t
			continue
		}
		out[i] = s
	}
	return out
}

func loadOutcomes(path string) ([]shadow.Outcome, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var arr []shadow.Outcome
	if json.Unmarshal(b, &arr) == nil {
		return arr, nil
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var o shadow.Outcome
		if json.Unmarshal(sc.Bytes(), &o) == nil {
			arr = append(arr, o)
		}
	}
	return arr, sc.Err()
}

func writeEvalSummary(path, model string, rows []evalRow, eval bool) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Explainer: `%s`. Distinct mismatches (query x category): %d.\n\n", model, len(rows))
	byCat := map[classify.Category][4]int{} // explained, with rewrite, parity, preserved-ok
	for _, r := range rows {
		v := byCat[r.Category]
		v[0]++
		if r.Expl.FixedSQL != "" {
			v[1]++
		}
		if r.Parity {
			v[2]++
		}
		if r.Preserved == nil || *r.Preserved {
			if r.Parity {
				v[3]++
			}
		}
		byCat[r.Category] = v
	}
	b.WriteString("| category | mismatches | concrete SQL rewrite offered | rewrite gives parity | parity and Postgres result preserved |\n|---|---|---|---|---|\n")
	tot := [4]int{}
	for _, c := range classify.AllCategories {
		v, ok := byCat[c]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", c, v[0], v[1], v[2], v[3])
		for i := range tot {
			tot[i] += v[i]
		}
	}
	fmt.Fprintf(&b, "| **total** | %d | %d | %d | %d |\n\n", tot[0], tot[1], tot[2], tot[3])
	if !eval {
		b.WriteString("(run with --eval to score rewrites)\n")
	}
	b.WriteString("| query | category | rewrite | parity | preserved | error |\n|---|---|---|---|---|---|\n")
	for _, r := range rows {
		if r.Expl.FixedSQL == "" {
			continue
		}
		fmt.Fprintf(&b, "| %s | %s | `%s` | %v | %s | %s |\n", r.Name, r.Category, strings.ReplaceAll(strings.ReplaceAll(r.Expl.FixedSQL, "\n", " "), "|", "\\|"), r.Parity, deref(r.Preserved), r.FixErr)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
