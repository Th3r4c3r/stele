// Package audit stores and queries the /admin/* action log.
// See migration 0012 + web.AuditAdminActions middleware for how rows
// land in admin_audit. This package is intentionally minimal: a Repo
// for read+write, plus context helpers so handlers can attach a
// human-readable summary to the row the middleware will write.
package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Entry is one row of admin_audit.
type Entry struct {
	ID          uuid.UUID
	At          time.Time
	ActorID     *uuid.UUID
	ActorEmail  string
	Method      string
	Path        string
	Status      int
	Summary     string // optional, filled by handlers via SetSummary
	IP          string
	UserAgent   string
}

// Repo persists and reads admin_audit rows.
type Repo struct{ pool *pgxpool.Pool }

func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Log inserts e. ID is generated here if Entry.ID is uuid.Nil so
// callers can stay terse. Errors are returned, never panicked: an
// audit insert failure must not break the underlying request.
func (r *Repo) Log(ctx context.Context, e Entry) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.Must(uuid.NewV7())
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO admin_audit (id, at, actor_user_id, actor_email,
		                        method, path, status, summary, ip, user_agent)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, e.ID, e.At, e.ActorID, e.ActorEmail,
		e.Method, e.Path, e.Status, nullable(e.Summary),
		nullable(e.IP), nullable(e.UserAgent))
	if err != nil {
		return fmt.Errorf("audit.Log: %w", err)
	}
	return nil
}

// List returns the most recent `limit` entries, newest first.
func (r *Repo) List(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, at, actor_user_id, actor_email, method, path, status,
		       COALESCE(summary, ''), COALESCE(ip, ''), COALESCE(user_agent, '')
		FROM admin_audit
		ORDER BY at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("audit.List: %w", err)
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.At, &e.ActorID, &e.ActorEmail,
			&e.Method, &e.Path, &e.Status, &e.Summary, &e.IP, &e.UserAgent); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// nullable returns nil for empty strings so empty user-agent /
// ip / summary land as NULL in the database instead of '' (cleaner
// for downstream queries that COALESCE).
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// --- context-injected summary ---
//
// Handlers call SetSummary(r.Context(), "...") to attach a
// human-readable description of the action. The middleware reads it
// back via SummaryFrom AFTER the handler returns and includes it on
// the audit row. Storing the slot in a pointer makes the value
// mutable through a single context value (so we don't have to thread
// a new context down the call stack just to set a string).

type summaryKey struct{}

// summarySlot is a mutable holder, registered once per request via
// WithSummarySlot. Handlers use SetSummary to write into it; the
// middleware uses SummaryFrom to read it back.
type summarySlot struct{ s string }

// WithSummarySlot installs an empty slot into ctx. Idempotent.
// The middleware calls this before invoking the wrapped handler.
func WithSummarySlot(ctx context.Context) context.Context {
	if ctx.Value(summaryKey{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, summaryKey{}, &summarySlot{})
}

// SetSummary updates the per-request audit summary. No-op if no slot
// has been installed (e.g., on non-admin routes), so handlers can
// call it unconditionally without worrying about route context.
func SetSummary(ctx context.Context, s string) {
	slot, ok := ctx.Value(summaryKey{}).(*summarySlot)
	if !ok {
		return
	}
	slot.s = s
}

// SummaryFrom retrieves the summary set by SetSummary, or "".
func SummaryFrom(ctx context.Context) string {
	slot, ok := ctx.Value(summaryKey{}).(*summarySlot)
	if !ok {
		return ""
	}
	return slot.s
}
