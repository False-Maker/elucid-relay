UPDATE payment_events
SET status = CASE
      WHEN processed_at IS NOT NULL THEN 'processed'
      WHEN processing_error <> '' THEN 'failed'
      ELSE status
    END,
    last_error = CASE
      WHEN processing_error <> '' AND last_error = '' THEN processing_error
      ELSE last_error
    END,
    last_attempt_at = COALESCE(last_attempt_at, processed_at),
    updated_at = now()
WHERE processed_at IS NOT NULL OR processing_error <> '';
