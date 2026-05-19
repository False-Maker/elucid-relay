# Elucid Relay

Personal AI API relay station for individual users.

## Quick Start

Requirements:

- Docker with Docker Compose.

Start the local stack:

```bash
docker compose up --build
```

Check the gateway API:

```bash
curl -fsS http://localhost:18080/healthz
```

Expected response:

```text
ok
```

Run database migrations manually:

```bash
docker compose run --rm gateway-api migrate up
```

Useful shortcuts:

```bash
make build
make up
make healthz
make migrate-up
make smoke-api
make down
```

Local entrypoints:

- Web console: <http://localhost:18081>
- Dev web console: <http://localhost:5173>
- Gateway API: <http://localhost:18080>

The web console is the only frontend entrypoint. Personal user pages, admin pages, and public information pages all live in `apps/portal`; there is no separate Admin or Public web app. On a fresh database, open the console and create the first platform administrator from the initialization screen. The setup endpoint closes after a `platform_owner` exists.

For production, start from `.env.production.example` and the production compose overlay:

```bash
cp .env.production.example .env.production
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml run --rm gateway-api migrate up
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml up -d --build
```

Then check both endpoints:

```bash
curl -fsS http://localhost:18080/healthz
curl -fsS http://localhost:18080/readyz
```

Production checklist before exposing traffic:

- Set a long random `VAULT_KEY`; rotating it requires re-encrypting upstream credentials.
- Set `COOKIE_SECURE=true` behind HTTPS and restrict `CORS_ALLOWED_ORIGINS`.
- Set `PORTAL_BASE_URL`, `PUBLIC_GATEWAY_API_URL`, `SMTP_HOST`, `SMTP_FROM`, and SMTP auth/TLS settings for password reset and email verification delivery.
- Configure payment providers in Admin `商业化闭环 -> 支付设置`. Stripe can still use `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`, `BILLING_SUCCESS_URL`, and `BILLING_CANCEL_URL`; Alipay, WeChat Pay, and EasyPay credentials are stored encrypted in the database.
- Keep `MIGRATE_ON_START=false` if migrations are managed by deployment automation.
- Put Postgres and Redis on private networks with backups and persistence enabled.
- Run `infra/backup-postgres.sh` before migrations and regularly afterward.
- Use the admin console `运维` page for built-in readiness, traffic, latency, account-pool, and event monitoring.

## Implemented v1 Surface

Backend:

- Portal auth: register, login, logout, me.
- Admin auth: login, logout, me.
- USD wallet read, ledger, admin manual credit/debit/adjustment.
- Redeem-code batch generation and personal claim.
- Personal API keys with copy-once secret, hashed storage, revoke/disable/update.
- Model catalog and endpoint capabilities.
- Provider/client/channel/account/proxy operations.
- Account runtime state, concurrency checkout, cooldown, quota-window filtering, and route explain.
- Encrypted upstream account API-key storage using `VAULT_KEY`.
- Redis-backed rate limits for auth, sensitive Portal actions, and northbound calls.
- Northbound `/v1/models` and proxy handlers for chat, responses, messages, embeddings, images, audio, realtime, and rerank.
- Usage records, wallet reserve/release/debit ledger entries, duplicate request-id protection, audit logs.
- Group policy, model allow/deny, price multipliers, RPM and monthly USD limits.
- Unified payment providers for Stripe, Alipay, WeChat Pay, and EasyPay; orders, subscriptions, refunds, affiliate rebates, and finance summary.
- Risk controls and risk event logging.
- Channel model discovery, sync jobs, and public channel status.
- Account pool import/export, quota refresh, health checks, quality scoring, wakeup jobs, platform policies, and batch actions.

Frontend:

- Single Portal app for personal dashboard, wallet, billing, API keys, usage, models, public information, and admin operations.

Verification:

```bash
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23-alpine go test ./...
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23-alpine go vet ./...
npm run build
npm run smoke:browser
bash infra/smoke-api.sh
npm run e2e:providers
npm run e2e:stripe
```

`infra/smoke-api.sh` starts the Compose `mock-upstream` profile and verifies provider/client/channel/account setup, model sync, public channel status, group billing policy, Stripe-like webhook events, refunds, subscriptions, affiliate settlement, risk events, a real `/v1/chat/completions` relay call, duplicate `X-Request-Id` rejection, usage, and wallet settlement against that local upstream.

`npm run e2e:providers` and `npm run e2e:stripe` are opt-in gates. Provider checks skip without provider credentials. Stripe requires `E2E_STRIPE_TESTMODE=1`, Stripe test secret, webhook secret, and a gateway started with matching Stripe env.

## Product Position

Elucid Relay is a self-hosted AI API relay platform for personal users. It provides public registration, USD wallet balance, redeem-code and payment-provider top-up, subscriptions, personal API keys, usage billing, group policy, risk controls, and an operations backend for model/channel/account-pool management.

This project is separate from `elucid-gateway`. The old enterprise concepts are not part of this product surface:

- No enterprise application flow.
- No project/team/member model.
- No enterprise SSO.
- No SaaS marketing site.

## Reference Projects

- `sub2api`: user self-service, quota distribution, redeem codes, personal API key flow.
- `new-api`: AI relay station product shape, model/channel pricing, user-facing usage and token management.
- `CLIProxyAPI`: CLI/OAuth upstream account pool, multi-account scheduling, quota windows, OpenAI/Gemini/Claude/Codex-compatible routes.

The implementation should reuse ideas, not copy code.

## Initial Architecture

```text
elucid-relay/
├── apps/
│   └── portal/          # Single web console for users, admins, and public information
├── services/
│   └── gateway-api/     # Go gateway, portal/admin API, northbound /v1 API
├── packages/
│   └── contracts/       # Shared API types
├── docs/                # Product, architecture, API, database, delivery plan
└── infra/               # Docker, deployment, scripts
```

## v1 Scope

- Public user registration and login.
- Personal USD wallet.
- Manual admin top-up, redeem-code top-up, Stripe checkout top-up, Alipay/WeChat/EasyPay top-up, and subscription wallet credits.
- Personal API key creation, listing, disabling, and revocation.
- Full northbound API surface: `/v1/models`, chat, responses, messages, embeddings, images, audio, realtime, rerank.
- Usage records and wallet ledger settlement.
- Admin management for users, balances, redeem codes, billing, refunds, affiliates, groups, risk, model catalog, pricing, channels, upstream accounts, proxies, model sync, and account pool health/automation.

## v1 Non-goals

- Enterprise billing, taxes, invoices, coupons, or Stripe Customer Portal.
- Enterprise projects, teams, SSO, or multi-organization tenancy.
- Public marketplace or provider resale onboarding.
- Importing runtime code from `sub2api`, `new-api`, or `CLIProxyAPI`.

## Documents

- [Product Spec](docs/product-spec.md)
- [Architecture](docs/architecture.md)
- [API Contracts](docs/api-contracts.md)
- [Database Schema](docs/database-schema.md)
- [Provider Compatibility](docs/provider-compatibility.md)
- [Production Readiness](docs/production-readiness.md)
- [Operations Runbook](docs/operations-runbook.md)
- [Implementation Plan](docs/implementation-plan.md)
- [Reference Mapping](docs/reference-mapping.md)
