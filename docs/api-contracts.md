# API Contracts

## Response Shape

Success:

```json
{
  "data": {},
  "meta": {}
}
```

Error:

```json
{
  "error": {
    "code": "insufficient_balance",
    "message": "Wallet balance is insufficient.",
    "type": "billing_error",
    "request_id": "req_xxx"
  }
}
```

## Portal API

Prefix: `/api/portal/v1`

- `POST /auth/register`
- `POST /auth/login`
- `POST /auth/logout`
- `GET /me`
- `GET /wallet`
- `GET /wallet/ledger`
- `POST /redeem`
- `GET /subscription-plans`
- `GET /orders`
- `POST /orders`
- `GET /orders/:orderId`
- `POST /orders/:orderId/checkout`
- `GET /subscriptions`
- `POST /affiliate-attribution`
- `GET /api-keys`
- `POST /api-keys`
- `PATCH /api-keys/:apiKeyId`
- `PUT /api-keys/:apiKeyId/spend-limit`
- `DELETE /api-keys/:apiKeyId`
- `GET /oauth/options`
- `GET /oauth/accounts`
- `POST /oauth/accounts`
- `POST /oauth/accounts/:accountId/reauth`
- `POST /oauth/accounts/:accountId/revoke`
- `GET /usage`
- `GET /models`

Key behavior:

- Registration creates `personal_user` plus an active USD wallet.
- API key creation returns full `secret` once.
- API keys include `routing_mode`. `pool` uses the shared account pool and wallet accounting. `byo` only routes to OAuth accounts owned by the same user and does not debit the Relay wallet.
- API key list never returns secret or hash.
- Redeem code can be claimed once unless explicitly configured otherwise.
- OAuth options returns active providers, channels, provider clients, and auth modes available for Portal BYO account creation. It never returns client secrets or token bundles.
- BYO OAuth accounts never enter the shared pool.
- Wallet top-up and subscription orders are created under `/orders`; Stripe hosted Checkout is requested with `/orders/:orderId/checkout`.
- Paid wallet orders credit `payment`; subscription activation credits `subscription_credit` and may grant a group membership; refunds debit `refund_reversal` before marking an order refunded.
- Affiliate attribution can be set before payment. Paid attributed orders create pending rebates for admin settlement.

## Admin API

Prefix: `/api/admin/v1`

