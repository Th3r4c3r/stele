#!/usr/bin/env bash
#
# Nightly pg_dump of the Stele database, with 7-day rotation.
# Intended to be cron-scheduled on the Hetzner host at 03:30 (Odoo's
# own backup runs at 03:00, so we avoid concurrent load on the disk).
#
# Install:
#   sudo mkdir -p /home/yan/backups/stele
#   cp deploy/backup/stele-backup.sh /home/yan/backups/stele-backup.sh
#   chmod +x /home/yan/backups/stele-backup.sh
#   crontab -e
#     30 3 * * * /home/yan/backups/stele-backup.sh >> /home/yan/backups/stele-backup.log 2>&1
#
# Verify restore (run occasionally):
#   bash deploy/backup/stele-restore-test.sh /home/yan/backups/stele/<file>.sql.gz

set -euo pipefail

BACKUP_DIR="${BACKUP_DIR:-/home/yan/backups/stele}"
KEEP_DAYS="${KEEP_DAYS:-7}"
COMPOSE_DIR="${COMPOSE_DIR:-/home/yan/stele/deploy}"

mkdir -p "$BACKUP_DIR"

TS=$(date -u +%Y%m%d-%H%M%S)
OUT="$BACKUP_DIR/stele-${TS}.sql.gz"

echo "[$(date -Iseconds)] pg_dump -> $OUT"
# pg_dump runs inside the stele-db container as the stele user.
docker compose -f "$COMPOSE_DIR/docker-compose.yml" exec -T db \
    pg_dump -U stele -d stele --no-owner --no-acl \
  | gzip -9 > "$OUT"

SIZE=$(stat -c %s "$OUT")
echo "[$(date -Iseconds)] dump size: $SIZE bytes"

# Documents tarball (ADR-010). Only if the directory exists and is
# non-empty. The same rotation policy applies.
DOCS_DIR="${DOCS_DIR:-/home/yan/data/documents}"
if [ -d "$DOCS_DIR" ] && [ -n "$(ls -A "$DOCS_DIR" 2>/dev/null)" ]; then
    DOCS_OUT="$BACKUP_DIR/docs-${TS}.tar.gz"
    echo "[$(date -Iseconds)] tarball -> $DOCS_OUT"
    tar -czf "$DOCS_OUT" -C "$(dirname "$DOCS_DIR")" "$(basename "$DOCS_DIR")"
    echo "[$(date -Iseconds)] docs tarball size: $(stat -c %s "$DOCS_OUT") bytes"
fi

echo "[$(date -Iseconds)] rotating: keep last $KEEP_DAYS days"
find "$BACKUP_DIR" -maxdepth 1 -name 'stele-*.sql.gz' -mtime "+$KEEP_DAYS" -delete
find "$BACKUP_DIR" -maxdepth 1 -name 'docs-*.tar.gz' -mtime "+$KEEP_DAYS" -delete

echo "[$(date -Iseconds)] backup OK"
