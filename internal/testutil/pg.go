// Package testutil starts a throwaway PostGIS database for integration tests.
// It uses the same image family as production (CloudNativePG PostGIS, PG 18).
package testutil

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/joshdurbin/fuck_cars/internal/storage"
)

const image = "ghcr.io/cloudnative-pg/postgis:18-3.6-standard-trixie"

// Postgres is a running container plus its DSN.
type Postgres struct {
	DSN       string
	container testcontainers.Container
}

// StartPostgres boots the container and applies all migrations.
func StartPostgres(ctx context.Context) (*Postgres, error) {
	req := testcontainers.ContainerRequest{
		Image:        image,
		Entrypoint:   []string{"bash", "-c"},
		Cmd:          []string{"initdb -D /tmp/pgdata -A trust -U postgres >/dev/null && echo 'host all all all trust' >> /tmp/pgdata/pg_hba.conf && exec postgres -D /tmp/pgdata -c listen_addresses=* -c fsync=off -c max_connections=100"},
		ExposedPorts: []string{"5432/tcp"},
		WaitingFor:   wait.ForListeningPort("5432/tcp").WithStartupTimeout(90 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		return nil, fmt.Errorf("start postgres container: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		return nil, err
	}
	port, err := c.MappedPort(ctx, "5432/tcp")
	if err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("postgres://postgres@%s:%s/postgres?sslmode=disable", host, port.Port())
	// The port can accept connections a moment before the server is ready.
	deadline := time.Now().Add(30 * time.Second)
	for {
		err = storage.Migrate(ctx, dsn)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = c.Terminate(ctx)
			return nil, fmt.Errorf("migrate: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return &Postgres{DSN: dsn, container: c}, nil
}

func (p *Postgres) Terminate(ctx context.Context) { _ = p.container.Terminate(ctx) }

// Reset truncates all data (keeps schema) so tests start clean.
func (p *Postgres) Reset(t testing.TB, s *storage.Store) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(), `TRUNCATE incident_events, match_reviews, incident_observations, incident_articles,
		incident_participants, incidents, article_extractions, article_fetch_attempts, articles,
		article_discoveries, discovered_urls, discovery_queries, discovery_sources RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
}
