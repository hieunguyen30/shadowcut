// Package queries embeds the demo app's SQL and the curated hazard query set so
// that the app, the scanner and the experiments all use the exact same text.
package queries

import (
	_ "embed"
	"fmt"

	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

//go:embed app.sql
var AppSQL string

//go:embed hazards.sql
var HazardsSQL string

// Named parses an annotated SQL file into name -> statement.
func Named(src string) map[string]sqlscan.Statement {
	out := map[string]sqlscan.Statement{}
	for _, st := range sqlscan.Split(src) {
		if st.Name != "" {
			out[st.Name] = st
		}
	}
	return out
}

// App returns the demo app's statements by name.
func App() map[string]sqlscan.Statement { return Named(AppSQL) }

// MustText returns the SQL text for a named statement or panics.
func MustText(m map[string]sqlscan.Statement, name string) string {
	st, ok := m[name]
	if !ok {
		panic(fmt.Sprintf("queries: no statement named %q", name))
	}
	return st.Text
}
