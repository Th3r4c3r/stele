// Package user holds the User read-only types and lookup helpers.
//
// User records are master data managed outside the event log (no
// User aggregate at M3; auth deferred to M5+). See ADR-008.
package user

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	PasswordHash    string
	DeactivatedAt   *time.Time
}

// IsAdmin returns true for users with role "admin".
func (u User) IsAdmin() bool { return u.Role == "admin" }

// IsActive returns true for users not soft-deleted.
func (u User) IsActive() bool { return u.DeactivatedAt == nil }

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
		SELECT id, email, name, role, region, specializations,
		       COALESCE(password_hash, ''), deactivated_at
		FROM users WHERE id = $1
	`, id).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.Specializations,
		&u.PasswordHash, &u.DeactivatedAt)
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
		SELECT id, email, name, role, region, specializations,
		       COALESCE(password_hash, ''), deactivated_at
		FROM users WHERE email = $1
	`, email).Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.Specializations,
		&u.PasswordHash, &u.DeactivatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("user.ByEmail: %w", err)
	}
	return u, nil
}

// List returns active users, ordered by name.
func (r *Repo) List(ctx context.Context) ([]User, error) {
	return r.list(ctx, false)
}

// ListAll returns all users including deactivated. Admin use only.
func (r *Repo) ListAll(ctx context.Context) ([]User, error) {
	return r.list(ctx, true)
}

func (r *Repo) list(ctx context.Context, includeDeactivated bool) ([]User, error) {
	q := `SELECT id, email, name, role, region, specializations,
	             COALESCE(password_hash, ''), deactivated_at
	      FROM users`
	if !includeDeactivated {
		q += ` WHERE deactivated_at IS NULL`
	}
	q += ` ORDER BY name`
	rows, err := r.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("user.list: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.Region, &u.Specializations,
			&u.PasswordHash, &u.DeactivatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetPassword stores a new PHC hash for the user.
func (r *Repo) SetPassword(ctx context.Context, userID uuid.UUID, hash string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2 WHERE id = $1`, userID, hash)
	if err != nil {
		return fmt.Errorf("user.SetPassword: %w", err)
	}
	return nil
}

// Deactivate marks the user as deactivated_at = now.
func (r *Repo) Deactivate(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET deactivated_at = now() WHERE id = $1`, userID)
	return err
}

// Reactivate clears deactivated_at.
func (r *Repo) Reactivate(ctx context.Context, userID uuid.UUID) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE users SET deactivated_at = NULL WHERE id = $1`, userID)
	return err
}

// Upsert inserts or updates a user by email. Used by the seeder; not
// meant for application code paths.
func (r *Repo) Upsert(ctx context.Context, u User) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.Must(uuid.NewV7())
	}
	if u.Specializations == nil {
		u.Specializations = []string{}
	}
	if u.Role == "" {
		u.Role = "ops"
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO users (id, email, name, role, region, specializations, password_hash)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''))
		ON CONFLICT (email) DO UPDATE
		   SET name = EXCLUDED.name,
		       role = EXCLUDED.role,
		       region = EXCLUDED.region,
		       specializations = EXCLUDED.specializations,
		       password_hash = COALESCE(EXCLUDED.password_hash, users.password_hash)
	`, u.ID, u.Email, u.Name, u.Role, u.Region, u.Specializations, u.PasswordHash)
	if err != nil {
		return fmt.Errorf("user.Upsert: %w", err)
	}
	return nil
}
