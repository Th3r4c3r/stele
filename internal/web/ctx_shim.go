package web

import "context"

// Tiny shims so handler helpers can take a context without dragging
// the import into every file that grew up before this package did.
type contextLike = context.Context

func asStdCtx(c contextLike) context.Context { return c }
