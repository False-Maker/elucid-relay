#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { runOnce } from "../apps/oauth-wrapper/src/runner.mjs";

const enabled = truthy(process.env.E2E_CODEX_OPENAI || process.env.E2E_OPENAI_CODEX);
if (!enabled) {
  console.log("codex-openai e2e skipped: set E2E_CODEX_OPENAI=1");
  process.exit(0);
}

const base = env("BASE_URL", "http://localhost:18080").replace(/\/$/, "");
const wrapperToken = env("OAUTH_WRAPPER_BEARER_TOKEN", "local-oauth-wrapper-token");
const codexAuthFile = env("CODEX_AUTH_FILE", path.join(os.homedir(), ".codex", "auth.json"));
const upstreamBaseURL = env("E2E_CODEX_OPENAI_BASE_URL", "https://api.openai.com").replace(/\/$/, "");
const upstreamModel = env("E2E_CODEX_OPENAI_MODEL", "gpt-4.1-mini");
const providerType = env("E2E_CODEX_OPENAI_PROVIDER_TYPE", "openai_compatible");
const allowQuotaError = truthy(process.env.E2E_CODEX_OPENAI_ALLOW_QUOTA_ERROR);
const run = `${Date.now()}-${crypto.randomBytes(3).toString("hex")}`;

const codexAuth = await readCodexAuth(codexAuthFile);
const codexAccessToken = codexAuth.tokens.access_token;
assert(codexAccessToken, `Codex auth file has no access token: ${codexAuthFile}`);

const adminLogin = await request("POST", "/api/admin/v1/auth/login", null, {
  email: env("OWNER_EMAIL", "owner@example.com"),
  password: env("OWNER_PASSWORD", "change-me-please-32-chars"),
});
const adminToken = adminLogin.data.session.session_token;

const modelName = `codex-openai-e2e-${run}`;
await request("POST", "/api/admin/v1/models", adminToken, {
  model_name: modelName,
  display_name: "Codex OpenAI E2E Model",
  endpoint_capabilities: ["chat"],
  request_usd: "0",
  min_charge_usd: "0",
  public_visible: true,
});

const provider = await request("POST", "/api/admin/v1/providers", adminToken, {
  name: `Codex OpenAI E2E Provider ${run}`,
  provider_type: providerType,
});
const providerId = provider.data.id;

const providerClient = await request("POST", "/api/admin/v1/provider-clients", adminToken, {
  provider_id: providerId,
  name: `Codex OpenAI E2E Client ${run}`,
  client_type: "oauth_app",
  metadata: { wrapper_strategy: "codex_cli", auth_file: codexAuthFile },
});
const providerClientId = providerClient.data.id;

const channel = await request("POST", "/api/admin/v1/channels", adminToken, {
  provider_id: providerId,
  name: `Codex OpenAI E2E Channel ${run}`,
  base_url: upstreamBaseURL,
  abilities: [{
    model_name: modelName,
    endpoint: "chat",
    upstream_model: upstreamModel,
    transform_capability: { mode: "native", lossless: true },
  }],
  metadata: { e2e_run_id: run, upstream_model: upstreamModel },
});
const channelId = channel.data.id;

const portalRegister = await request("POST", "/api/portal/v1/auth/register", null, {
  email: `codex-openai-e2e-${run}@example.com`,
  password: `codex-openai-e2e-password-${run}`,
  display_name: "Codex OpenAI E2E User",
});
const portalToken = portalRegister.data.session.session_token;
const userId = portalRegister.data.user.id;

const account = await request("POST", "/api/portal/v1/oauth/accounts", portalToken, {
  provider_id: providerId,
  channel_id: channelId,
  provider_client_id: providerClientId,
  name: `Codex OpenAI E2E Account ${run}`,
  auth_mode: "codex_cli",
});
const accountId = account.data.id;
assert(account.data.auth_status === "pending", "Codex OpenAI account should start pending");

const processed = await runOnce({
  baseUrl: base,
  token: wrapperToken,
  leaseOwner: `codex-openai-e2e-${run}`,
  leaseSeconds: 300,
  supportedModes: ["codex_cli"],
  authMode: "codex_cli",
}, { logger: silentLogger() });
assert(processed === true, "wrapper should process the Codex OpenAI OAuth job");

