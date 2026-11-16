package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/chart"
	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/rowset"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/state"
)

func exp2Cmd() *cobra.Command {
	var stateDir, out string
	c := &cobra.Command{
		Use:   "exp2",
		Short: "Experiment 2: summarize live shadow-read parity (run scripts/exp2.sh)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var s shadow.Summary
			if err := state.ReadJSON(filepath.Join(stateDir, "shadow-summary.json"), &s); err != nil {
				return err
			}
			os.MkdirAll(out, 0o755)
			if err := copyFile(filepath.Join(stateDir, "shadow-summary.json"), filepath.Join(out, "shadow-summary.json")); err != nil {
				return err
			}
			_ = copyFile(filepath.Join(stateDir, "mismatches.jsonl"), filepath.Join(out, "mismatches.jsonl"))
			examples, err := firstExamples(filepath.Join(stateDir, "mismatches.jsonl"))
			if err != nil {
				return err
			}
			var b strings.Builder
			rate := 100 * float64(s.Mismatches) / float64(max64(s.Compared, 1))
			fmt.Fprintf(&b, "Live window %s to %s UTC. %s.\n\n", s.StartedAt.Format("2006-01-02 15:04"), s.UpdatedAt.Format("15:04"), envLabel)
			fmt.Fprintf(&b, "**%d reads shadowed, %d mismatched (%.1f%%)**, %d not shadowed because the queue was full.\n\n", s.Compared, s.Mismatches, rate, s.Dropped)
			b.WriteString("| root cause | mismatches | % of all reads | % of mismatches |\n|---|---|---|---|\n")
			var cats []string
			var vals []float64
			for _, c := range classify.AllCategories {
				n := s.ByCategory[c]
				if n == 0 {
					continue
				}
				fmt.Fprintf(&b, "| %s | %d | %.1f%% | %.1f%% |\n", c, n, 100*float64(n)/float64(max64(s.Compared, 1)), 100*float64(n)/float64(max64(s.Mismatches, 1)))
				cats = append(cats, string(c))
				vals = append(vals, float64(n))
			}
			b.WriteString("\n| query | reads | mismatches | rate | root causes |\n|---|---|---|---|---|\n")
			for _, q := range s.SortedQueries() {
				name := q.Name
				if name == "" {
					name = "`" + q.Fingerprint + "`"
				}
				var cs []string
				for _, c := range classify.AllCategories {
					if n := q.ByCategory[c]; n > 0 {
						cs = append(cs, fmt.Sprintf("%s %d", c, n))
					}
				}
				fmt.Fprintf(&b, "| %s | %d | %d | %.0f%% | %s |\n", name, q.Compared, q.Mismatches, 100*float64(q.Mismatches)/float64(max64(q.Compared, 1)), strings.Join(cs, ", "))
			}
			b.WriteString("\n### One real example per root cause\n\n")
			keys := make([]string, 0, len(examples))
			for k := range examples {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				o := examples[k]
				fmt.Fprintf(&b, "**%s**: `%s` (args %v)\n\n> %s\n\n", k, o.Name, o.Args, strings.ReplaceAll(o.Reason, "\n", " "))
				if len(o.SamplePrimary) > 0 || len(o.SampleShadow) > 0 {
					fmt.Fprintf(&b, "Postgres rows: `%s`<br>Spanner rows: `%s`\n\n", rowsStr(o.SamplePrimary), rowsStr(o.SampleShadow))
				}
			}
			if err := os.WriteFile(filepath.Join(out, "summary.md"), []byte(b.String()), 0o644); err != nil {
				return err
			}
			return chart.Chart{Title: "Exp 2: live mismatches by root cause (emulator)", Bars: true, Categories: cats, YLabel: "mismatches",
				Series: []chart.Series{{Name: "mismatches", Y: vals}}}.Save(filepath.Join(out, "by_cause.svg"))
		},
	}
	c.Flags().StringVar(&stateDir, "state", "state/shadow", "shadow output dir")
	c.Flags().StringVar(&out, "out", "results/exp2", "output dir")
	return c
}

func rowsStr(rs []rowset.Row) string {
	var parts []string
	for _, r := range rs {
		var cells []string
		for _, v := range r {
			cells = append(cells, v.String())
		}
		s := strings.Join(cells, " | ")
		if rr := []rune(s); len(rr) > 120 {
			s = string(rr[:117]) + "..."
		}
		parts = append(parts, "("+s+")")
	}
	return strings.ReplaceAll(strings.Join(parts, " "), "`", "'")
}

func firstExamples(path string) (map[string]shadow.Outcome, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]shadow.Outcome{}, nil
		}
		return nil, err
	}
	defer f.Close()
	out := map[string]shadow.Outcome{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var o shadow.Outcome
		if json.Unmarshal(sc.Bytes(), &o) != nil {
			continue
		}
		k := string(o.Category)
		cur, ok := out[k]
		switch {
		case !ok && o.Confidence != "low":
			out[k] = o
		case ok && cur.Confidence != "high" && o.Confidence == "high":
			out[k] = o // prefer high-confidence examples
		}
	}
	return out, sc.Err()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	o, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer o.Close()
	_, err = io.Copy(o, in)
	return err
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
