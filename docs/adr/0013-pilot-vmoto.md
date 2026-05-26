# ADR-013: Pilot Vmoto + lifting the "synthetic-only" constraint

- Status: Accepted
- Date: 2026-05-26
- Authors: Claude (PM agent), direction by Yan
- Touches: CLAUDE.md (project constraints), ADR-001 D8 (operating
  conventions), ADR-007 (fault_case domain — gains masters + part events)

## Context

M0..M8 shipped on a synthetic dataset (200 generated cases, 12 fake
dealers, 5 fake users) per the original CLAUDE.md guardrail:
*"No real Vmoto data in this repo or in any deployed instance.
Synthetic datasets only."*

That guardrail was correct for the build phase: it forced the design
to be domain-honest without leaking IP. Now that the domain shape is
stable, Yan wants to point Stele at a real Vmoto pilot to answer a
genuinely useful question:

> **What is our failure rate by model and by part?**

This requires three new things that the synthetic dataset cannot give:
1. A real **vehicle master** so a VIN resolves to model + year.
2. A real **parts master** so a fault links to a P/N (price + label).
3. A way to **attach parts to a case** (replaced under warranty,
   quoted out of warranty) so cost and frequency become measurable.

The constraint is lifted with this ADR. The lifting is explicit, not
silent, because it changes the privacy and operational risk profile
of the deployment.

## Decisions

### D1. Lift the "synthetic-only" guardrail

The CLAUDE.md line *"No real Vmoto data"* is replaced by a more
nuanced version covering what's allowed and what isn't.

Allowed: vehicle master (VIN, model, year, country), parts master
(P/N, description, price), part-replacement events on cases.

Still NOT allowed: owner data (name, email, phone), real-money flows
(invoices, payments), service contracts, anything that needs explicit
consent under GDPR Art. 6(1)(a).

### D2. Privacy posture: VIN is attenuated PII, no owner data

- A VIN can in principle be traced to a registered owner through
  third-party databases. We treat it as **attenuated PII**.
- Stele never stores owner_name / email / phone / address. The link
  to the owner lives in Odoo / paper / dealer systems; Stele only
  cares that "this VIN had this fault".
- Legal basis for processing: GDPR Art. 6(1)(f) legitimate interest
  in product reliability of our own manufacture, with no
  identification of the data subject beyond the VIN.
- Retention: vehicle records keep their full life. When a recall
  campaign closes or a model is withdrawn, the master row can be
  marked `archived_at` (deferred to a later ADR; not in M9).
- Right to erasure: handled via a future `VehicleRedacted` event
  that nulls the VIN and orphans events. Not implemented at M9;
  documented as the path.

### D3. Master data source for the pilot: CSV upload via admin

- New pages `/admin/vehicles` and `/admin/parts` with a file picker.
- One-shot or periodic manual; idempotent upsert (re-uploading the
  same VIN updates fields, never duplicates).
- Validation report after each import: rows inserted, rows updated,
  rows skipped with reason (bad VIN format, unknown model code, etc.).
- Odoo sync (XML-RPC nightly) is **deferred** until the pilot proves
  out the metric. Documented as the obvious next step after M10.

### D4. Domain extension at M9: master tables + part events

Three new tables (migration 0009):

```sql
vehicle_models (
    code         text        PRIMARY KEY,        -- e.g. "VMI_SPORT_2024"
    name         text        NOT NULL,            -- "VMI Sport"
    generation   text        NULL,                -- "Gen 2"
    segment      text        NULL,                -- "scooter L1e"
    capacity_kwh numeric(5,2) NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

vehicles (
    vin               text        PRIMARY KEY CHECK (length(vin) = 17),
    model_code        text        NOT NULL REFERENCES vehicle_models(code),
    manufactured_year int         NULL,
    sold_at           date        NULL,           -- first-sale date when known
    country           text        NULL,
    created_at        timestamptz NOT NULL DEFAULT now()
);

parts (
    pn             text          PRIMARY KEY,     -- "BAT-72V-40AH"
    description    text          NOT NULL,
    category       text          NULL,            -- "battery_pack"
    price_eur      numeric(10,2) NULL,            -- dealer price reference
    supersedes_pn  text          NULL REFERENCES parts(pn),
    created_at     timestamptz   NOT NULL DEFAULT now()
);
```