- `POST /auth/login`
- `POST /auth/logout`
- `GET /me`
- `GET /users`
- `PATCH /users/:userId`
- `GET /users/:userId/api-keys`
- `GET /users/:userId/wallet`
- `GET /users/:userId/wallet/ledger`
- `POST /users/:userId/wallet/adjustments`
- `GET /redeem-codes`
- `POST /redeem-codes`
- `PATCH /redeem-codes/:redeemCodeId`
- `GET /redeem-codes/:redeemCodeId/claims`
- `GET /usage`
- `GET /usage/summary`
- `GET /usage/export`
- `POST /usage/cleanup`
- `GET /audit`
- `GET /spend-limits`
- `PUT /spend-limits`
- `GET /notification-channels`
- `POST /notification-channels`
- `PATCH /notification-channels/:channelId`
- `POST /notification-channels/:channelId/test`
- `GET /notification-events`
- `GET /models`
- `POST /models`
- `GET /models/conflicts`
- `POST /models/batch`
- `POST /models/sync-from-channels`
- `PATCH /models/:modelName`
- `GET /announcements`
- `POST /announcements`
- `PATCH /announcements/:announcementId`
- `GET /content-pages`
- `POST /content-pages`
- `PATCH /content-pages/:pageId`
- `GET /groups`
- `POST /groups`
- `PATCH /groups/:groupId`
- `GET /groups/effective-policy`
- `POST /groups/:groupId/members`
- `DELETE /groups/:groupId/members/:userId`
- `POST /groups/:groupId/model-permissions`
- `GET /system-settings`
- `PUT /system-settings`
- `GET /risk-controls`
- `POST /risk-controls`
- `PATCH /risk-controls/:ruleId`
- `GET /risk-events`
- `GET /providers`
- `POST /providers`
- `PATCH /providers/:providerId`
- `GET /provider-clients`
- `POST /provider-clients`
- `PATCH /provider-clients/:providerClientId`
- `GET /channels`
- `POST /channels`
- `PATCH /channels/:channelId`
- `POST /channels/:channelId/model-sync`
- `GET /model-sync-jobs`
- `POST /model-sync-jobs`
- `GET /accounts`
- `POST /accounts`
- `GET /accounts/export`
- `GET /account-import-templates`
- `POST /accounts/import-preview`
- `POST /accounts/import`
- `POST /accounts/batch`
- `POST /accounts/health-check`
- `POST /accounts/quota-refresh`
- `PATCH /accounts/:accountId`
- `POST /accounts/:accountId/quality-action`
- `POST /accounts/:accountId/quota-refresh`
- `GET /account-quality`
- `POST /account-quality/recompute`
- `GET /account-pool-strategy-events`
- `GET /account-wakeup-jobs`
- `POST /account-wakeup-jobs`
- `POST /account-wakeup-jobs/:jobId/run`
- `GET /account-platform-configs`
- `PUT /account-platform-configs/:providerType`
- `GET /account-pool-groups`
- `POST /account-pool-groups`
- `PATCH /account-pool-groups/:groupId`
- `POST /account-pool-groups/:groupId/members`
- `DELETE /account-pool-groups/:groupId/members/:accountId`
- `GET /account-auth-states`
- `PATCH /account-auth-states/:accountId`
- `GET /oauth/jobs`
- `POST /oauth/jobs`
- `POST /oauth/jobs/:jobId/input`
- `PATCH /oauth/jobs/:jobId`
- `GET /account-quota-snapshots`
- `GET /account-quota-refresh-jobs`
- `GET /account-quota-windows`
- `POST /account-quota-windows`
- `PATCH /account-quota-windows/:quotaWindowId`
- `GET /proxies`
- `POST /proxies`
- `GET /proxies/export`
- `POST /proxies/import`
- `POST /proxies/batch`
- `POST /proxies/quality`
- `GET /proxy-test-results`
- `POST /proxies/:proxyId/test`
- `PATCH /proxies/:proxyId`

Proxy `proxy_url` accepts `http`, `https`, `socks5`, and `socks5h` URLs. The special values `direct` and `none` are normalized to `direct` and explicitly bypass environment proxy settings for that route.

- `GET /channel-tests`
- `POST /channel-tests`
- `GET /runtime/route-explain`
- `GET /runtime/overview`
- `GET /ops/overview`
- `GET /subscription-plans`
- `POST /subscription-plans`
- `PATCH /subscription-plans/:planId`
- `GET /orders`
- `POST /orders/:orderId/refund`
- `GET /subscriptions`
- `POST /subscriptions/:subscriptionId/cancel`
- `GET /affiliate-codes`
- `POST /affiliate-codes`
- `GET /affiliate-rebates`
- `POST /affiliate-rebates/:rebateId/settle`
- `GET /finance/summary`

Key behavior:

- Admin and personal-user pages are delivered by the same `apps/portal` frontend. API prefixes remain separate for authorization and contract clarity.
- `POST /model-sync-jobs` runs live discovery for supplied `channel_ids`; if omitted, it syncs active channels up to the provided limit.
- Account pool automation is controlled by `system_settings` and per-provider `account-platform-configs`; jobs write quota snapshots, wakeup jobs, quality snapshots, strategy events, and audit entries.
- Group model policy is an intersection with API-key model scope: deny wins, allow rows form an allow-list, and groups with no allow rows inherit global model visibility.
- Refunds that cannot reverse wallet credit are moved to `refund_blocked`; admins can retry after balance/reserved balance is corrected.
- `GET /risk-events` supports filtering by `action` and `user_id`.
- `GET /finance/summary` reports paid revenue, refunds, wallet liability, subscription MRR, pending affiliate rebates, usage cost, refund blocked count, and active subscriptions.

