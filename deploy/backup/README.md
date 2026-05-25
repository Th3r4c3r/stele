# Backups

Daily `pg_dump` of the Stele database, gzipped, with 7-day rotation.

## Why 03:30

Odoo's own backup runs at 03:00 on the same host. Scheduling Stele's
dump at 03:30 keeps disk I/O serialized so neither stack starves the
other during the nightly window.

## Files

- `stele-backup.sh` — the dump script. Cron entry it expects:
  ```
  30 3 * * * /home/yan/backups/stele-backup.sh >> /home/yan/backups/stele-backup.log 2>&1
  ```
- `stele-restore-test.sh` — loads a dump into a throwaway Postgres
  container and prints row counts on the main tables. Run weekly to
  catch corrupted dumps before they're the only thing you have.

## Storage

Dumps land in `/home/yan/backups/stele/`. At current volumes
(~200 claims, ~1k events) the dumps are kilobytes. M3+ adds
documents which will inflate the size; revisit retention then.

Off-host copy (Hetzner Storage Box or S3-compatible) is deferred:
single-user, synthetic data, host disk is RAID-1.
