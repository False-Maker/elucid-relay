import { runOnce } from "../apps/oauth-wrapper/src/runner.mjs";

const base = process.env.BASE_URL || "http://localhost:18080";
const wrapperToken = process.env.OAUTH_WRAPPER_BEARER_TOKEN || "local-oauth-wrapper-token";
const codexAuthFile = process.env.CODEX_AUTH_FILE || "/home/elucid/.codex/auth.json";
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

async function requestWithHeaders(method, path, token, body, expected = [200, 201], extraHeaders = {}) {
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
  return { headers: response.headers, body: text ? JSON.parse(text) : {} };
}

const adminLogin = await request("POST", "/api/admin/v1/auth/login", null, {
  email: process.env.OWNER_EMAIL || "owner@example.com",
  password: process.env.OWNER_PASSWORD || "change-me-please-32-chars",
});
const adminToken = adminLogin.data.session.session_token;

const modelName = `codex-cli-model-${run}`;
await request("POST", "/api/admin/v1/models", adminToken, {
  model_name: modelName,
  display_name: "Codex CLI Smoke Model",
  endpoint_capabilities: ["chat"],
  request_usd: "0",
  min_charge_usd: "0",
  public_visible: true,
});

const provider = await request("POST", "/api/admin/v1/providers", adminToken, {
  name: `Codex CLI Provider ${run}`,
  provider_type: "openai_compatible",
});
const providerId = provider.data.id;

const providerClient = await request("POST", "/api/admin/v1/provider-clients", adminToken, {
  provider_id: providerId,
  name: `Codex CLI Client ${run}`,
  client_type: "oauth_app",
  metadata: { wrapper_strategy: "codex_cli", auth_file: codexAuthFile },
});
const providerClientId = providerClient.data.id;

const channel = await request("POST", "/api/admin/v1/channels", adminToken, {
  provider_id: providerId,
  name: `Codex CLI Channel ${run}`,
  base_url: "http://mock-upstream:8090",
  abilities: [{ model_name: modelName, endpoint: "chat" }],
});
const channelId = channel.data.id;

const portalRegister = await request("POST", "/api/portal/v1/auth/register", null, {
  email: `codex-cli-${run}@example.com`,
  password: `codex-cli-password-${run}`,
  display_name: "Codex CLI Smoke User",
});
const portalToken = portalRegister.data.session.session_token;
const userId = portalRegister.data.user.id;

const account = await request("POST", "/api/portal/v1/oauth/accounts", portalToken, {
  provider_id: providerId,
  channel_id: channelId,
  provider_client_id: providerClientId,
  name: `Codex CLI Account ${run}`,
  auth_mode: "codex_cli",
});
const accountId = account.data.id;
assert(account.data.auth_status === "pending", "Codex CLI account should start pending");

const processed = await runOnce({
  baseUrl: base,
  token: wrapperToken,
  leaseOwner: `codex-cli-smoke-${run}`,
  leaseSeconds: 300,
  supportedModes: ["codex_cli"],
  providerName: "",
  providerType: "",
  authMode: "codex_cli",
}, { logger: { log() {}, error(message) { process.stderr.write(`${message}\n`); } } });
assert(processed === true, "wrapper should process the Codex CLI OAuth job");

const states = await request("GET", `/api/admin/v1/account-auth-states?account_id=${accountId}`, adminToken);
assert(states.data[0]?.auth_status === "active", "Codex CLI wrapper should activate account auth state");
assert(states.data[0]?.provider_subject, "Codex CLI auth state should record provider subject");

const explain = await request("GET", `/api/admin/v1/runtime/route-explain?model=${modelName}&endpoint=chat&routing_mode=byo&user_id=${userId}`, adminToken);
assert(explain.data.available === true, "Codex CLI BYO account should route");
assert(explain.data.selected.account_id === accountId, "route explain should select Codex CLI account");
assert(explain.data.selected.auth_status === "active", "route explain should show active Codex auth");

const key = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `codex-cli-key-${run}`,
  routing_mode: "byo",
  model_scope: [modelName],
});
const requestId = `codex-cli-chat-${run}`;
const chat = await requestWithHeaders("POST", "/v1/chat/completions", key.data.secret, {
  model: modelName,
  messages: [{ role: "user", content: "ping" }],
}, [200], { "x-request-id": requestId });
assert(chat.body.id === "chatcmpl_mock", "Codex CLI BYO route should reach mock upstream");
assert(chat.headers.get("x-mock-upstream-auth") === "bearer", "mock upstream should confirm relay-injected upstream auth");

const usage = await request("GET", "/api/portal/v1/usage?limit=10", portalToken);
const usageRow = usage.data.find((row) => row.request_id === requestId);
assert(usageRow?.status === "success", "Codex CLI usage should be success");
assert(Number(usageRow.actual_cost) === 0, "Codex CLI BYO usage should cost zero");

console.log(`codex-cli smoke ok: ${modelName} ${accountId}`);
