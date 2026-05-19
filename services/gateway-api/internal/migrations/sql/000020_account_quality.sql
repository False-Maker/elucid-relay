CREATE TABLE IF NOT EXISTS account_quality_snapshots (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  provider_id uuid REFERENCES providers(id) ON DELETE SET NULL,
  channel_id uuid REFERENCES channels(id) ON DELETE SET NULL,
  quality_status text NOT NULL DEFAULT 'unknown' CHECK (quality_status IN ('healthy', 'degraded', 'isolated', 'unknown')),
  decision text NOT NULL DEFAULT 'watch' CHECK (decision IN ('allow', 'watch', 'isolate')),
  quality_score integer NOT NULL DEFAULT 0 CHECK (quality_score >= 0 AND quality_score <= 100),
  availability_score integer NOT NULL DEFAULT 0 CHECK (availability_score >= 0 AND availability_score <= 100),
  latency_score integer NOT NULL DEFAULT 0 CHECK (latency_score >= 0 AND latency_score <= 100),
  quota_score integer NOT NULL DEFAULT 0 CHECK (quota_score >= 0 AND quota_score <= 100),
  error_score integer NOT NULL DEFAULT 0 CHECK (error_score >= 0 AND error_score <= 100),
  reason_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  metrics_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS account_quality_snapshots_account_created_idx
  ON account_quality_snapshots(account_id, created_at DESC);

CREATE INDEX IF NOT EXISTS account_quality_snapshots_decision_created_idx
  ON account_quality_snapshots(decision, created_at DESC);

CREATE INDEX IF NOT EXISTS account_quality_snapshots_score_created_idx
  ON account_quality_snapshots(quality_score, created_at DESC);

INSERT INTO system_settings (setting_key, category, setting_value_json, is_public)
VALUES
  ('account_pool.quality_enabled', 'account_pool', '{"enabled":false}'::jsonb, false),
  ('account_pool.quality_interval_seconds', 'account_pool', '{"seconds":300}'::jsonb, false),
  ('account_pool.quality_isolation_enabled', 'account_pool', '{"enabled":false}'::jsonb, false),
  ('account_pool.quality_isolation_threshold', 'account_pool', '{"score":40}'::jsonb, false),
  ('account_pool.quality_watch_threshold', 'account_pool', '{"score":70}'::jsonb, false)
ON CONFLICT (setting_key) DO NOTHING;
