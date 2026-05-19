ALTER TABLE payment_events
  ADD COLUMN IF NOT EXISTS status text NOT NULL DEFAULT 'pending',
  ADD COLUMN IF NOT EXISTS attempts integer NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS last_attempt_at timestamptz,
  ADD COLUMN IF NOT EXISTS next_attempt_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN IF NOT EXISTS last_error text NOT NULL DEFAULT '',
  ADD COLUMN IF NOT EXISTS updated_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE payment_events
  DROP CONSTRAINT IF EXISTS payment_events_status_check;

ALTER TABLE payment_events
  ADD CONSTRAINT payment_events_status_check
  CHECK (status IN ('pending', 'processing', 'processed', 'failed', 'replayed'));

ALTER TABLE payment_events
  DROP CONSTRAINT IF EXISTS payment_events_attempts_check;

ALTER TABLE payment_events
  ADD CONSTRAINT payment_events_attempts_check
  CHECK (attempts >= 0);

DROP TRIGGER IF EXISTS payment_events_set_updated_at ON payment_events;

CREATE TRIGGER payment_events_set_updated_at
BEFORE UPDATE ON payment_events
FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX IF NOT EXISTS payment_events_status_created_idx
  ON payment_events(status, created_at DESC);

CREATE INDEX IF NOT EXISTS payment_events_type_created_idx
  ON payment_events(event_type, created_at DESC);
