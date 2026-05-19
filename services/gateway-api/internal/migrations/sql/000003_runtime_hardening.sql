ALTER TABLE channels
  ADD COLUMN IF NOT EXISTS proxy_id uuid REFERENCES proxies(id) ON DELETE SET NULL;

ALTER TABLE accounts
  ADD COLUMN IF NOT EXISTS proxy_id uuid REFERENCES proxies(id) ON DELETE SET NULL;

ALTER TABLE provider_clients
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  ADD COLUMN IF NOT EXISTS credential_vault_record_id uuid REFERENCES credential_vault_records(id) ON DELETE SET NULL;

ALTER TABLE account_runtime_states
  ADD COLUMN IF NOT EXISTS success_count bigint NOT NULL DEFAULT 0 CHECK (success_count >= 0),
  ADD COLUMN IF NOT EXISTS failure_count bigint NOT NULL DEFAULT 0 CHECK (failure_count >= 0),
  ADD COLUMN IF NOT EXISTS last_success_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_failure_at timestamptz;

ALTER TABLE usage_records
  ADD COLUMN IF NOT EXISTS upstream_status integer,
  ADD COLUMN IF NOT EXISTS duration_ms integer NOT NULL DEFAULT 0 CHECK (duration_ms >= 0);

CREATE TABLE IF NOT EXISTS northbound_idempotency_keys (
  request_id text NOT NULL,
  api_key_id uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  request_fingerprint text NOT NULL,
  status text NOT NULL DEFAULT 'processing' CHECK (status IN ('processing', 'success', 'failed', 'rejected')),
  created_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  expires_at timestamptz NOT NULL DEFAULT now() + interval '24 hours',
  PRIMARY KEY (request_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS northbound_idempotency_expires_idx
  ON northbound_idempotency_keys(expires_at);

CREATE INDEX IF NOT EXISTS channels_proxy_idx
  ON channels(proxy_id);

CREATE INDEX IF NOT EXISTS accounts_proxy_idx
  ON accounts(proxy_id);

CREATE INDEX IF NOT EXISTS account_runtime_available_idx
  ON account_runtime_states(cooldown_until, active_requests);

CREATE INDEX IF NOT EXISTS account_quota_windows_runtime_idx
  ON account_quota_windows(account_id, window_type, reset_at);

INSERT INTO account_runtime_states (account_id)
SELECT id FROM accounts
ON CONFLICT (account_id) DO NOTHING;
