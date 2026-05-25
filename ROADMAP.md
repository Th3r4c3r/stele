# Roadmap

Each milestone is a deployed, demoable increment. No milestone is "done" until it
runs on the Hetzner instance at a URL Yan can open.

## M0 — Skeleton (DONE 2026-05-25)
- Go module, `cmd/stele` serving a static "hello" page.
- docker-compose with Postgres 16 + app.
- CI: lint, test, build (GitHub Actions).
- Deployed to Hetzner at a chosen subdomain via Caddy.
- **Exit criteria:** Yan opens the URL and sees "Stele M0 alive". ✅
- Live: https://stele.178-105-44-164.nip.io

## M1 — Event store + projection engine (IN PROGRESS)
- `events` table: id, aggregate_type, aggregate_id, type, payload jsonb, occurred_at, recorded_at, recorded_by. ✅
- Append-only API enforced by DB trigger. ✅
- Store interface (Append/Load/Stream) with keyset-paginated streaming. ✅
- Migrations embedded + autorun on boot. ✅
- Integration tests over synthetic event streams. ✅
- **Pending:** Projection framework (register handler, materialize into table, replay command). Next session.

## M2 — Warranty domain
- Aggregate: `Claim`. Events: `ClaimOpened`, `ClaimUpdated`, `ClaimClosed`, `NoteAdded`.
- Projection: `current_claims` (status, owner, last_update).
- UI: list claims, open claim, add note, close.
- Synthetic dataset: 200 fake claims over 18 months.

## M3 — Documents
- Attach PDF to a claim via event.
- Extract text (pdftotext / pure Go pdf lib). No AI.
- Link extracted snippets to events.
- UI: documents alongside the event timeline of a claim.

## M4 — Relations
- Vehicles (VIN), Parts (SKU), Dealers (code).
- Claims reference all three.
- Projection joins: "claims by dealer", "claims for VIN".
- Synthetic dataset extended.

## M5 — Time-travel
- API: state as of `(occurred_at, recorded_at)`.
- UI: dual date pickers.
- Bug-bash: corrections (backdated events) must reproject correctly.

## M6 — Schema-as-git
- Schema changes (new event type, new field) modeled as commits.
- Branch projection definitions, test on synthetic data, merge.
- Most speculative milestone; revisit after M5.

## Pace

~30 minutes of Yan time per day. AI runs background sessions. Each milestone targets
2-4 weeks of calendar time at this pace. Reforecast after M1.