const states = await request("GET", `/api/admin/v1/account-auth-states?account_id=${accountId}`, adminToken);
assert(states.data[0]?.auth_status === "active", "Codex OpenAI wrapper should activate account auth state");
assert(states.data[0]?.provider_subject, "Codex OpenAI auth state should record provider subject");

const explain = await request("GET", `/api/admin/v1/runtime/route-explain?model=${encodeURIComponent(modelName)}&endpoint=chat&routing_mode=byo&user_id=${encodeURIComponent(userId)}`, adminToken);
assert(explain.data.available === true, "Codex OpenAI BYO account should route");
assert(explain.data.selected.account_id === accountId, "route explain should select the Codex OpenAI account");
assert(explain.data.selected.auth_status === "active", "route explain should show active Codex auth");

const apiKey = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `codex-openai-e2e-key-${run}`,
  routing_mode: "byo",
  model_scope: [modelName],
});

const requestId = `codex-openai-e2e-chat-${run}`;
const chat = await rawRequest("POST", "/v1/chat/completions", apiKey.data.secret, {
  model: modelName,
  messages: [{ role: "user", content: "Say ok." }],
  max_tokens: 8,
}, { "x-request-id": requestId });

const usage = await request("GET", `/api/portal/v1/usage?limit=20`, portalToken);
const usageRow = usage.data.find((row) => row.request_id === requestId);
assert(usageRow, "Codex OpenAI usage row should be recorded");
assert(Number(usageRow.actual_cost) === 0, "Codex OpenAI BYO usage should cost zero");

if (chat.response.ok) {
  assertRecognizedChatResponse(chat.body);
  assert(usageRow.status === "success", "Codex OpenAI usage should be success");
  console.log(`codex-openai e2e ok: success ${modelName} upstream=${upstreamBaseURL} model=${upstreamModel}`);
  process.exit(0);
}

if (!allowQuotaError) {
  throw new Error(`Codex OpenAI gateway request failed ${chat.response.status}: ${safeBody(chat.bodyText)}`);
}

const direct = await directUpstreamProbe(codexAccessToken, upstreamBaseURL, upstreamModel);
assert(isQuotaError(direct.status, direct.body), `direct OpenAI probe was not an insufficient_quota response: ${direct.status} ${safeBody(direct.text)}`);
assert(usageRow.status === "failed", "Codex OpenAI quota-mode usage should be failed");
console.log(`codex-openai e2e ok: Codex OAuth reached real upstream, but account has no API quota (${direct.status} insufficient_quota) ${modelName}`);

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
  const bodyText = await response.text();
  return {
    response,
    bodyText,
    body: parseJSON(bodyText),
  };
}

async function directUpstreamProbe(accessToken, upstreamBase, model) {
  const response = await fetch(`${upstreamBase}/v1/chat/completions`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${accessToken}`,
      "content-type": "application/json",
    },
    body: JSON.stringify({
      model,
      messages: [{ role: "user", content: "Say ok." }],
      max_tokens: 8,
    }),
  });
  const text = await response.text();
  return { status: response.status, body: parseJSON(text), text };
}

async function readCodexAuth(filePath) {
  const parsed = parseJSON(await fs.readFile(filePath, "utf8"));
  if (!parsed?.tokens || typeof parsed.tokens !== "object") {
    throw new Error(`Invalid Codex auth file: ${filePath}`);
  }
  return parsed;
}

function assertRecognizedChatResponse(body) {
  assert(body && typeof body === "object", "chat response should be a JSON object");
  assert(body.id || Array.isArray(body.choices), "chat response schema was not recognized");
}

function isQuotaError(status, body) {
  const error = body?.error || {};
  return status === 429 && (error.code === "insufficient_quota" || /quota/i.test(String(error.message || "")));
}

function parseJSON(text) {
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

function safeBody(value) {
  const text = typeof value === "string" ? value : JSON.stringify(value);
  return text.slice(0, 500);
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
