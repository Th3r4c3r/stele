// Package migrations embeds Stele's SQL migration files into the binary.
//
// The naming convention is NNNN_description.up.sql / .down.sql, matching
// golang-migrate's expectations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
