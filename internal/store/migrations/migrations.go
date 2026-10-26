// Package migrations embeds the SQL migrations, one directory per Postgres schema.
package migrations

import "embed"

// FS holds every schema's migrations.
//
//go:embed control/*.sql events/*.sql auth/*.sql rtdb/*.sql storage/*.sql documents/*.sql
var FS embed.FS

// Schemas lists the schemas in the order they are migrated.
var Schemas = []string{"control", "events", "auth", "rtdb", "storage", "documents"}
