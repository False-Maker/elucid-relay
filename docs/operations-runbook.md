# Operations Runbook

## Single-Node Production Start

1. Copy `.env.production.example` to `.env.production`.
2. Replace every secret and set `PORTAL_BASE_URL` plus `PUBLIC_GATEWAY_API_URL`.
3. Run migrations once:

```bash
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml run --rm gateway-api migrate up
```

4. Start the stack:

```bash
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml up -d --build
```

5. Check liveness/readiness:

```bash
curl -fsS http://localhost:18080/healthz
curl -fsS http://localhost:18080/readyz
```

Use `healthz` for process liveness and `readyz` for traffic readiness. A failed `readyz` should keep the instance out of rotation.

## Monitoring

Open the admin console `运维` page. It reads the built-in operations overview and shows readiness, request trend, latency buckets, error distribution, degraded accounts, recent failed requests, and event queues.

If the page has no data:

- Confirm `gateway-api` is healthy and `/readyz` returns `status=ok`.
- Confirm the selected time range contains rows in `usage_records`.
- Check account-pool, payment, notification, and risk tables from their matching admin pages if a specific section is empty.

## Backup And Restore

Run a backup:

```bash
BACKUP_DIR=./backups/postgres ./infra/backup-postgres.sh
```

Restore to the configured database:

```bash
CONFIRM_RESTORE=restore-elucid_relay ./infra/restore-postgres.sh ./backups/postgres/elucid-relay-elucid_relay-YYYYMMDDTHHMMSSZ.dump
```

Operational rules:

- Back up before every migration.
- Keep at least 14 daily backups by default.
- Sync backups off-host for any public deployment.
- Test restore on a non-production instance before relying on the backup policy.

## Release Gate

Default local gate:

```bash
npm run release:gate
```

Full local gate:

```bash
RUN_DOCKER=1 RUN_SMOKE=1 RUN_BROWSER_SMOKE=1 npm run release:gate
```

Set `PROD_ENV_FILE=.env.production` when validating a real production environment file instead of the example file.

Browser smoke uses Playwright and checks that the portal renders without blank screen or critical console/request failures. Set `SMOKE_ADMIN_EMAIL` and `SMOKE_ADMIN_PASSWORD` to also verify the admin shell and menu render.

## Add A Real Provider

1. Create a model in Admin with the endpoint capability you need.
2. Create a provider with the right `provider_type`.
3. Create a channel with `base_url` and ability rows.
4. Create an account with the upstream API key.
5. Create an account quota window.
6. Run route explain.
7. Create a personal user key and call `/v1/chat/completions`.

Minimum OpenAI-compatible test:

```bash
curl -fsS "$BASE_URL/v1/chat/completions" \
  -H "Authorization: Bearer $PERSONAL_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"YOUR_MODEL","messages":[{"role":"user","content":"ping"}]}'
```

## API Key Rotation

Provider account key:

1. Create or edit the upstream account in Admin.
2. Set the new `api_key`.
3. Run route explain for affected model/endpoint.
4. Run one northbound request.
5. Confirm a new `usage_records` row uses the account.

Vault key:

1. Stop writes.
2. Decrypt existing upstream credentials with the old `VAULT_KEY`.
3. Re-encrypt them with the new `VAULT_KEY`.
4. Deploy the new `VAULT_KEY`.
5. Run smoke and one real provider check.

## Wallet Accounting Checks

Reserved balance should match live reservations:

```sql
SELECT w.user_id, w.balance, w.reserved_balance
FROM wallet_accounts w
WHERE w.reserved_balance < 0 OR w.balance < w.reserved_balance;
```

Request ledger trail:

```sql
SELECT entry_type, amount, balance_after, reserved_after, metadata_json, created_at
FROM wallet_ledgers
WHERE reference_type = 'northbound_request'
  AND reference_id = '<request_id>'
ORDER BY created_at;
```

Usage and settlement snapshot:

```sql
SELECT request_id, status, actual_cost, usage_source, settlement_snapshot_json
FROM usage_records
WHERE request_id = '<request_id>';
```

Refund-blocked orders are business-blocked, not webhook failures. Inspect the order and wallet first:

```sql
SELECT id, user_id, status, amount_usd, refund_blocked_reason
FROM orders
WHERE status = 'refund_blocked'
ORDER BY updated_at DESC;
```

