package user

import (
	"context"
	"errors"

	"github.com/google/uuid"
)

// ctxKey is unexported so other packages must go through the helpers.
type ctxKey struct{}

// WithID returns a derived context carrying the active user id.
// Used by the HTTP middleware.
func WithID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromCtx returns the active user id, or ErrNoCurrentUser if missing.
// Background workers (e.g., projection runner) do not set it; they
// should write events with recorded_by = "system" explicitly.
func FromCtx(ctx context.Context) (uuid.UUID, error) {
	v, ok := ctx.Value(ctxKey{}).(uuid.UUID)
	if !ok || v == uuid.Nil {
		return uuid.Nil, ErrNoCurrentUser
	}
	return v, nil
}

// ErrNoCurrentUser is returned by FromCtx when nothing was injected.
var ErrNoCurrentUser = errors.New("user: no current user in context")
