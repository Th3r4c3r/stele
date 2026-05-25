package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ResetTTL is how long a password reset link is valid.
const ResetTTL = time.Hour

// ErrInvalidResetToken is returned by ConsumeResetToken for any failure.
var ErrInvalidResetToken = errors.New("invalid or expired reset token")

// ResetTokens manages the password_reset_tokens table.
type ResetTokens struct {
	pool *pgxpool.Pool
}

func NewResetTokens(pool *pgxpool.Pool) *ResetTokens {
	return &ResetTokens{pool: pool}
}

// Create returns the plaintext token (to be sent in the email). The
// table stores only its sha256, so a DB leak does not enable resets.
func (r *ResetTokens) Create(ctx context.Context, userID uuid.UUID) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	plain := base64.URLEncoding.EncodeToString(raw)
	hash := hashToken(plain)
	expires := time.Now().Add(ResetTTL).UTC()
	_, err := r.pool.Exec(ctx, `
		INSERT INTO password_reset_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)
	`, hash, userID, expires)
	if err != nil {
		return "", fmt.Errorf("reset.Create: %w", err)
	}
	return plain, nil
}

// Consume looks up the token and marks it used. Returns the user id
// associated with the token. Used tokens cannot be consumed again.
func (r *ResetTokens) Consume(ctx context.Context, plain string) (uuid.UUID, error) {
	hash := hashToken(plain)
	var userID uuid.UUID
	err := r.pool.QueryRow(ctx, `
		UPDATE password_reset_tokens
		   SET used_at = now()
		 WHERE token_hash = $1
		   AND used_at IS NULL
		   AND expires_at > now()
		RETURNING user_id
	`, hash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInvalidResetToken
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("reset.Consume: %w", err)
	}
	return userID, nil
}

// PurgeExpired removes used or expired reset tokens (housekeeping).
func (r *ResetTokens) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM password_reset_tokens
		WHERE used_at IS NOT NULL OR expires_at <= now()
	`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func hashToken(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}
