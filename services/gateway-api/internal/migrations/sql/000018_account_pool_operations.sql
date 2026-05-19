CREATE TABLE IF NOT EXISTS account_quota_snapshots (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  provider_id uuid REFERENCES providers(id) ON DELETE SET NULL,
  channel_id uuid REFERENCES channels(id) ON DELETE SET NULL,
  refresh_job_id uuid,
  status text NOT NULL DEFAULT 'success' CHECK (status IN ('success', 'failed', 'unsupported')),
  source text NOT NULL DEFAULT '',
  window_type text NOT NULL DEFAULT 'requests',
  remaining numeric(20,10),
  limit_value numeric(20,10),
  reset_at timestamptz,
  error_message text NOT NULL DEFAULT '',
  raw_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS account_quota_snapshots_account_created_idx
  ON account_quota_snapshots(account_id, created_at DESC);

CREATE INDEX IF NOT EXISTS account_quota_snapshots_status_created_idx
  ON account_quota_snapshots(status, created_at DESC);

CREATE TABLE IF NOT EXISTS account_quota_refresh_jobs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id uuid REFERENCES accounts(id) ON DELETE SET NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'success', 'failed')),
  trigger_type text NOT NULL DEFAULT 'manual' CHECK (trigger_type IN ('manual', 'scheduled')),
  requested_by uuid REFERENCES users(id) ON DELETE SET NULL,
  total_count integer NOT NULL DEFAULT 0 CHECK (total_count >= 0),
  success_count integer NOT NULL DEFAULT 0 CHECK (success_count >= 0),
  failed_count integer NOT NULL DEFAULT 0 CHECK (failed_count >= 0),
  unsupported_count integer NOT NULL DEFAULT 0 CHECK (unsupported_count >= 0),
  error_message text NOT NULL DEFAULT '',
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  started_at timestamptz,
  finished_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE account_quota_snapshots
  DROP CONSTRAINT IF EXISTS account_quota_snapshots_refresh_job_fk;

ALTER TABLE account_quota_snapshots
  ADD CONSTRAINT account_quota_snapshots_refresh_job_fk
  FOREIGN KEY (refresh_job_id) REFERENCES account_quota_refresh_jobs(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS account_quota_refresh_jobs_created_idx
  ON account_quota_refresh_jobs(created_at DESC);

CREATE INDEX IF NOT EXISTS account_quota_refresh_jobs_account_created_idx
  ON account_quota_refresh_jobs(account_id, created_at DESC);

CREATE TABLE IF NOT EXISTS account_pool_groups (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  priority integer NOT NULL DEFAULT 100,
  default_route_tags_json jsonb NOT NULL DEFAULT '[]'::jsonb,
  default_metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER account_pool_groups_set_updated_at
BEFORE UPDATE ON account_pool_groups
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS account_pool_groups_status_priority_idx
  ON account_pool_groups(status, priority, name);

CREATE TABLE IF NOT EXISTS account_pool_group_members (
  group_id uuid NOT NULL REFERENCES account_pool_groups(id) ON DELETE CASCADE,
  account_id uuid NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, account_id)
);

CREATE INDEX IF NOT EXISTS account_pool_group_members_account_idx
  ON account_pool_group_members(account_id);

INSERT INTO account_pool_groups (name, status, priority)
SELECT DISTINCT metadata_json->>'pool_group', 'active', 100
FROM accounts
WHERE COALESCE(metadata_json->>'pool_group', '') <> ''
ON CONFLICT (name) DO NOTHING;

INSERT INTO account_pool_group_members (group_id, account_id)
SELECT g.id, a.id
FROM accounts a
JOIN account_pool_groups g ON g.name = a.metadata_json->>'pool_group'
WHERE COALESCE(a.metadata_json->>'pool_group', '') <> ''
ON CONFLICT (group_id, account_id) DO NOTHING;

INSERT INTO system_settings (setting_key, category, setting_value_json, is_public)
VALUES
  ('account_pool.health_enabled', 'account_pool', '{"enabled":false}'::jsonb, false),
  ('account_pool.quota_refresh_enabled', 'account_pool', '{"enabled":false}'::jsonb, false),
  ('account_pool.health_interval_seconds', 'account_pool', '{"seconds":300}'::jsonb, false),
  ('account_pool.quota_refresh_interval_seconds', 'account_pool', '{"seconds":900}'::jsonb, false)
ON CONFLICT (setting_key) DO NOTHING;
