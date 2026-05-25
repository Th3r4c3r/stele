package web

import (
	"context"

	userpkg "github.com/Th3r4c3r/stele/internal/user"
	"github.com/Th3r4c3r/stele/internal/web/templates"
)

// navFor builds the topbar NavUser for the request's current user.
// Called by every handler that renders a full page. Cheap: one
// indexed lookup per request, behind a tiny request lifetime.
func navFor(ctx context.Context, users *userpkg.Repo) templates.NavUser {
	id, err := userpkg.FromCtx(ctx)
	if err != nil {
		return templates.NavUser{}
	}
	u, err := users.ByID(ctx, id)
	if err != nil {
		return templates.NavUser{}
	}
	return templates.NavUser{
		Name:    u.Name,
		Email:   u.Email,
		IsAdmin: u.IsAdmin(),
	}
}
