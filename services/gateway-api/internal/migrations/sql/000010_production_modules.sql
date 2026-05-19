ALTER TABLE users
  ADD COLUMN IF NOT EXISTS email_verified_at timestamptz,
  ADD COLUMN IF NOT EXISTS password_changed_at timestamptz;

CREATE TABLE IF NOT EXISTS user_security_tokens (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_type text NOT NULL CHECK (token_type IN ('email_verification', 'password_reset')),
  token_hash text NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS user_security_tokens_lookup_idx
  ON user_security_tokens(token_type, token_hash)
  WHERE consumed_at IS NULL;

CREATE INDEX IF NOT EXISTS user_security_tokens_user_idx
  ON user_security_tokens(user_id, token_type, created_at DESC);

CREATE TABLE IF NOT EXISTS spend_limits (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  target_type text NOT NULL CHECK (target_type IN ('user', 'api_key')),
  target_id uuid NOT NULL,
  daily_usd_limit numeric(20,10) CHECK (daily_usd_limit IS NULL OR daily_usd_limit >= 0),
  monthly_usd_limit numeric(20,10) CHECK (monthly_usd_limit IS NULL OR monthly_usd_limit >= 0),
  daily_request_limit integer CHECK (daily_request_limit IS NULL OR daily_request_limit >= 0),
  monthly_request_limit integer CHECK (monthly_request_limit IS NULL OR monthly_request_limit >= 0),
  low_balance_threshold numeric(20,10) CHECK (low_balance_threshold IS NULL OR low_balance_threshold >= 0),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (target_type, target_id)
);

CREATE TRIGGER spend_limits_set_updated_at
BEFORE UPDATE ON spend_limits
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS spend_limits_target_idx
  ON spend_limits(target_type, target_id, status);

CREATE TABLE IF NOT EXISTS notification_channels (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  channel_type text NOT NULL DEFAULT 'webhook' CHECK (channel_type IN ('webhook')),
  target_url_ciphertext bytea NOT NULL,
  target_url_nonce bytea NOT NULL,
  min_severity text NOT NULL DEFAULT 'warning' CHECK (min_severity IN ('info', 'warning', 'critical')),
  event_types_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER notification_channels_set_updated_at
BEFORE UPDATE ON notification_channels
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS notification_channels_active_idx
  ON notification_channels(status, min_severity);

CREATE TABLE IF NOT EXISTS notification_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  event_type text NOT NULL,
  severity text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
  title text NOT NULL,
  message text NOT NULL DEFAULT '',
  target_type text NOT NULL DEFAULT '',
  target_id text NOT NULL DEFAULT '',
  payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'sent', 'failed', 'suppressed')),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  sent_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER notification_events_set_updated_at
BEFORE UPDATE ON notification_events
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS notification_events_pending_idx
  ON notification_events(status, next_attempt_at, created_at)
  WHERE status = 'pending';

CREATE INDEX IF NOT EXISTS notification_events_created_idx
  ON notification_events(created_at DESC);
