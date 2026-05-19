CREATE TABLE IF NOT EXISTS northbound_route_affinities (
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  api_key_id uuid NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
  model_name text NOT NULL REFERENCES model_catalog(model_name) ON DELETE CASCADE,
  endpoint text NOT NULL,
  session_key_hash text NOT NULL,
  channel_id uuid REFERENCES channels(id) ON DELETE SET NULL,
  account_id uuid REFERENCES accounts(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now() + interval '7 days',
  PRIMARY KEY (api_key_id, model_name, endpoint, session_key_hash)
);

CREATE INDEX IF NOT EXISTS northbound_route_affinities_user_idx
  ON northbound_route_affinities(user_id, expires_at);

CREATE INDEX IF NOT EXISTS northbound_route_affinities_account_idx
  ON northbound_route_affinities(account_id, expires_at);
