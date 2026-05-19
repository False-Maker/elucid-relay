CREATE EXTENSION IF NOT EXISTS citext;

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE IF NOT EXISTS users (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_type text NOT NULL CHECK (user_type IN ('personal_user', 'operator', 'platform_owner')),
  email citext NOT NULL UNIQUE,
  password_hash text NOT NULL,
  display_name text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'pending')),
  last_login_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER users_set_updated_at
BEFORE UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS user_sessions (
  session_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  audience text NOT NULL CHECK (audience IN ('portal', 'admin')),
  token_hash text NOT NULL UNIQUE,
  csrf_token text NOT NULL,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS user_sessions_user_id_idx ON user_sessions(user_id);
CREATE INDEX IF NOT EXISTS user_sessions_active_idx ON user_sessions(token_hash, audience)
WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS wallet_accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL UNIQUE REFERENCES users(id) ON DELETE CASCADE,
  balance numeric(20,10) NOT NULL DEFAULT 0 CHECK (balance >= 0),
  reserved_balance numeric(20,10) NOT NULL DEFAULT 0 CHECK (reserved_balance >= 0),
  currency text NOT NULL DEFAULT 'USD' CHECK (currency = 'USD'),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (reserved_balance <= balance)
);

CREATE TRIGGER wallet_accounts_set_updated_at
BEFORE UPDATE ON wallet_accounts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS wallet_ledgers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  wallet_account_id uuid NOT NULL REFERENCES wallet_accounts(id) ON DELETE CASCADE,
  entry_type text NOT NULL CHECK (entry_type IN ('credit', 'debit', 'reserve', 'release', 'adjustment', 'redeem')),
  amount numeric(20,10) NOT NULL CHECK (amount >= 0),
  balance_after numeric(20,10) NOT NULL,
  reserved_after numeric(20,10) NOT NULL,
  reference_type text NOT NULL DEFAULT '',
  reference_id text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS wallet_ledgers_wallet_created_idx ON wallet_ledgers(wallet_account_id, created_at DESC);
CREATE INDEX IF NOT EXISTS wallet_ledgers_reference_idx ON wallet_ledgers(reference_type, reference_id);

CREATE TABLE IF NOT EXISTS redeem_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  code_hash text NOT NULL UNIQUE,
  display_prefix text NOT NULL,
  grant_value numeric(20,10) NOT NULL CHECK (grant_value > 0),
  currency text NOT NULL DEFAULT 'USD' CHECK (currency = 'USD'),
  max_claims integer NOT NULL DEFAULT 1 CHECK (max_claims > 0),
  claim_count integer NOT NULL DEFAULT 0 CHECK (claim_count >= 0),
  expires_at timestamptz,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'expired')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (claim_count <= max_claims)
);

CREATE TRIGGER redeem_codes_set_updated_at
BEFORE UPDATE ON redeem_codes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS redeem_claims (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  redeem_code_id uuid NOT NULL REFERENCES redeem_codes(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  wallet_ledger_id uuid NOT NULL REFERENCES wallet_ledgers(id) ON DELETE RESTRICT,
  claimed_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (redeem_code_id, user_id)
);

CREATE TABLE IF NOT EXISTS api_keys (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_type text NOT NULL DEFAULT 'personal_user' CHECK (owner_type = 'personal_user'),
  owner_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  key_hash text NOT NULL UNIQUE,
  display_prefix text NOT NULL,
  name text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'revoked')),
  expires_at timestamptz,
  ip_allowlist_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  model_scope_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  last_used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER api_keys_set_updated_at
BEFORE UPDATE ON api_keys
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS api_keys_owner_idx ON api_keys(owner_id, created_at DESC);
CREATE INDEX IF NOT EXISTS api_keys_active_hash_idx ON api_keys(key_hash)
WHERE status = 'active';