## Public API

Prefix: `/api/public/v1`

- `GET /pricing`
- `GET /channel-status`
- `GET /rankings`
- `GET /announcements`
- `GET /site-settings`
- `GET /pages`
- `GET /pages/:slug`

Public views are rendered inside `apps/portal`; there is no separate public frontend package.

## Billing Webhook API

Prefix: `/api/billing/v1`

- `POST /stripe/webhook`

Stripe webhooks are verified with `Stripe-Signature` when `STRIPE_WEBHOOK_SECRET` is configured. Without a secret, local smoke tests can post unsigned events.

## OAuth Wrapper API

Prefix: `/api/oauth-wrapper/v1`

- `POST /jobs/claim`
- `POST /jobs/:jobId/complete`
- `POST /jobs/:jobId/fail`

Authentication:

```http
Authorization: Bearer <OAUTH_WRAPPER_BEARER_TOKEN>
```

The wrapper owns provider-specific login, refresh, revoke, and reauth flows. The gateway owns job leasing, token bundle storage, account auth state, routing, settlement, and audit.

`POST /jobs/claim` returns one leased job or `{"job": null}`. Leased jobs include provider metadata, provider-client metadata, and wrapper-only secrets needed to execute the job:

```json
{
  "job": {
    "id": "uuid",
    "account_id": "uuid",
    "job_type": "refresh",
    "auth_mode": "oauth",
    "provider": { "id": "uuid", "name": "OpenAI", "provider_type": "openai_compatible" },
    "provider_client": {
      "id": "uuid",
      "client_type": "oauth_app",
      "credential": "client-secret-if-configured",
      "metadata": { "token_url": "https://provider.example/token" }
    },
    "payload": {},
    "token_bundle": {
      "type": "oauth",
      "access_token": "redacted",
      "refresh_token": "redacted"
    }
  }
}
```

`token_bundle` and `provider_client.credential` are only returned from wrapper-authenticated endpoints. They are not returned by admin, portal, route explain, usage, logs, or metrics.

Token bundles stored in `credential_vault_records` are normalized JSON:

```json
{
  "type": "oauth",
  "access_token": "redacted",
  "refresh_token": "redacted",
  "expires_at": "2026-05-05T10:00:00Z",
  "scopes": ["scope-a"],
  "provider": "openai",
  "subject": "provider-user-id"
}
```

Operations:

- `GET /api/admin/v1/ops/overview?time_range=24h` returns the built-in admin operations overview for readiness, traffic, latency, failures, account health, and event queues. Supported ranges are `1h`, `6h`, `24h`, and `7d`.

## Northbound API

Authentication:

```http
Authorization: Bearer <personal_api_key>
```

Endpoints:

- `GET /v1/models`
- `POST /v1/chat/completions`
- `POST /v1/responses`
- `POST /v1/messages`
- `POST /v1/embeddings`
- `POST /v1/images/generations`
- `POST /v1/audio/transcriptions`
- `POST /v1/audio/translations`
- `POST /v1/audio/speech`
- `POST /v1/realtime/sessions`
- `GET /v1/realtime?model=<model>` as WebSocket upgrade
- `POST /v1/rerank`

Required behavior:

- Only active personal API keys are accepted.
- Disabled users, revoked keys, expired keys, IP-blocked keys, and out-of-scope models are rejected before routing.
- Insufficient wallet balance returns `insufficient_balance`.
- Unsupported model/endpoint capability returns `unsupported_model_capability`.
- No valid upstream account returns `no_available_account`.
- Successful calls write usage and wallet ledger entries.
- `routing_mode=byo` requests select only the calling user's owned OAuth accounts, never fall back to pool, and record zero wallet cost.
- Optional route affinity uses `X-Elucid-Relay-Session`, `X-Relay-Session`, common conversation headers, `session_id`, `conversation_id`, or `thread_id` to keep the same personal API key/model/endpoint on the same upstream account while that account remains available.
- Usage rows include `usage_source`, `stream_event_count`, and `websocket_frame_count` for metering provenance.
