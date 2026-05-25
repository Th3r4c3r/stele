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

## M1 — Event store + projection engine (DONE 2026-05-25)
- `events` table with bi-temporal columns + DB-level append-only trigger. ✅
- Store interface (Append/Load/Stream) with keyset-paginated streaming. ✅
- Migrations embedded + autorun on boot. ✅
- Projection framework: Projector type, Runner with one goroutine per
  projector, polling, atomic Apply + cursor advance per event. ✅
- `stele replay <name>` / `replay --all` sub-command. ✅
- Example projector (event_count_by_type), idempotent on replay. ✅
- Integration tests over synthetic streams, replay idempotency, cursor advance. ✅
- Endpoints live: `/debug/projections`, plus existing `/debug/event[s]`.

## M2 — Fault-case domain (DONE 2026-05-25, refactored at M2.5)
- Originally landed as `warranty_claim`; refactored to `fault_case` after
  Yan flagged the modelling error (see ADR-007). UI at /cases.
- Aggregate: `fault_case`. Status: triage -> classified -> closed.
- Kinds (enum): warranty, out_of_warranty, goodwill, recall, unrelated,
  customer_education.
- Events: `CaseOpened`, `NoteAdded`, `Classified`, `CaseClosed`. ✅
- Projection: `current_cases` (status, kind nullable, dealer, vin,
  fault_code, note_count, classified_at, closed_at, ...). ✅
- UI: HTMX + Templ. Three tabs (Triage / Classified / Closed) with kind
  filter chips on Classified+Closed. Detail with status+kind badges and
  contextual actions (Classify, Re-classify, Close, Add note). ✅
- Synthetic dataset: `cmd/seed -count 200` with realistic kind
  distribution (~1175 events in ~830ms). ✅
- Backup: nightly `pg_dump` at 03:30, 7-day rotation. ✅
- Live: 200 cases (189 closed / 10 classified / 1 triage at re-seed time)
  at https://stele.178-105-44-164.nip.io/cases

## M3 — Multi-user prep + auto-assignment (DONE 2026-05-25)
- Master data: users, dealers (with region), assignment_rules. ✅
- New event: `CaseAssigned { assignee_id, reason, rule_name, transferred_from }`.
  Reasons: `opener`, `rule:fault_prefix`, `rule:dealer_region`, `manual`. ✅
- Routing: pure function with priority fault_prefix > dealer_region > opener default. ✅
- Reassign command (manual transfer at any time). ✅
- UI: assignee column in list, transfer form in detail, "My cases" tab,
  assignment history in timeline. ✅
- HTTP middleware reads `STELE_DEFAULT_USER_EMAIL` -> resolves user_id ->
  injects into ctx. Auth replaces this in M5+ with zero handler refactor. ✅
- Synthetic seed: 12 dealers (regions IT/FR/ES/DE), 5 users with roles +
  specializations, 4 rules; re-seeded 200 cases with routing applied. ✅
- Distribution in prod: Mario 54 (BMS_*), Yan 54 (opener fallback),
  Ana 43 (MOTOR_*), JP 25 (ES dealers), Kris 24 (DE dealers).

## M4 — Auth + admin dashboard (DONE 2026-05-25)
- Argon2id password hashing + server-side sessions (HMAC-signed cookie). ✅
- Password reset via SMTP, with log fallback when no SMTP configured. ✅
- Login / logout / forgot / reset pages (Templ, minimal AuthShell). ✅
- AuthMiddleware: cookie -> ctx; unauth GET -> /login?return=... ✅
- AdminOnly guard: role=admin gated /admin/* (others get 403). ✅
- Admin dashboard: /admin overview + /admin/users (invite/edit/reset/
  deactivate) + /admin/rules (list/create) + /admin/dealers (list/create). ✅
- Brute-force resistance: 5 failures / 15 min triggers 60s block. ✅
- Seeder sets dev password 'stele-dev-2026' (Yan=admin, others=ops). ✅
- Filtro Assignee bonus su Triage/Classified/Closed tabs. ✅

## M5 — Documents (storage only) (DONE 2026-05-25)
- Attach files to a case via event. Filesystem under
  `/data/documents/<id>`, sha256 + metadata in event, no bytea. ✅
- 25 MiB hard cap (configurable via STELE_DOCS_MAX_BYTES). ✅
- UI: collapsible upload form + documents table in case detail. ✅
- Streaming download with Content-Disposition: attachment. ✅
- Timeline summary: "<user> uploaded <file> (<type>, <size>)". ✅
- Backup tarball ~/data/documents alongside pg_dump, 7-day rotation. ✅
- Idempotent projection (ON CONFLICT DO NOTHING). ✅
- Text extraction deferred indefinitely.

## M4 — Relations
- Vehicles (VIN), Parts (SKU), Dealers (code).
- Claims reference all three.
- Projection joins: "claims by dealer", "claims for VIN".
- Synthetic dataset extended.

## M6 — Global search (DONE 2026-05-25)
- Topbar search input (every authenticated page). ✅
- /search?q=... grouped results: Cases / Notes / Documents. ✅
- ILIKE on dealer, vin, fault_code, description, note text, doc filename. ✅
- Snippet with case-insensitive `<mark>` highlight, HTML-escaped. ✅
- Live: smoke matched 11 cases for BMS_FAULT_03, 50 notes for "reflashed",
  17 dealer matches, 1 document. Empty result handled, min 2 chars enforced.
- Trigram/GIN indexes deferred until volume warrants.

## (deferred indefinitely) — Time-travel
Bi-temporal state-as-of API + UI. Deferred until a concrete consumer
(audit, legal hold, backdated-edit workflow) emerges. Every event
already carries occurred_at + recorded_at, so adopting it later is
non-breaking.


## M7 — Schema-as-git
- Schema changes (new event type, new field) modeled as commits.
- Branch projection definitions, test on synthetic data, merge.
- Most speculative milestone; revisit after M5.

## Pace

~30 minutes of Yan time per day. AI runs background sessions. Each milestone targets
2-4 weeks of calendar time at this pace. Reforecast after M1.
