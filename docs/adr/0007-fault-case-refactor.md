# ADR-007: Fault case as the primary aggregate (supersedes ADR-005)

- Status: Accepted
- Date: 2026-05-25
- Authors: Claude (PM agent), correction by Yan
- Supersedes: ADR-005 (warranty domain model)

## Context

ADR-005 modelled the primary aggregate as `warranty_claim`. After M2
deployed, Yan pushed back: in real aftersales, a case starts as a
**reported fault** with no prior knowledge of whether warranty
applies. The warranty determination is the *outcome* of a triage and
classification process, not the identity of the case.

Modelling on a `warranty_claim` aggregate hard-codes a happy-path
assumption ("this is in warranty") and forces awkward workarounds for
cases that turn out to be out-of-warranty, goodwill, recall, or
customer education ("works as designed"). A non-trivial fraction
of real aftersales tickets are the last one; we want to count them.

Refactoring now (before M3 documents builds on warranty) is cheap
compared to doing it later. M2 is in production for one day with only
synthetic data, so re-seed is the right call (per the answered
question; alternatives were per-event migration or empty start).

## Decisions

### D1. Aggregate: `fault_case`

- Aggregate type string: `"fault_case"`.
- Package: `internal/fault/` (Go's `case` is a keyword).
- UI nouns: "Case", "Cases". Existing /claims routes redirect to /cases.

### D2. Status state machine

```
+--------+        Classified         +-------------+    CaseClosed    +--------+
| triage |  ----------------------> | classified  | ---------------> | closed |
+--------+                          +-------------+                  +--------+
```

- `triage`: initial state on CaseOpened. No kind yet.
- `classified`: a Classified event has set a `kind`. Investigation done.
- `closed`: CaseClosed has been applied. Terminal. The row keeps the
  kind from the last Classified, so analytics still work on the closed
  history.

Cases can be Closed directly from `triage` if the classification was
trivial and not worth recording separately (rare); the row's kind
stays NULL. Allowed; the projector does not block it.

### D3. Kinds (closed enum at this refactor)

| Kind                  | Meaning                                                                  |
| --------------------- | ------------------------------------------------------------------------ |
| `warranty`            | Covered by manufacturer warranty.                                        |
| `out_of_warranty`     | Out of warranty, paid repair quoted to the customer.                     |
| `goodwill`            | Out of warranty but repaired free as a commercial gesture.               |
| `recall`              | Linked to an active recall campaign.                                     |
| `unrelated`           | User damage, third-party intervention, fraud, off-topic.                 |
| `customer_education`  | Product works as designed; the report is a misunderstanding to address.  |

The enum is enforced by a CHECK constraint on `current_cases.kind`.
The events themselves accept any string (forward-compatibility), but
the projector ignores Classified events with an unknown kind so a
forgotten migration cannot corrupt the read model.

`customer_education` is first-class because in real-world aftersales
it is a two-digit percentage of incoming reports, and tracking it
informs documentation, dealer training, and onboarding UX.

### D4. Event types

- `CaseOpened { dealer, vin, fault_code, description }` — birth.
- `NoteAdded { author, text }` — same as before, lives in any status.
- `Classified { kind, reasoning }` — determines the flow. Multiple
  Classified events on the same case are allowed (re-classification);
  the projector applies the latest by id, the others are visible in
  the timeline as history.
- `CaseClosed { resolution, closed_by }` — terminal.

Sub-flow events specific to a kind (WarrantyApproved, QuoteSent,
RecallLinked, etc.) are intentionally deferred. We add them when a
real workflow needs them. ADR-005's lesson: do not pre-populate the
event vocabulary with generic shapes; add specific events when the
need is real.

### D5. Read model: `current_cases`

```sql
current_cases (
    id              uuid        PRIMARY KEY,
    status          text        NOT NULL CHECK (status IN ('triage', 'classified', 'closed')),
    kind            text        NULL    CHECK (kind IS NULL OR kind IN (
                                            'warranty', 'out_of_warranty', 'goodwill',
                                            'recall', 'unrelated', 'customer_education')),
    dealer          text        NOT NULL,
    vin             text        NOT NULL,
    fault_code      text        NOT NULL,
    description     text        NOT NULL,
    opened_at       timestamptz NOT NULL,
    classified_at   timestamptz NULL,
    closed_at       timestamptz NULL,
    last_update     timestamptz NOT NULL,
    note_count      int         NOT NULL DEFAULT 0,
    last_event_id   uuid        NOT NULL
);
CREATE INDEX current_cases_status_opened_idx ON current_cases (status, opened_at DESC);
CREATE INDEX current_cases_kind_idx          ON current_cases (kind) WHERE kind IS NOT NULL;
CREATE INDEX current_cases_dealer_idx        ON current_cases (dealer);
```

Migration 0004 drops `current_claims` (read model only, no append-only
trigger on it; reproducible from the event log via projector replay if
ever needed). The 200 legacy `warranty_claim` events stay in the
event log forever (append-only), but no projector reads them.

### D6. UI

- Top-level `/cases` with three tabs: `Triage (N)`, `Classified (N)`, `Closed (N)`.
- Inside Classified, a kind filter chip set (or a select).
- Case detail shows status badge + kind badge (when classified).
- Contextual actions on detail:
  - In `triage`: "Add note", "Classify" form (kind + reasoning), "Close"
    (allowed but discouraged via UI placement; uses an explicit "Close
    without classifying" link).
  - In `classified`: "Add note", "Re-classify", "Close".
  - In `closed`: read-only.

The /claims URLs are kept as 301 redirects to /cases for one release
in case any bookmarks exist; removed at M3.

### D7. Re-seed

The 200 synthetic `warranty_claim` rows are NOT migrated. Re-seed
produces fresh `fault_case` data with realistic distribution:

- 90% closed, 10% open.
- Of closed: 80% are also Classified; 20% closed directly from triage.
- Kind distribution (when present):
  - warranty 35%
  - out_of_warranty 20%
  - customer_education 15%
  - goodwill 10%
  - unrelated 15%
  - recall 5%

This matches roughly what a small EV aftersales team sees over 18
months and gives the UI interesting numbers to display.

## Consequences

- One day of M2 production data discarded; acceptable, all synthetic.
- ADR-005 marked Superseded; its core ideas (typed events, command
  validation, per-row last_event_id guard, status state machine) are
  preserved and extended here.
- Two abandoned aggregate types in the event log: `claim` (3 legacy
  smoke events) and `warranty_claim` (200 + smoke). Filter explicitly
  in every projector. The CurrentEventCountByType projector keeps
  counting them for the bridge period (visible as historical context
  in /debug/projections).
- `internal/warranty/` package gone, replaced by `internal/fault/`.
  Tests, commands, projector all renamed.
- The cost of getting the aggregate name wrong was real but small here
  because we caught it at M2; the same mistake at M5+ would have been
  much more expensive. Worth a CLAUDE.md note: when modelling a
  domain, name the *thing observed*, not the *outcome the system hopes
  for*.

## Open questions deferred

- Sub-flow events per kind (M4+ when needed).
- Re-classification UI: at M2.5 we just allow another Classified
  event; richer "why was this re-classified" UI deferred.
- Per-kind SLA / aging: also deferred; first need real usage.
