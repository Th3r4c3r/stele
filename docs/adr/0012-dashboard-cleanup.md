# ADR-012: Dashboard + cleanup pass

- Status: Accepted
- Date: 2026-05-26
- Authors: Claude (PM agent), direction by Yan
- Touches: ADR-008 (assignment_rules can now be edited via admin),
  ADR-001 D8 (operating conventions), ADR-007 (fault_case domain)

## Context

After M6, the app has a search box and a rich case detail, but no
single page that answers "how are we doing this week?". Ops + admin
both ask that question first when they log in. Today the only proxy
for it is the `/debug/projections` JSON endpoint that nobody reads.

This milestone delivers `/dashboard` and at the same time cleans up
three pieces of debt that have been deferred since their respective
introductions:

1. `/debug/*` endpoints from M1: served as scaffolding until the
   real UI existed. The UI now covers their use cases.
2. `/claims -> /cases` 301 redirect from M2.5: bridge for any
   bookmark from the warranty era. One release was the promise; we
   are well past it.
3. Eight duplicate assignment rules accumulated through the
   three re-seeds (M3, M4 re-seed, M5 re-seed). Each re-seed minted
   new rule rows with new UUIDs without deactivating the previous
   set, so the routing matrix has four pairs of duplicates that work
   correctly only because the priority order happens to pick a
   winner deterministically.

## Decisions

### D1. Dashboard scope at v1

A single `/dashboard` page rendering, in order:

- **Four KPI cards** at the top:
  - Total **open** cases (triage + classified)
  - **My open** cases (assigned to the current user)
  - Cases **opened this week** (last 7 days)
  - Cases **closed this week**
- **Classification mix** table (kinds × count, sorted desc)
- **Queue per assignee** table (user name × open cases, plus
  "unassigned" via the ops generalist fallback)
- **Top 5 dealers by volume** table (last 30 days), with breakdown
  of open vs closed
- **Activity sparkline** for the last 7 days (events per day),
  rendered as inline SVG, no JS

Everything is computed in Go with direct SQL aggregations on
`current_cases` + `events`. No new projection, no cache, no live
updates. The dashboard renders in < 50 ms on the synthetic dataset
and the queries are cheap (indexes already cover `status`,
`assignee_id`, `dealer`, `recorded_at`).

### D2. Visibility

Authenticated only (same gate as `/cases`). All users see the same
numbers. Per-role visibility (e.g., regional ops sees only their
region's dealers) is deferred until a real per-role scope emerges
elsewhere.

### D3. `/` redirects to `/dashboard`

Previously `/` -> `/cases`. The dashboard is the better "where am I"
landing for an operator opening the app. Direct links to /cases
still work; the topbar exposes both.

### D4. Topbar nav

Order: **Dashboard · Cases [· Admin]**. Dashboard first because it's
the home; Cases second because it's the highest-frequency landing.

### D5. Cleanup: `/debug/*` removed

The three `/debug/event`, `/debug/events`, `/debug/projections`
endpoints are deleted from `cmd/stele/main.go` and from `web.go`.
- The replay command (`stele replay <name>`) and the projector logs
  cover the "is the runner OK?" question.
- The dashboard covers the "how many events of which type?" question.
- Direct SQL via `docker compose exec db psql` remains for emergency.

### D6. Cleanup: `/claims` redirect dropped

ADR-007 D6 promised "kept as 301 for one release". We've shipped
many releases since; no inbound traffic on `/claims` exists. Drop
the route. A future visitor with a stale bookmark gets a 404 (or
the public-path matcher's auth redirect), which surfaces the issue
honestly.

### D7. Cleanup: 8 duplicate assignment rules

A single SQL command at deploy time deactivates any
`assignment_rule` whose `(name, priority, assignee_id)` triple
matches a newer row, keeping the most recently created copy active.
The query is captured in `deploy/cleanup-rules.sql` so it can be
re-run after future re-seeds.

### D8. No new package surface for cleanup

The cleanup steps are all one-shots: handler deletions, route
removals, a SQL script. They live in this commit's diff; no
runtime feature flag, no migration to add and later remove.

## Consequences

- Two new files (`internal/dashboard/dashboard.go`,
  `internal/web/templates/dashboard.templ`), one new handler in
  `internal/web/dashboard_handlers.go`, one new route.
- Five existing route handlers removed (debug-3, claims-1, plus
  the `legacyClaimsRedirect` helper).
- The page-load time for `/` slightly grows (dashboard query vs a
  302 redirect). Still well below perception threshold; ~50 ms on
  the CPX22 with the seeded volume.
- Rule cleanup is irreversible without re-seeding, but rules are
  already in `assignment_rules` table (no events for them); a
  future admin can re-activate via `/admin/rules`.

## Open questions deferred

- Date-range picker on the dashboard ("show me last quarter"):
  add when the dataset is rich enough for that to mean something.
- Per-region scoping on the dealers table: needs role scoping
  elsewhere first.
- Real chart library (e.g., Chart.js, ECharts): NO. Plain SVG +
  HTML tables keep the bundle at 0 JS dependencies and load fast.
  Revisit if we need interactive zoom or drill-down.
- Live updates via SSE / HTMX polling: NO. Refresh-on-action is
  enough; this isn't a control-room screen.
