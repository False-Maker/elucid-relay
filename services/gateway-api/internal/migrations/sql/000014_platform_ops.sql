CREATE TABLE IF NOT EXISTS proxy_test_results (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  proxy_id uuid NOT NULL REFERENCES proxies(id) ON DELETE CASCADE,
  test_type text NOT NULL DEFAULT 'connectivity' CHECK (test_type IN ('connectivity', 'quality')),
  target_url text NOT NULL DEFAULT '',
  status text NOT NULL CHECK (status IN ('success', 'failed')),
  latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
  error_message text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  tested_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS proxy_test_results_proxy_idx
  ON proxy_test_results(proxy_id, tested_at DESC);

CREATE TABLE IF NOT EXISTS channel_test_results (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_id uuid REFERENCES providers(id) ON DELETE SET NULL,
  channel_id uuid REFERENCES channels(id) ON DELETE CASCADE,
  account_id uuid REFERENCES accounts(id) ON DELETE SET NULL,
  test_type text NOT NULL DEFAULT 'health',
  model_name text NOT NULL DEFAULT '',
  endpoint text NOT NULL DEFAULT '',
  status text NOT NULL CHECK (status IN ('success', 'failed', 'skipped')),
  latency_ms integer NOT NULL DEFAULT 0 CHECK (latency_ms >= 0),
  upstream_status integer,
  error_message text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  tested_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS channel_test_results_channel_idx
  ON channel_test_results(channel_id, tested_at DESC);

CREATE INDEX IF NOT EXISTS channel_test_results_account_idx
  ON channel_test_results(account_id, tested_at DESC);

CREATE TABLE IF NOT EXISTS announcements (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  title text NOT NULL,
  body text NOT NULL DEFAULT '',
  audience text NOT NULL DEFAULT 'all' CHECK (audience IN ('all', 'portal', 'admin')),
  severity text NOT NULL DEFAULT 'info' CHECK (severity IN ('info', 'warning', 'critical')),
  starts_at timestamptz,
  ends_at timestamptz,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER announcements_set_updated_at
BEFORE UPDATE ON announcements
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS announcements_public_idx
  ON announcements(status, audience, starts_at, ends_at);

CREATE TABLE IF NOT EXISTS content_pages (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug text NOT NULL UNIQUE,
  title text NOT NULL,
  body text NOT NULL DEFAULT '',
  page_type text NOT NULL DEFAULT 'custom' CHECK (page_type IN ('custom', 'faq', 'api_info', 'legal', 'about', 'privacy', 'terms')),
  public_visible boolean NOT NULL DEFAULT false,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published', 'archived')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER content_pages_set_updated_at
BEFORE UPDATE ON content_pages
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS content_pages_public_idx
  ON content_pages(public_visible, status, page_type);

CREATE TABLE IF NOT EXISTS groups (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  model_multiplier numeric(20,10) NOT NULL DEFAULT 1 CHECK (model_multiplier >= 0),
  rpm_limit integer CHECK (rpm_limit IS NULL OR rpm_limit >= 0),
  monthly_usd_limit numeric(20,10) CHECK (monthly_usd_limit IS NULL OR monthly_usd_limit >= 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER groups_set_updated_at
BEFORE UPDATE ON groups
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS user_group_memberships (
  group_id uuid NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role text NOT NULL DEFAULT 'member',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, user_id)
);

CREATE INDEX IF NOT EXISTS user_group_memberships_user_idx
  ON user_group_memberships(user_id);

CREATE TABLE IF NOT EXISTS group_model_permissions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  group_id uuid NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
  model_name text NOT NULL REFERENCES model_catalog(model_name) ON DELETE CASCADE,
  endpoint text NOT NULL DEFAULT '',
  permission text NOT NULL DEFAULT 'allow' CHECK (permission IN ('allow', 'deny')),
  rpm_limit integer CHECK (rpm_limit IS NULL OR rpm_limit >= 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (group_id, model_name, endpoint)
);

CREATE INDEX IF NOT EXISTS group_model_permissions_lookup_idx
  ON group_model_permissions(group_id, model_name, endpoint);

CREATE TABLE IF NOT EXISTS system_settings (
  setting_key text PRIMARY KEY,
  category text NOT NULL DEFAULT 'general',
  setting_value_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  is_public boolean NOT NULL DEFAULT false,
  updated_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER system_settings_set_updated_at
BEFORE UPDATE ON system_settings
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS system_settings_category_idx
  ON system_settings(category, is_public);

CREATE TABLE IF NOT EXISTS risk_rules (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  rule_type text NOT NULL CHECK (rule_type IN ('sensitive_word', 'ssrf_target', 'request_limit', 'bot_protection', 'abuse_pattern')),
  name text NOT NULL,
  pattern text NOT NULL DEFAULT '',
  action text NOT NULL DEFAULT 'flag' CHECK (action IN ('flag', 'block', 'throttle')),
  severity text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'critical')),
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER risk_rules_set_updated_at
BEFORE UPDATE ON risk_rules
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS risk_rules_type_status_idx
  ON risk_rules(rule_type, status);

CREATE TABLE IF NOT EXISTS subscription_plans (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL,
  status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'archived')),
  price_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (price_usd >= 0),
  billing_period text NOT NULL DEFAULT 'month' CHECK (billing_period IN ('month', 'year', 'one_time')),
  wallet_credit_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (wallet_credit_usd >= 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER subscription_plans_set_updated_at
BEFORE UPDATE ON subscription_plans
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS user_subscriptions (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  plan_id uuid NOT NULL REFERENCES subscription_plans(id) ON DELETE RESTRICT,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'past_due', 'canceled', 'expired')),
  starts_at timestamptz NOT NULL DEFAULT now(),
  ends_at timestamptz,
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER user_subscriptions_set_updated_at
BEFORE UPDATE ON user_subscriptions
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS user_subscriptions_user_idx
  ON user_subscriptions(user_id, status);

CREATE TABLE IF NOT EXISTS orders (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  plan_id uuid REFERENCES subscription_plans(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'paid', 'failed', 'canceled', 'refunded')),
  amount_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (amount_usd >= 0),
  currency text NOT NULL DEFAULT 'USD',
  feature_flag text NOT NULL DEFAULT 'payments',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER orders_set_updated_at
BEFORE UPDATE ON orders
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS orders_user_idx
  ON orders(user_id, created_at DESC);

CREATE TABLE IF NOT EXISTS payment_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid REFERENCES orders(id) ON DELETE CASCADE,
  provider text NOT NULL DEFAULT '',
  provider_event_id text NOT NULL DEFAULT '',
  event_type text NOT NULL DEFAULT '',
  payload_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS payment_events_order_idx
  ON payment_events(order_id, created_at DESC);

CREATE TABLE IF NOT EXISTS affiliate_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code text NOT NULL UNIQUE,
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER affiliate_codes_set_updated_at
BEFORE UPDATE ON affiliate_codes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS affiliate_rebates (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  affiliate_code_id uuid NOT NULL REFERENCES affiliate_codes(id) ON DELETE CASCADE,
  order_id uuid REFERENCES orders(id) ON DELETE SET NULL,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  amount_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (amount_usd >= 0),
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'settled', 'canceled')),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER affiliate_rebates_set_updated_at
BEFORE UPDATE ON affiliate_rebates
FOR EACH ROW EXECUTE FUNCTION set_updated_at();
