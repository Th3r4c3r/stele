# ADR-011: Global search

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent), direction by Yan
- Supersedes: nothing
- Touches: ADR-007 (fault_case is the searchable surface), ADR-010 (documents are searchable by filename)

## Context

Day-to-day aftersales work needs answering questions like:
- "Where is the case for VIN ABC1234567890XYZ?"
- "What did Mario say about that BMS_FAULT_03?"
- "Did any dealer in DE report DASH_NO_BOOT this month?"

Today you reach those answers by scrolling the list + Ctrl-F or by SQL.
Both break at scale.

M6 ships a **global search box** in the topbar.

Time-travel (the previous M6 plan) is deferred indefinitely: every
event already carries `occurred_at` + `recorded_at`, so adopting it
later is non-breaking. The added complexity is not justified by any
current consumer.

## Decisions

### D1. Scope

The search box queries the following fields with a single user query:

- `current_cases.dealer` (text)
- `current_cases.vin` (text, exact match preferred but ILIKE is fine)
- `current_cases.fault_code` (text)
- `current_cases.description` (text)
- `current_documents.filename` (text)
- `events.payload->>text` for events of type `NoteAdded` (note body)
- `events.payload->>reasoning` for events of type `Classified` (classification reason)

Out of scope:
- Users, dealers, rules tables: admin-only entities, search them in /admin.
- Document file contents (no extraction; deferred indefinitely).
- Full-text natural language ranking. Substring match is enough at
  current volume; revisit when the case count exceeds ~1k.

### D2. Query strategy: ILIKE first, GIN/trigram later

- v1: plain ILIKE `'%term%'` on each field, UNION'd into a typed result.
  At ~200 cases + ~1k events the response is < 50 ms uncached.
- When volume grows past ~5k cases or queries get sluggish, add
  `pg_trgm` GIN indexes on the searched columns. No schema change in
  the application layer.

The query layer lives in `internal/search/search.go`. It returns a
typed `Result` struct grouping matches by kind (cases vs notes vs
documents) so the UI can render snippets per group.

### D3. Security: same visibility as /cases

The search results respect the same access surface as the rest of the
app: authenticated users see everything; the only privileged surface
remains `/admin`. M7+ can add per-role visibility (dealer scope,
case-level ACL) without changing the search interface.

### D4. UI: topbar input + results page

- A `<input type="search" name="q">` lives in the layout topbar
  (right of the primary nav, left of the user menu). It submits to
  `GET /search`. No HTMX live-search at v1 — simple GET works on
  every device, every keyboard, no JS dependency.
- `GET /search?q=foo` renders a page grouping results into three
  sections: **Cases (N)**, **Notes (N)**, **Documents (N)**, each with
  a small snippet showing where the term matched.
- Each result links to `/cases/{id}` (the document one anchors to the
  Documents section; v2 may scroll-into-view).

### D5. Snippet rendering

For each match, store the matched substring + ~30 chars of context on
either side, with the term highlighted via `<mark>`. Computed in Go
(no PG `ts_headline`), bounded to ~80 chars total. Keeps it portable
when ILIKE is replaced by trigram.

### D6. Query length and rate limit

- Min 2 chars. Below that, render an empty result without hitting the DB.
- Max 200 chars (clipped). Sanity bound only; no DoS concern at our scale.
- No rate limit at v1 (authenticated users only, small audience).

## Consequences

- One new package + one new handler + one new templ page. No
  migration needed at v1.
- The search field becomes the primary entry point for "find the
  thing I'm thinking of", reducing reliance on the My cases / Triage /
  Classified / Closed funnel for needle-in-haystack questions.
- When trigram is later added, callers of `search.Find()` don't change.

## Open questions deferred

- Saved searches / search history per user: nice to have, not at v1.
- Boolean / phrase operators (quotes, AND, OR): not at v1.
- Faceted refinement (after the result, filter by status / kind /
  dealer): emerges naturally if needed; trivial add-on to the handler.
- Highlight class color: uses default `<mark>` styling; revisit if
  jarring.
