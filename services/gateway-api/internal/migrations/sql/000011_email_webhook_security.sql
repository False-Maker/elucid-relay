ALTER TABLE notification_channels
  ADD COLUMN IF NOT EXISTS signing_secret_ciphertext bytea,
  ADD COLUMN IF NOT EXISTS signing_secret_nonce bytea;
