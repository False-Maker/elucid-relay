# Database Schema

## Core Tables

### users

- `id`
- `user_type`: `personal_user`, `operator`, `platform_owner`
- `email`
- `password_hash`
- `display_name`
- `status`: `active`, `disabled`, `pending`
- `last_login_at`
- timestamps

### user_sessions

- `session_id`
- `user_id`
- `audience`: `portal`, `admin`
- `csrf_token`
- `expires_at`
- `revoked_at`

### wallet_accounts

- `id`
- `user_id`
- `balance numeric(20,10)`
- `reserved_balance numeric(20,10)`
- `currency`: `USD`
- `status`
- timestamps

### wallet_ledgers

- `id`
- `wallet_account_id`
- `entry_type`: `credit`, `debit`, `reserve`, `release`, `adjustment`, `redeem`
- `amount`
- `balance_after`
- `reserved_after`
- `reference_type`
- `reference_id`
- `metadata_json`
- `created_at`

### redeem_codes

- `id`
- `code`
- `grant_value`
- `currency`
- `max_claims`
- `claim_count`
- `expires_at`
- `status`
- `metadata_json`
- timestamps

### redeem_claims

- `id`
- `redeem_code_id`
- `user_id`
- `wallet_ledger_id`
- `claimed_at`

### api_keys

- `id`
- `owner_type`: `personal_user`
- `owner_id`: user id
- `key_hash`
- `display_prefix`
- `name`
- `status`
- `expires_at`
- `ip_allowlist_json`
- `model_scope_json`
- timestamps

### usage_records

- `id`
- `request_id`
- `request_fingerprint`
- `user_id`
- `api_key_id`
- `channel_id`
- `account_id`
- `requested_model`
- `upstream_model`
- `endpoint`
- token/request/image/audio counts
- cost fields
- status/error fields
- `upstream_status`
- `duration_ms`
- `usage_source`: `provider_usage`, `stream_parsed`, `websocket_parsed`, or `estimated_fallback`
- `stream_event_count`
- `websocket_frame_count`
- `metering_metadata_json`
- pricing and settlement snapshots
- `created_at`

### northbound_route_affinities

- `user_id`
- `api_key_id`
- `model_name`
- `endpoint`
- `session_key_hash`
- `channel_id`
- `account_id`
- `last_seen_at`
- `expires_at`

## Operations Tables

- `model_catalog`
- `model_aliases`
- `providers`
- `provider_clients`
- `channels`
- `channel_abilities`
- `accounts`
- `account_runtime_states`
- `account_quota_windows`
- `proxies`
- `credential_vault_records`
- `audit_logs`

## Removed Concepts

Do not introduce these in v1:

- organizations as user-visible tenancy.
- projects.
- project_memberships.
- applications.
- enterprise registration requests.
- enterprise identity providers.
