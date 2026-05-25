# ADR-006: Templ + HTMX patterns

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent)
- Supersedes: ADR-002 D1 (which had deferred Templ to M2)

## Context

ADR-002 D1 promised to revisit the templating choice at M2, when the
first real UI lands. The decision arrives now. We also need to fix
the HTMX patterns we will reuse across handlers so M3+ does not
re-invent them.

## Decisions

### D1. Adopt Templ for HTML rendering

- Library: `github.com/a-h/templ` v0.3.x.
- Source files: `internal/web/templates/*.templ`.
- Code-gen: `templ generate` writes `*_templ.go` next to each `.templ`
  source.
- Generated files ARE committed to the repo (`*_templ.go` tracked, not
  gitignored). Rationale:
  - Build pipeline stays a plain `go build`. CI, Docker, and Hetzner
    do not need `templ` installed.
  - Reviewers see the rendered Go alongside the source.
  - Trade-off: PRs that touch templates are noisier; acceptable on a
    single-user repo.
- A `make templ` target (or `go generate ./...` via a `//go:generate`
  directive in `internal/web/templates/doc.go`) regenerates before
  commit. CI verifies via `templ generate && git diff --exit-code`.

Why Templ over stdlib `html/template`:
- Type-safe: components are Go functions, parameters checked at
  compile time. No runtime "no such field .Foo" surprises.
- Composition matches Go syntax (`@Layout(...) { @Children() }`),
  natural to express HTMX-driven partial fragments.
- Zero runtime overhead vs `html/template`; the generated code is
  plain `io.Writer` calls.
- Cost: extra binary in the dev loop. Bounded because we commit the
  generated files.

### D2. HTMX as the only interactivity layer

- Single `<script src="/static/htmx.min.js">` in the base layout.
- No bundler, no SPA build, no other JS.
- HTMX version pinned, file vendored under `internal/web/static/` and
  served by an `http.FileServer`. (No CDN; offline-friendly, also
  avoids 3rd-party requests from the app.)

### D3. Page vs fragment endpoints

Every interactive widget has two response shapes:

- **Page response** (full HTML, with `<html>` + base layout) for
  direct navigation, refresh, bookmarks.
- **Fragment response** (no layout, just the changed partial) for
  HTMX `hx-target` swaps.

The handler decides by inspecting `HX-Request: true` header:

```go
if r.Header.Get("HX-Request") == "true" {
    fragment.Render(ctx, w)
    return
}
layout.Render(ctx, w, fragment)
```

Convention: every Templ component is "fragment-shaped" by default; the
`Layout` component wraps a child. No component renders both layout and
content.

### D4. Form submissions: progressive enhancement

- All forms POST to a real endpoint that returns a useful HTML response
  without HTMX (works with JS disabled).
- HTMX adds `hx-post` + `hx-target` to swap only the relevant fragment.
- Server returns the same content; client uses HX-Request to choose
  fragment vs page.

### D5. Route layout

```
GET  /                            redirect to /claims (M2)
GET  /claims                      list view
GET  /claims/new                  new-claim form
POST /claims                      create claim, redirect to detail
GET  /claims/{id}                 detail with event timeline
POST /claims/{id}/notes           add note (returns updated timeline fragment)
POST /claims/{id}/close           close claim (returns updated status fragment)

GET  /static/*                    HTMX + minimal CSS
GET  /healthz                     (unchanged)
GET  /debug/event[s], /debug/projections   (unchanged, M1)
```

The `/debug/*` endpoints stay for the bridge period; remove when M2
acceptance confirms warranty UI fully covers their use cases.

### D6. CSS: hand-written, no framework

- Single file `internal/web/static/stele.css`, < 200 lines.
- System fonts. No web fonts.
- Goal: clean, legible, no design ambition. M5+ can introduce a real
  theme if Yan wants.
- Reasoning: 200 lines of CSS lasts longer than any framework upgrade
  cycle, and HTMX patterns don't benefit from Tailwind/Bootstrap.

### D7. Error rendering

- Validation errors: server returns 422 with a fragment that re-renders
  the form with inline error messages. HTMX swaps in place.
- Unexpected errors: server returns 500 with a generic message;
  detailed error in logs only.
- No flash messages, no toast notifications at M2. Inline errors are
  enough.

## Consequences

- A developer setting up the project locally needs `templ` only if
  modifying templates. CI catches drift via `templ generate && diff`.
- `*_templ.go` files inflate diff size; bounded.
- Fragment vs page split: forces handler discipline but pays dividends
  when every action becomes inline-swappable.
- Vendored HTMX increases binary size by ~50KB (gzipped). Trivial.

## Open questions deferred

- Server-sent events / WebSockets for live updates (e.g., notes
  appearing in real time across tabs): not at M2, single user.
- Dark mode: not at M2; CSS variables make it trivial to add later.
- Internationalization: not at M2; UI text in English per CLAUDE.md
  conventions.
