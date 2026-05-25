# ADR-004: Projection engine

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent)
- Builds on: ADR-003 (event store, Store.Stream as the substrate)

## Context

M1's event store is the source of truth. To answer queries efficiently
(list claims by dealer, count events by type, etc.) we materialize
read models from the event stream. This ADR fixes the projection
engine shape so M2 (warranty domain) can plug in projectors without
re-deciding the framework.

## Decisions

### D1. Projector is a function with a name and an event filter

```go
type Projector struct {
    Name           string                            // unique, used as cursor key
    AggregateType  string                            // "" means all
    Apply          func(ctx, *pgx.Tx, Event) error   // idempotent
}
```

- `Apply` receives the same transaction that advances the cursor, so
  the materialized write and the cursor bump are atomic. Either both
  commit or neither does. No partial state on crash mid-event.
- `Apply` MUST be idempotent: it may receive the same event twice if a
  previous attempt failed after the side-effect but before the commit
  (rare with the atomic tx, but possible for any external side-effects
  the handler might do).
- Filtering is conjunctive with `Store.StreamOptions.AggregateType`;
  an empty filter consumes the entire stream.

### D2. Cursor lives in its own table

```sql
projection_cursors (
    name              text        PRIMARY KEY,
    last_recorded_at  timestamptz NOT NULL,
    last_event_id     uuid        NOT NULL,
    updated_at        timestamptz NOT NULL DEFAULT now()
)
```

- One row per projector.
- Reset = `DELETE` (or `UPDATE ... SET last_recorded_at = epoch`); used
  by the replay command.
- The cursor table is **not** subject to the append-only trigger; only
  `events` is. Cursors are mutable by design.

### D3. Runner: one goroutine per projector, polling

- On app boot, the `Runner` starts one goroutine per registered
  projector. Each goroutine loops:
  1. Read cursor.
  2. `Store.Stream` from `(last_recorded_at, last_event_id)` with
     `BatchSize`.
  3. For each event in batch: `BEGIN; Apply; UPDATE cursor; COMMIT`.
  4. If batch was empty, sleep `STELE_PROJECTION_POLL_INTERVAL`
     (default 2s).
- Polling, not LISTEN/NOTIFY, because:
  - One in-process consumer per projector; polling latency budget is
    seconds (admin UI, not realtime).
  - Postgres LISTEN/NOTIFY requires a long-lived dedicated connection
    per listener, which we'd rather not spend at M1.
  - Revisit when latency requirements tighten (probably never in
    Phase 1) or when projection workers move out of process.

### D4. Replay = sub-command, not HTTP endpoint

- `stele replay <name>` (and `stele replay --all`).
- Argument parsing in `main()` with `os.Args`, no cobra. One sub-command
  does not warrant a CLI library at M1.
- Replay flow: open pool, run migrations, set cursor to zero for the
  target projector, run a one-shot Runner pass (no goroutine), exit.
- Why not an HTTP admin endpoint: replay is destructive (wipes the
  projection state); keeping it off the HTTP surface eliminates the
  whole "someone hits the admin URL by accident" class of bug. And we
  can `docker compose exec stele-app /stele replay foo` from the host.

### D5. Resetting projection state on replay

- Replay does not truncate the projection table; the `Apply` function
  must be **upsert-shaped** so reprocessing converges to the same state.
- Rationale: lets us shrink the surface (one place to be careful: the
  projector), and gives free composition (two replayers can run on the
  same table without race-conditioning on TRUNCATE).
- The example projector in M1b does this with `ON CONFLICT (key) DO
  UPDATE SET count = projection_event_counts.count + EXCLUDED.count`.
  Wait, that double-counts on replay. Correct shape:
  `INSERT ... ON CONFLICT (...) DO UPDATE SET count = count_query`
  where the count is recomputed from a SELECT, OR the projector keeps
  a `last_seen_event_id` per row and skips events older than it.
- M1b adopts the simpler approach: **the projector reads the event's id
  and the existing row, and ignores events older than the row's
  `last_event_id`** (per-row idempotency). The replay command first
  deletes the cursor; the projection table is left intact and rebuilds
  consistently because each `Apply` is a no-op on already-seen events.

### D6. Error handling

- `Apply` returning an error: log + sleep + retry the same event.
  Do not skip. A poison-pill event blocks the projector indefinitely;
  this is visible (cursor frozen) and forces investigation rather than
  silent data loss.
- Future: dead-letter queue after N retries, escalation to ops. Deferred
  until a real projector hits a real poison pill.
- Panics inside `Apply` are recovered and logged; same retry behavior.

### D7. Registration

- Projectors are registered in `main()` before the Runner starts:
  ```go
  runner := projection.NewRunner(store, pool)
  runner.Register(projectors.EventCountByType())
  runner.Start(ctx)
  ```
- Explicit registration (not magic init), so the registered set is
  visible in one place.

## Consequences

- Adding a projector = write a function + register it + write a
  migration for the read-model table. No framework code to touch.
- A misbehaving projector cannot block the event log (it only blocks
  its own cursor).
- The transactional atomicity (Apply + cursor in same tx) requires the
  projector to write to the same Postgres database. Cross-database
  projectors (e.g., write to Elasticsearch) would need a different
  pattern; ignore until needed.
- Polling overhead: 1 SELECT per projector per 2s = trivial. Scales
  fine to dozens of projectors before we'd want LISTEN/NOTIFY.

## Open questions deferred

- Snapshots: not yet needed (M1b has one projector reading a few
  events). Revisit when any projection takes > 1s to rebuild.
- Parallelism inside one projector: events are applied serially per
  projector. Trivial parallelism (multiple goroutines on the same
  cursor) is unsafe without locking; deferred until a real projector
  is throughput-bound.
