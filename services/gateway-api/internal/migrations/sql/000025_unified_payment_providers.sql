ALTER TABLE orders
  ADD COLUMN IF NOT EXISTS payment_provider text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS payment_method text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS provider_instance_id uuid,
  ADD COLUMN IF NOT EXISTS pay_currency text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS pay_amount_cents integer NOT NULL DEFAULT 0 CHECK (pay_amount_cents >= 0),
  ADD COLUMN IF NOT EXISTS fx_rate numeric(20,10),
  ADD COLUMN IF NOT EXISTS upstream_trade_no text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS upstream_transaction_id text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS expires_at timestamptz;

CREATE TABLE IF NOT EXISTS payment_provider_instances (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider_type text NOT NULL CHECK (provider_type IN ('stripe', 'easypay', 'alipay', 'wechat')),
  name text NOT NULL,
  status text NOT NULL DEFAULT 'disabled' CHECK (status IN ('active', 'disabled')),
  priority integer NOT NULL DEFAULT 100 CHECK (priority >= 0),
  weight integer NOT NULL DEFAULT 1 CHECK (weight >= 0),
  supported_methods text[] NOT NULL DEFAULT '{}',
  min_amount_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (min_amount_usd >= 0),
  max_amount_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (max_amount_usd >= 0),
  daily_limit_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (daily_limit_usd >= 0),
  config_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  secret_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER payment_provider_instances_set_updated_at
BEFORE UPDATE ON payment_provider_instances
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS payment_provider_instances_method_idx
  ON payment_provider_instances(status, priority, created_at);

CREATE TABLE IF NOT EXISTS payment_method_routes (
  method text PRIMARY KEY CHECK (method IN ('stripe', 'alipay', 'wechat')),
  enabled boolean NOT NULL DEFAULT false,
  display_name text NOT NULL DEFAULT '',
  provider_types text[] NOT NULL DEFAULT '{}',
  min_amount_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (min_amount_usd >= 0),
  max_amount_usd numeric(20,10) NOT NULL DEFAULT 0 CHECK (max_amount_usd >= 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER payment_method_routes_set_updated_at
BEFORE UPDATE ON payment_method_routes
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE IF NOT EXISTS payment_order_attempts (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  order_id uuid NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
  provider_instance_id uuid REFERENCES payment_provider_instances(id) ON DELETE SET NULL,
  provider_type text NOT NULL DEFAULT '',
  method text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'created' CHECK (status IN ('created', 'pending', 'paid', 'failed', 'expired', 'canceled')),
  upstream_trade_no text NOT NULL DEFAULT '',
  upstream_transaction_id text NOT NULL DEFAULT '',
  pay_currency text NOT NULL DEFAULT '',
  pay_amount_cents integer NOT NULL DEFAULT 0 CHECK (pay_amount_cents >= 0),
  checkout_url text NOT NULL DEFAULT '',
  qr_code_url text NOT NULL DEFAULT '',
  raw_response_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER payment_order_attempts_set_updated_at
BEFORE UPDATE ON payment_order_attempts
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS payment_order_attempts_order_idx
  ON payment_order_attempts(order_id, created_at DESC);

CREATE INDEX IF NOT EXISTS payment_order_attempts_trade_idx
  ON payment_order_attempts(provider_type, upstream_trade_no)
  WHERE upstream_trade_no <> '';

CREATE UNIQUE INDEX IF NOT EXISTS orders_provider_trade_unique_idx
  ON orders(payment_provider, upstream_trade_no)
  WHERE payment_provider <> '' AND upstream_trade_no <> '';

UPDATE orders
SET payment_provider = 'stripe',
    payment_method = 'stripe',
    pay_currency = COALESCE(NULLIF(currency, ''), 'USD'),
    upstream_trade_no = COALESCE(NULLIF(stripe_checkout_session_id, ''), upstream_trade_no),
    upstream_transaction_id = COALESCE(NULLIF(stripe_payment_intent_id, ''), upstream_transaction_id)
WHERE payment_provider = ''
  AND (
    COALESCE(stripe_checkout_session_id, '') <> ''
    OR COALESCE(stripe_payment_intent_id, '') <> ''
    OR COALESCE(stripe_subscription_id, '') <> ''
  );

INSERT INTO payment_method_routes (method, enabled, display_name, provider_types)
VALUES
  ('stripe', false, 'Stripe', ARRAY['stripe']),
  ('alipay', false, '支付宝', ARRAY['alipay', 'easypay']),
  ('wechat', false, '微信支付', ARRAY['wechat', 'easypay'])
ON CONFLICT (method) DO NOTHING;
