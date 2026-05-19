#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:18080}"
OWNER_EMAIL="${OWNER_EMAIL:-owner@example.com}"
OWNER_PASSWORD="${OWNER_PASSWORD:-change-me-please-32-chars}"
RUN_ID="$(date +%s)"
USER_EMAIL="smoke-${RUN_ID}@example.com"
USER_PASSWORD="smoke-password-${RUN_ID}"
PROMOTER_EMAIL="smoke-promoter-${RUN_ID}@example.com"
PROMOTER_PASSWORD="smoke-promoter-password-${RUN_ID}"
MODEL_NAME="smoke-model-${RUN_ID}"
MODEL_ALIAS="smoke-alias-${RUN_ID}"
DENIED_MODEL_NAME="smoke-denied-model-${RUN_ID}"

docker compose --profile smoke up -d --force-recreate mock-upstream >/dev/null

extract() {
  node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); console.log(${1});"
}

admin_login="$(
  curl -fsS "${BASE_URL}/api/admin/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"${OWNER_EMAIL}\",\"password\":\"${OWNER_PASSWORD}\"}"
)"
admin_token="$(printf '%s' "${admin_login}" | extract 'data.data.session.session_token')"

curl -fsS "${BASE_URL}/api/admin/v1/models" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"model_name\":\"${MODEL_NAME}\",\"display_name\":\"Smoke Model\",\"aliases\":[\"${MODEL_ALIAS}\"],\"endpoint_capabilities\":[\"chat\",\"responses\",\"embeddings\",\"realtime\"],\"request_usd\":\"0\",\"min_charge_usd\":\"0.001\",\"public_visible\":true}" \
  >/dev/null

curl -fsS "${BASE_URL}/api/admin/v1/models" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"model_name\":\"${DENIED_MODEL_NAME}\",\"display_name\":\"Smoke Denied Model\",\"endpoint_capabilities\":[\"chat\"],\"request_usd\":\"0\",\"min_charge_usd\":\"0.001\",\"public_visible\":true}" \
  >/dev/null

provider_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/providers" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Smoke Provider ${RUN_ID}\",\"provider_type\":\"openai_compatible\"}"
)"
provider_id="$(printf '%s' "${provider_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/admin/v1/provider-clients" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"provider_id\":\"${provider_id}\",\"name\":\"Smoke Client ${RUN_ID}\",\"client_type\":\"api_key\",\"secret\":\"mock-client-secret\"}" \
  >/dev/null

channel_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/channels" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"provider_id\":\"${provider_id}\",\"name\":\"Smoke Channel ${RUN_ID}\",\"base_url\":\"http://mock-upstream:8090\",\"abilities\":[{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"chat\"},{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"realtime\"}]}"
)"
channel_id="$(printf '%s' "${channel_response}" | extract 'data.data.id')"

account_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/accounts" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"provider_id\":\"${provider_id}\",\"channel_id\":\"${channel_id}\",\"name\":\"Smoke Account ${RUN_ID}\",\"api_key\":\"mock-key\"}"
)"
account_id="$(printf '%s' "${account_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/admin/v1/account-quota-windows" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"account_id\":\"${account_id}\",\"window_type\":\"requests\",\"remaining\":\"10\"}" \
  >/dev/null

model_sync_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/channels/${channel_id}/model-sync" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d '{}'
)"
printf '%s' "${model_sync_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.data.discovered_count < 1 || data.data.updated_count < 1) process.exit(1);"

model_sync_jobs="$(
  curl -fsS "${BASE_URL}/api/admin/v1/model-sync-jobs?limit=1" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${model_sync_jobs}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const job=data.data[0]; if (!job || job.status !== 'success' || job.channel_id !== '${channel_id}') process.exit(1);"

batch_model_sync="$(
  curl -fsS "${BASE_URL}/api/admin/v1/model-sync-jobs" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"channel_ids\":[\"${channel_id}\"]}"
)"
printf '%s' "${batch_model_sync}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.data.channel_count !== 1 || data.data.success_count !== 1 || data.data.updated_count < 1) process.exit(1);"

