# ADR-001: Foundations

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent), reviewed by Yan

## Context

We are building Stele from scratch as a side project, ~30 min/day human review.
We must pick the foundations that are hard to change later, and defer everything else.

## Decisions

### D1. Storage: Postgres 16 with append-only event table + materialized projections

- One table `events` (id, aggregate_id, type, payload jsonb, occurred_at, recorded_at, recorded_by).
- Append-only. No UPDATE, no DELETE except a separate "redaction" ceremony for GDPR.
- Projection tables (`current_claims`, `current_vehicles`, ...) are rebuildable by replay.
- Use Postgres LISTEN/NOTIFY to push updates to projection workers.

### D2. Bi-temporal model from day one

- `occurred_at` — when the fact is true in business reality. User-provided.
- `recorded_at` — when Stele learned about it. System-provided, immutable.
- All projections expose both temporal axes.
- Default queries: "as of now, as known now". Time-travel is a first-class API.

### D3. Backend language: Go

- Single-binary deploy. No JVM, no Python runtime to manage.
- stdlib covers HTTP/JSON/SQL for our scale.
- Faster solo velocity than Rust for this scope; revisit at M5+ if perf demands.
- Rejected: Rust (slower iteration), Elixir (smaller help pool), Node (runtime ops).

### D4. UI: HTMX + Templ, server-rendered

- No SPA build chain.
- Forms post to server, server returns HTML fragments.
- Progressive enhancement; works without JS for core flows.
- Rejected: SvelteKit (extra build complexity), React (ceremony for single-user app).

### D5. Deployment: docker-compose on existing Hetzner CPX22

- Sibling to existing Odoo test instance; share host, not data.
- Caddy reverse proxy with automatic TLS.
- Postgres in container, volume on host disk.
- Backups: `pg_dump` nightly to Hetzner Storage Box (target TBD at M0).
- Subdomain: `stele.178-105-44-164.sslip.io` (placeholder; Caddy host-based routing).

### D6. Auth: cookie session, single user

- Argon2id password hash, HTTP-only secure cookie.
- No SSO, no multi-user, no roles in Phase 1.
- Sufficient for "just for Yan and Claude" scope.

### D7. Repo layout

```
stele/
  cmd/stele/         # main binary
  internal/event/    # event store
  internal/project/  # projection engine
  internal/warranty/ # first domain
  internal/web/      # HTTP handlers + templ templates
  migrations/        # SQL migrations
  docs/adr/          # architectural decision records
  deploy/            # docker-compose, Caddyfile, scripts
  .github/workflows/ # CI
```

### D8. Project management workflow

- Yan reviews ~30 min/day. AI is PM and primary contributor.
- Persistent memory lives in the Obsidian vault at `wiki/stele/` (PM brain).
- Every session writes to `wiki/stele/session_log.md` so the next session resumes cleanly.
- ADRs in repo for code-affecting decisions. Vault for process/strategy/notes.

## Consequences

- Event sourcing has a learning curve; mitigated by starting with a narrow domain.
- Postgres jsonb is flexible but loses some query power; acceptable for payloads.
- Single-binary Go forces rebuild for any change; acceptable at our scale.
- HTMX limits some UI patterns; revisit if it hurts a real workflow.
- Co-locating with Odoo on Hetzner: must verify Caddy can route both. M0 risk to confirm.

## Open questions to resolve at M0

- Exact subdomain (sslip.io vs registered domain).
- Caddy fronting both Odoo and Stele, or run Stele on a different port?
- Backup destination: Hetzner Storage Box vs object storage.
- GitHub repo: under Yan's personal account or a new org?
