import { runOnce } from "../apps/oauth-wrapper/src/runner.mjs";

const base = process.env.BASE_URL || "http://localhost:18080";
const wrapperToken = process.env.OAUTH_WRAPPER_BEARER_TOKEN || "local-oauth-wrapper-token";
const run = Date.now();

function assert(value, message) {
  if (!value) throw new Error(message);
}

async function request(method, path, token, body, expected = [200, 201], extraHeaders = {}) {
  const headers = { "content-type": "application/json", ...extraHeaders };
  if (token) headers.authorization = `Bearer ${token}`;
  const response = await fetch(`${base}${path}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!expected.includes(response.status)) {
    throw new Error(`${method} ${path} returned ${response.status}: ${text.slice(0, 400)}`);
  }
  return text ? JSON.parse(text) : {};
}

const adminLogin = await request("POST", "/api/admin/v1/auth/login", null, {
  email: process.env.OWNER_EMAIL || "owner@example.com",
  password: process.env.OWNER_PASSWORD || "change-me-please-32-chars",
});
const adminToken = adminLogin.data.session.session_token;

const modelName = `oauth-wrapper-model-${run}`;
await request("POST", "/api/admin/v1/models", adminToken, {
  model_name: modelName,
  display_name: "OAuth Wrapper Smoke Model",
  endpoint_capabilities: ["chat"],
  request_usd: "0",
  min_charge_usd: "0",
  public_visible: true,
});

const provider = await request("POST", "/api/admin/v1/providers", adminToken, {
  name: `OAuth Wrapper Provider ${run}`,
  provider_type: "openai_compatible",
});
const providerId = provider.data.id;

const providerClient = await request("POST", "/api/admin/v1/provider-clients", adminToken, {
  provider_id: providerId,
  name: `OAuth Wrapper Client ${run}`,
  client_type: "oauth_app",
  metadata: { wrapper_strategy: "mock", scopes: ["chat"] },
});
const providerClientId = providerClient.data.id;

const channel = await request("POST", "/api/admin/v1/channels", adminToken, {
  provider_id: providerId,
  name: `OAuth Wrapper Channel ${run}`,
  base_url: "http://mock-upstream:8090",
  abilities: [{ model_name: modelName, endpoint: "chat" }],
});
const channelId = channel.data.id;

const portalRegister = await request("POST", "/api/portal/v1/auth/register", null, {
  email: `oauth-wrapper-${run}@example.com`,
  password: `oauth-wrapper-password-${run}`,
  display_name: "OAuth Wrapper Smoke User",
});
const portalToken = portalRegister.data.session.session_token;
const userId = portalRegister.data.user.id;

const account = await request("POST", "/api/portal/v1/oauth/accounts", portalToken, {
  provider_id: providerId,
  channel_id: channelId,
  provider_client_id: providerClientId,
  name: `OAuth Wrapper Account ${run}`,
  auth_mode: "mock",
});
const accountId = account.data.id;
assert(account.data.auth_status === "pending", "new wrapper account should be pending");

const pendingExplain = await request("GET", `/api/admin/v1/runtime/route-explain?model=${modelName}&endpoint=chat&routing_mode=byo&user_id=${userId}`, adminToken);
assert(pendingExplain.data.available === false, "pending account should not route");

const processed = await runOnce({
  baseUrl: base,
  token: wrapperToken,
  leaseOwner: `oauth-wrapper-smoke-${run}`,
  leaseSeconds: 300,
  supportedModes: ["mock"],
  providerName: "",
  providerType: "",
  authMode: "",
}, { logger: { log() {}, error(message) { process.stderr.write(`${message}\n`); } } });
assert(processed === true, "wrapper should process one OAuth job");

const states = await request("GET", `/api/admin/v1/account-auth-states?account_id=${accountId}`, adminToken);
assert(states.data[0]?.auth_status === "active", "wrapper should activate account auth state");

await request("PATCH", `/api/admin/v1/account-auth-states/${accountId}`, adminToken, {
  auth_status: "refresh_due",
  queue_job: true,
});
const refreshed = await runOnce({
  baseUrl: base,
  token: wrapperToken,
  leaseOwner: `oauth-wrapper-smoke-refresh-${run}`,
  leaseSeconds: 300,
  supportedModes: ["mock"],
  providerName: "",
  providerType: "",
  authMode: "",
}, { logger: { log() {}, error(message) { process.stderr.write(`${message}\n`); } } });
assert(refreshed === true, "wrapper should process one refresh job");
const refreshedStates = await request("GET", `/api/admin/v1/account-auth-states?account_id=${accountId}`, adminToken);
assert(refreshedStates.data[0]?.auth_status === "active", "wrapper refresh should return auth state to active");

const activeExplain = await request("GET", `/api/admin/v1/runtime/route-explain?model=${modelName}&endpoint=chat&routing_mode=byo&user_id=${userId}`, adminToken);
assert(activeExplain.data.available === true, "active account should route");
assert(activeExplain.data.selected.account_id === accountId, "route explain should select the wrapper account");
assert(activeExplain.data.selected.auth_status === "active", "route explain should show active auth");

const key = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `oauth-wrapper-key-${run}`,
  routing_mode: "byo",
  model_scope: [modelName],
});
const requestId = `oauth-wrapper-chat-${run}`;
const chat = await request("POST", "/v1/chat/completions", key.data.secret, {
  model: modelName,
  messages: [{ role: "user", content: "ping" }],
}, [200], { "x-request-id": requestId });
assert(chat.id === "chatcmpl_mock", "BYO wrapper account should reach mock upstream");

const usage = await request("GET", "/api/portal/v1/usage?limit=10", portalToken);
const usageRow = usage.data.find((row) => row.request_id === requestId);
assert(usageRow?.status === "success", "BYO wrapper usage should be success");
assert(Number(usageRow.actual_cost) === 0, "BYO wrapper usage should cost zero");

const poolKey = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `oauth-wrapper-pool-key-${run}`,
  routing_mode: "pool",
  model_scope: [modelName],
});
const poolResponse = await fetch(`${base}/v1/chat/completions`, {
  method: "POST",
  headers: { authorization: `Bearer ${poolKey.data.secret}`, "content-type": "application/json" },
  body: JSON.stringify({ model: modelName, messages: [{ role: "user", content: "ping" }] }),
});
assert(poolResponse.status === 503, `pool key must not fall back to BYO account, got ${poolResponse.status}`);

console.log(`oauth-wrapper smoke ok: ${modelName} ${accountId}`);
