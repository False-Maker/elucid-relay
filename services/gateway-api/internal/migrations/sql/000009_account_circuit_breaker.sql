ALTER TABLE account_runtime_states
  ADD COLUMN IF NOT EXISTS circuit_state text NOT NULL DEFAULT 'closed',
  ADD COLUMN IF NOT EXISTS circuit_failure_count integer NOT NULL DEFAULT 0 CHECK (circuit_failure_count >= 0),
  ADD COLUMN IF NOT EXISTS circuit_opened_at timestamptz,
  ADD COLUMN IF NOT EXISTS circuit_half_open_after timestamptz;

UPDATE account_runtime_states
SET circuit_state = 'closed'
WHERE circuit_state = '';

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'account_runtime_states_circuit_state_chk'
  ) THEN
    ALTER TABLE account_runtime_states
      ADD CONSTRAINT account_runtime_states_circuit_state_chk
      CHECK (circuit_state IN ('closed', 'open', 'half_open'));
  END IF;
END $$;

CREATE INDEX IF NOT EXISTS account_runtime_circuit_idx
  ON account_runtime_states(circuit_state, circuit_half_open_after, active_requests);
