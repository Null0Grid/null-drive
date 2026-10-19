// Package db opens the SQLite metadata database and applies embedded
// migrations on startup.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens (creating if needed) the database at path.
//
// Connection settings, all applied per connection via the DSN:
//   - journal_mode=WAL: readers never block the writer and vice versa.
//   - busy_timeout=5000: wait up to 5s for a lock instead of failing.
//   - foreign_keys=1: SQLite has them off by default.
//   - synchronous=NORMAL: safe with WAL (no corruption on power loss; at
//     worst the last transaction is rolled back) and much faster than FULL.
//   - secure_delete(1): deleted rows are overwritten with zeros instead of
//     left in free pages. Turning on end-to-end encryption deletes the
//     account's plaintext names and hashes; without this they would stay
//     readable in the file until the pages happened to be reused.
//   - _txlock=immediate: transactions take the write lock at BEGIN, so two
//     writers queue on busy_timeout instead of deadlocking on lock upgrade.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=secure_delete(1)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetConnMaxIdleTime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return db, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		num, _, ok := strings.Cut(name, "_")
		v, err := strconv.Atoi(num)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %q: name must start with NNNN_", name)
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate applies every embedded migration that has not run yet, each in
// its own transaction, and returns the resulting schema version.
func Migrate(ctx context.Context, db *sql.DB) (int, error) {
	return migrateTo(ctx, db, int(^uint(0)>>1))
}

// migrateTo applies migrations up to and including version max. Tests use
// it to put data into an old schema and check that later migrations
// transform it correctly.
func migrateTo(ctx context.Context, db *sql.DB, max int) (int, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT NOT NULL,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}
	migs, err := loadMigrations()
	if err != nil {
		return 0, err
	}
	var current int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return 0, err
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if m.version > max {
			break
		}
		if err := apply(ctx, db, m); err != nil {
			return current, fmt.Errorf("migration %s: %w", m.name, err)
		}
		current = m.version
	}
	return current, nil
}

func apply(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)`,
		m.version, m.name, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	return tx.Commit()
}
