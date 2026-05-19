CREATE TABLE IF NOT EXISTS registration_email_codes (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email text NOT NULL,
  code_hash text NOT NULL,
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS registration_email_codes_email_created_idx
  ON registration_email_codes(email, created_at DESC);

CREATE INDEX IF NOT EXISTS registration_email_codes_expires_idx
  ON registration_email_codes(expires_at)
  WHERE consumed_at IS NULL;

INSERT INTO system_settings (setting_key, category, setting_value_json, is_public)
VALUES (
  'auth.email_verification',
  'auth',
  '{"registration_verification_enabled":false,"smtp":{"host":"","port":587,"username":"","password_ciphertext":"","password_nonce":"","from":"","tls_mode":"starttls"}}'::jsonb,
  false
)
ON CONFLICT (setting_key) DO NOTHING;
