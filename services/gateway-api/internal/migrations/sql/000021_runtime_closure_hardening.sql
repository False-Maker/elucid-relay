ALTER TABLE orders
  DROP CONSTRAINT IF EXISTS orders_status_check;

ALTER TABLE orders
  ADD CONSTRAINT orders_status_check
  CHECK (status IN ('pending', 'paid', 'failed', 'canceled', 'refunded', 'refund_blocked'));

CREATE TABLE IF NOT EXISTS account_pool_strategy_events (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid REFERENCES accounts(id) ON DELETE SET NULL,
  provider_id uuid REFERENCES providers(id) ON DELETE SET NULL,
  channel_id uuid REFERENCES channels(id) ON DELETE SET NULL,
  actor_user_id uuid REFERENCES users(id) ON DELETE SET NULL,
  event_type text NOT NULL,
  action text NOT NULL,
  reason text NOT NULL DEFAULT '',
  previous_status text NOT NULL DEFAULT '',
  next_status text NOT NULL DEFAULT '',
  decision text NOT NULL DEFAULT '',
  quality_score integer CHECK (quality_score IS NULL OR (quality_score >= 0 AND quality_score <= 100)),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS account_pool_strategy_events_created_idx
  ON account_pool_strategy_events(created_at DESC);

CREATE INDEX IF NOT EXISTS account_pool_strategy_events_account_created_idx
  ON account_pool_strategy_events(account_id, created_at DESC);
