package migrations

import "embed"

// FS contains *sql schema migration files.
//
//go:embed *.sql
var FS embed.FS