After correcting the user's available wallet balance or reserved balance, retry refund from Admin Billing. A successful retry moves the order to `refunded` and writes a `refund_reversal` ledger row.

## Spend Limits

User and API-key limits are enforced before upstream routing. Inspect configured limits:

```sql
SELECT target_type, target_id, daily_usd_limit, monthly_usd_limit,
       daily_request_limit, monthly_request_limit, low_balance_threshold, status
FROM spend_limits
ORDER BY updated_at DESC;
```

Use Admin Controls to set per-user or per-key limits. `NULL` means no limit for that dimension. `low_balance_threshold` applies to user wallet targets only.

## Notification Channels

Webhook channels are managed in Admin Controls. Events are stored before delivery, so failed dispatches can be inspected:

```sql
SELECT event_type, severity, title, target_type, target_id, status, attempts, last_error, created_at
FROM notification_events
ORDER BY created_at DESC
LIMIT 50;
```

Current built-in alert events include `account_circuit_open`, `oauth_job_failed`, `low_balance`, and `security_email_failed`.

Webhook deliveries include:

- `X-Elucid-Event-Id`: notification event id.
- `X-Elucid-Timestamp`: Unix timestamp used for signing.
- `X-Elucid-Signature`: `v1=<hex hmac sha256>` over `<timestamp>.<raw body>`.

The webhook signing secret is shown once when a channel is created or rotated. Store it in the receiver and reject stale timestamps. Channels created before signing support must be rotated before they can dispatch.

## Cooldown Recovery

Inspect cooldown accounts:

```sql
SELECT account_id, active_requests, cooldown_until, circuit_state,
       circuit_failure_count, circuit_opened_at, circuit_half_open_after,
       last_error, failure_count, last_failure_at
FROM account_runtime_states
WHERE cooldown_until > now() OR circuit_state <> 'closed'
ORDER BY cooldown_until DESC NULLS LAST;
```

Manual recovery:

```sql
UPDATE account_runtime_states
SET cooldown_until = NULL,
    circuit_state = 'closed',
    circuit_failure_count = 0,
    circuit_opened_at = NULL,
    circuit_half_open_after = NULL,
    last_error = '',
    updated_at = now()
WHERE account_id = '<account_id>';
```

Use this only after the upstream key, quota, proxy, or provider incident is fixed.

## Account Pool Strategy Events

Automatic quality isolation and manual quality actions write strategy events:

```sql
SELECT account_id, event_type, action, previous_status, next_status,
       decision, quality_score, reason, created_at
FROM account_pool_strategy_events
ORDER BY created_at DESC
LIMIT 50;
```

Use these events before re-enabling an account so the operator can see whether the state change came from quality scoring, a manual action, or recovery work.

## OAuth Account Operations

Wrapper setup:

1. Set `OAUTH_WRAPPER_BEARER_TOKEN` on the gateway and wrapper.
2. Register provider client metadata in Admin when a provider needs app/client configuration.
3. Run the wrapper out of process. It should claim jobs, perform provider-specific auth, and complete or fail jobs.

Local Docker run:

```bash
docker compose --profile oauth-wrapper up --build oauth-wrapper
```

One-shot local worker:

```bash
GATEWAY_BASE_URL=http://localhost:18080 \
OAUTH_WRAPPER_BEARER_TOKEN=local-oauth-wrapper-token \
npm run oauth-wrapper -- once
```

Codex CLI local smoke after `codex login`:

```bash
BASE_URL=http://localhost:18080 \
OAUTH_WRAPPER_BEARER_TOKEN=local-oauth-wrapper-token \
CODEX_AUTH_FILE="$HOME/.codex/auth.json" \
node infra/codex-cli-smoke.mjs
```

Real Codex OAuth to OpenAI-compatible upstream E2E. Run through Docker so the gateway and the runner share the compose network, and mount only the Codex auth file:

```bash
docker compose up -d gateway-api

docker run --rm --network elucid-relay_default \
  -v "$PWD:/work:ro" \
  -v "$HOME/.codex/auth.json:/codex-auth/auth.json:ro" \
  -w /work \
  -e BASE_URL=http://gateway-api:8080 \
  -e OAUTH_WRAPPER_BEARER_TOKEN=local-oauth-wrapper-token \
  -e CODEX_AUTH_FILE=/codex-auth/auth.json \
  -e E2E_CODEX_OPENAI=1 \
  -e E2E_CODEX_OPENAI_MODEL=gpt-4.1-mini \
  node:22-alpine node infra/codex-openai-e2e.mjs
```

