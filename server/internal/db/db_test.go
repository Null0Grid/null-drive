package db

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMigrateIsIdempotent(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	v1, err := Migrate(ctx, d)
	if err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if v1 < 1 {
		t.Fatalf("schema version = %d, want >= 1", v1)
	}
	v2, err := Migrate(ctx, d)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if v1 != v2 {
		t.Fatalf("re-running migrations changed version %d -> %d", v1, v2)
	}

	for _, table := range []string{"users", "sessions", "devices", "files", "settings", "change_counter"} {
		var n int
		if err := d.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	var setup string
	if err := d.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='setup_complete'`).Scan(&setup); err != nil {
		t.Fatal(err)
	}
	if setup != "false" {
		t.Errorf("setup_complete = %q, want false", setup)
	}
}

func TestForeignKeysEnabled(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "fk.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var on int
	if err := d.QueryRow(`PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Fatalf("foreign_keys = %d, want 1", on)
	}
}

func TestFolderBackfill(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "bf.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := migrateTo(ctx, d, 1); err != nil {
		t.Fatal(err)
	}
	// Phase 1 data: implicit folders only, plus a tombstoned row at a path
	// that is about to become a folder.
	stmts := []string{
		`INSERT INTO users (id, username, password_hash, created_at) VALUES (1, 'a', 'h', 'x')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at) VALUES (1, 'docs/sub/a.txt', 1, 'h', 1, 'x')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at) VALUES (1, 'docs/b.txt', 1, 'h', 2, 'x')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at) VALUES (1, 'top.txt', 1, 'h', 3, 'x')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at, deleted_at) VALUES (1, 'docs', 1, 'h', 4, 'x', 'x')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at, deleted_at) VALUES (1, 'gone/x', 1, 'h', 5, 'x', 'x')`,
		`UPDATE change_counter SET value = 5`,
	}
	for _, s := range stmts {
		if _, err := d.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	rows, err := d.QueryContext(ctx,
		`SELECT path, version FROM files WHERE is_dir = 1 AND deleted_at IS NULL ORDER BY path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		var v int64
		if err := rows.Scan(&p, &v); err != nil {
			t.Fatal(err)
		}
		if v != 6 {
			t.Errorf("folder %s version = %d, want 6", p, v)
		}
		got = append(got, p)
	}
	if want := []string{"docs", "docs/sub"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("folders = %v, want %v (deleted 'gone/x' must not create 'gone')", got, want)
	}
}

// TestChunkBackfill: Phase 1-5 blobs become one-chunk versions in place,
// with correct refcounts, and deleted files stay restorable.
func TestChunkBackfill(t *testing.T) {
	ctx := context.Background()
	d, err := Open(filepath.Join(t.TempDir(), "cb.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := migrateTo(ctx, d, 3); err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		`INSERT INTO users (id, username, password_hash, created_at) VALUES (1, 'a', 'h', 'x')`,
		`INSERT INTO files (user_id, path, is_dir, size, sha256, version, modified_at) VALUES (1, 'docs', 1, 0, '', 1, '2026-10-01T00:00:00.000Z')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at) VALUES (1, 'docs/a.txt', 3, 'aaa', 2, '2026-10-01T00:00:00.000Z')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at) VALUES (1, 'copy.txt', 3, 'aaa', 3, '2026-10-02T00:00:00.000Z')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at) VALUES (1, 'empty.txt', 0, 'e3b0', 4, '2026-10-02T00:00:00.000Z')`,
		`INSERT INTO files (user_id, path, size, sha256, version, modified_at, deleted_at) VALUES (1, 'old.txt', 5, 'bbb', 5, '2026-10-02T00:00:00.000Z', '2026-10-03T00:00:00.000Z')`,
	}
	for _, s := range stmts {
		if _, err := d.ExecContext(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := Migrate(ctx, d); err != nil {
		t.Fatal(err)
	}
	q := func(sql string) int {
		var n int
		if err := d.QueryRowContext(ctx, sql).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	checks := map[string]int{
		`SELECT COUNT(*) FROM chunks`:                                                  2, // aaa (shared) + bbb; empty files have no chunk
		`SELECT refcount FROM chunks WHERE hash = 'aaa'`:                               2,
		`SELECT refcount FROM chunks WHERE hash = 'bbb'`:                               1,
		`SELECT COUNT(*) FROM file_versions`:                                           4, // folders get none
		`SELECT COUNT(*) FROM files WHERE is_dir = 0 AND version_id IS NULL`:           0,
		`SELECT COUNT(*) FROM version_chunks`:                                          3,
		`SELECT COUNT(*) FROM files WHERE path = 'old.txt' AND version_id IS NOT NULL`: 1,
	}
	for sql, want := range checks {
		if got := q(sql); got != want {
			t.Errorf("%s = %d, want %d", sql, got, want)
		}
	}
	// The triggers are live after the backfill.
	if _, err := d.ExecContext(ctx, `DELETE FROM version_chunks WHERE hash = 'bbb'`); err != nil {
		t.Fatal(err)
	}
	if got := q(`SELECT refcount FROM chunks WHERE hash = 'bbb'`); got != 0 {
		t.Fatalf("trigger: refcount %d", got)
	}
}
