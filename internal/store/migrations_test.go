package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/HaticeStudio/seo-platform/migrations"
	_ "modernc.org/sqlite"
)

func TestUpgradeFromInitialSchemaPreservesReportRows(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0001_init.sql", "0002_oauth_states.sql"} {
		raw, err := fs.ReadFile(migrations.SQLite, "sqlite/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, '2026-08-01T00:00:00Z')`, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO sites (id, public_url, sitemap_url, timezone) VALUES ('default', 'https://example.test', 'https://example.test/sitemap.xml', 'UTC')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO report_rows (dataset, row_key, data, updated_at) VALUES ('search/daily', 'day', '{"clicks":4}', '2026-08-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	rows, err := upgraded.ListReportRows(context.Background(), "default", "search/daily", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Data["clicks"] != float64(4) {
		t.Fatalf("migrated rows = %+v", rows)
	}
	var applied int
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = '0003_security_and_report_scope.sql'`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("migration records = %d, want 1", applied)
	}
	if err := upgraded.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE name = '0004_oauth_return_to.sql'`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("OAuth return migration records = %d, want 1", applied)
	}
	var returnTo string
	if err := upgraded.db.QueryRow(`SELECT return_to FROM oauth_states LIMIT 1`).Scan(&returnTo); err != sql.ErrNoRows {
		t.Fatalf("OAuth return_to column check: %v", err)
	}
}
