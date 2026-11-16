package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/classify"
	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/shadow"
	"github.com/shadowcut/shadowcut/internal/state"
)

func reportCmd() *cobra.Command {
	c := &cobra.Command{Use: "report", Short: "Render reports from the catalog and saved results"}
	var out string
	catalog := &cobra.Command{
		Use:   "catalog",
		Short: "Render the hazard catalog as Markdown (docs/HAZARDS.md)",
		RunE: func(cmd *cobra.Command, args []string) error {
			w, closeFn, err := outWriter(out)
			if err != nil {
				return err
			}
			defer closeFn()
			writeCatalog(w)
			return nil
		},
	}
	catalog.Flags().StringVar(&out, "out", "", "output file (default stdout)")

	var summary string
	sh := &cobra.Command{
		Use:   "shadow",
		Short: "Render a shadow-read summary (shadow-summary.json) as Markdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			var s shadow.Summary
			if err := state.ReadJSON(summary, &s); err != nil {
				return err
			}
			w, closeFn, err := outWriter(out)
			if err != nil {
				return err
			}
			defer closeFn()
			WriteShadowMarkdown(w, &s)
			return nil
		},
	}
	sh.Flags().StringVar(&summary, "summary", "state/shadow/shadow-summary.json", "summary file")
	sh.Flags().StringVar(&out, "out", "", "output file (default stdout)")
	c.AddCommand(catalog, sh)
	return c
}

func outWriter(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}

func writeCatalog(w io.Writer) {
	fmt.Fprintln(w, "# Hazard catalog")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Generated from `internal/hazard/catalog.go` by `shadowcut report catalog --out docs/HAZARDS.md`.")
	fmt.Fprintln(w, "Do not edit by hand; `TestCatalogDocumented` fails if this file falls out of sync.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Every rule cites its source: official Spanner documentation or a reproducible experiment.")
	fmt.Fprintln(w, "`results/probe/probe.md` is produced by `sc-exp probe`, which runs 142 feature probes on PostgreSQL 16 and on the")
	fmt.Fprintln(w, "**Spanner emulator** through PGAdapter. Emulator-derived evidence must be confirmed on real Spanner")
	fmt.Fprintln(w, "(README, *Pending cloud experiments*).")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "**Predicts** is the Layer 2 mismatch category the rule expects shadow reads to observe.")
	fmt.Fprintln(w, "\"—\" means the hazard cannot be observed by comparing read results (for example hotspots or DDL), so")
	fmt.Fprintln(w, "Experiment 1 does not score it.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| ID | Severity | Area | Predicts | Title |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, r := range hazard.Catalog {
		p := string(r.Predicts)
		if p == "" {
			p = "—"
		}
		fmt.Fprintf(w, "| [%s](#%s) | %s | %s | %s | %s |\n", r.ID, strings.ToLower(r.ID), r.Severity, r.Area, p, r.Title)
	}
	fmt.Fprintln(w)
	for _, r := range hazard.Catalog {
		fmt.Fprintf(w, "## %s\n\n**%s** · severity **%s** · area %s", r.ID, r.Title, r.Severity, r.Area)
		if r.Predicts != "" {
			fmt.Fprintf(w, " · predicts `%s`", r.Predicts)
		}
		fmt.Fprintf(w, "\n\n%s\n\n**Fix.** %s\n\n**Sources.**\n", r.Explain, r.Fix)
		for _, s := range r.Sources {
			if strings.HasPrefix(s, "http") {
				fmt.Fprintf(w, "- <%s>\n", s)
			} else {
				fmt.Fprintf(w, "- %s\n", s)
			}
		}
		fmt.Fprintln(w)
	}
	ev := hazard.LoadEvidence()
	fmt.Fprintln(w, "## Appendix: function support observed on the emulator")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "From `internal/hazard/evidence.csv`, a copy of the probe results that drives Q-UNSUP-FUNC and Q-FUNC-SEMANTICS.")
	fmt.Fprintln(w)
	var unsup, partial, differs []string
	for name, f := range ev.Funcs {
		switch {
		case f.Unsupported():
			unsup = append(unsup, name)
		case f.Partial():
			partial = append(partial, name)
		case f.Differs():
			differs = append(differs, name)
		}
	}
	sort.Strings(unsup)
	sort.Strings(partial)
	sort.Strings(differs)
	fmt.Fprintf(w, "- **Rejected** (%d): `%s`\n", len(unsup), strings.Join(unsup, "`, `"))
	fmt.Fprintf(w, "- **Rejected for some argument types** (%d): `%s`\n", len(partial), strings.Join(partial, "`, `"))
	fmt.Fprintf(w, "- **Supported but results differ** (%d): `%s`\n", len(differs), strings.Join(differs, "`, `"))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "## Checked and found equivalent (emulator)")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Default NULL ordering (ASC → NULLS LAST, DESC → NULLS FIRST), integer division and modulo, numeric")
	fmt.Fprintln(w, "division scale, avg() over numeric and bigint, float/numeric/bool/jsonb text rendering,")
	fmt.Fprintln(w, "`INSERT … ON CONFLICT DO UPDATE … RETURNING`, `= ANY(ARRAY[...])`, `IN (...)`, the regex operator `~`, CTEs, FULL JOIN,")
	fmt.Fprintln(w, "FETCH FIRST, TABLESAMPLE, and timestamp precision (microseconds round-trip exactly). See `results/probe/probe.md`.")
}

// WriteShadowMarkdown renders a shadow summary.
func WriteShadowMarkdown(w io.Writer, s *shadow.Summary) {
	rate := 0.0
	if s.Compared > 0 {
		rate = 100 * float64(s.Mismatches) / float64(s.Compared)
	}
	fmt.Fprintf(w, "Compared **%d** reads, **%d** mismatches (%.1f%%), %d dropped (queue full). Window %s – %s.\n\n",
		s.Compared, s.Mismatches, rate, s.Dropped, s.StartedAt.Format("2006-01-02 15:04"), s.UpdatedAt.Format("15:04 UTC"))
	fmt.Fprintln(w, "| root cause | mismatches | share |")
	fmt.Fprintln(w, "|---|---|---|")
	for _, c := range classify.AllCategories {
		if n := s.ByCategory[c]; n > 0 {
			fmt.Fprintf(w, "| %s | %d | %.1f%% |\n", c, n, 100*float64(n)/float64(max64(s.Mismatches, 1)))
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "| query | compared | mismatches | rate | causes |")
	fmt.Fprintln(w, "|---|---|---|---|---|")
	for _, q := range s.SortedQueries() {
		name := q.Name
		if name == "" {
			name = "`" + trunc(q.Fingerprint, 50) + "`"
		}
		var causes []string
		for _, c := range classify.AllCategories {
			if n := q.ByCategory[c]; n > 0 {
				causes = append(causes, fmt.Sprintf("%s %d", c, n))
			}
		}
		fmt.Fprintf(w, "| %s | %d | %d | %.0f%% | %s |\n", name, q.Compared, q.Mismatches, 100*float64(q.Mismatches)/float64(max64(q.Compared, 1)), strings.Join(causes, ", "))
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
