# Implementation Plan

## Current Target

Elucid Relay is implemented as one web console in `apps/portal`. The same frontend contains the personal user portal, admin workspace, and public information views. There is no separate Admin app and no `apps/public` package.

## Completed Product Loops

- Identity, sessions, wallet, redeem codes, API keys, usage records, audit, and northbound `/v1/*` runtime.
- Admin operations for users, models, providers, channels, accounts, provider clients, proxies, OAuth jobs, route explain, and runtime overview.
- Commercial loop: Stripe hosted checkout, webhook processing, orders, subscriptions, subscription group grants, refunds, affiliate rebates, and finance summary.
- Runtime policy loop: API-key scope, effective group policy, model allow/deny, price multipliers, RPM, monthly USD limits, risk controls, usage snapshots, and wallet ledger settlement metadata.
- Channel model sync: single-channel sync, batch active-channel sync, sync job history, and public channel status fields.
- Account pool loop: templates/import preview/import/export, account groups, quota windows, quota refresh jobs, health checks, quality scoring, wakeup jobs, platform policies, batch actions, and worker scheduling.
- Frontend product surface: single-entry user portal, admin workspace, public information views, and a grouped account-pool workbench.

## Remaining Delivery Discipline

- Do not add a second frontend entrypoint.
- Keep `apps/portal` as the only web app exposed by Docker and npm scripts.
- Add new backend capability only behind existing API domains: `/api/portal/v1`, `/api/admin/v1`, `/api/public/v1`, `/api/billing/v1`, or `/v1/*`.
- Prefer extending smoke tests and focused backend tests before broad refactors.

## Verification Gate

Before calling a productization pass complete, run:

```bash
npm run typecheck --workspace @elucid-relay/portal
npm run build --workspace @elucid-relay/portal
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23-alpine go test ./internal/httpserver
docker compose build gateway-api portal
BASE_URL=http://localhost:18080 bash infra/smoke-api.sh
```

The smoke test must cover login, model/channel/account setup, model sync, public status, group allow-list policy, group billing policy, Stripe-like webhooks, successful refunds, refund-blocked orders, subscriptions, affiliate settlement, risk events, account-pool strategy events, northbound calls, duplicate request protection, usage records, and wallet ledger settlement.
