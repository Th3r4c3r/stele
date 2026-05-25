# ADR-002: M0 deployment choices

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent)
- Supersedes: nothing
- Resolves open questions from: ADR-001

## Context

ADR-001 left several decisions deferred to M0. With the skeleton being
scaffolded and the Hetzner host inspected, we now have enough information
to lock them in.

Hetzner inspection (2026-05-25):
- CPX22, 63 GB free disk, 2.2 GB RAM available.
- Caddy 2 runs in container `odoo-caddy-1` on docker network `odoo_internal`.
- Odoo (Odoo 18 + Postgres 15) and an undocumented `gidd-app` Streamlit
  container are co-tenants.
- Host loopback is not directly reachable from `odoo-caddy-1`, so the
  Stele app must join `odoo_internal` to be reverse-proxied.

## Decisions

### D1. Templating: stdlib `html/template` for M0, revisit at M2

- M0 ships a single hello page. No need for type-safe templating yet.
- Adopting Templ now would introduce a code-generation step before there
  is anything to generate. Defer until the warranty UI (M2) is real
  enough to justify the build dependency.
- Until M2, all views use `html/template`.

### D2. Database migrations: golang-migrate, file-based

- Library: `github.com/golang-migrate/migrate/v4` with the `file` source
  and `postgres` driver.
- Migrations live in `/migrations` at the repo root (per ADR-001 layout).
- Embedded via `embed.FS` into the binary so a single artifact ships
  schema + code. The migrator runs on startup before HTTP serves.
- Versioning: `NNNN_description.up.sql` and `NNNN_description.down.sql`.
- Not yet vendored at M0 (the skeleton has zero deps). First migration
  arrives with M1 when the `events` table is introduced.

### D3. Reverse proxy: extend the existing Odoo Caddy

- Append the Stele site block (`deploy/Caddyfile.snippet`) to
  `~/odoo/caddy/Caddyfile` on the Hetzner host, then restart the Odoo
  Caddy container. No second Caddy instance.
- Rationale: one TLS cert manager, one log pipeline, less moving parts
  on a 3.7 GB RAM box.
- Risk: a misconfiguration in the Stele block can take down Odoo's
  reverse proxy. Mitigation: validate locally with `caddy fmt` and
  `caddy validate` before reload. Document the rollback (`git revert`
  the Caddyfile edit on the host) in the M0 deploy runbook.

### D4. Docker networking: split per concern

- `stele` (project-private bridge): `stele-app` <-> `stele-db`.
- `odoo_internal` (external, joined): exposes `stele-app` to
  `odoo-caddy-1` only.
- `stele-db` is intentionally **not** attached to `odoo_internal`. The
  Odoo containers cannot reach the Stele database.

### D5. Subdomain: `stele.178-105-44-164.sslip.io`

- sslip.io maps `<dashed-ip>.sslip.io` to the IP. Free, no DNS to manage.
- Caddy gets a Let's Encrypt cert automatically.
- Revisit if/when Yan registers a real domain (post-M5 at the earliest).

### D6. Go module path: `github.com/Th3r4c3r/stele`

- Resolved 2026-05-25: Yan created the repo at
  https://github.com/Th3r4c3r/stele under his personal account.
- The initial scaffold used a `github.com/yan-mtl/stele` placeholder
  while waiting for confirmation; commit after `f617684` renames it.

### D7. Local toolchain: not required at M0

- Yan's Windows machine does not have Go or Docker installed.
- M0 validation pipeline: GitHub Actions runs `go vet`, `gofmt`, `go test`,
  `go build`, and `docker build`. Hetzner runs the image.
- Decision: do not block on installing toolchain locally. Reconsider when
  iteration speed becomes the bottleneck (likely M2).

## Consequences

- Sharing Caddy with Odoo couples the two systems' uptime. Acceptable for
  a single-user side project; revisit if Stele ever serves real users.
- No local `go run` for Yan until he installs Go. Iteration loop is
  push -> CI -> Hetzner. Slow but acceptable at ~30 min/day.
- Module rename will produce a noisy commit. Bounded blast radius
  (only `go.mod` and imports in `/internal` and `/cmd`).

## Follow-ups

- M0 close-out: ask Yan about GitHub destination and public URL exposure.
- Add `caddy validate` step to the deploy runbook when it is written.
- When Templ is adopted at M2, supersede D1 with a new ADR.
