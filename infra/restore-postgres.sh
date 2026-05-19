#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 /path/to/backup.dump" >&2
  exit 2
fi

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_FILE="$1"
COMPOSE_FILES="${COMPOSE_FILES:--f docker-compose.yml -f docker-compose.prod.yml}"
SERVICE="${POSTGRES_SERVICE:-postgres}"
DB_NAME="${POSTGRES_DB:-elucid_relay}"
DB_USER="${POSTGRES_USER:-relay}"
CONFIRM="${CONFIRM_RESTORE:-}"

if [ ! -f "${BACKUP_FILE}" ]; then
  echo "backup file not found: ${BACKUP_FILE}" >&2
  exit 2
fi

if [ "${CONFIRM}" != "restore-${DB_NAME}" ]; then
  echo "refusing restore without confirmation" >&2
  echo "set CONFIRM_RESTORE=restore-${DB_NAME} to overwrite database ${DB_NAME}" >&2
  exit 2
fi

cd "${PROJECT_ROOT}"
cat "${BACKUP_FILE}" | docker compose ${COMPOSE_FILES} exec -T "${SERVICE}" \
  pg_restore -U "${DB_USER}" -d "${DB_NAME}" --clean --if-exists --no-owner --no-acl

echo "postgres restore completed: ${BACKUP_FILE} -> ${DB_NAME}"
