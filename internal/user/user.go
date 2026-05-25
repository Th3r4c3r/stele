// Package user holds the User read-only types and lookup helpers.
//
// User records are master data managed outside the event log (no
// User aggregate at M3; auth deferred to M5+). See ADR-008.
package user

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// User mirrors a row in the users table.
type User struct {
	ID              uuid.UUID
	Email           string
	Name            string
	Role            string
	Region          *string  // null for cross-region users
	Specializations []string // fault-code prefixes, e.g., ["BMS_", "MOTOR_"]
}

// ErrNotFound is returned when a lookup yields no row.
var ErrNotFound = errors.New("user not found")

// Repo is a thin wrapper for users queries.
type Repo struct {
	pool *pgxpool.Pool
}

func NewRepo(pool *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

// ByID returns the user with the given id.
func (r *Repo) ByID(ctx context.Context, id uuid.UUID) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, name, role, region, specializations
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.Specializations)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("user.ByID: %w", err)
	}
	return u, nil
}

// ByEmail returns the user with the given email (case-insensitive,
// thanks to citext).
func (r *Repo) ByEmail(ctx context.Context, email string) (User, error) {
	var u User
	err := r.pool.QueryRow(ctx, `
		SELECT id, email, name, role, region, specializations
		FROM users WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.Specializations)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("user.ByEmail: %w", err)
	}
	return u, nil
}

// List returns all users, ordered by name. Cached by the caller if
// the list is queried often (e.g., dropdowns).
func (r *Repo) List(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, email, name, role, region, specializations
		FROM users ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("user.List: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.Specializations); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// Upsert inserts or updates a user by email. Used by the seeder; not
// meant for application code paths.
func (r *Repo) Upsert(ctx context.Context, u User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.Must(uuid.NewV7())
	}
	// pgx maps a nil []string to NULL; the column is NOT NULL DEFAULT '{}'
	// but the default applies only when the column is omitted. Coalesce
	// here so callers don't need to remember to pass an empty slice.
	if u.Specializations == nil {
		u.Specializations = []string{}
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO users (id, email, name, role, region, specializations)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (email) DO UPDATE
		   SET name = EXCLUDED.name,
		       role = EXCLUDED.role,
		       region = EXCLUDED.region,
		       specializations = EXCLUDED.specializations
	`, u.ID, u.Email, u.Name, u.Role, u.Region, u.Specializations)
	if err != nil {
		return fmt.Errorf("user.Upsert: %w", err)
	}
	return nil
}
