ALTER TABLE usage_records
  ADD COLUMN IF NOT EXISTS usage_source text NOT NULL DEFAULT 'estimated_fallback',
  ADD COLUMN IF NOT EXISTS stream_event_count integer NOT NULL DEFAULT 0 CHECK (stream_event_count >= 0),
  ADD COLUMN IF NOT EXISTS websocket_frame_count integer NOT NULL DEFAULT 0 CHECK (websocket_frame_count >= 0),
  ADD COLUMN IF NOT EXISTS metering_metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS usage_records_created_idx
  ON usage_records(created_at DESC);

CREATE INDEX IF NOT EXISTS usage_records_endpoint_created_idx
  ON usage_records(endpoint, created_at DESC);

CREATE INDEX IF NOT EXISTS usage_records_status_created_idx
  ON usage_records(status, created_at DESC);