public_channel_status="$(
  curl -fsS "${BASE_URL}/api/public/v1/channel-status"
)"
printf '%s' "${public_channel_status}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const row=data.data.find((item)=>item.channel_name==='Smoke Channel ${RUN_ID}'); if (!row || row.last_model_sync_status !== 'success' || !row.last_model_sync_at) process.exit(1);"

route_explain="$(
  curl -fsS "${BASE_URL}/api/admin/v1/runtime/route-explain?model=${MODEL_NAME}&endpoint=chat" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${route_explain}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.data.available !== true) process.exit(1);"

portal_register="$(
  curl -fsS "${BASE_URL}/api/portal/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"${USER_EMAIL}\",\"password\":\"${USER_PASSWORD}\",\"display_name\":\"Smoke User\"}"
)"
portal_token="$(printf '%s' "${portal_register}" | extract 'data.data.session.session_token')"
user_id="$(printf '%s' "${portal_register}" | extract 'data.data.user.id')"

blocked_order_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/orders" \
    -H "Authorization: Bearer ${portal_token}" \
    -H 'Content-Type: application/json' \
    -d '{"order_type":"wallet_topup","amount_usd":"2.00"}'
)"
blocked_order_id="$(printf '%s' "${blocked_order_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/billing/v1/stripe/webhook" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"evt_blocked_${RUN_ID}\",\"type\":\"checkout.session.completed\",\"data\":{\"object\":{\"id\":\"cs_blocked_${RUN_ID}\",\"payment_intent\":\"pi_blocked_${RUN_ID}\",\"metadata\":{\"order_id\":\"${blocked_order_id}\",\"user_id\":\"${user_id}\",\"order_type\":\"wallet_topup\"}}}}" \
  >/dev/null

curl -fsS "${BASE_URL}/api/admin/v1/users/${user_id}/wallet/adjustments" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d '{"entry_type":"debit","amount":"1.50","reason":"force refund-blocked smoke state"}' \
  >/dev/null

blocked_refund_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/orders/${blocked_order_id}/refund" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d '{}'
)"
printf '%s' "${blocked_refund_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.data.status !== 'refund_blocked' || !data.data.refund_blocked_reason) process.exit(1);"

promoter_register="$(
  curl -fsS "${BASE_URL}/api/portal/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"${PROMOTER_EMAIL}\",\"password\":\"${PROMOTER_PASSWORD}\",\"display_name\":\"Smoke Promoter\"}"
)"
promoter_id="$(printf '%s' "${promoter_register}" | extract 'data.data.user.id')"

group_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/groups" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Smoke Group ${RUN_ID}\",\"priority\":1,\"model_multiplier\":\"2\",\"rpm_limit\":20}"
)"
group_id="$(printf '%s' "${group_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/admin/v1/groups/${group_id}/members" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"${user_id}\",\"role\":\"member\"}" \
  >/dev/null

curl -fsS "${BASE_URL}/api/admin/v1/groups/${group_id}/model-permissions" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"chat\",\"permission\":\"allow\",\"price_multiplier\":\"1.5\"}" \
  >/dev/null

curl -fsS "${BASE_URL}/api/admin/v1/groups/${group_id}/model-permissions" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"\",\"permission\":\"allow\",\"price_multiplier\":\"1\"}" \
  >/dev/null

effective_policy="$(
  curl -fsS "${BASE_URL}/api/admin/v1/groups/effective-policy?user_id=${user_id}&model=${MODEL_NAME}&endpoint=chat" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${effective_policy}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.data.group_id !== '${group_id}' || data.data.billing_multiplier !== '3.0000000000') process.exit(1);"

subscription_group_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/groups" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Smoke Subscription Group ${RUN_ID}\",\"priority\":50,\"model_multiplier\":\"1\"}"
)"
subscription_group_id="$(printf '%s' "${subscription_group_response}" | extract 'data.data.id')"

subscription_plan_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/subscription-plans" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Smoke Plan ${RUN_ID}\",\"status\":\"active\",\"price_usd\":\"3.00\",\"billing_period\":\"month\",\"wallet_credit_usd\":\"0.50\",\"group_id\":\"${subscription_group_id}\",\"features\":[\"smoke\"]}"
)"
subscription_plan_id="$(printf '%s' "${subscription_plan_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/admin/v1/users/${user_id}/wallet/adjustments" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d '{"entry_type":"credit","amount":"5.00","reason":"smoke"}' \
  >/dev/null

redeem_batch="$(
  curl -fsS "${BASE_URL}/api/admin/v1/redeem-codes" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d '{"grant_value":"1.00","count":1,"max_claims":1}'
)"
redeem_code="$(printf '%s' "${redeem_batch}" | extract 'data.data.codes[0].code')"

