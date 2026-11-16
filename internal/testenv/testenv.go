// Package testenv provides Postgres and Spanner (emulator + PGAdapter) for
// integration tests. If SC_TEST_PG_DSN / SC_TEST_SPANNER_DSN are set (e.g. the
// local compose stack or CI service containers) they are used; otherwise the
// databases are started with testcontainers.
package testenv

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/shadowcut/shadowcut/internal/pgutil"
	"github.com/shadowcut/shadowcut/internal/sqlscan"
)

// Env holds connected pools.
type Env struct {
	PG, SP       *pgxpool.Pool
	PGDSN, SPDSN string
}

// Start returns a ready environment or skips the test when Docker is unavailable.
func Start(t *testing.T) *Env {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	pgDSN, spDSN := os.Getenv("SC_TEST_PG_DSN"), os.Getenv("SC_TEST_SPANNER_DSN")
	if pgDSN == "" || spDSN == "" {
		var err error
		pgDSN, spDSN, err = containers(ctx, t)
		if err != nil {
			t.Skipf("no test databases (set SC_TEST_PG_DSN/SC_TEST_SPANNER_DSN or run Docker): %v", err)
		}
	}
	pg, err := pgutil.Connect(ctx, pgDSN, 8)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := pgutil.Connect(ctx, spDSN, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pg.Close(); sp.Close() })
	return &Env{PG: pg, SP: sp, PGDSN: pgDSN, SPDSN: spDSN}
}

func containers(ctx context.Context, t *testing.T) (string, string, error) {
	pgC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "postgres:16",
			Env:          map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_USER": "test", "POSTGRES_DB": "wiki"},
			ExposedPorts: []string{"5432/tcp"},
			WaitingFor:   wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return "", "", err
	}
	t.Cleanup(func() { _ = pgC.Terminate(context.Background()) })
	spC, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "gcr.io/cloud-spanner-pg-adapter/pgadapter-emulator:latest",
			ExposedPorts: []string{"5432/tcp", "9010/tcp"},
			WaitingFor:   wait.ForListeningPort("5432/tcp").WithStartupTimeout(120 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		return "", "", err
	}
	t.Cleanup(func() { _ = spC.Terminate(context.Background()) })
	ph, _ := pgC.Host(ctx)
	pp, _ := pgC.MappedPort(ctx, "5432")
	sh, _ := spC.Host(ctx)
	sp, _ := spC.MappedPort(ctx, "5432")
	return fmt.Sprintf("postgres://test:test@%s:%s/wiki?sslmode=disable", ph, pp.Port()),
		fmt.Sprintf("postgres://%s:%s/wiki?sslmode=disable", sh, sp.Port()), nil
}

// ApplyDDL executes each statement, ignoring "already exists" errors.
func ApplyDDL(ctx context.Context, p *pgxpool.Pool, ddl string) error {
	for _, st := range sqlscan.Split(ddl) {
		if _, err := p.Exec(ctx, st.Text); err != nil {
			m := strings.ToLower(err.Error())
			if strings.Contains(m, "already exists") || strings.Contains(m, "duplicate") {
				continue
			}
			return fmt.Errorf("%s: %w", st.Text, err)
		}
	}
	return nil
}

// Unique returns a name suffix unique to this test run (tables are created per test).
func Unique() string { return fmt.Sprintf("%d", time.Now().UnixNano()%1_000_000_000) }
