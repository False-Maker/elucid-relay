# Architecture

## System Shape

Elucid Relay is a modular monolith:

- Backend: Go HTTP service with PostgreSQL and Redis.
- Frontend: one `apps/portal` web app that contains personal user pages, the admin workspace, and public information pages.
- Database: PostgreSQL as source of truth.
- Redis: sessions, rate limits, scheduler hot state, idempotency windows.
- Runtime: provider executors, request translators, scheduler, wallet settlement.

## Boundaries

```text
Portal web console
  -> /api/portal/v1
Portal admin workspace
  -> /api/admin/v1
Portal public information views
  -> /api/public/v1
Client SDKs / AI tools
  -> /v1/*

gateway-api
  -> identity
  -> wallet
  -> api_key
  -> catalog
  -> policy
  -> metering
  -> runtime
  -> upstream operations
```

## Backend Modules

- `identity`: personal registration, login, password hash, sessions, admin auth.
- `wallet`: wallet account, balance reservation, ledger, redeem-code claim, manual adjustment.
- `api_key`: personal API key secret generation, hashing, scope, IP allowlist, status.
- `account_security`: email verification delivery, password-reset delivery, session revocation after password reset.
- `limits`: user/API-key daily and monthly USD/request limits enforced before upstream routing.
- `catalog`: model catalog, aliases, endpoint capability, pricing metadata.
- `policy`: API-key model scope, IP allowlist, effective group policy, group/model allow-deny, price multiplier, RPM, and monthly USD limits.
- `metering`: usage records, cost calculation, settlement snapshots.
- `runtime`: route selection, optional per-key route affinity, provider execution, streaming, WebSocket relay, retries, quota window updates.
- `upstream`: providers, channels, accounts, provider clients, proxies, vault, model discovery, channel sync jobs.
- `billing`: Stripe checkout/webhook, orders, subscriptions, refunds, subscription group grants, affiliate rebates, finance summary.
- `risk`: sensitive-word, SSRF, request-limit, bot/abuse rules plus risk events.
- `account_pool_ops`: account import/export, quota refresh, health checks, quality scoring, wakeup jobs, platform policies, and batch operations.
- `audit`: user/admin/security/action logs.
- `notifications`: admin-managed signed webhook channels plus runtime notification events for critical operations and low-balance thresholds.

## Northbound Request Flow

1. Parse bearer token and resolve active personal API key.
2. Validate user status, key status, expiry, IP allowlist, and API-key model scope.
3. Resolve requested model alias to canonical model and endpoint capability.
4. Resolve the user's effective active group by group priority and membership creation time.
5. Apply group/model allow-deny policy, group/model price multiplier, group RPM, and monthly USD limits.
6. Evaluate risk rules and record risk events for flag/block/throttle decisions.
7. Estimate request cost from pricing metadata and effective multiplier.
8. Enforce user/API-key spend and request limits.
9. Reserve wallet balance.
10. Select upstream channel/account by capability, status, cooldown, quota window, circuit state, priority, weight, concurrency, route tags, and optional per-API-key route affinity.
11. Execute provider request and stream, relay WebSocket, or return response.
12. Calculate actual cost from usage result and effective multiplier.
13. Write usage record with group, multiplier, effective-policy, and risk snapshots.
14. Settle wallet ledger with settlement metadata, or release reservation on failure.
15. Emit deduplicated low-balance notification events when a user balance is at or below its threshold.

## Wallet Rules

- Currency is USD.
- User can hold `balance` and `reserved_balance`.
- Available balance is `balance - reserved_balance`.
- Successful requests create debit ledger entries.
- Debit ledger metadata includes account, multiplier, effective policy, reservation, and settlement snapshot fields for auditability.
- Failed upstream requests release reservation and do not debit, unless a provider cost was definitively incurred and recorded.
- Duplicate request idempotency must not double-charge.

## Security Rules

- API key secret is shown only once at creation.
- Store only secret hash and display prefix.
- Passwords use strong password hashing.
- Portal and Admin sessions use separate audiences inside the same web console.
- Write requests require CSRF protection for cookie sessions.
- Registration, login, redeem-code claim, and API key creation are rate-limited.
- Admin routes require operator/platform-owner role.

## Reference Mapping

- From `sub2api`: personal wallet, redeem-code flow, user-facing key management, quota distribution semantics.
- From `new-api`: relay station admin experience, model/channel/pricing management, usage analytics.
- From `CLIProxyAPI`: OAuth account pool, CLI model compatibility, provider-specific routes, quota window awareness.
- From `Sub-Router`: session-to-account affinity and strict router metadata stripping for reverse-proxy safety.
