// Package dealer holds the Dealer master data and lookups.
//
// Dealer codes are first-class strings already stored on events;
// this table enriches them with region + country for routing.
// See ADR-008.
package dealer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dealer mirrors a row in the dealers table.
type Dealer struct {
	Code    string
	Name    string
	Region  string
	Country string
}

// ErrNotFound is returned when a lookup yields no row.
var ErrNotFound = errors.New("dealer not found")

type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) ByCode(ctx context.Context, code string) (Dealer, error) {
	var d Dealer
	err := r.pool.QueryRow(ctx, `
		SELECT code, name, region, country FROM dealers WHERE code = $1
	`, code).Scan(&d.Code, &d.Name, &d.Region, &d.Country)
	if errors.Is(err, pgx.ErrNoRows) {
		return Dealer{}, ErrNotFound
	}
	if err != nil {
		return Dealer{}, fmt.Errorf("dealer.ByCode: %w", err)
	}
	return d, nil
}

func (r *Repo) List(ctx context.Context) ([]Dealer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT code, name, region, country FROM dealers ORDER BY code
	`)
	if err != nil {
		return nil, fmt.Errorf("dealer.List: %w", err)
	}
	defer rows.Close()
	var out []Dealer
	for rows.Next() {
		var d Dealer
		if err := rows.Scan(&d.Code, &d.Name, &d.Region, &d.Country); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Upsert inserts or updates a dealer by code. Seeder-only.
func (r *Repo) Upsert(ctx context.Context, d Dealer) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO dealers (code, name, region, country)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (code) DO UPDATE
		   SET name = EXCLUDED.name,
		       region = EXCLUDED.region,
		       country = EXCLUDED.country
	`, d.Code, d.Name, d.Region, d.Country)
	if err != nil {
		return fmt.Errorf("dealer.Upsert: %w", err)
	}
	return nil
}