curl -fsS "${BASE_URL}/api/portal/v1/redeem" \
  -H "Authorization: Bearer ${portal_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"code\":\"${redeem_code}\"}" \
  >/dev/null

api_key_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/api-keys" \
    -H "Authorization: Bearer ${portal_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"smoke\",\"model_scope\":[\"${MODEL_NAME}\",\"${DENIED_MODEL_NAME}\"]}"
)"
api_key_secret="$(printf '%s' "${api_key_response}" | extract 'data.data.secret')"

topup_order_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/orders" \
    -H "Authorization: Bearer ${portal_token}" \
    -H 'Content-Type: application/json' \
    -d '{"order_type":"wallet_topup","amount_usd":"2.00"}'
)"
topup_order_id="$(printf '%s' "${topup_order_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/billing/v1/stripe/webhook" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"evt_topup_${RUN_ID}\",\"type\":\"checkout.session.completed\",\"data\":{\"object\":{\"id\":\"cs_topup_${RUN_ID}\",\"payment_intent\":\"pi_topup_${RUN_ID}\",\"metadata\":{\"order_id\":\"${topup_order_id}\",\"user_id\":\"${user_id}\",\"order_type\":\"wallet_topup\"}}}}" \
  >/dev/null

topup_order="$(
  curl -fsS "${BASE_URL}/api/portal/v1/orders/${topup_order_id}" \
    -H "Authorization: Bearer ${portal_token}"
)"
printf '%s' "${topup_order}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.data.status !== 'paid' || data.data.stripe_payment_intent_id !== 'pi_topup_${RUN_ID}') process.exit(1);"

refund_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/orders/${topup_order_id}/refund" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d '{}'
)"
printf '%s' "${refund_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.data.status !== 'refunded' || data.data.refund_blocked_reason !== '') process.exit(1);"

