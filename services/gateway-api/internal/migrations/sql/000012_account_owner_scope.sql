DO $$
BEGIN
  ALTER TABLE accounts
    ADD CONSTRAINT accounts_routing_owner_scope_check
    CHECK (
      (routing_mode = 'pool' AND owner_user_id IS NULL)
      OR (routing_mode = 'byo' AND owner_user_id IS NOT NULL)
    )
    NOT VALID;
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;
