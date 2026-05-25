// Package static embeds the small set of files served unchanged.
package static

import "embed"

//go:embed *.css *.js
var FS embed.FS
