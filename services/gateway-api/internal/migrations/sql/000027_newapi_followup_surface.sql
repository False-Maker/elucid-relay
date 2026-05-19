ALTER TABLE northbound_route_affinities
  ADD COLUMN IF NOT EXISTS rule_name text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS hit_count integer NOT NULL DEFAULT 0 CHECK (hit_count >= 0),
  ADD COLUMN IF NOT EXISTS miss_count integer NOT NULL DEFAULT 0 CHECK (miss_count >= 0),
  ADD COLUMN IF NOT EXISTS last_hit_at timestamptz,
  ADD COLUMN IF NOT EXISTS last_miss_at timestamptz;

CREATE INDEX IF NOT EXISTS northbound_route_affinities_rule_expires_idx
  ON northbound_route_affinities (rule_name, expires_at);

CREATE INDEX IF NOT EXISTS northbound_route_affinities_model_endpoint_expires_idx
  ON northbound_route_affinities (api_key_id, model_name, endpoint, expires_at);
