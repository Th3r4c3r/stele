# ADR-003: Event store schema and append-only enforcement

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent)
- Builds on: ADR-001 (event sourcing, bi-temporal), ADR-002 (golang-migrate)

## Context

M1 introduces the first stateful surface of Stele: the `events` table.
This table is load-bearing for the entire project. The decisions in this
ADR shape every future projection, replay, and time-travel query, so we
lock them down before writing migrations.

## Decisions

### D1. Identifier: UUIDv7

- Column: `id uuid PRIMARY KEY`.
- Generated client-side in Go using `github.com/google/uuid` (v1.6+).
- Why v7 and not bigserial:
  - Sortable by time (the first 48 bits encode ms timestamp), so insertion
    order is preserved without relying on a monotonic sequence.
  - Globally unique without DB round-trip; trivial to merge streams from
    multiple writers if we ever shard or replicate.
  - bigserial creates a single point of contention (the sequence) and
    leaks production volume in URLs.
- Why v7 over v4:
  - v4 random ids destroy B-tree locality on insert (page splits, bloat).
    v7 ids cluster naturally by time.

### D2. Column layout

```sql
events (
  id            uuid          PRIMARY KEY,
  aggregate_type text         NOT NULL,
  aggregate_id  uuid          NOT NULL,
  type          text          NOT NULL,
  payload       jsonb         NOT NULL,
  occurred_at   timestamptz   NOT NULL,
  recorded_at   timestamptz   NOT NULL DEFAULT now(),
  recorded_by   text          NOT NULL DEFAULT 'system'
)
```

- `aggregate_type` and `aggregate_id`: scope events to a domain entity.
  Separating the type avoids needing a registry table at M1; we can
  introspect with `SELECT DISTINCT aggregate_type`.
- `occurred_at`: business reality time. User-provided. Can be backdated.
- `recorded_at`: when Stele learned the fact. `DEFAULT now()`, never
  user-provided. The trigger in D4 also enforces this on `INSERT`.
- `recorded_by`: who/what wrote the event. M1 hardcodes `'system'` and
  the future auth layer (post-M5) sets the user/agent.
- `payload jsonb`: event-specific shape. Validated by the domain layer,
  not the schema, to keep the events table agnostic.

### D3. Indexes

- PK on `id` (B-tree, automatic).
- `(aggregate_id, occurred_at)`: hot path for "load aggregate state".
  Composite, covers the common WHERE.
- `(aggregate_type, occurred_at)`: projection rebuild scans by type.
- `(recorded_at)` BRIN: bi-temporal time-travel queries on insertion
  time, append-only data is ideal for BRIN (small index, good for range
  scans).
- No GIN on `payload` yet. Add per-projection materialized columns
  instead; jsonb indexes are expensive and we do not need ad-hoc
  payload search at M1.

### D4. Append-only enforced at the DB layer

A trigger rejects any `UPDATE` or `DELETE` unless the session role is
`stele_redactor`. The application role `stele` cannot mutate or remove
events.

```sql
CREATE OR REPLACE FUNCTION events_block_mutation() RETURNS trigger AS $$
BEGIN
  IF current_user <> 'stele_redactor' THEN
    RAISE EXCEPTION 'events is append-only (current_user=%)', current_user;
  END IF;
  RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;
```

- Rationale: a future bug in application code cannot corrupt the log.
  Belt-and-suspenders with the `Store` interface that only exposes
  `Append`.
- The redaction role does not exist at M1. It is created with a separate
  ceremony when GDPR demands it (probably M3 when documents land).

### D5. Migration strategy

- Library: `github.com/golang-migrate/migrate/v4` with `iofs` source and
  `pgx5` driver (matches our application driver).
- Migrations embedded via `//go:embed migrations/*.sql`.
- Autorun on app startup, BEFORE the HTTP server begins listening.
  Failure to migrate panics; we never serve from a misversioned schema.
- Down migrations exist but are never run in production. They live for
  local dev convenience and tests.

### D6. Driver: pgx v5 with pgxpool

- `github.com/jackc/pgx/v5/pgxpool`.
- Native protocol (no `database/sql` wrapping), better perf and types.
- Connection pool sized via env (`STELE_DB_POOL_MAX`, default 10).
  Single tenant, conservative.

### D7. Store interface (Go)

```go
type Store interface {
    Append(ctx context.Context, evs []Event) error
    Load(ctx context.Context, aggregateID uuid.UUID, since time.Time) ([]Event, error)
    Stream(ctx context.Context, opts StreamOptions) iter.Seq2[Event, error]
}
```

- `Append` accepts a batch, transactional. Ids may be zero-valued; the
  Store assigns UUIDv7 in that case.
- `Load` returns events for a single aggregate, ordered by `occurred_at`.
  `since` enables cursor-based pagination.
- `Stream` is the projection engine's entry point (see D8).
- Returns Go 1.23 `iter.Seq2` so callers can `for ev, err := range stream { ... }`.

### D8. Projection engine: in-process at M1

- A `Projector` is a function `func(ctx, Event) error` registered against
  an `aggregate_type` filter.
- Stele runs all projectors in-process, sequentially per aggregate,
  parallel across aggregates. Single binary, no separate worker daemon.
- M1 ships the framework but the only registered projector is the
  smoke-test "event count by type" projection.
- Revisit at M4 (relations / joins across aggregates) whether to keep
  in-process or spawn a sidecar.

### D9. Testing: integration tests guarded by env

- Unit tests for pure functions run with `go test`.
- Integration tests requiring Postgres are guarded by
  `requirePostgres(t)` which calls `t.Skip` unless
  `STELE_TEST_DATABASE_URL` is set.
- CI sets that env from a `postgres:16` service container.
- Local dev without Go/Docker still gets the unit subset to pass.

## Consequences

- `go.sum` is born at this commit (pgx, golang-migrate, google/uuid).
  Three direct deps, all permissively licensed.
- The trigger introduces a slight write overhead. Acceptable: append-only
  workloads are typically not write-bound.
- Two Postgres roles (`stele` + `stele_redactor`) eventually needed.
  `stele_redactor` is deferred until first redaction is required.
- BRIN on `recorded_at` is great for append-only but degrades with
  out-of-order inserts. We have no out-of-order writes by construction.

## Open questions deferred

- Stream consumer for projection workers across processes (LISTEN/NOTIFY
  vs polling): deferred until projections are split out of the main
  binary, post-M4.
- Snapshot strategy for fast aggregate reload: deferred until any
  aggregate exceeds ~1000 events. Synthetic dataset at M2 will tell us
  when.
