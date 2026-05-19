CREATE UNIQUE INDEX IF NOT EXISTS orders_stripe_checkout_session_unique_idx
  ON orders(stripe_checkout_session_id)
  WHERE stripe_checkout_session_id <> '';

CREATE UNIQUE INDEX IF NOT EXISTS orders_stripe_payment_intent_unique_idx
  ON orders(stripe_payment_intent_id)
  WHERE stripe_payment_intent_id <> '';

CREATE INDEX IF NOT EXISTS user_subscriptions_order_idx
  ON user_subscriptions(order_id)
  WHERE order_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS user_subscriptions_granted_group_idx
  ON user_subscriptions(granted_group_id, user_id)
  WHERE granted_group_id IS NOT NULL;
