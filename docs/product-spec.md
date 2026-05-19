# Product Spec

## Goal

Build a single-entry AI API relay station. Users register publicly, top up USD balance, subscribe to plans, create API keys, attach BYO accounts when allowed, and call OpenAI-compatible or provider-compatible endpoints through the platform.

## Users

- Personal user: manages wallet, subscriptions, orders, API keys, usage, model pricing, docs, and BYO account access.
- Operator: manages users, billing, refunds, affiliates, groups, models, upstreams, account pool automation, proxies, risk controls, content, settings, and audit.

## Core User Flow

1. User registers with email and password.
2. User logs into the portal.
3. User redeems a code, receives manual admin top-up, pays a Stripe order, or activates a subscription plan.
4. User creates a personal API key with optional model scope and IP allowlist.
5. User calls `/v1/*` with `Authorization: Bearer <key>`.
6. Gateway applies API-key scope, effective group policy, risk controls, spend limits, wallet reservation, upstream routing, usage metering, and wallet settlement.

## Portal Pages

- Login and registration.
- Dashboard: balance, recent usage, active keys, model availability.
- Billing wallet: balance, ledger, redeem-code claim form, Stripe checkout orders, subscriptions, refund state, and affiliate attribution.
- API Keys: create, copy-once secret, disable, revoke, model scope, IP allowlist.
- Security: email verification, password reset email flow, user/API-key spend and request limits.
- Usage: filter by key, model, status, date.
- Models: public model list, endpoint capabilities, pricing.
- Public information: price, channel status, rankings, announcements, FAQ/API pages, privacy, and terms inside the same Portal app.
- Docs: base URL, auth example, SDK/client snippets.

## Admin Pages

- Overview: runtime health, requests, errors, active users, account pool health, revenue proxy metrics.
- Commercial operations: subscription plans, Stripe orders, refunds, subscriptions, affiliate codes, rebate settlement, finance summary.
- Users: search, status, balance, usage, API keys.
- Wallet operations: manual credit/debit and ledger audit.
- Redeem codes: batch generation, status, expiry, claim history.
- Models and pricing: model catalog, aliases, endpoint capabilities, user-visible price, channel model sync jobs.
- Upstream operations: providers, channels, accounts, OAuth clients, proxies, route explain, quota windows, public channel status.
- Account pool operations: grouped account inventory, quota refresh, health checks, quality scoring, strategy event history, wakeup jobs, platform policies, import/export, and batch actions.
- Group policy: effective user group resolution, allow/deny model policy, price multiplier, RPM, and monthly spend limits.
- Risk controls: sensitive words, SSRF target blocking, request throttles, bot/abuse patterns, and risk events.
- Controls: user/API-key spend limits, low-balance alerts, and signed webhook notification channels/events.
- Content and public pages: announcements, custom pages, FAQ/API info, privacy, and terms.
- Audit: login, key, wallet, admin, and northbound request events.

## Product Decisions

- Launch mode: public registration.
- Billing mode: USD wallet.
- Top-up mode: manual admin top-up, redeem codes, Stripe checkout, subscription wallet credits.
- API surface: keep all existing `/v1/*` endpoint families.
- Admin surface: operations backend retained, enterprise project/team semantics removed.
- Frontend surface: one `apps/portal` web app. It contains personal user pages, admin workspace, and public information pages. No standalone `apps/public` app is part of the product.
