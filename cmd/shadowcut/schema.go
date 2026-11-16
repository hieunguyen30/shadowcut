package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

func schemaCmd() *cobra.Command {
	c := &cobra.Command{Use: "schema", Short: "Manage the Spanner target schema"}
	var dsn, file string
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Apply schema/spanner.sql through PGAdapter (idempotent: existing objects are skipped)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			src, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			conn, err := pgutil.Connect(ctx, dsn, 1)
			if err != nil {
				return err
			}
			defer conn.Close()
			applied, skipped := 0, 0
			for _, st := range sqlscan.Split(string(src)) {
				if _, err := conn.Exec(ctx, st.Text); err != nil {
					msg := strings.ToLower(err.Error())
					if strings.Contains(msg, "duplicate") || strings.Contains(msg, "already exists") {
						skipped++
						continue
					}
					return fmt.Errorf("line %d: %w", st.Line, err)
				}
				applied++
			}
			slog.Info("schema applied", "statements", applied, "skipped_existing", skipped)
			return nil
		},
	}
	apply.Flags().StringVar(&dsn, "spanner", pgutil.DefaultSpanner(), "PGAdapter DSN")
	apply.Flags().StringVar(&file, "file", "schema/spanner.sql", "DDL file")
	c.AddCommand(apply)
	return c
}
