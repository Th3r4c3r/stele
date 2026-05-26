package web

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/Th3r4c3r/stele/internal/audit"
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

// statusRecorder is a minimal http.ResponseWriter wrapper that
// remembers the status code so the audit middleware can decide
// whether to record the row. Default 200 mirrors net/http's
// implicit behaviour when a handler writes a body without an
// explicit WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(p)
}

// AuditAdminActions wraps an admin sub-handler. For non-GET/HEAD
// requests with a 2xx/3xx status it inserts one row into admin_audit.
// Failures (4xx/5xx) are skipped to keep the log meaningful: those
// requests didn't actually mutate state. The summary is set by each
// handler via audit.SetSummary; a missing summary still records the
// row (path + method are enough to reconstruct intent).
//
// Audit insert errors never bubble up to the user: the user's
// request already completed; we only log via slog.
func AuditAdminActions(repo *audit.Repo, users *userpkg.Repo, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cheap path: read-only methods skip audit entirely. Avoids
		// a context allocation on every /admin/* GET.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		ctx := audit.WithSummarySlot(r.Context())
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		// Only log successful mutations. 4xx/5xx mean the user did
		// not change state, so an audit row would be misleading.
		if rec.status < 200 || rec.status >= 400 {
			return
		}
		entry := audit.Entry{
			Method:    r.Method,
			Path:      r.URL.Path,
			Status:    rec.status,
			Summary:   audit.SummaryFrom(ctx),
			IP:        clientIP(r),
			UserAgent: r.UserAgent(),
		}
		if uid, err := userpkg.FromCtx(ctx); err == nil {
			entry.ActorID = &uid
			if u, err := users.ByID(ctx, uid); err == nil {
				entry.ActorEmail = u.Email
			}
		}
		if err := repo.Log(ctx, entry); err != nil {
			slog.Error("audit insert failed",
				"path", entry.Path, "actor", entry.ActorEmail, "err", err)
		}
	})
}

// clientIP is shared with auth_handlers.go — see that file for the
// implementation (handles X-Forwarded-For + RemoteAddr).
