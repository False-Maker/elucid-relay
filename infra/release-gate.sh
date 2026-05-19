#!/usr/bin/env bash
set -euo pipefail

RUN_DOCKER="${RUN_DOCKER:-0}"
RUN_SMOKE="${RUN_SMOKE:-0}"
RUN_BROWSER_SMOKE="${RUN_BROWSER_SMOKE:-0}"
RUN_GO_VET="${RUN_GO_VET:-1}"
BASE_URL="${BASE_URL:-http://localhost:18080}"
PROD_ENV_FILE="${PROD_ENV_FILE:-.env.production.example}"

npm run typecheck --workspace @elucid-relay/contracts
npm run build --workspace @elucid-relay/contracts
npm run typecheck --workspace @elucid-relay/portal
npm run build --workspace @elucid-relay/portal

docker run --rm \
  -v "$PWD/services/gateway-api:/src" \
  -w /src \
  golang:1.23-alpine \
  go test ./...

if [ "${RUN_GO_VET}" = "1" ]; then
  docker run --rm \
    -v "$PWD/services/gateway-api:/src" \
    -w /src \
    golang:1.23-alpine \
    go vet ./...
fi

if [ "${RUN_DOCKER}" = "1" ]; then
  docker compose build gateway-api portal
  docker compose --env-file "${PROD_ENV_FILE}" -f docker-compose.yml -f docker-compose.prod.yml config >/dev/null
fi

if [ "${RUN_SMOKE}" = "1" ]; then
  docker compose --profile smoke up -d postgres redis gateway-api portal mock-upstream
  BASE_URL="${BASE_URL}" bash infra/smoke-api.sh
fi

if [ "${RUN_BROWSER_SMOKE}" = "1" ]; then
  npx playwright install --with-deps chromium
  npm run smoke:browser
fi