For ChatGPT/Codex accounts without OpenAI API platform quota, add `-e E2E_CODEX_OPENAI_ALLOW_QUOTA_ERROR=1`. That mode still verifies Codex OAuth onboarding, vault storage, active BYO routing, gateway injection, zero Relay wallet cost, and that the real upstream classified the credential as `insufficient_quota` rather than invalid auth.

Real Gemini CLI / Code Assist OAuth E2E. This runner is opt-in and does not launch Google login. It only uses an existing access token, Gemini CLI OAuth file, or Google ADC file, then sends one non-stream and one stream request through BYO routing:

```bash
BASE_URL=http://localhost:18080 \
E2E_GEMINI_CODEASSIST=1 \
E2E_GEMINI_CODEASSIST_ACCESS_TOKEN="$GOOGLE_CLOUD_ACCESS_TOKEN" \
node infra/gemini-codeassist-e2e.mjs
```

Alternative credential files:

```bash
GEMINI_OAUTH_CREDS_FILE="$HOME/.gemini/oauth_creds.json" \
E2E_GEMINI_CODEASSIST_ALLOW_REFRESH=1 \
E2E_GEMINI_CODEASSIST=1 \
node infra/gemini-codeassist-e2e.mjs
```

When `HTTP_PROXY`, `HTTPS_PROXY`, or `ALL_PROXY` is set, the runner automatically restarts itself with `NODE_USE_ENV_PROXY=1` so Node `fetch` uses the local proxy. Set `E2E_DISABLE_AUTO_ENV_PROXY=1` to disable that behavior.

The runner calls `v1internal:loadCodeAssist` before chat to discover `cloudaicompanionProject` and injects the discovered project into provider metadata. Set `E2E_GEMINI_CODEASSIST_PROJECT` only to override discovery. Set `E2E_GEMINI_CODEASSIST_ALLOW_ONBOARD=1` only when you explicitly want the runner to call `onboardUser` if `loadCodeAssist` returns no project. Set `E2E_GEMINI_CODEASSIST_ALLOW_EXPECTED_AUTH_ERROR=1` to accept a low-frequency `401/403/429` as a partial proof that the credential reached Google but was rejected by account/quota/validation policy. `cloud_code_private_api_disabled` means the selected project has not enabled `cloudcode-pa.googleapis.com`.

Real GitHub Copilot OAuth E2E. This runner is opt-in and does not launch GitHub login or read VS Code keychains. It uses an existing Copilot API token, or explicitly exchanges an existing GitHub OAuth token when `E2E_GITHUB_COPILOT_ALLOW_TOKEN_EXCHANGE=1`, then sends one non-stream and one stream request through BYO routing:

```bash
BASE_URL=http://localhost:18080 \
E2E_GITHUB_COPILOT=1 \
E2E_GITHUB_COPILOT_ACCESS_TOKEN="copilot-api-token" \
node infra/github-copilot-e2e.mjs
```

Optional GitHub OAuth token exchange:

```bash
BASE_URL=http://localhost:18080 \
E2E_GITHUB_COPILOT=1 \
E2E_GITHUB_COPILOT_GITHUB_TOKEN="github-oauth-token" \
E2E_GITHUB_COPILOT_ALLOW_TOKEN_EXCHANGE=1 \
node infra/github-copilot-e2e.mjs
```

Set `E2E_GITHUB_COPILOT_ACCOUNT_TYPE=business` or `enterprise` for subscription-specific Copilot base URLs. Set `E2E_GITHUB_COPILOT_ALLOW_EXPECTED_AUTH_ERROR=1` to accept a low-frequency `401/403/429` as a partial proof that the credential reached Copilot but was rejected by account/quota policy.

Antigravity OAuth uses the Google PKCE wrapper with Antigravity client defaults when the provider type or metadata targets `antigravity`. Do not launch a new Google login from automation unless explicitly intended; prefer using an already authorized token bundle for real-provider smoke.

