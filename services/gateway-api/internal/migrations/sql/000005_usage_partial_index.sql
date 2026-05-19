DROP INDEX IF EXISTS usage_records_request_api_key_uidx;

CREATE UNIQUE INDEX IF NOT EXISTS usage_records_request_api_key_uidx
  ON usage_records(request_id, api_key_id)
  WHERE api_key_id IS NOT NULL;
