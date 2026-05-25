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

## M3 — Multi-user prep + auto-assignment
- Master data: users, dealers (with region), assignment_rules.
- New event: `CaseAssigned { assignee_id, reason, rule_name, transferred_from }`.
  Reasons: `opener`, `rule:fault_prefix`, `rule:dealer_region`, `manual`.
- Routing: pure function with priority fault_prefix > dealer_region > opener default.
- Reassign command (manual transfer at any time).
- UI: assignee column in list, transfer form in detail, "My cases" tab,
  assignment history in timeline.
- HTTP middleware reads `STELE_DEFAULT_USER_EMAIL` -> resolves user_id ->
  injects into ctx. Auth replaces this in M5+ with zero handler refactor.
- Synthetic seed: 12 dealers (regions IT/FR/ES/DE), 5 users with roles +
  specializations, 4-5 rules; re-seed 200 cases with routing applied.

## M4 — Documents (storage only)
- Attach PDF to a case via event. Filesystem under `/data/documents/`,
  sha256 in event, no bytea in DB.
- UI: documents alongside the event timeline; download link; no extraction.
- Text extraction deferred to a later milestone when there is a concrete
  consumer (full-text search, AI features).

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
