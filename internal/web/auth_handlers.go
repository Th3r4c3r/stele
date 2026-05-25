package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Th3r4c3r/stele/internal/auth"
	"github.com/Th3r4c3r/stele/internal/mail"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// authHandlers bundles the unauthenticated routes (login/logout/forgot/reset).
type authHandlers struct {
	users      *userpkg.Repo
	sessions   *auth.Sessions
	resets     *auth.ResetTokens
	rateLimit  *auth.LoginRateLimit
	mailSender mail.Sender
	baseURL    string // used to build reset links
}

func (h *authHandlers) loginGET(w http.ResponseWriter, r *http.Request) {
	ret := safeReturn(r.URL.Query().Get("return"))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.LoginPage(ret, templates.LoginFormData{}).Render(r.Context(), w)
}

func (h *authHandlers) loginPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	pwd := r.PostForm.Get("password")
	ret := safeReturn(r.PostForm.Get("return"))

	render := func(msg string, code int) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		_ = templates.LoginPage(ret, templates.LoginFormData{Email: email, ErrorMsg: msg}).Render(r.Context(), w)
	}

	if email == "" || pwd == "" {
		render("Email and password are required.", http.StatusBadRequest)
		return
	}
	if !h.rateLimit.Allow(email) {
		render("Too many attempts. Try again in a minute.", http.StatusTooManyRequests)
		return
	}

	u, err := h.users.ByEmail(r.Context(), email)
	if errors.Is(err, userpkg.ErrNotFound) {
		h.rateLimit.Failed(email)
		render("Email or password is incorrect.", http.StatusUnauthorized)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	if !u.IsActive() {
		render("This account is deactivated. Contact an admin.", http.StatusForbidden)
		return
	}
	if u.PasswordHash == "" {
		render("This account has no password set. Use 'Forgot your password?'.", http.StatusUnauthorized)
		return
	}
	if err := auth.VerifyPassword(u.PasswordHash, pwd); err != nil {
		h.rateLimit.Failed(email)
		render("Email or password is incorrect.", http.StatusUnauthorized)
		return
	}
	h.rateLimit.Reset(email)

	cookieVal, _, err := h.sessions.Create(r.Context(), u.ID, clientIP(r), auth.HashUA(r.UserAgent()))
	if err != nil {
		httpErr(w, err)
		return
	}
	setSessionCookie(w, cookieVal)
	http.Redirect(w, r, ret, http.StatusSeeOther)
}

func (h *authHandlers) logoutPOST(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		if sess, err := h.sessions.Resolve(r.Context(), cookie.Value); err == nil {
			_ = h.sessions.Invalidate(r.Context(), sess.ID)
		}
	}
	clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (h *authHandlers) forgotGET(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.ForgotPage(templates.ForgotFormData{}).Render(r.Context(), w)
}

func (h *authHandlers) forgotPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	email := strings.ToLower(strings.TrimSpace(r.PostForm.Get("email")))
	render := func() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = templates.ForgotPage(templates.ForgotFormData{Email: email, Submitted: true}).Render(r.Context(), w)
	}
	if email == "" {
		render()
		return
	}
	// Always render the "submitted" response to avoid email enumeration.
	u, err := h.users.ByEmail(r.Context(), email)
	if err == nil && u.IsActive() {
		token, terr := h.resets.Create(r.Context(), u.ID)
		if terr == nil {
			link := h.baseURL + "/reset?token=" + url.QueryEscape(token)
			body := "Hi " + u.Name + ",\n\nUse this link to set a new password (valid for 1 hour):\n" + link + "\n\nIf you did not request this, ignore this email.\n"
			_ = h.mailSender.Send(email, "Stele — password reset", body)
		}
	}
	render()
}

func (h *authHandlers) resetGET(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.ResetPage(token, templates.ResetFormData{}).Render(r.Context(), w)
}

func (h *authHandlers) resetPOST(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token := r.PostForm.Get("token")
	pwd := r.PostForm.Get("password")
	pwd2 := r.PostForm.Get("password2")

	render := func(msg string, code int) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		_ = templates.ResetPage(token, templates.ResetFormData{ErrorMsg: msg}).Render(r.Context(), w)
	}

	if pwd != pwd2 {
		render("Passwords do not match.", http.StatusBadRequest)
		return
	}
	hash, err := auth.HashPassword(pwd)
	if errors.Is(err, auth.ErrInvalidPassword) {
		render("Password must be at least 10 characters.", http.StatusBadRequest)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	userID, err := h.resets.Consume(r.Context(), token)
	if errors.Is(err, auth.ErrInvalidResetToken) {
		render("This reset link is invalid or expired. Request a new one.", http.StatusBadRequest)
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	if err := h.users.SetPassword(r.Context(), userID, hash); err != nil {
		httpErr(w, err)
		return
	}
	// Invalidate any pre-existing sessions; force fresh login.
	_ = h.sessions.InvalidateAllForUser(r.Context(), userID)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.ResetPage(token, templates.ResetFormData{Success: true}).Render(r.Context(), w)
}

// --- helpers ---

func setSessionCookie(w http.ResponseWriter, val string) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    val,
		Path:     "/",
		MaxAge:   int(auth.SessionTTL.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Unix(0, 0),
	})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, _ := strings.Cut(r.RemoteAddr, ":")
	return host
}

// safeReturn caps the return-to URL to a relative path on this site,
// to prevent open-redirect via the login form.
func safeReturn(raw string) string {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "/cases"
	}
	return raw
}
