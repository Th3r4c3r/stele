package web

import (
	"net/http"
	"strings"

	"github.com/Th3r4c3r/stele/internal/auth"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
)

// AuthMiddleware verifies the session cookie and injects user_id into
// ctx. Unauthenticated GETs are 302'd to /login?return=<url>; other
// methods get 401 (HTMX-friendly).
//
// Public routes are matched by prefix and skip the check.
type AuthMiddleware struct {
	sessions *auth.Sessions
	users    *userpkg.Repo
}

func NewAuthMiddleware(sessions *auth.Sessions, users *userpkg.Repo) *AuthMiddleware {
	return &AuthMiddleware{sessions: sessions, users: users}
}

// publicPath returns true for routes that do NOT require auth.
func publicPath(p string) bool {
	switch {
	case p == "/login", p == "/logout", p == "/forgot", p == "/reset":
		return true
	case p == "/healthz":
		return true
	case strings.HasPrefix(p, "/static/"):
		return true
	}
	return false
}

func (m *AuthMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if publicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(auth.CookieName)
		if err != nil {
			m.deny(w, r)
			return
		}
		sess, err := m.sessions.Resolve(r.Context(), cookie.Value)
		if err != nil {
			m.deny(w, r)
			return
		}
		u, err := m.users.ByID(r.Context(), sess.UserID)
		if err != nil || !u.IsActive() {
			_ = m.sessions.Invalidate(r.Context(), sess.ID)
			m.deny(w, r)
			return
		}
		ctx := userpkg.WithID(r.Context(), u.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (m *AuthMiddleware) deny(w http.ResponseWriter, r *http.Request) {
	// Safe methods navigate to a login page; mutating methods get a
	// machine-readable 401 (HTMX-friendly).
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		http.Redirect(w, r, "/login?return="+r.URL.Path, http.StatusFound)
		return
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// AdminOnly wraps a handler so it returns 403 unless the current user
// has role "admin".
func AdminOnly(users *userpkg.Repo, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := userpkg.FromCtx(r.Context())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		u, err := users.ByID(r.Context(), id)
		if err != nil || !u.IsAdmin() {
			http.Error(w, "admin only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