CREATE TABLE IF NOT EXISTS model_catalog (
  model_name text PRIMARY KEY,
  display_name text NOT NULL DEFAULT '',
  provider_hint text NOT NULL DEFAULT '',
  endpoint_capabilities jsonb NOT NULL DEFAULT '[]'::jsonb,
  input_usd_per_1k numeric(20,10) NOT NULL DEFAULT 0 CHECK (input_usd_per_1k >= 0),
  output_usd_per_1k numeric(20,10) NOT NULL DEFAULT 0 CHECK (output_usd_per_1k >= 0),
  request_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (request_usd >= 0),
  min_charge_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (min_charge_usd >= 0),
  public_visible boolean NOT NULL DEFAULT true,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER model_catalog_set_updated_at
BEFORE UPDATE ON model_catalog
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS model_aliases (
  alias text PRIMARY KEY,
  model_name text NOT NULL REFERENCES model_catalog(model_name) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS providers (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  provider_type text NOT NULL DEFAULT 'openai_compatible',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER providers_set_updated_at
BEFORE UPDATE ON providers
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS provider_clients (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  name text NOT NULL,
  client_type text NOT NULL DEFAULT 'api_key',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER provider_clients_set_updated_at
BEFORE UPDATE ON provider_clients
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS channels (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  name text NOT NULL,
  base_url text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'cooldown')),
  priority integer NOT NULL DEFAULT 100,
  weight integer NOT NULL DEFAULT 1 CHECK (weight > 0),
  timeout_seconds integer NOT NULL DEFAULT 120 CHECK (timeout_seconds > 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER channels_set_updated_at
BEFORE UPDATE ON channels
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS channels_status_priority_idx ON channels(status, priority, weight);

CREATE TABLE IF NOT EXISTS channel_abilities (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  channel_id uuid NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  model_name text NOT NULL REFERENCES model_catalog(model_name) ON DELETE CASCADE,
  endpoint text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (channel_id, model_name, endpoint)
);

CREATE INDEX IF NOT EXISTS channel_abilities_lookup_idx ON channel_abilities(model_name, endpoint, status);

CREATE TABLE IF NOT EXISTS credential_vault_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  secret_ciphertext bytea NOT NULL,
  secret_nonce bytea NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS accounts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid NOT NULL REFERENCES providers(id) ON DELETE CASCADE,
  channel_id uuid REFERENCES channels(id) ON DELETE SET NULL,
  credential_vault_record_id uuid REFERENCES credential_vault_records(id) ON DELETE SET NULL,
  name text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'cooldown', 'exhausted')),
  priority integer NOT NULL DEFAULT 100,
  max_concurrency integer NOT NULL DEFAULT 10 CHECK (max_concurrency > 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER accounts_set_updated_at
BEFORE UPDATE ON accounts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS accounts_route_idx ON accounts(channel_id, status, priority);

CREATE TABLE IF NOT EXISTS account_runtime_states (
  account_id uuid PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
  active_requests integer NOT NULL DEFAULT 0 CHECK (active_requests >= 0),
  cooldown_until timestamptz,
  last_error text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS account_quota_windows (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  window_type text NOT NULL,
  reset_at timestamptz,
  remaining numeric(20,10),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER account_quota_windows_set_updated_at
BEFORE UPDATE ON account_quota_windows
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS proxies (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  proxy_url text NOT NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER proxies_set_updated_at
BEFORE UPDATE ON proxies
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS usage_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  request_id text NOT NULL UNIQUE,
  request_fingerprint text NOT NULL DEFAULT '',
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  api_key_id uuid REFERENCES api_keys(id) ON DELETE SET NULL,
  channel_id uuid REFERENCES channels(id) ON DELETE SET NULL,
  account_id uuid REFERENCES accounts(id) ON DELETE SET NULL,
  requested_model text NOT NULL DEFAULT '',
  upstream_model text NOT NULL DEFAULT '',
  endpoint text NOT NULL,
  input_tokens integer NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  output_tokens integer NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  image_count integer NOT NULL DEFAULT 0 CHECK (image_count >= 0),
  audio_seconds numeric(20,10) NOT NULL DEFAULT 0 CHECK (audio_seconds >= 0),
  request_count integer NOT NULL DEFAULT 1 CHECK (request_count >= 0),
  estimated_cost numeric(20,10) NOT NULL DEFAULT 0 CHECK (estimated_cost >= 0),
  actual_cost numeric(20,10) NOT NULL DEFAULT 0 CHECK (actual_cost >= 0),
  status text NOT NULL CHECK (status IN ('success', 'failed', 'rejected')),
  error_code text NOT NULL DEFAULT '',
  error_message text NOT NULL DEFAULT '',
  pricing_snapshot_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  settlement_snapshot_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS usage_records_user_created_idx ON usage_records(user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS usage_records_key_created_idx ON usage_records(api_key_id, created_at DESC);

CREATE TABLE IF NOT EXISTS audit_logs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  actor_type text NOT NULL DEFAULT 'system',
  action text NOT NULL,
  target_type text NOT NULL DEFAULT '',
  target_id text NOT NULL DEFAULT '',
  ip_address text NOT NULL DEFAULT '',
  user_agent text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_logs_created_idx ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS audit_logs_actor_idx ON audit_logs(actor_user_id, created_at DESC);
