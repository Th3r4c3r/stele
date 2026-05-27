# ADR-014: Fleet telemetry view (newplat snapshot)

- Status: Accepted
- Date: 2026-05-27
- Authors: Claude (PM agent), direction by Yan
- Touches: ADR-013 (pilot Vmoto data), new package `internal/newplat`,
  new package `internal/telemetry`, migration 0013, case detail UI.

## Context

Case investigation today shows only static facts about a bike: model,
year, color, recall codes (M9.2). It does NOT show live state — is
the bike online right now, what is its SOC, when was it last seen,
is the SIM about to expire. Those facts live on Vmoto's `newplat`
SaaS (https://newplat.vmotosoco-service.com), behind a JWT.

Yan's operational question: *"When a customer reports a fault, can I
see in Stele whether the bike is reachable, what its battery state
is, and whether the SIM still works — without leaving the case
detail page?"*

This ADR captures the design decision behind the answer.

## Decision

Add a **fleet telemetry view** to Stele, fed by **snapshot** (not
live) lookups against newplat. One row per VIN in
`vehicle_telemetry`, upserted on demand by an admin-triggered sync.
Snapshots have a `snapshot_at` timestamp so the UI can render an age
("synced 3 minutes ago") and the operator can decide whether to
re-sync before acting.

### Snapshot vs live: snapshot wins

| Criterion | Live lookup | Snapshot (chosen) |
|---|---|---|
| Freshness | sub-second | minutes to hours |
| Page load latency | newplat round-trip on every render | DB read only |
| Resilience to newplat downtime | breaks Stele case page | shows stale snapshot |
| Token rotation impact | every request risks 401 | sync fails, UI keeps working |
| Indexability | impossible | trivial (DB columns) |
| Audit trail | none | every sync is an event line |

A case investigation rarely needs sub-minute freshness: the operator
glances at SOC, last-online, SIM expiration as **context**, not as a
live monitor. The UI exposes a "refresh" button per VIN that runs a
single sync on demand, which gives effective freshness on demand
without paying it on every page load.

### Schema

Migration 0013 introduces one wide table. One row per VIN, upserted.
History is intentionally NOT kept here — when trend matters we add
`vehicle_telemetry_history` (append-only, per-snapshot). For now the
value is "current state", not "history".

```sql
CREATE TABLE vehicle_telemetry (
    vin                 text         PRIMARY KEY REFERENCES vehicles(vin) ON DELETE CASCADE,
    snapshot_at         timestamptz  NOT NULL DEFAULT now(),
    is_online           boolean      NOT NULL,
    imei                text         NULL,
    iccid               text         NULL,
    sim_end_time        timestamptz  NULL,
    agreement_end_time  timestamptz  NULL,
    last_online_at      timestamptz  NULL,   -- pojo.lastGpsTime
    login_time          timestamptz  NULL,   -- pojo.loginTime
    soc_pct             int          NULL,   -- pojo.nowElec
    endurance_km        int          NULL,   -- pojo.endurance
    total_mileage_km    numeric(10,2) NULL,  -- carBaseInfo.totalMileage
    latitude            numeric(10,7) NULL,
    longitude           numeric(10,7) NULL,
    bms_temperature     int          NULL,
    gsm_signal          int          NULL,
    gps_satellites      int          NULL,
    fota_version        text         NULL,
    raw_payload         jsonb        NULL    -- full newplat detail, for forensics
);
```

`raw_payload` is the unflattened newplat response. Cheap insurance:
if we later realise we missed a useful field, we can re-project
without re-fetching from newplat.

### Token management

Newplat authentication is a JWT (`Authorization: <token>`) tied to
the "Vmoto After Sales" account. The token has no documented public
refresh endpoint: when it expires, the operator logs into newplat in
a browser and copies the new token from DevTools Network tab.

We store it as `STELE_NEWPLAT_TOKEN` in `~/stele/deploy/.env` (chmod
600, gitignored). On 401/403 from newplat, the sync surfaces a clear
"token expired, refresh from admin" message; the UI keeps working
with stale snapshots.

Token is NEVER committed and NEVER logged. The newplat client
package masks it in any structured log output it emits.

### Sync surface

Three entry points, all admin-only:

1. **`POST /admin/telemetry/sync` with `vin=<single>`** — one VIN,
   for the "refresh" button on case detail.
2. **`POST /admin/telemetry/sync` with `mode=case-vins`** — every
   VIN that appears on a non-closed case. Realistic batch (~tens of
   VINs in the pilot), runs synchronously in <30s.
3. **`POST /admin/telemetry/sync` with `mode=all` + `limit=N`** —
   any-VIN sweep, capped. Useful for first backfill or periodic
   refresh. Deferred: a real cron is M13.x territory.

No background daemon yet. The pilot volume (~38k VINs total but
only ~100s actively investigated) makes on-demand sync workable.

### UI placement

- **Case detail page**: a "Telemetry" block under the VIN cell,
  shown only when a row exists in `vehicle_telemetry` for that VIN.
  Shows online status (green/red dot), SOC %, endurance km, last
  seen (with humanised age), SIM expiration warning if within 30
  days, snapshot age. A "refresh" button POSTs to the per-VIN sync.

- **`/admin/telemetry`**: a list of recent snapshots with batch sync
  buttons. Filterable by online status, SIM-expiring-soon, etc.
  (MVP: just the list + sync buttons; filters are follow-up.)

## Consequences

### Positive

- Case investigators see live(-ish) bike state without leaving Stele.
- No invasive coupling: newplat downtime degrades gracefully to
  "stale snapshot" instead of breaking pages.
- `raw_payload` future-proofs the projection: schema changes can be
  re-derived without re-hitting newplat.
- Pattern reusable: future external integrations (Softway, shoushou)
  follow the same client + snapshot table + admin-trigger model.

### Negative

- Token rotation is manual. Operator burden ~once per month
  (token TTL guess; we'll learn). Mitigation: clear failure mode
  + documented refresh procedure.
- Schema is wide. If newplat field names change, migration needed.
  Mitigation: `raw_payload` lets us survive a few additions/renames
  without immediate migration.
- Snapshot model means stale data is a UX risk. Mitigation: every
  read shows snapshot age; >24h old shown in muted color.

### Security

- Token treated as a credential (env var, chmod 600, gitignored,
  never logged).
- Audit log (M12) captures every sync POST with actor + VIN count.
- newplat data IS attenuated PII (location of customers' bikes).
  Same posture as ADR-013: deployment only, never in repo, never
  exported.

## Out of scope

- Real-time push from newplat to Stele (would need newplat to support
  webhooks; it doesn't).
- Historical trend visualisation (no `vehicle_telemetry_history` yet).
- Live position map (we have lat/long but the UI is one bike at a
  time; a fleet map is a separate feature).
- Background cron sync. Manual + on-case-view is sufficient for the
  pilot.

## Open questions (revisited later)

- How long until newplat token expires in practice? Track on first
  rotation event.
- Is `pojo.lastGpsTime` reliable as "last seen", or does it lag
  `pojo.loginTime`? Compare across a few weeks of data.
- Should "stale" threshold be 24h, or per-customer SLA? Wait for
  operator feedback.
