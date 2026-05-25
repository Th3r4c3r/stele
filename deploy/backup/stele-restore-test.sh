#!/usr/bin/env bash
#
# Restore-test: load a Stele dump into a throwaway Postgres container
# and assert row counts on the main tables. Does NOT touch production.
#
# Usage:
#   bash stele-restore-test.sh /path/to/stele-YYYYMMDD-HHMMSS.sql.gz
set -euo pipefail

DUMP="${1:?usage: stele-restore-test.sh <dump.sql.gz>}"
[ -f "$DUMP" ] || { echo "no such file: $DUMP" >&2; exit 1; }

PG_NAME="stele-restore-test-$$"
trap 'docker rm -f "$PG_NAME" >/dev/null 2>&1 || true' EXIT

echo "starting throwaway postgres ($PG_NAME)"
docker run -d --name "$PG_NAME" \
  -e POSTGRES_USER=stele -e POSTGRES_PASSWORD=stele -e POSTGRES_DB=stele \
  postgres:16-alpine >/dev/null

for i in $(seq 1 30); do
  docker exec "$PG_NAME" pg_isready -U stele -d stele >/dev/null 2>&1 && break
  sleep 1
done

echo "restoring $DUMP"
gunzip -c "$DUMP" | docker exec -i "$PG_NAME" psql -U stele -d stele >/dev/null

echo "smoke checks:"
for tbl in events current_claims projection_cursors projection_event_counts; do
  n=$(docker exec "$PG_NAME" psql -U stele -d stele -tA -c "SELECT count(*) FROM $tbl")
  printf "  %-30s %s\n" "$tbl" "$n"
done
echo "restore OK"