subscription_order_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/orders" \
    -H "Authorization: Bearer ${portal_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"order_type\":\"subscription\",\"plan_id\":\"${subscription_plan_id}\"}"
)"
subscription_order_id="$(printf '%s' "${subscription_order_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/billing/v1/stripe/webhook" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"evt_sub_${RUN_ID}\",\"type\":\"checkout.session.completed\",\"data\":{\"object\":{\"id\":\"cs_sub_${RUN_ID}\",\"subscription\":\"sub_smoke_${RUN_ID}\",\"metadata\":{\"order_id\":\"${subscription_order_id}\",\"user_id\":\"${user_id}\",\"order_type\":\"subscription\"}}}}" \
  >/dev/null

subscriptions_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/subscriptions" \
    -H "Authorization: Bearer ${portal_token}"
)"
subscription_id="$(printf '%s' "${subscriptions_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const sub=data.data.find((item)=>item.stripe_subscription_id==='sub_smoke_${RUN_ID}' && item.granted_group_id==='${subscription_group_id}' && item.status==='active'); if (!sub) process.exit(1); console.log(sub.id);")"

curl -fsS "${BASE_URL}/api/admin/v1/subscriptions/${subscription_id}/cancel" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d '{}' \
  >/dev/null

affiliate_code="AFF${RUN_ID}"
curl -fsS "${BASE_URL}/api/admin/v1/affiliate-codes" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"owner_user_id\":\"${promoter_id}\",\"code\":\"${affiliate_code}\",\"rebate_rate\":\"0.2\"}" \
  >/dev/null

affiliate_order_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/orders" \
    -H "Authorization: Bearer ${portal_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"order_type\":\"wallet_topup\",\"amount_usd\":\"1.00\",\"affiliate_code\":\"${affiliate_code}\"}"
)"
affiliate_order_id="$(printf '%s' "${affiliate_order_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/billing/v1/stripe/webhook" \
  -H 'Content-Type: application/json' \
  -d "{\"id\":\"evt_affiliate_${RUN_ID}\",\"type\":\"checkout.session.completed\",\"data\":{\"object\":{\"id\":\"cs_affiliate_${RUN_ID}\",\"payment_intent\":\"pi_affiliate_${RUN_ID}\",\"metadata\":{\"order_id\":\"${affiliate_order_id}\",\"user_id\":\"${user_id}\",\"order_type\":\"wallet_topup\"}}}}" \
  >/dev/null

affiliate_rebates="$(
  curl -fsS "${BASE_URL}/api/admin/v1/affiliate-rebates" \
    -H "Authorization: Bearer ${admin_token}"
)"
affiliate_rebate_id="$(printf '%s' "${affiliate_rebates}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const rebate=data.data.find((item)=>item.order_id==='${affiliate_order_id}' && item.status==='pending'); if (!rebate) process.exit(1); console.log(rebate.id);")"

curl -fsS "${BASE_URL}/api/admin/v1/affiliate-rebates/${affiliate_rebate_id}/settle" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d '{}' \
  >/dev/null

finance_summary="$(
  curl -fsS "${BASE_URL}/api/admin/v1/finance/summary" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${finance_summary}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')).data; for (const key of ['paid_revenue_usd','refunded_usd','wallet_liability_usd','affiliate_pending_usd','usage_actual_cost_usd']) { if (!(key in data)) process.exit(1); }"

models_response="$(
  curl -fsS "${BASE_URL}/v1/models" \
    -H "Authorization: Bearer ${api_key_secret}"
)"
printf '%s' "${models_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (!data.data.some((model) => model.id === '${MODEL_NAME}') || data.data.some((model) => model.id === '${DENIED_MODEL_NAME}')) process.exit(1);"

portal_models="$(
  curl -fsS "${BASE_URL}/api/portal/v1/models" \
    -H "Authorization: Bearer ${portal_token}"
)"
printf '%s' "${portal_models}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const model=data.data.find((item) => item.model_name === '${MODEL_NAME}'); if (!model || !model.aliases.includes('${MODEL_ALIAS}')) process.exit(1);"

chat_response="$(
  curl -fsS "${BASE_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${api_key_secret}" \
    -H "X-Request-Id: smoke-chat-${RUN_ID}" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"${MODEL_NAME}\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}"
)"
printf '%s' "${chat_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (data.id !== 'chatcmpl_mock') process.exit(1);"

curl -fsS "${BASE_URL}/api/admin/v1/risk-controls" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"rule_type\":\"sensitive_word\",\"name\":\"Smoke Risk ${RUN_ID}\",\"pattern\":\"smoke-block-${RUN_ID}\",\"action\":\"block\",\"severity\":\"critical\"}" \
  >/dev/null

risk_status="$(
  curl -sS -o /tmp/elucid-relay-risk.json -w '%{http_code}' "${BASE_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${api_key_secret}" \
    -H "X-Request-Id: smoke-risk-${RUN_ID}" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"${MODEL_NAME}\",\"messages\":[{\"role\":\"user\",\"content\":\"smoke-block-${RUN_ID}\"}]}"
)"
if [ "${risk_status}" != "403" ]; then
  cat /tmp/elucid-relay-risk.json
  exit 1
fi

risk_events="$(
  curl -fsS "${BASE_URL}/api/admin/v1/risk-events?limit=20" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${risk_events}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (!data.data.some((row) => row.request_id === 'smoke-risk-${RUN_ID}' && row.action === 'block')) process.exit(1);"