Kiro accounts require a valid Kiro Desktop or Builder ID/AWS SSO OIDC refresh token. Queue refresh with `auth_mode=kiro` or `auth_mode=oauth` plus `wrapper_strategy=kiro`; the wrapper can read a configured `credentials_file`, `token_file`, or `sqlite_file`, calls `https://prod.<region>.auth.desktop.kiro.dev/refreshToken` for desktop credentials, and calls `https://oidc.<region>.amazonaws.com/token` with JSON camelCase client-registration fields for Builder ID/AWS SSO OIDC credentials.

Windsurf/Codeium local import uses `auth_mode=windsurf_cli` and reads a configured `config_file`, `token_file`, or common Codeium/Windsurf config path containing `api_key`. Imported metadata includes the Codeium API server URL, language-server version, and chat-client query fields used by open-source Windsurf/Codeium clients.

Claim loop:

```bash
curl -fsS "$BASE_URL/api/oauth-wrapper/v1/jobs/claim" \
  -H "Authorization: Bearer $OAUTH_WRAPPER_BEARER_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"lease_owner":"worker-1","supported_modes":["openai_cli","claude_cli","google_pkce","github_device","oauth","kiro","windsurf_cli","codeium_cli"]}'
```

Health checks:

- Admin OAuth jobs should not remain `leased` past `leased_until`.
- `refresh_due` should drain into `active` or `reauth_required`.
- `revoked`, `failed`, `pending`, and `reauth_required` accounts are excluded from routing.
- Route explain reports `auth_status`, `auth_expires_at`, `refresh_due_at`, `routing_mode`, `owner_scope`, and `excluded_reasons`.
- `infra/oauth-wrapper-smoke.mjs` verifies wrapper claim/complete, BYO routing, zero wallet cost, and no pool fallback against the local mock upstream.

BYO rules:

- Portal-created BYO accounts are owned by exactly one personal user.
- Personal API keys with `routing_mode=byo` only use that user's OAuth accounts.
- BYO requests never fall back to pool and settle at zero Relay wallet cost.
- Revoking a BYO account disables the account, marks auth state `revoked`, and queues a revoke job for the wrapper.

## Quota Window Guidance

- `window_type=requests` or `request` decrements once per completed upstream attempt.
- `remaining` blocks routing when active and `<= 0`.
- `metadata_json.limit` improves headroom scoring.
- Set `reset_at` to the upstream quota reset time when known.

Example:

```json
{
  "account_id": "...",
  "window_type": "requests",
  "remaining": "1000",
  "reset_at": "2026-05-05T00:00:00Z",
  "metadata": { "limit": 1000 }
}
```

## Debug Common Failures

`401`:

- Check personal API key status, expiry, hash, and owner status.
- Check upstream account key if the 401 is returned by the provider.

`429`:

- Account is cooled down for retryable upstream throttling.
- Check upstream quota and `account_quota_windows.remaining`.

`5xx`:

- Check the admin `运维` page or `/api/admin/v1/ops/overview?time_range=1h`.
- Check account `last_error`.
- Confirm upstream base URL, proxy, DNS, and timeout.

No route:

- Run `/api/admin/v1/runtime/route-explain?model=<model>&endpoint=<endpoint>`.
- Check `excluded_reasons`, `headroom_score`, `failure_penalty`, cooldown, and quota exhaustion.

## Real Provider E2E

```bash
npm run e2e:providers
E2E_OPENAI_API_KEY=... node infra/provider-e2e/runner.mjs --provider openai
E2E_ANTHROPIC_API_KEY=... node infra/provider-e2e/runner.mjs --provider anthropic
E2E_GEMINI_API_KEY=... node infra/provider-e2e/runner.mjs --provider gemini-openai-compatible
```

Each run creates isolated provider/channel/account/model/user/key resources with a unique prefix.

## Stripe Test-Mode E2E

```bash
E2E_STRIPE_TESTMODE=1 \
STRIPE_SECRET_KEY=sk_test_... \
STRIPE_WEBHOOK_SECRET=whsec_... \
npm run e2e:stripe
```

The gateway must be running with the same `STRIPE_SECRET_KEY` and `STRIPE_WEBHOOK_SECRET`. The check creates a wallet top-up order, creates a real Stripe test-mode Checkout Session, posts a signed webhook event, replays the same event to verify idempotency, and reads the order back as `paid`.
