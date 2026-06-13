package db

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// New opens a pgx connection pool.
func New(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return pool, nil
}

// Migrate runs all pending UP migrations in order.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	// Sort by the leading integer prefix of each filename so that migrations
	// with four-or-more-digit numbers (e.g. 1000_...) still sort after
	// three-digit ones (e.g. 999_...) regardless of zero-padding.
	sort.Slice(entries, func(i, j int) bool {
		return migrationNumber(entries[i].Name()) < migrationNumber(entries[j].Name())
	})

	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version := strings.TrimSuffix(e.Name(), ".sql")

		var applied bool
		err := pool.QueryRow(ctx, "SELECT TRUE FROM schema_migrations WHERE version=$1", version).Scan(&applied)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if applied {
			continue
		}

		data, err := migrationFS.ReadFile(filepath.Join("migrations", e.Name()))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", e.Name(), err)
		}

		upSQL := ExtractUpBlock(string(data))

		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin transaction for migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(ctx, upSQL); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", e.Name(), err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations(version) VALUES($1)", version); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", e.Name(), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", e.Name(), err)
		}
	}
	return nil
}

// migrationNumber extracts the leading integer from a migration filename
// (e.g. "001_create_users.sql" → 1, "1000_add_index.sql" → 1000).
// Returns 0 if no leading integer is found, placing the file first.
func migrationNumber(name string) int {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	parts := strings.SplitN(base, "_", 2)
	n, _ := strconv.Atoi(parts[0])
	return n
}

// ExtractUpBlock returns only the SQL between -- +migrate Up and -- +migrate Down.
func ExtractUpBlock(sql string) string {
	up := strings.Index(sql, "-- +migrate Up")
	if up == -1 {
		return sql
	}
	sql = sql[up+len("-- +migrate Up"):]
	down := strings.Index(sql, "-- +migrate Down")
	if down != -1 {
		sql = sql[:down]
	}
	return strings.TrimSpace(sql)
}
