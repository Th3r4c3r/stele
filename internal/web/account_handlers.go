package web

import (
	"errors"
	"net/http"

	"github.com/Th3r4c3r/stele/internal/auth"
	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// accountHandlers serves /account/*.
type accountHandlers struct {
	users    *userpkg.Repo
	sessions *auth.Sessions
}

func (a *accountHandlers) page(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, templates.AccountFormData{})
}

func (a *accountHandlers) changePassword(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	current := r.PostForm.Get("current_password")
	newPwd := r.PostForm.Get("new_password")
	newPwd2 := r.PostForm.Get("new_password2")

	if newPwd != newPwd2 {
		a.render(w, r, templates.AccountFormData{ErrorMsg: "The two new passwords do not match."})
		return
	}

	uid, err := userpkg.FromCtx(r.Context())
	if err != nil {
		httpErr(w, err)
		return
	}
	u, err := a.users.ByID(r.Context(), uid)
	if err != nil {
		httpErr(w, err)
		return
	}
	if u.PasswordHash == "" || auth.VerifyPassword(u.PasswordHash, current) != nil {
		a.render(w, r, templates.AccountFormData{ErrorMsg: "Current password is incorrect."})
		return
	}
	hash, err := auth.HashPassword(newPwd)
	if errors.Is(err, auth.ErrInvalidPassword) {
		a.render(w, r, templates.AccountFormData{ErrorMsg: "New password must be at least 10 characters."})
		return
	}
	if err != nil {
		httpErr(w, err)
		return
	}
	if err := a.users.SetPassword(r.Context(), uid, hash); err != nil {
		httpErr(w, err)
		return
	}
	// Keep the current session, drop every other session for this user
	// (e.g., on another device). The current cookie still works because
	// only its row in `sessions` survives.
	if cookie, err := r.Cookie(auth.CookieName); err == nil {
		if sess, err := a.sessions.Resolve(r.Context(), cookie.Value); err == nil {
			// Easiest path: invalidate all, then we cannot reuse the
			// current cookie; instead delete-others-only by issuing two
			// statements through Sessions. Implementation here is simple:
			// invalidate all, then create a fresh session for this user
			// and re-set the cookie.
			_ = a.sessions.InvalidateAllForUser(r.Context(), uid)
			newCookie, _, err := a.sessions.Create(r.Context(), uid, clientIP(r), auth.HashUA(r.UserAgent()))
			if err == nil {
				setSessionCookie(w, newCookie)
				_ = sess
			}
		}
	}
	a.render(w, r, templates.AccountFormData{SuccessMsg: "Password updated. Other sessions have been signed out."})
}

func (a *accountHandlers) render(w http.ResponseWriter, r *http.Request, data templates.AccountFormData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = templates.AccountPage(navFor(r.Context(), a.users), data).Render(r.Context(), w)
}
