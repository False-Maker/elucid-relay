CREATE TABLE IF NOT EXISTS model_pricing_overrides (
  model_name text PRIMARY KEY REFERENCES model_catalog(model_name) ON DELETE CASCADE,
  billing_mode text NOT NULL DEFAULT 'standard' CHECK (billing_mode IN ('standard', 'tiered_expr')),
  billing_expr text NOT NULL DEFAULT '',
  cache_read_usd_per_1k numeric(20,10) NOT NULL DEFAULT 0 CHECK (cache_read_usd_per_1k >= 0),
  cache_write_usd_per_1k numeric(20,10) NOT NULL DEFAULT 0 CHECK (cache_write_usd_per_1k >= 0),
  image_usd_per_unit numeric(20,10) NOT NULL DEFAULT 0 CHECK (image_usd_per_unit >= 0),
  audio_usd_per_second numeric(20,10) NOT NULL DEFAULT 0 CHECK (audio_usd_per_second >= 0),
  metadata_json jsonb NOT NULL DEFAULT '{}'::jsonb,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TRIGGER model_pricing_overrides_set_updated_at
BEFORE UPDATE ON model_pricing_overrides
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS model_pricing_overrides_mode_idx
  ON model_pricing_overrides(billing_mode);
