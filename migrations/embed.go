// Package migrations embeds the SQL migration files so the standalone server
// can apply them without shipping loose files. Migrations are forward-only
// and each file must be safe to re-run reasoning about partial failures.
package migrations

import "embed"

//go:embed sqlite/*.sql
var SQLite embed.FS
