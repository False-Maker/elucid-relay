CREATE TABLE IF NOT EXISTS account_platform_configs (
  provider_type text PRIMARY KEY,
  display_name text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  health_enabled boolean NOT NULL DEFAULT true,
  quota_refresh_enabled boolean NOT NULL DEFAULT true,
  wakeup_enabled boolean NOT NULL DEFAULT true,
  health_interval_seconds integer NOT NULL DEFAULT 300 CHECK (health_interval_seconds > 0),
  quota_refresh_interval_seconds integer NOT NULL DEFAULT 900 CHECK (quota_refresh_interval_seconds > 0),
  wakeup_interval_seconds integer NOT NULL DEFAULT 300 CHECK (wakeup_interval_seconds > 0),
  quota_low_threshold_percent integer NOT NULL DEFAULT 20 CHECK (quota_low_threshold_percent >= 0 AND quota_low_threshold_percent <= 100),
  max_failure_count integer NOT NULL DEFAULT 5 CHECK (max_failure_count >= 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER account_platform_configs_set_updated_at
BEFORE UPDATE ON account_platform_configs
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

INSERT INTO account_platform_configs (provider_type, display_name)
VALUES
  ('openai_compatible', 'OpenAI Compatible'),
  ('codex_compatible', 'Codex CLI'),
  ('anthropic', 'Anthropic'),
  ('anthropic_compatible', 'Anthropic Compatible'),
  ('github_copilot', 'GitHub Copilot'),
  ('gemini', 'Gemini'),
  ('gemini_cli', 'Gemini CLI'),
  ('gemini_openai_compatible', 'Gemini OpenAI Compatible'),
  ('antigravity', 'Antigravity'),
  ('kiro', 'Kiro'),
  ('windsurf_codeium', 'Windsurf Codeium')
ON CONFLICT (provider_type) DO NOTHING;

CREATE TABLE IF NOT EXISTS account_wakeup_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid REFERENCES accounts(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'success', 'failed', 'canceled')),
  trigger_type text NOT NULL DEFAULT 'manual' CHECK (trigger_type IN ('manual', 'scheduled')),
  requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
  target_path text NOT NULL DEFAULT '/models',
  model_name text NOT NULL DEFAULT '',
  endpoint text NOT NULL DEFAULT '',
  scheduled_for timestamptz NOT NULL DEFAULT now(),
  total_count integer NOT NULL DEFAULT 0 CHECK (total_count >= 0),
  success_count integer NOT NULL DEFAULT 0 CHECK (success_count >= 0),
  failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
  skipped_count integer NOT NULL DEFAULT 0 CHECK (skipped_count >= 0),
  error_message text NOT NULL DEFAULT '',
  result_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS account_wakeup_jobs_status_schedule_idx
  ON account_wakeup_jobs(status, scheduled_for, created_at);

CREATE INDEX IF NOT EXISTS account_wakeup_jobs_account_created_idx
  ON account_wakeup_jobs(account_id, created_at DESC);

INSERT INTO system_settings (setting_key, category, setting_value_json, is_public)
VALUES
  ('account_pool.wakeup_enabled', 'account_pool', '{"enabled":false}'::jsonb, false),
  ('account_pool.wakeup_interval_seconds', 'account_pool', '{"seconds":300}'::jsonb, false)
ON CONFLICT (setting_key) DO NOTHING;
