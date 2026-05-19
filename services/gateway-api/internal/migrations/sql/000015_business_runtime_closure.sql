ALTER TABLE groups
  ADD COLUMN IF NOT EXISTS priority integer NOT NULL DEFAULT 100;

CREATE INDEX IF NOT EXISTS groups_effective_policy_idx
  ON groups(status, priority, created_at);

ALTER TABLE group_model_permissions
  ADD COLUMN IF NOT EXISTS price_multiplier numeric(20,10) NOT NULL DEFAULT 1 CHECK (price_multiplier >= 0);

ALTER TABLE usage_records
  ADD COLUMN IF NOT EXISTS group_id uuid REFERENCES groups(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS billing_multiplier numeric(20,10) NOT NULL DEFAULT 1 CHECK (billing_multiplier >= 0),
  ADD COLUMN IF NOT EXISTS effective_policy_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  ADD COLUMN IF NOT EXISTS risk_decision_json jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS usage_records_group_created_idx
  ON usage_records(group_id, created_at DESC);

ALTER TABLE subscription_plans
  ADD COLUMN IF NOT EXISTS group_id uuid REFERENCES groups(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS stripe_price_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS features_json jsonb NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE user_subscriptions
  ADD COLUMN IF NOT EXISTS stripe_subscription_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS granted_group_id uuid REFERENCES groups(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS order_id uuid REFERENCES orders(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS current_period_start timestamptz,
  ADD COLUMN IF NOT EXISTS current_period_end timestamptz;

CREATE INDEX IF NOT EXISTS user_subscriptions_stripe_idx
  ON user_subscriptions(stripe_subscription_id)
  WHERE stripe_subscription_id <> '';

ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS order_type text NOT NULL DEFAULT 'wallet_topup',
  ADD COLUMN IF NOT EXISTS stripe_checkout_session_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS stripe_payment_intent_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS stripe_subscription_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS stripe_refund_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS checkout_url text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS paid_at timestamptz,
  ADD COLUMN IF NOT EXISTS refunded_at timestamptz,
  ADD COLUMN IF NOT EXISTS refund_blocked_reason text NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS orders_stripe_checkout_session_idx
  ON orders(stripe_checkout_session_id)
  WHERE stripe_checkout_session_id <> '';

CREATE INDEX IF NOT EXISTS orders_stripe_payment_intent_idx
  ON orders(stripe_payment_intent_id)
  WHERE stripe_payment_intent_id <> '';

CREATE INDEX IF NOT EXISTS orders_status_created_idx
  ON orders(status, created_at DESC);

ALTER TABLE payment_events
  ADD COLUMN IF NOT EXISTS processed_at timestamptz,
  ADD COLUMN IF NOT EXISTS processing_error text NOT NULL DEFAULT '';

CREATE UNIQUE INDEX IF NOT EXISTS payment_events_provider_event_unique_idx
  ON payment_events(provider, provider_event_id)
  WHERE provider <> '' AND provider_event_id <> '';

CREATE TABLE IF NOT EXISTS risk_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  api_key_id uuid REFERENCES api_keys(id) ON DELETE SET NULL,
  rule_id uuid REFERENCES risk_rules(id) ON DELETE SET NULL,
  request_id text NOT NULL DEFAULT '',
  rule_type text NOT NULL DEFAULT '',
  action text NOT NULL CHECK (action IN ('flag', 'block', 'throttle')),
  severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'critical')),
  target text NOT NULL DEFAULT '',
  matched_value text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS risk_events_created_idx
  ON risk_events(created_at DESC);

CREATE INDEX IF NOT EXISTS risk_events_user_created_idx
  ON risk_events(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS model_sync_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  channel_id uuid REFERENCES channels(id) ON DELETE SET NULL,
  provider_id uuid REFERENCES providers(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'success', 'failed')),
  requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
  discovered_count integer NOT NULL DEFAULT 0 CHECK (discovered_count >= 0),
  updated_count integer NOT NULL DEFAULT 0 CHECK (updated_count >= 0),
  error_message text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS model_sync_jobs_created_idx
  ON model_sync_jobs(created_at DESC);

CREATE INDEX IF NOT EXISTS model_sync_jobs_channel_idx
  ON model_sync_jobs(channel_id, created_at DESC);

CREATE TABLE IF NOT EXISTS affiliate_attributions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  affiliate_code_id uuid NOT NULL REFERENCES affiliate_codes(id) ON DELETE CASCADE,
  order_id uuid REFERENCES orders(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'converted', 'canceled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (user_id, affiliate_code_id)
);

CREATE TRIGGER affiliate_attributions_set_updated_at
BEFORE UPDATE ON affiliate_attributions
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS affiliate_attributions_user_idx
  ON affiliate_attributions(user_id, status, created_at DESC);

ALTER TABLE affiliate_rebates
  ADD COLUMN IF NOT EXISTS wallet_ledger_id uuid REFERENCES wallet_ledgers(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS settled_at timestamptz;

ALTER TABLE affiliate_codes
  ADD COLUMN IF NOT EXISTS rebate_rate numeric(10,6) NOT NULL DEFAULT 0.100000 CHECK (rebate_rate >= 0 AND rebate_rate <= 1);

ALTER TABLE wallet_ledgers
  DROP CONSTRAINT IF EXISTS wallet_ledgers_entry_type_check;

ALTER TABLE wallet_ledgers
  ADD CONSTRAINT wallet_ledgers_entry_type_check
  CHECK (entry_type IN (
    'credit', 'debit', 'reserve', 'release', 'adjustment', 'redeem',
    'payment', 'subscription_credit', 'refund_reversal', 'affiliate_rebate'
  ));
