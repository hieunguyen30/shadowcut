package hazard

import (
	_ "embed"
	"encoding/csv"
	"sort"
	"strings"
)

// evidence.csv is copied from results/probe/probe.csv by `sc-exp probe`
// (columns id, area, feature, status). It drives function-support rules so the
// scanner's knowledge comes from a reproducible experiment, not from memory.
//
//go:embed evidence.csv
var evidenceCSV string

// FuncEvidence summarizes probe outcomes for one function.
type FuncEvidence struct {
	Name     string
	Statuses map[string]int // same | differs | unsupported | ...
	ProbeIDs []string
}

// Unsupported reports whether every probe of the function was rejected.
func (f FuncEvidence) Unsupported() bool {
	return f.Statuses["unsupported"] > 0 && f.Statuses["same"] == 0 && f.Statuses["differs"] == 0
}

// Partial reports mixed support (rejected for some argument types).
func (f FuncEvidence) Partial() bool {
	return f.Statuses["unsupported"] > 0 && (f.Statuses["same"] > 0 || f.Statuses["differs"] > 0)
}

// Differs reports an observed result difference.
func (f FuncEvidence) Differs() bool { return f.Statuses["differs"] > 0 }

// Evidence is the parsed probe evidence.
type Evidence struct {
	Funcs  map[string]*FuncEvidence
	Syntax map[string]string // feature -> status
}

// LoadEvidence parses the embedded CSV.
func LoadEvidence() *Evidence {
	return ParseEvidence(evidenceCSV)
}

// ParseEvidence parses probe CSV text.
func ParseEvidence(src string) *Evidence {
	ev := &Evidence{Funcs: map[string]*FuncEvidence{}, Syntax: map[string]string{}}
	recs, err := csv.NewReader(strings.NewReader(src)).ReadAll()
	if err != nil {
		return ev
	}
	for i, r := range recs {
		if i == 0 || len(r) < 4 {
			continue
		}
		id, area, feature, status := r[0], r[1], r[2], r[3]
		switch area {
		case "function":
			f := ev.Funcs[feature]
			if f == nil {
				f = &FuncEvidence{Name: feature, Statuses: map[string]int{}}
				ev.Funcs[feature] = f
			}
			f.Statuses[status]++
			f.ProbeIDs = append(f.ProbeIDs, id)
		default:
			ev.Syntax[feature] = status
		}
	}
	return ev
}

// UnsupportedFuncs lists fully unsupported functions (sorted).
func (e *Evidence) UnsupportedFuncs() []string {
	var out []string
	for n, f := range e.Funcs {
		if f.Unsupported() {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}