WS_URL="${BASE_URL/http/ws}/v1/realtime?model=${MODEL_NAME}&session_id=smoke-ws-${RUN_ID}" \
API_KEY_SECRET="${api_key_secret}" \
node -e '
const crypto = require("node:crypto");
const net = require("node:net");
const url = new URL(process.env.WS_URL);
const port = Number(url.port || (url.protocol === "wss:" ? 443 : 80));
const key = crypto.randomBytes(16).toString("base64");
let buffer = Buffer.alloc(0);
const client = net.createConnection({ host: url.hostname, port }, () => {
  client.write([
    `GET ${url.pathname}${url.search} HTTP/1.1`,
    `Host: ${url.host}`,
    "Upgrade: websocket",
    "Connection: Upgrade",
    `Sec-WebSocket-Key: ${key}`,
    "Sec-WebSocket-Version: 13",
    `Authorization: Bearer ${process.env.API_KEY_SECRET}`,
    "OpenAI-Beta: realtime=v1",
    "X-Elucid-Relay-Session: smoke-session",
    "\r\n",
  ].join("\r\n"));
});
client.on("data", (chunk) => {
  buffer = Buffer.concat([buffer, chunk]);
  const headerEnd = buffer.indexOf("\r\n\r\n");
  if (headerEnd < 0) return;
  const head = buffer.slice(0, headerEnd).toString("utf8");
  if (!head.includes(" 101 ")) process.exit(1);
  const frame = buffer.slice(headerEnd + 4);
  if (frame.length < 2) return;
  const length = frame[1] & 0x7f;
  if (frame.length < 2 + length) return;
  const message = frame.slice(2, 2 + length).toString("utf8");
  if (message !== "realtime-ok") process.exit(1);
  client.end();
  process.exit(0);
});
client.on("error", () => process.exit(1));
setTimeout(() => process.exit(1), 5000);
'

duplicate_status="$(
  curl -sS -o /tmp/elucid-relay-duplicate.json -w '%{http_code}' "${BASE_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${api_key_secret}" \
    -H "X-Request-Id: smoke-chat-${RUN_ID}" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"${MODEL_NAME}\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}"
)"
if [ "${duplicate_status}" != "409" ]; then
  cat /tmp/elucid-relay-duplicate.json
  exit 1
fi

usage_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/usage?request_id=smoke-chat-${RUN_ID}&limit=1" \
    -H "Authorization: Bearer ${portal_token}"
)"
printf '%s' "${usage_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const row=data.data[0]; if (!row || row.status !== 'success' || row.requested_model !== '${MODEL_NAME}' || row.request_count < 1 || row.group_id !== '${group_id}' || row.billing_multiplier !== '3.0000000000') process.exit(1);"

admin_usage="$(
  curl -fsS "${BASE_URL}/api/admin/v1/usage?request_id=smoke-chat-${RUN_ID}&limit=1" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${admin_usage}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const row=data.data[0]; if (!row || row.requested_model !== '${MODEL_NAME}' || row.duration_ms < 0 || row.effective_policy.billing_multiplier !== '3.0000000000') process.exit(1);"

wallet_ledger="$(
  curl -fsS "${BASE_URL}/api/admin/v1/users/${user_id}/wallet/ledger?limit=30" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${wallet_ledger}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); for (const type of ['debit','payment','refund_reversal','subscription_credit']) { if (!data.data.some((row) => row.entry_type === type)) process.exit(1); }"
printf '%s' "${wallet_ledger}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); const debit=data.data.find((row)=>row.entry_type==='debit' && row.reference_id==='smoke-chat-${RUN_ID}'); if (!debit || !debit.metadata || debit.metadata.billing_multiplier !== 3) process.exit(1);"

claims="$(
  curl -fsS "${BASE_URL}/api/admin/v1/redeem-codes/$(printf '%s' "${redeem_batch}" | extract 'data.data.codes[0].id')/claims" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${claims}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (!data.data.some((row) => row.email === '${USER_EMAIL}')) process.exit(1);"

curl -fsS "${BASE_URL}/api/admin/v1/accounts/${account_id}/quality-action" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d '{"action":"isolate","reason":"smoke strategy event"}' \
  >/dev/null

curl -fsS "${BASE_URL}/api/admin/v1/accounts/${account_id}/quality-action" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d '{"action":"restore","reason":"smoke strategy event restore"}' \
  >/dev/null

strategy_events="$(
  curl -fsS "${BASE_URL}/api/admin/v1/account-pool-strategy-events?limit=20" \
    -H "Authorization: Bearer ${admin_token}"
)"
printf '%s' "${strategy_events}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (!data.data.some((row) => row.account_id === '${account_id}' && row.event_type === 'manual_quality_action' && row.action === 'isolate')) process.exit(1);"

echo "smoke ok: ${USER_EMAIL} ${MODEL_NAME}"
