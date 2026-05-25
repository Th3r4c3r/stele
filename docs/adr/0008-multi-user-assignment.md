# ADR-008: Multi-user prep + auto-assignment

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent), Yan (direction)
- Builds on: ADR-001 D6 (auth deferred), ADR-007 (fault_case as primary)

## Context

ADR-001 D6 deferred auth and multi-user to Phase 1.5. After M2.5
landed, Yan asked for the system to be **prepared** for multi-user
from M3, even before authentication exists. The motivation: model
the ownership/assignment shape now, defer the login gate until
others are actually invited. Same principle that made the M2.5
correction cheap: model the real domain early, layer access later.

Routing must support:
- Default: assignee = the user who opened the case.
- Override: a configurable rule matches the case (fault code prefix
  or dealer region) and assigns to a specialist.
- Always: manual transfer at any time.

Documents (M3 in the original ROADMAP) move to M4, and their text
extraction is deferred indefinitely (no concrete consumer yet).

## Decisions

### D1. Three new master tables

```sql
users (
    id              uuid        PRIMARY KEY,
    email           citext      UNIQUE NOT NULL,
    name            text        NOT NULL,
    role            text        NOT NULL,
    region          text        NULL,
    specializations text[]      NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now()
);

dealers (
    code       text        PRIMARY KEY,
    name       text        NOT NULL,
    region     text        NOT NULL,
    country    text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

assignment_rules (
    id                  uuid        PRIMARY KEY,
    name                text        NOT NULL,
    priority            int         NOT NULL,
    match_fault_prefix  text        NULL,
    match_dealer_region text        NULL,
    assignee_id         uuid        NOT NULL REFERENCES users(id),
    active              bool        NOT NULL DEFAULT true,
    created_at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assignment_rules_priority_idx ON assignment_rules (priority) WHERE active;
```

- `users.email` uses `citext` to avoid case-sensitivity surprises;
  requires `CREATE EXTENSION citext` in the migration.
- `dealers.code` is the human-readable id ("DEALER_01"), matching the
  string already stored on events; PK by code keeps lookups simple.
- `assignment_rules` is intentionally a table (not a YAML file) so the
  rule set can be edited at runtime by a future admin UI without a deploy.

### D2. New event: `CaseAssigned`

```go
type CaseAssigned struct {
    AssigneeID       uuid.UUID `json:"assignee_id"`
    Reason           string    `json:"reason"`            // opener | rule:fault_prefix | rule:dealer_region | manual
    RuleName         string    `json:"rule_name,omitempty"`
    TransferredFrom  *uuid.UUID `json:"transferred_from,omitempty"`
}
```

- Emitted automatically on OpenCase (the dispatcher computes the
  assignee).
- Emitted on every Reassign command.
- Multiple CaseAssigned events on the same case = full transfer
  history, visible in the timeline.

The projector updates `current_cases.assignee_id` to the latest
CaseAssigned (per-row guard via `last_event_id <`).

### D3. Routing: pure function with priority

```
Route(case, dealer, rules, opener) -> (assignee_id, reason, rule_name?)

1. For each rule in priority order (ASC), if active and match:
   - match_fault_prefix non-empty AND case.fault_code starts with it
   - AND/OR match_dealer_region non-empty AND dealer.region equals it
   -> return rule.assignee_id, "rule:" + dominant_predicate, rule.name
2. Otherwise: return opener.id, "opener", ""
```

