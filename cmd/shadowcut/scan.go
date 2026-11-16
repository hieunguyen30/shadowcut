package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/hazard"
	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/state"
)

func scanCmd() *cobra.Command {
	var schemas, queryFiles, logs []string
	var statsDSN, jsonOut, mdOut, waivers, failOn, minSev string
	c := &cobra.Command{
		Use:   "scan",
		Short: "Layer 1: static hazard scan of schema and queries (readiness report)",
		Long: `Reads the application's schema (DDL) and SQL (annotated .sql files, JSONL query
logs with a "sql" field, and/or pg_stat_statements) and reports known
Postgres -> Spanner (PostgreSQL interface) hazards with severity, location,
explanation, fix and source. Exit status 1 when a finding at or above
--fail-on is not waived.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var schemaIn, queryIn []hazard.Input
			for _, f := range schemas {
				b, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				schemaIn = append(schemaIn, hazard.Input{File: f, SQL: string(b)})
			}
			for _, f := range queryFiles {
				src, err := readQueries(f)
				if err != nil {
					return err
				}
				queryIn = append(queryIn, hazard.Input{File: f, SQL: src})
			}
			for _, f := range logs {
				src, err := readJSONLQueries(f)
				if err != nil {
					return err
				}
				queryIn = append(queryIn, hazard.Input{File: f, SQL: src})
			}
			if statsDSN != "" {
				src, err := pgStatStatements(cmd, statsDSN)
				if err != nil {
					return err
				}
				queryIn = append(queryIn, hazard.Input{File: "pg_stat_statements", SQL: src})
			}
			rep := hazard.New(nil).Scan(schemaIn, queryIn)
			if waivers != "" {
				ws, err := hazard.LoadWaivers(waivers)
				if err != nil {
					return err
				}
				rep.ApplyWaivers(ws)
			}
			rep.WriteText(os.Stdout, hazard.Severity(minSev))
			if jsonOut != "" {
				if err := state.WriteJSON(jsonOut, rep); err != nil {
					return err
				}
			}
			if mdOut != "" {
				f, err := os.Create(mdOut)
				if err != nil {
					return err
				}
				rep.WriteMarkdown(f)
				f.Close()
			}
			if failOn != "" && failOn != "none" && rep.Unresolved(hazard.Severity(failOn)) > 0 {
				return exitCode(1)
			}
			return nil
		},
	}
	c.Flags().StringSliceVar(&schemas, "schema", []string{"schema/postgres.sql"}, "DDL files")
	c.Flags().StringSliceVar(&queryFiles, "queries", []string{"queries/app.sql"}, "annotated SQL files")
	c.Flags().StringSliceVar(&logs, "log", nil, "JSONL query logs (objects with a \"sql\" field, e.g. state/shadow/mismatches.jsonl)")
	c.Flags().StringVar(&statsDSN, "pg-stat-statements", "", "Postgres DSN to read captured queries from pg_stat_statements")
	c.Flags().StringVar(&jsonOut, "json", "", "write the JSON report here")
	c.Flags().StringVar(&mdOut, "markdown", "", "write a Markdown report here")
	c.Flags().StringVar(&waivers, "waivers", "", "YAML file of accepted hazards")
	c.Flags().StringVar(&failOn, "fail-on", "none", "exit 1 if an unwaived finding has at least this severity (high|medium|low|none)")
	c.Flags().StringVar(&minSev, "min-severity", "low", "minimum severity to print")
	return c
}

// readJSONLQueries turns {"sql": ...} lines into a ;-separated script.
func readJSONLQueries(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	seen := map[string]bool{}
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var m struct {
			SQL  string `json:"sql"`
			Name string `json:"name"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil || m.SQL == "" || seen[m.SQL] {
			continue
		}
		seen[m.SQL] = true
		if m.Name != "" {
			fmt.Fprintf(&b, "-- name: %s\n", m.Name)
		}
		b.WriteString(strings.TrimRight(m.SQL, "; \n"))
		b.WriteString(";\n\n")
	}
	return b.String(), sc.Err()
}

// pgStatStatements pulls normalized statements captured by pg_stat_statements.
func pgStatStatements(cmd *cobra.Command, dsn string) (string, error) {
	if dsn == "default" {
		dsn = pgutil.DefaultPG()
	}
	conn, err := pgx.Connect(cmd.Context(), dsn)
	if err != nil {
		return "", err
	}
	defer conn.Close(cmd.Context())
	rows, err := conn.Query(cmd.Context(), `SELECT query FROM pg_stat_statements
		WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND query !~* '^\s*(begin|commit|rollback|set|show|select \$1|select pg_|deallocate|discard)'
		  AND query !~ 'pg_catalog|information_schema|pg_stat_statements|shadowcut\.control'
		ORDER BY calls DESC LIMIT 1000`)
	if err != nil {
		return "", fmt.Errorf("pg_stat_statements: %w", err)
	}
	qs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, q := range qs {
		b.WriteString(strings.TrimRight(q, "; \n"))
		b.WriteString(";\n\n")
	}
	return b.String(), nil
}
