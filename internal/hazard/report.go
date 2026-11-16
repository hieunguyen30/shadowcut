package hazard

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Waiver accepts a hazard for a rule (and optionally a query name/fingerprint).
type Waiver struct {
	Rule   string `yaml:"rule"`
	Query  string `yaml:"query"` // name, fingerprint, or "*" / "" for all
	Reason string `yaml:"reason"`
}

// LoadWaivers reads a waivers YAML file ({waivers: [...]}).
func LoadWaivers(path string) ([]Waiver, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Waivers []Waiver `yaml:"waivers"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc.Waivers, nil
}

// ApplyWaivers marks matching findings as waived.
func (r *Report) ApplyWaivers(ws []Waiver) {
	for i := range r.Findings {
		f := &r.Findings[i]
		for _, w := range ws {
			if w.Rule != f.Rule && w.Rule != "*" {
				continue
			}
			if w.Query == "" || w.Query == "*" || w.Query == f.Query || w.Query == f.Fingerprint {
				f.Waived = w.Reason
				if f.Waived == "" {
					f.Waived = "waived"
				}
				break
			}
		}
	}
}

// WriteText renders a human-readable report.
func (r *Report) WriteText(w io.Writer, minSev Severity) {
	fmt.Fprintf(w, "Shadowcut readiness report: %d findings (", len(r.Findings))
	var parts []string
	for _, s := range []Severity{High, Medium, Low, Info} {
		parts = append(parts, fmt.Sprintf("%s %d", s, r.BySeverity[s]))
	}
	fmt.Fprintf(w, "%s)\n", strings.Join(parts, ", "))
	fmt.Fprintf(w, "Unresolved high-severity: %d\n\n", r.Unresolved(High))
	for _, f := range r.Findings {
		if f.Severity.Rank() < minSev.Rank() {
			continue
		}
		loc := fmt.Sprintf("%s:%d", f.File, f.Line)
		q := ""
		if f.Query != "" {
			q = " [" + f.Query + "]"
		}
		waived := ""
		if f.Waived != "" {
			waived = " (WAIVED: " + f.Waived + ")"
		}
		fmt.Fprintf(w, "%-6s %-20s %s%s%s\n       %s\n       fix: %s\n       source: %s\n", strings.ToUpper(string(f.Severity)), f.Rule, loc, q, waived, f.Message, f.Fix, strings.Join(f.Sources, " ; "))
	}
}

// WriteMarkdown renders the report as Markdown.
func (r *Report) WriteMarkdown(w io.Writer) {
	fmt.Fprintf(w, "# Shadowcut readiness report\n\nGenerated %s from %s.\n\n", r.GeneratedAt.Format("2006-01-02 15:04 UTC"), strings.Join(r.Inputs, ", "))
	fmt.Fprintf(w, "| severity | count |\n|---|---|\n")
	for _, s := range []Severity{High, Medium, Low, Info} {
		fmt.Fprintf(w, "| %s | %d |\n", s, r.BySeverity[s])
	}
	fmt.Fprintf(w, "\n**Unresolved high-severity findings: %d**\n\n", r.Unresolved(High))
	rules := make([]string, 0, len(r.ByRule))
	for k := range r.ByRule {
		rules = append(rules, k)
	}
	sort.Slice(rules, func(i, j int) bool { return RuleByID[rules[i]].Severity.Rank() > RuleByID[rules[j]].Severity.Rank() || (RuleByID[rules[i]].Severity == RuleByID[rules[j]].Severity && rules[i] < rules[j]) })
	fmt.Fprintf(w, "| rule | severity | findings | title |\n|---|---|---|---|\n")
	for _, id := range rules {
		fmt.Fprintf(w, "| [%s](../../docs/HAZARDS.md#%s) | %s | %d | %s |\n", id, strings.ToLower(id), RuleByID[id].Severity, r.ByRule[id], RuleByID[id].Title)
	}
	fmt.Fprintf(w, "\n## Findings\n\n| severity | rule | location | query | message |\n|---|---|---|---|---|\n")
	for _, f := range r.Findings {
		msg := strings.ReplaceAll(f.Message, "|", "\\|")
		if f.Waived != "" {
			msg += " _(waived: " + f.Waived + ")_"
		}
		fmt.Fprintf(w, "| %s | %s | %s:%d | %s | %s |\n", f.Severity, f.Rule, f.File, f.Line, f.Query, msg)
	}
}
