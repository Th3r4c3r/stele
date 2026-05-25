# ADR-005: Warranty domain model

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent)
- Builds on: ADR-001 (warranty as first domain), ADR-003 (event store), ADR-004 (projection engine)

## Context

M2 ships the first real domain: warranty claims. Until now we only had
a smoke endpoint posting arbitrary JSON. Now we need typed events, a
command surface, a read model, and a UI. This ADR fixes the shape that
M3+ will extend with documents (M3), relations (M4), and time-travel
(M5).

## Decisions

### D1. Aggregate: Claim

- One claim = one warranty case opened against a vehicle by a dealer.
- `aggregate_type = "warranty_claim"`. Explicit prefix to avoid future
  collisions (a future `service_claim` is plausible).
- The 3 legacy smoke events using `aggregate_type = "claim"` stay in
  the log (append-only). The CurrentClaims projector filters on
  `warranty_claim` and ignores them. History preserved, domain dataset
  clean.

### D2. Event types

Each event has a Go struct in `internal/warranty/events.go`. The struct
marshals to JSON and is stored as the `payload` jsonb.

- `ClaimOpened { dealer, vin, fault_code, description }` — birth event.
- `NoteAdded { author, text }` — appended notes during investigation.
- `ClaimClosed { resolution, closed_by }` — terminal event.

Naming: PascalCase, past tense, suffix indicates the verb (Opened,
Added, Closed). Never `ClaimUpdate` — events are facts, not commands.

`ClaimUpdated` from the original ROADMAP is dropped: changes to a
claim's metadata (severity, owner reassignment) will be modelled as
specific events when needed (`OwnerReassigned`, `SeverityChanged`).
Generic `Updated` events are an antipattern in event sourcing.

### D3. Status state machine

```
                +-----------+
   open ------> | open      | --close--> closed
                +-----------+
                  |
                  v
              (NoteAdded does not change status)
```

- Status starts as `"open"` on ClaimOpened.
- NoteAdded does not change status.
- ClaimClosed transitions to `"closed"`. Cannot reopen at M2; if needed
  later, model as a new `ClaimReopened` event.
- The CurrentClaims projector enforces the transition: applying
  ClaimClosed to a claim already closed is a no-op (idempotent).

### D4. Command handlers

`internal/warranty/commands.go` exposes one function per command:

```go
func OpenClaim(ctx, store, params) (claimID, error)
func AddNote(ctx, store, claimID, params) error
func CloseClaim(ctx, store, claimID, params) error
```

Each:
1. Validates inputs (empty fields, status preconditions via Load).
2. Constructs the appropriate Event with `aggregate_id = claimID`.
3. Calls `store.Append`.

Preconditions are checked against the projection (CurrentClaims) for
speed; if the projection is lagging this could miss a race, acceptable
at single-user scope. M5+ will revisit if multi-actor.

Commands do NOT call the projector directly. The Runner picks up the
new event within poll_interval and updates `current_claims`.

### D5. Read model: current_claims

```sql
current_claims (
    id            uuid        PRIMARY KEY,
    status        text        NOT NULL,
    dealer        text        NOT NULL,
    vin           text        NOT NULL,
    fault_code    text        NOT NULL,
    description   text        NOT NULL,
    opened_at     timestamptz NOT NULL,
    closed_at     timestamptz NULL,
    last_update   timestamptz NOT NULL,
    note_count    int         NOT NULL DEFAULT 0,
    last_event_id uuid        NOT NULL
)
CREATE INDEX current_claims_status_opened_idx ON current_claims (status, opened_at DESC);
CREATE INDEX current_claims_dealer_idx ON current_claims (dealer);
```

- `last_event_id` for per-row replay idempotency (same pattern as
  EventCountByType).
- `note_count` denormalized for the list view; recomputable from events
  on replay.
- No FK to a `dealers` or `vehicles` table at M2; dealer and VIN are
  free strings. M4 introduces the relations.

### D6. CurrentClaims projector

Registered in `main()` alongside `EventCountByType`. Filters on
`aggregate_type = "warranty_claim"`. Apply switches on event type:

- ClaimOpened: INSERT current_claims row, status='open'.
- NoteAdded: UPDATE current_claims SET note_count = note_count + 1,
  last_update = now() WHERE id = $aggregate AND last_event_id < $eventID.
- ClaimClosed: UPDATE current_claims SET status='closed', closed_at,
  last_update WHERE id = $aggregate AND last_event_id < $eventID AND
  status = 'open'.

The `last_event_id <` guard makes every Apply replay-safe.

### D7. Synthetic dataset

`cmd/seed/main.go` generates ~200 claims directly via the Store.Append
API (not via HTTP, faster). Distribution:

- 90% closed, 10% open.
- 5-15 events per claim (1 ClaimOpened + N NoteAdded + maybe ClaimClosed).
- Dealers from a fixed pool of 12 synthetic names (`DEALER_01`..`DEALER_12`).
- VINs are random valid-format 17-char strings (no real-world VINs).
- `occurred_at` spans the last 18 months, uniformly.

No real Vmoto data, ever (per CLAUDE.md and ADR-001 D8 constraints).

### D8. UI scope at M2

Minimum viable, no auth, single user. See ADR-006 for the rendering
stack and route shape. List + new + detail + add-note + close. No
edit, no search filters (deferred to M3), no pagination needed at 200
synthetic rows.

## Consequences

- Two `aggregate_type` values coexist in the events log (`claim` legacy,
  `warranty_claim` canonical). Future projectors must be explicit.
- Status state machine is enforced by the command layer + projector,
  not by a DB constraint. Easier to evolve; relies on tests for
  correctness.
- `note_count` denormalized: on schema change to NoteAdded payload, the
  projector must handle the migration. Replay rebuilds from scratch.
- Dropping generic `ClaimUpdated`: future fields require new specific
  event types. More events to maintain but clearer history.

## Open questions deferred

- Searching/filtering claims (text search, status filter, dealer
  filter): not at M2 with 200 rows. Add when list scrolls past 50.
- Bulk operations (close many at once): not in scope, single-user.
- Attaching PDFs: M3 (Documents milestone).
