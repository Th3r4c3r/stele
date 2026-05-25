package web

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/google/uuid"

	userpkg "github.com/Th3r4c3r/stele/internal/user"
)

// CurrentUserMiddleware resolves STELE_DEFAULT_USER_EMAIL into a
// user_id at boot and injects it into every request context. When
// real auth lands, swap the resolution source from env to a session
// cookie; handlers do not change.
//
// If the env points to no user, the constructor returns an error so
// the app refuses to start (configuration error, not silent fallback).
type CurrentUserMiddleware struct {
	userID uuid.UUID
}

func NewCurrentUserMiddleware(ctx context.Context, repo *userpkg.Repo) (*CurrentUserMiddleware, error) {
	email := os.Getenv("STELE_DEFAULT_USER_EMAIL")
	if email == "" {
		email = "yan@stele.local"
	}
	u, err := repo.ByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	slog.Info("current-user middleware resolved", "email", u.Email, "id", u.ID)
	return &CurrentUserMiddleware{userID: u.ID}, nil
}

// Wrap is the standard http middleware.
func (m *CurrentUserMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := userpkg.WithID(r.Context(), m.userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// UserID returns the resolved user id (for code that runs outside an
// HTTP request, e.g., a CLI sub-command). At M3 it equals the env-resolved
// user; at M5+ this helper becomes irrelevant once handlers always go
// through context.
func (m *CurrentUserMiddleware) UserID() uuid.UUID { return m.userID }
