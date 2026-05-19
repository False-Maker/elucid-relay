#!/usr/bin/env node
import crypto from "node:crypto";

if (!flag("E2E_STRIPE_TESTMODE")) {
  console.log("stripe testmode e2e skipped: set E2E_STRIPE_TESTMODE=1");
  process.exit(0);
}

const baseURL = env("E2E_BASE_URL", "http://localhost:18080").replace(/\/$/, "");
const stripeSecretKey = required("E2E_STRIPE_SECRET_KEY", "STRIPE_SECRET_KEY");
const stripeWebhookSecret = required("E2E_STRIPE_WEBHOOK_SECRET", "STRIPE_WEBHOOK_SECRET");
const amountUSD = env("E2E_STRIPE_AMOUNT_USD", "1.00");
const runID = `${Date.now()}-${crypto.randomBytes(3).toString("hex")}`;

if (!stripeSecretKey.startsWith("sk_test_") && !flag("E2E_STRIPE_ALLOW_LIVE")) {
  throw new Error("refusing to run Stripe E2E with a non-test secret key; set E2E_STRIPE_ALLOW_LIVE=1 only for an intentional live-mode run");
}

const portal = await request("/api/portal/v1/auth/register", {
  method: "POST",
  body: {
    email: `stripe-e2e-${runID}@example.com`,
    password: `stripe-e2e-password-${runID}`,
    display_name: `stripe-e2e-${runID}`,
  },
});
const portalToken = portal.session.session_token;

const order = await request("/api/portal/v1/orders", {
  method: "POST",
  token: portalToken,
  body: {
    order_type: "wallet_topup",
    amount_usd: amountUSD,
    metadata: { e2e_run_id: runID },
  },
});

const checkout = await request(`/api/portal/v1/orders/${order.id}/checkout`, {
  method: "POST",
  token: portalToken,
});

if (!checkout.stripe_checkout_session_id || !checkout.checkout_url) {
  throw new Error("checkout response did not include stripe_checkout_session_id and checkout_url");
}

const session = await stripeRequest(`/v1/checkout/sessions/${encodeURIComponent(checkout.stripe_checkout_session_id)}`);
if (session.id !== checkout.stripe_checkout_session_id || session.client_reference_id !== order.id) {
  throw new Error("Stripe checkout session did not match the local order");
}

const payload = JSON.stringify({
  id: `evt_stripe_e2e_${runID}`,
  type: "checkout.session.completed",
  data: {
    object: {
      id: checkout.stripe_checkout_session_id,
      payment_intent: `pi_stripe_e2e_${runID}`,
      metadata: {
        order_id: order.id,
        user_id: portal.user.id,
        order_type: "wallet_topup",
      },
    },
  },
});
const signature = stripeSignatureHeader(payload, stripeWebhookSecret);

const webhook = await request("/api/billing/v1/stripe/webhook", {
  method: "POST",
  headers: { "content-type": "application/json", "Stripe-Signature": signature },
  rawBody: payload,
});
if (!webhook.received) {
  throw new Error("Stripe webhook did not return received=true");
}

const duplicate = await request("/api/billing/v1/stripe/webhook", {
  method: "POST",
  headers: { "content-type": "application/json", "Stripe-Signature": signature },
  rawBody: payload,
});
if (!duplicate.duplicate) {
  throw new Error("duplicate Stripe webhook was not treated as idempotent");
}

const paidOrder = await request(`/api/portal/v1/orders/${order.id}`, { token: portalToken });
if (paidOrder.status !== "paid" || paidOrder.stripe_checkout_session_id !== checkout.stripe_checkout_session_id) {
  throw new Error(`order was not marked paid after signed webhook: ${JSON.stringify(paidOrder)}`);
}

console.log(`stripe testmode e2e ok: order=${order.id} checkout_session=${checkout.stripe_checkout_session_id}`);

async function request(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  if (options.token) headers.authorization = `Bearer ${options.token}`;
  if (options.body && !headers["content-type"]) headers["content-type"] = "application/json";
  const response = await fetch(`${baseURL}${path}`, {
    method: options.method || "GET",
    headers,
    body: options.rawBody ?? (options.body ? JSON.stringify(options.body) : undefined),
  });
  const text = await response.text();
  let parsed = {};
  if (text) {
    try {
      parsed = JSON.parse(text);
    } catch {
      throw new Error(`${path} returned non-JSON response ${response.status}: ${text}`);
    }
  }
  if (!response.ok || parsed.error) {
    throw new Error(`${path} ${response.status}: ${text}`);
  }
  return parsed.data;
}

async function stripeRequest(path) {
  const response = await fetch(`https://api.stripe.com${path}`, {
    headers: { authorization: `Bearer ${stripeSecretKey}` },
  });
  const text = await response.text();
  let parsed = {};
  if (text) parsed = JSON.parse(text);
  if (!response.ok) {
    throw new Error(`Stripe ${path} ${response.status}: ${text}`);
  }
  return parsed;
}

function stripeSignatureHeader(payload, secret) {
  const timestamp = Math.floor(Date.now() / 1000);
  const signature = crypto.createHmac("sha256", secret).update(`${timestamp}.${payload}`).digest("hex");
  return `t=${timestamp},v1=${signature}`;
}

function required(...names) {
  for (const name of names) {
    if (process.env[name]) return process.env[name];
  }
  throw new Error(`${names.join(" or ")} is required`);
}

function env(name, fallback) {
  return process.env[name] || fallback;
}

function flag(name) {
  return ["1", "true", "yes"].includes(String(process.env[name] || "").toLowerCase());
}