- "Dominant predicate" = which of the two matched (if both, report
  fault_prefix since it's the more specific signal). Code keeps it
  simple: one rule = at most one predicate per row at M3; combined
  predicates can come later with a new column.
- Priority is an explicit int (lower = first). No implicit
  fault-prefix-beats-region; the priority column makes the order
  inspectable.

Routing is a **pure function** so it can be unit-tested without DB.
The dispatcher calls it after loading the small rule set into memory.

### D4. Dispatcher: synchronous append, not background

The dispatcher runs INSIDE `OpenCase`:
1. Append `CaseOpened`.
2. Compute Route().
3. Append `CaseAssigned`.

Why synchronous (not a separate goroutine like the projection runner):
- A case without an assignee is a usability bug ("who picks this up?").
  Inline append guarantees the case is born already assigned.
- Both events land in the same batch; the projector sees them together.
- If routing throws, OpenCase fails as a unit; the user retries.

Trade-off: routing latency adds ~1 DB round-trip to OpenCase. Trivial
at our scale.

### D5. Reassign command

```go
func Reassign(ctx, store, caseID, newAssigneeID, reason string) error
```

- `reason` must be `"manual"` (only valid reason for manual transfer).
- Appends `CaseAssigned { reason: manual, transferred_from: <current> }`.
- Looks up current assignee to populate `transferred_from`. If the
  case has never been assigned (unlikely after D4), TransferredFrom is nil.

### D6. Current-user middleware (auth bridge)

```go
func WithCurrentUser(resolver UserResolver) func(http.Handler) http.Handler
```

- Reads `STELE_DEFAULT_USER_EMAIL` from env (default
  `yan@stele.local`).
- Resolves to a user_id at process boot (cached).
- Middleware injects the user_id into request `ctx`.
- Handlers call `currentuser.From(r.Context())` to get the active user.

When real auth lands (M5+), the middleware swaps its source from env
to session cookie; handlers do not change.

If `STELE_DEFAULT_USER_EMAIL` resolves to no user, the app refuses to
start (configuration error). Better than a silent fallback.

### D7. UI changes

- List: add "Assignee" column showing the assignee's name (small text).
- Tabs: existing Triage / Classified / Closed plus a new
  **My cases** tab that filters by `assignee_id = current_user`.
- Detail: assignee badge in the summary block; a "Transfer" form that
  shows a dropdown of users and a free-text reason note (the reason
  string is fixed to `manual` for now; the free text becomes a
  follow-up NoteAdded so the human context lives in the timeline).
- Timeline: CaseAssigned events render as
  `Assigned to <name> (reason: <reason> [via rule '<name>']
  [transferred from <prev>])`.

### D8. Recorded-by from current user

Every Append call gains `recorded_by = <current user's email>`. Until
now it was hard-coded `"system"`. The Store API already accepts the
field; commands read from ctx.

For commands invoked from background goroutines (the projection
runner is the only one today, and it doesn't write events), use
`"system"` explicitly.

### D9. Seed data

`cmd/seed` now also seeds:

- 12 dealers (DEALER_01..12) with regions distributed
  IT 4 / FR 3 / ES 3 / DE 2.
- 5 users:
  - `yan@stele.local` — ops generalist (region: all)
  - `mario.bms@stele.local` — battery specialist (specializations: BMS)
  - `ana.motor@stele.local` — motor specialist (specializations: MOTOR)
  - `jp.es@stele.local` — regional ops ES
  - `kris.de@stele.local` — regional ops DE
- Rules (priority asc):
  1. fault_prefix=`BMS_` -> mario.bms (priority 10)
  2. fault_prefix=`MOTOR_` -> ana.motor (priority 20)
  3. dealer_region=`ES` -> jp.es (priority 30)
  4. dealer_region=`DE` -> kris.de (priority 40)
- 200 cases re-seeded; each goes through `OpenCase(openerID=yan)`
  which auto-emits `CaseAssigned`. Distribution of routing reasons
  emerges from the data.

## Consequences

- Re-seed for the third time on prod. Acceptable, all synthetic.
- Three more tables and one more event type. The event log gets
  larger (one extra event per case on creation, plus N more per
  transfer in life).
- The dispatcher inside OpenCase couples case creation to the rule
  set. If rules misbehave (e.g., all reference a missing user),
  OpenCase fails for every new case. Mitigation: rule integrity
  checked at seed time and on app boot.
- Auth still deferred. The env-var middleware is a stop-gap; the
  ADR-001 D6 plan stays the target.

## Open questions deferred

- Real auth (cookie session + Argon2id). Probably M5 alongside
  documents-with-redaction.
- Rule combinator (AND between fault_prefix and dealer_region in a
  single rule). Add a column if needed.
- Per-region notifications, on-call rotation, escalation: out of
  scope at M3.
- A "free-form" reason field on Reassign that survives independently
  of the timeline note. Possibly add to the CaseAssigned event later.
