#!/usr/bin/env node
import crypto from "node:crypto";
import path from "node:path";
import os from "node:os";
import { runOnce } from "../apps/oauth-wrapper/src/runner.mjs";

const enabled = truthy(process.env.E2E_CODEX_CHATGPT_RESPONSES);
if (!enabled) {
  console.log("codex-chatgpt-responses e2e skipped: set E2E_CODEX_CHATGPT_RESPONSES=1");
  process.exit(0);
}

const base = env("BASE_URL", "http://localhost:18080").replace(/\/$/, "");
const wrapperToken = env("OAUTH_WRAPPER_BEARER_TOKEN", "local-oauth-wrapper-token");
const codexAuthFile = env("CODEX_AUTH_FILE", path.join(os.homedir(), ".codex", "auth.json"));
const upstreamBaseURL = env("E2E_CODEX_CHATGPT_BASE_URL", "https://chatgpt.com/backend-api/codex").replace(/\/$/, "");
const upstreamModel = env("E2E_CODEX_CHATGPT_MODEL", "gpt-5.5");
const run = `${Date.now()}-${crypto.randomBytes(3).toString("hex")}`;

const adminLogin = await request("POST", "/api/admin/v1/auth/login", null, {
  email: env("OWNER_EMAIL", "owner@example.com"),
  password: env("OWNER_PASSWORD", "change-me-please-32-chars"),
});
const adminToken = adminLogin.data.session.session_token;

const modelName = `codex-chatgpt-responses-e2e-${run}`;
await request("POST", "/api/admin/v1/models", adminToken, {
  model_name: modelName,
  display_name: "Codex ChatGPT Responses E2E Model",
  endpoint_capabilities: ["responses"],
  request_usd: "0",
  min_charge_usd: "0",
  public_visible: true,
});

const provider = await request("POST", "/api/admin/v1/providers", adminToken, {
  name: `Codex ChatGPT Responses Provider ${run}`,
  provider_type: "codex_compatible",
});
const providerId = provider.data.id;

const providerClient = await request("POST", "/api/admin/v1/provider-clients", adminToken, {
  provider_id: providerId,
  name: `Codex ChatGPT Responses Client ${run}`,
  client_type: "oauth_app",
  metadata: { wrapper_strategy: "codex_cli", auth_file: codexAuthFile },
});
const providerClientId = providerClient.data.id;

const channel = await request("POST", "/api/admin/v1/channels", adminToken, {
  provider_id: providerId,
  name: `Codex ChatGPT Responses Channel ${run}`,
  base_url: upstreamBaseURL,
  abilities: [{
    model_name: modelName,
    endpoint: "responses",
    upstream_model: upstreamModel,
    transform_capability: { mode: "native", lossless: true },
  }],
  metadata: { e2e_run_id: run, upstream_model: upstreamModel },
});
const channelId = channel.data.id;

const portalRegister = await request("POST", "/api/portal/v1/auth/register", null, {
  email: `codex-chatgpt-responses-e2e-${run}@example.com`,
  password: `codex-chatgpt-responses-e2e-password-${run}`,
  display_name: "Codex ChatGPT Responses E2E User",
});
const portalToken = portalRegister.data.session.session_token;
const userId = portalRegister.data.user.id;

const account = await request("POST", "/api/portal/v1/oauth/accounts", portalToken, {
  provider_id: providerId,
  channel_id: channelId,
  provider_client_id: providerClientId,
  name: `Codex ChatGPT Responses Account ${run}`,
  auth_mode: "codex_cli",
});
const accountId = account.data.id;
assert(account.data.auth_status === "pending", "Codex ChatGPT account should start pending");

const processed = await runOnce({
  baseUrl: base,
  token: wrapperToken,
  leaseOwner: `codex-chatgpt-responses-e2e-${run}`,
  leaseSeconds: 300,
  supportedModes: ["codex_cli"],
  authMode: "codex_cli",
}, { logger: silentLogger() });
assert(processed === true, "wrapper should process the Codex OAuth job");

const states = await request("GET", `/api/admin/v1/account-auth-states?account_id=${accountId}`, adminToken);
assert(states.data[0]?.auth_status === "active", "Codex wrapper should activate account auth state");
assert(states.data[0]?.provider_subject, "Codex auth state should record provider subject");

const explain = await request("GET", `/api/admin/v1/runtime/route-explain?model=${encodeURIComponent(modelName)}&endpoint=responses&routing_mode=byo&user_id=${encodeURIComponent(userId)}`, adminToken);
assert(explain.data.available === true, "Codex ChatGPT BYO account should route");
assert(explain.data.selected.account_id === accountId, "route explain should select the Codex account");

const apiKey = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `codex-chatgpt-responses-e2e-key-${run}`,
  routing_mode: "byo",
  model_scope: [modelName],
});

const requestId = `codex-chatgpt-responses-e2e-${run}`;
const response = await rawRequest("POST", "/v1/responses", apiKey.data.secret, {
  model: modelName,
  input: [{
    type: "message",
    role: "user",
    content: [{ type: "input_text", text: "Say ok." }],
  }],
  tools: [],
  tool_choice: "auto",
  parallel_tool_calls: true,
  reasoning: { effort: "low", summary: null },
  stream: true,
}, {
  "accept": "text/event-stream",
  "x-request-id": requestId,
  "x-elucid-relay-session": requestId,
});

const usage = await request("GET", "/api/portal/v1/usage?limit=20", portalToken);
const usageRow = usage.data.find((row) => row.request_id === requestId);
assert(usageRow, "Codex ChatGPT usage row should be recorded");
assert(Number(usageRow.actual_cost) === 0, "Codex ChatGPT BYO usage should cost zero");

if (!response.ok) {
  throw new Error(`Codex ChatGPT /responses failed ${response.status}: ${safeBody(response.text)}`);
}
assert(response.text.includes("data:"), "Codex ChatGPT /responses should return an SSE stream");
assert(usageRow.status === "success", "Codex ChatGPT usage should be success");

console.log(`codex-chatgpt-responses e2e ok: ${modelName} upstream=${upstreamBaseURL} model=${upstreamModel}`);

async function request(method, requestPath, token, body, expected = [200, 201]) {
  const headers = { "content-type": "application/json" };
  if (token) headers.authorization = `Bearer ${token}`;
  const response = await fetch(`${base}${requestPath}`, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!expected.includes(response.status)) {
    throw new Error(`${method} ${requestPath} returned ${response.status}: ${safeBody(text)}`);
  }
  return text ? JSON.parse(text) : {};
}

async function rawRequest(method, requestPath, token, body, extraHeaders = {}) {
  const response = await fetch(`${base}${requestPath}`, {
    method,
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
      ...extraHeaders,
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  return { ok: response.ok, status: response.status, text };
}

function safeBody(value) {
  return String(value || "").slice(0, 500);
}

function silentLogger() {
  return {
    log() {},
    error(message) {
      process.stderr.write(`${message}\n`);
    },
  };
}

function assert(value, message) {
  if (!value) throw new Error(message);
}

function env(name, fallback) {
  return process.env[name] || fallback;
}

function truthy(value) {
  return ["1", "true", "yes", "on"].includes(String(value || "").trim().toLowerCase());
}