Two new event types on the `fault_case` aggregate:

- `PartReplaced { pn, qty, reason, kind }` where kind is `warranty`
  / `goodwill` / `out_of_warranty` so the cost attribution per kind
  is clean.
- `PartQuoted { pn, qty, quoted_amount_eur }` for out-of-warranty
  proposals (whether or not the customer accepts).

Both feed a new read model `case_parts` so per-case lookups and
per-part aggregations both stay cheap.

### D5. Case enrichment via VIN

- Case detail summary cell "VIN" gains a sub-line "BMI Sport · 2024"
  when the VIN resolves to a `vehicles` row. Unknown VINs show "VIN
  not in master (consider importing)" with a link to /admin/vehicles.
- Cases list keeps the VIN code in the existing column; clicking the
  case row goes to the detail (per the UI refresh in the previous
  session).
- Search by VIN already works; M10 will add "search by model name"
  via the vehicle lookup.

### D6. Failure-rate analytics: deferred to M10

M9 lays the data foundation. M10 adds `/analytics` with the three
metrics Yan selected:
- Failure rate per model (cases / fleet at risk, 12 months)
- Top failed parts + cumulative cost
- MTBF estimated per model (requires `sold_at` on vehicles)

Splitting like this keeps each milestone small enough to ship in one
session and to roll back independently.

### D7. Repository remains open source MIT, public

The code is and stays public. The **dataset** (CSV import files,
DB dump, document tarballs) is **never** committed: stays on the
Hetzner host under `~/data/` and `~/backups/`, gitignored at the
repo root level.

`.gitignore` gets explicit entries for `*.csv`, `*.dump`, and
`/data/`, in case anyone is tempted to drag-and-drop into the repo
during onboarding.

### D8. Hardening deferred but documented

These are not blockers for the pilot but are the obvious next steps
once the metric value is proven:

- **Disk encryption at rest** on the Hetzner volume (right now plain
  ext4 on the host disk).
- **Off-host backup** (S3-compat / Hetzner Storage Box). Today the
  pg_dump + docs tarball live only on the same disk as the DB; a
  host loss = total loss.
- **Audit log** for who viewed which VIN (currently logged at the
  HTTP access log level only).
- **VehicleRedacted event** + UI to satisfy a future right-to-erasure
  request.
- **Odoo XML-RPC sync** to eliminate the CSV manual loop.

Each gets its own ADR when triggered. None are in M9 scope.

## Consequences

- The deployed instance now contains real Vmoto VINs and P/Ns. The
  host (`hetzner-odoo`) becomes a small piece of production data
  surface. SSH access matters; the dev password posture must change
  before more users are invited (item already on the radar).
- The synthetic dataset stays valid: M9 doesn't wipe the existing
  200 cases. Real cases just start landing alongside them. The
  legacy aggregate_type values (`claim`, `warranty_claim`) keep
  being filtered out by current projectors.
- The "side project" framing in CLAUDE.md still holds operationally
  (Yan still has carte blanche, no IT sign-off process). The blast
  radius is bounded by the host and by the no-owner-data rule.
- The CSV import workflow doubles as the dry-run of an eventual
  Odoo sync: same idempotent upsert, same validation report.

## Open questions deferred

- Multi-tenant / per-region scoping when external dealers get
  accounts: still out of scope.
- Recall master (linking the `recall` kind to a campaign): natural
  extension of M9, possibly M11.
- Per-VIN history view ("everything that ever happened to this
  vehicle"): one query away from M9 data, but UX-wise belongs to
  M10 alongside the analytics page.
