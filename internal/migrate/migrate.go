// Package migrate runs the embedded schema migrations on startup.
package migrate

import (
	"embed"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

// Up applies all pending migrations from the embedded filesystem.
// Returns nil if the schema is already at head.
//
// databaseURL must be a libpq-style DSN, e.g.
// postgres://user:pass@host:5432/db?sslmode=disable.
// The migrate driver opens its own connection; the application pool is
// not shared, by design (migrations should not consume app pool slots).
func Up(fsys embed.FS, databaseURL string) error {
	src, err := iofs.New(fsys, ".")
	if err != nil {
		return fmt.Errorf("migrate: iofs: %w", err)
	}
	// pgx v5 driver registers itself under the "pgx5" scheme.
	pgxURL := "pgx5://" + trimScheme(databaseURL)
	m, err := migrate.NewWithSourceInstance("iofs", src, pgxURL)
	if err != nil {
		return fmt.Errorf("migrate: new: %w", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: up: %w", err)
	}
	return nil
}

// trimScheme strips a leading "postgres://" or "postgresql://".
func trimScheme(url string) string {
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if len(url) > len(prefix) && url[:len(prefix)] == prefix {
			return url[len(prefix):]
		}
	}
	return url
}
