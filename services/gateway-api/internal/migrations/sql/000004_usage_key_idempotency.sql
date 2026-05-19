ALTER TABLE usage_records
  DROP CONSTRAINT IF EXISTS usage_records_request_id_key;

CREATE UNIQUE INDEX IF NOT EXISTS usage_records_request_api_key_uidx
  ON usage_records(request_id, api_key_id);

ALTER TABLE channel_abilities
  ADD COLUMN IF NOT EXISTS upstream_model text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS transform_capability_json jsonb NOT NULL DEFAULT '{"mode":"native","lossless":true}'::jsonb,
  ADD COLUMN IF NOT EXISTS priority integer NOT NULL DEFAULT 100,
  ADD COLUMN IF NOT EXISTS weight integer NOT NULL DEFAULT 1 CHECK (weight > 0),
  ADD COLUMN IF NOT EXISTS retry_priority integer NOT NULL DEFAULT 100;

UPDATE channel_abilities
SET upstream_model = model_name
WHERE upstream_model = '';
