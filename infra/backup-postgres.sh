#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BACKUP_DIR="${BACKUP_DIR:-${PROJECT_ROOT}/backups/postgres}"
COMPOSE_FILES="${COMPOSE_FILES:--f docker-compose.yml -f docker-compose.prod.yml}"
SERVICE="${POSTGRES_SERVICE:-postgres}"
DB_NAME="${POSTGRES_DB:-elucid_relay}"
DB_USER="${POSTGRES_USER:-relay}"
RETENTION_DAYS="${BACKUP_RETENTION_DAYS:-14}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="${BACKUP_DIR}/elucid-relay-${DB_NAME}-${TIMESTAMP}.dump"

mkdir -p "${BACKUP_DIR}"

cd "${PROJECT_ROOT}"
docker compose ${COMPOSE_FILES} exec -T "${SERVICE}" \
  pg_dump -U "${DB_USER}" -d "${DB_NAME}" -Fc --no-owner --no-acl > "${OUT}"

if [ -s "${OUT}" ]; then
  echo "postgres backup written: ${OUT}"
else
  rm -f "${OUT}"
  echo "postgres backup failed: empty output" >&2
  exit 1
fi

find "${BACKUP_DIR}" -type f -name "elucid-relay-${DB_NAME}-*.dump" -mtime +"${RETENTION_DAYS}" -print -delete
