# Production Readiness

## Ready

- Personal-user API relay with USD wallet reservation and settlement.
- Email verification/password-reset delivery and user/API-key spend/request limits.
- OpenAI-compatible, Anthropic-compatible, and Gemini OpenAI-compatible adapters.
- SSE usage parsing with fallback metering.
- Realtime WebSocket relay with frame count, byte count, duration, and fallback settlement.
- Account selection using affinity, priority, headroom, failure penalty, quota reset, active request count, and stable creation order.
- Group allow-list/deny model policy, group price multipliers, RPM/monthly limits, and usage/ledger policy snapshots.
- Stripe-style order, subscription, refund, refund-blocked, and affiliate rebate runtime paths covered by smoke.
- Account-pool quality scoring, manual quality actions, and strategy event history.
- Built-in admin operations overview at `GET /api/admin/v1/ops/overview`.
- Docker Compose production overlay with `/readyz` and local Postgres backup scripts.
- Browser smoke test using Playwright for non-blank portal/admin rendering.
- Admin runtime overview at `GET /api/admin/v1/runtime/overview` and operations overview at `GET /api/admin/v1/ops/overview`.
- Admin notification channels/events for signed operational and low-balance webhooks.
- Duplicate `X-Request-Id` protection by API key and request fingerprint.
- Header filtering for downstream `Authorization`, `Cookie`, `X-Api-Key`, WebSocket upgrade internals, gateway metadata, and route pass-through rules.

## Experimental

- Native Gemini transform.
- Rerank provider interoperability beyond matching `/v1/rerank` upstreams.
- Provider-specific realtime usage beyond known JSON usage events.

## Required Environment

- `DATABASE_URL`
- `REDIS_ADDR`
- `VAULT_KEY`: long random value. Rotating this requires re-encrypting upstream credentials.
- `COOKIE_SECURE=true` behind HTTPS.
- `CORS_ALLOWED_ORIGINS` restricted to the single web console origin.
- `TRUSTED_PROXY_CIDRS`: proxy IPs/CIDRs allowed to supply `X-Forwarded-For`.
- `PORTAL_BASE_URL`: public portal URL used in password-reset and email-verification links.
- `PUBLIC_GATEWAY_API_URL`: browser-facing gateway URL baked into the production portal image. Use the same origin as `PORTAL_BASE_URL` when a reverse proxy routes `/api` to the gateway.
- `SMTP_HOST`, `SMTP_PORT`, `SMTP_FROM`, `SMTP_TLS_MODE`: outbound email transport. `SMTP_USERNAME` and `SMTP_PASSWORD` are required only when the SMTP server requires auth.
- `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `BILLING_SUCCESS_URL`, `BILLING_CANCEL_URL`: optional environment defaults for Checkout. Admin page database settings override them.

Use `.env.production.example` as the starting point for a single-node Docker deployment. Do not reuse the example secrets.

## Docker Production

Single-node production uses the base compose file plus the production overlay:

```bash
cp .env.production.example .env.production
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml up -d --build
```

Run migrations explicitly unless your deployment automation owns it:

```bash
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml run --rm gateway-api migrate up
```

Liveness and readiness are separate:

- `GET /healthz`: process is alive.
- `GET /readyz`: database, Redis, and production config are usable.

The production overlay keeps services bound to localhost by default. Put TLS and public routing in a reverse proxy in front of `portal`.

## Operations Overview

Open the admin console `运维` page to inspect readiness, throughput, latency, error distribution, degraded accounts, failed requests, payment events, notification events, risk events, and account-pool strategy events. The page reads `GET /api/admin/v1/ops/overview?time_range=24h` and uses normal admin-session permission checks.

The built-in overview does not expose API keys, emails, request bodies, response bodies, or upstream secrets.

Minimal reverse-proxy routing:

- `/` -> portal
- `/api/`, `/v1/`, `/healthz`, `/readyz` -> gateway-api

## Backups

Default Postgres backups are local compressed custom dumps:

```bash
BACKUP_DIR=./backups/postgres ./infra/backup-postgres.sh
```

Restore requires explicit confirmation:

```bash
CONFIRM_RESTORE=restore-elucid_relay ./infra/restore-postgres.sh ./backups/postgres/elucid-relay-elucid_relay-YYYYMMDDTHHMMSSZ.dump
```

Recommended baseline:

- Run backups before every migration and at least daily.
- Keep local backups out of the web root and sync them off-host if this is exposed to real users.
- Test restore on a separate machine before declaring the deployment recoverable.

## Validation

Local baseline:

```bash
npm run release:gate
```

The Docker compose validation inside `release:gate` uses `.env.production.example` by default. Set `PROD_ENV_FILE=.env.production` to validate real deployment values.

Full local baseline:

```bash
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23-alpine go test ./...
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23-alpine go vet ./...
npm run typecheck
npm run build
docker compose up --build -d gateway-api portal
curl -fsS http://localhost:18080/healthz
curl -fsS http://localhost:18080/readyz
bash infra/smoke-api.sh
bash infra/chaos-smoke.sh
RUN_BROWSER_SMOKE=1 npm run release:gate
npm run e2e:providers
npm run e2e:stripe
```

Real provider checks:

```bash
npm run e2e:providers
```

Provider checks skip unless their explicit provider key or enable flag is set.

Stripe test-mode checkout and signed webhook check:

```bash
E2E_STRIPE_TESTMODE=1 \
STRIPE_SECRET_KEY=sk_test_... \
STRIPE_WEBHOOK_SECRET=whsec_... \
npm run e2e:stripe
```

This creates a real Stripe test-mode Checkout Session, posts a signed webhook payload to the gateway, verifies webhook idempotency, and confirms the local order becomes paid. The gateway process must also be started with matching Stripe environment variables.

Admin Stripe settings can also be stored through the page. Page settings are encrypted in the database and override env values for Checkout, webhook, and refund flows. The admin Stripe test action calls Stripe `/v1/account` and reports account/mode status without returning secrets.
