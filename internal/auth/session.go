package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// CookieName is the session cookie used by Stele.
	CookieName = "stele_session"
	// SessionTTL is how long a session lasts without activity.
	SessionTTL = 30 * 24 * time.Hour
	// MinSecretLen is the minimum byte length for STELE_SESSION_SECRET.
	MinSecretLen = 32
)

// Session is a row in the sessions table.
type Session struct {
	ID         uuid.UUID
	UserID     uuid.UUID
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
}

// Sessions is the persistence + HMAC layer for sessions.
type Sessions struct {
	pool   *pgxpool.Pool
	secret []byte
}

// NewSessions wraps a pool and the signing secret. Returns an error if
// secret is shorter than MinSecretLen.
func NewSessions(pool *pgxpool.Pool, secret []byte) (*Sessions, error) {
	if len(secret) < MinSecretLen {
		return nil, fmt.Errorf("auth.NewSessions: secret too short (%d < %d)", len(secret), MinSecretLen)
	}
	return &Sessions{pool: pool, secret: secret}, nil
}

// Create persists a session for userID and returns the cookie string.
func (s *Sessions) Create(ctx context.Context, userID uuid.UUID, ip, uaHash string) (string, *Session, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC()
	sess := &Session{
		ID:         id,
		UserID:     userID,
		CreatedAt:  now,
		ExpiresAt:  now.Add(SessionTTL),
		LastSeenAt: now,
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO sessions (id, user_id, created_at, expires_at, last_seen_at, ip, user_agent_hash)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, sess.ID, sess.UserID, sess.CreatedAt, sess.ExpiresAt, sess.LastSeenAt, ip, uaHash)
	if err != nil {
		return "", nil, fmt.Errorf("sessions.Create: %w", err)
	}
	return s.cookieValue(id), sess, nil
}

// Resolve parses+validates a cookie value, looks up the session, and
// refreshes last_seen_at (sliding expiry). Returns ErrInvalidSession
// for any failure (bad signature, expired, not found).
var ErrInvalidSession = errors.New("invalid or expired session")

func (s *Sessions) Resolve(ctx context.Context, cookieValue string) (*Session, error) {
	id, ok := s.parseCookie(cookieValue)
	if !ok {
		return nil, ErrInvalidSession
	}
	var sess Session
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, created_at, expires_at, last_seen_at
		FROM sessions
		WHERE id = $1 AND expires_at > now()
	`, id).Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrInvalidSession
	}
	if err != nil {
		return nil, fmt.Errorf("sessions.Resolve: %w", err)
	}
	// Sliding expiry: touch last_seen_at + push expires_at forward.
	now := time.Now().UTC()
	_, _ = s.pool.Exec(ctx, `
		UPDATE sessions SET last_seen_at = $2, expires_at = $3 WHERE id = $1
	`, sess.ID, now, now.Add(SessionTTL))
	return &sess, nil
}

// Invalidate deletes one session (logout).
func (s *Sessions) Invalidate(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

// InvalidateAllForUser deletes every session of a user (e.g., after
// password change or admin deactivate).
func (s *Sessions) InvalidateAllForUser(ctx context.Context, userID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, userID)
	return err
}

// PurgeExpired deletes rows past their expires_at. Called by a cron or
// at boot; pure housekeeping.
func (s *Sessions) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// cookieValue builds "<id>.<hmac-hex>".
func (s *Sessions) cookieValue(id uuid.UUID) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(id.String()))
	return id.String() + "." + hex.EncodeToString(mac.Sum(nil))
}

// parseCookie verifies signature and returns the embedded session id.
func (s *Sessions) parseCookie(v string) (uuid.UUID, bool) {
	parts := strings.SplitN(v, ".", 2)
	if len(parts) != 2 {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, false
	}
	sig, err := hex.DecodeString(parts[1])
	if err != nil {
		return uuid.Nil, false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(parts[0]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return uuid.Nil, false
	}
	return id, true
}

// HashUA returns a short stable hash for the User-Agent string. Used
// only for forensic logging, not for binding.
func HashUA(ua string) string {
	h := sha256.Sum256([]byte(ua))
	return hex.EncodeToString(h[:6])
}
