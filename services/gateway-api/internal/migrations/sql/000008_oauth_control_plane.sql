ALTER TABLE api_keys
  ADD COLUMN IF NOT EXISTS routing_mode text NOT NULL DEFAULT 'pool'
    CHECK (routing_mode IN ('pool', 'byo'));

ALTER TABLE accounts
  ADD COLUMN IF NOT EXISTS owner_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS routing_mode text NOT NULL DEFAULT 'pool'
    CHECK (routing_mode IN ('pool', 'byo'));

ALTER TABLE provider_clients
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  ADD COLUMN IF NOT EXISTS credential_vault_record_id uuid REFERENCES credential_vault_records(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS api_keys_routing_mode_idx
  ON api_keys(owner_id, routing_mode, status);

CREATE INDEX IF NOT EXISTS accounts_owner_routing_idx
  ON accounts(routing_mode, owner_user_id, status, priority);

CREATE TABLE IF NOT EXISTS account_auth_states (
  account_id uuid PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  auth_mode text NOT NULL,
  auth_status text NOT NULL DEFAULT 'pending'
    CHECK (auth_status IN ('pending', 'active', 'refresh_due', 'reauth_required', 'revoked', 'failed', 'disabled')),
  provider_subject text NOT NULL DEFAULT '',
  scopes_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  expires_at timestamptz,
  refresh_due_at timestamptz,
  last_refresh_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER account_auth_states_set_updated_at
BEFORE UPDATE ON account_auth_states
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS account_auth_states_status_refresh_idx
  ON account_auth_states(auth_status, refresh_due_at, expires_at);

CREATE INDEX IF NOT EXISTS account_auth_states_provider_subject_idx
  ON account_auth_states(provider_subject)
  WHERE provider_subject <> '';

CREATE TABLE IF NOT EXISTS oauth_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  provider_client_id uuid REFERENCES provider_clients(id) ON DELETE SET NULL,
  requested_by_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  job_type text NOT NULL
    CHECK (job_type IN ('onboarding', 'refresh', 'revoke', 'reauth')),
  auth_mode text NOT NULL,
  status text NOT NULL DEFAULT 'queued'
    CHECK (status IN ('queued', 'leased', 'succeeded', 'failed', 'canceled')),
  priority integer NOT NULL DEFAULT 100,
  idempotency_key text NOT NULL,
  lease_owner text NOT NULL DEFAULT '',
  leased_until timestamptz,
  attempt_count integer NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
  max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts > 0),
  payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  result_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz,
  UNIQUE (idempotency_key)
);

CREATE TRIGGER oauth_jobs_set_updated_at
BEFORE UPDATE ON oauth_jobs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS oauth_jobs_claim_idx
  ON oauth_jobs(status, priority, created_at)
  WHERE status IN ('queued', 'leased');

CREATE INDEX IF NOT EXISTS oauth_jobs_account_created_idx
  ON oauth_jobs(account_id, created_at DESC);
