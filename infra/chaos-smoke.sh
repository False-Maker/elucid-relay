#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:18080}"
OWNER_EMAIL="${OWNER_EMAIL:-owner@example.com}"
OWNER_PASSWORD="${OWNER_PASSWORD:-change-me-please-32-chars}"
RUN_ID="$(date +%s)"
USER_EMAIL="chaos-${RUN_ID}@example.com"
USER_PASSWORD="chaos-password-${RUN_ID}"
MODEL_NAME="chaos-model-${RUN_ID}"

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
  -d "{\"model_name\":\"${MODEL_NAME}\",\"display_name\":\"Chaos Model\",\"endpoint_capabilities\":[\"chat\",\"responses\",\"embeddings\",\"realtime\"],\"request_usd\":\"0\",\"min_charge_usd\":\"0.001\",\"public_visible\":true}" \
  >/dev/null

provider_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/providers" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"Chaos Provider ${RUN_ID}\",\"provider_type\":\"openai_compatible\"}"
)"
provider_id="$(printf '%s' "${provider_response}" | extract 'data.data.id')"

channel_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/channels" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"provider_id\":\"${provider_id}\",\"name\":\"Chaos Channel ${RUN_ID}\",\"base_url\":\"http://mock-upstream:8090\",\"abilities\":[{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"chat\"},{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"responses\"},{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"embeddings\"},{\"model_name\":\"${MODEL_NAME}\",\"endpoint\":\"realtime\"}]}"
)"
channel_id="$(printf '%s' "${channel_response}" | extract 'data.data.id')"

account_response="$(
  curl -fsS "${BASE_URL}/api/admin/v1/accounts" \
    -H "Authorization: Bearer ${admin_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"provider_id\":\"${provider_id}\",\"channel_id\":\"${channel_id}\",\"name\":\"Chaos Account ${RUN_ID}\",\"api_key\":\"mock-key\"}"
)"
account_id="$(printf '%s' "${account_response}" | extract 'data.data.id')"

curl -fsS "${BASE_URL}/api/admin/v1/account-quota-windows" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d "{\"account_id\":\"${account_id}\",\"window_type\":\"requests\",\"remaining\":\"10\",\"metadata\":{\"limit\":10}}" \
  >/dev/null

portal_register="$(
  curl -fsS "${BASE_URL}/api/portal/v1/auth/register" \
    -H 'Content-Type: application/json' \
    -d "{\"email\":\"${USER_EMAIL}\",\"password\":\"${USER_PASSWORD}\",\"display_name\":\"Chaos User\"}"
)"
portal_token="$(printf '%s' "${portal_register}" | extract 'data.data.session.session_token')"
user_id="$(printf '%s' "${portal_register}" | extract 'data.data.user.id')"

curl -fsS "${BASE_URL}/api/admin/v1/users/${user_id}/wallet/adjustments" \
  -H "Authorization: Bearer ${admin_token}" \
  -H 'Content-Type: application/json' \
  -d '{"entry_type":"credit","amount":"2.00","reason":"chaos"}' \
  >/dev/null

api_key_response="$(
  curl -fsS "${BASE_URL}/api/portal/v1/api-keys" \
    -H "Authorization: Bearer ${portal_token}" \
    -H 'Content-Type: application/json' \
    -d "{\"name\":\"chaos\",\"model_scope\":[\"${MODEL_NAME}\"]}"
)"
api_key_secret="$(printf '%s' "${api_key_response}" | extract 'data.data.secret')"

chat_response="$(
  curl -fsS "${BASE_URL}/v1/chat/completions" \
    -H "Authorization: Bearer ${api_key_secret}" \
    -H "X-Request-Id: chaos-chat-${RUN_ID}" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"${MODEL_NAME}\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}"
)"
printf '%s' "${chat_response}" | node -e "const fs=require('fs'); const data=JSON.parse(fs.readFileSync(0,'utf8')); if (!data.id) process.exit(1);"

echo "chaos smoke ok: ${USER_EMAIL} ${MODEL_NAME}"
