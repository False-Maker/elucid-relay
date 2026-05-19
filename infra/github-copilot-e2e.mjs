#!/usr/bin/env node
import crypto from "node:crypto";

const enabled = truthy(process.env.E2E_GITHUB_COPILOT);
if (!enabled) {
  console.log("github-copilot e2e skipped: set E2E_GITHUB_COPILOT=1");
  process.exit(0);
}

const base = env("BASE_URL", "http://localhost:18080").replace(/\/$/, "");
const accountType = githubCopilotAccountType();
const upstreamBaseURL = env("E2E_GITHUB_COPILOT_BASE_URL", githubCopilotBaseURL(accountType)).replace(/\/$/, "");
const upstreamModel = env("E2E_GITHUB_COPILOT_MODEL", "gpt-5.1");
const allowExpectedAuthError = truthy(process.env.E2E_GITHUB_COPILOT_ALLOW_EXPECTED_AUTH_ERROR);
const run = `${Date.now()}-${crypto.randomBytes(3).toString("hex")}`;

const rawCredentials = await resolveGitHubCopilotCredentials();
if (truthy(process.env.E2E_GITHUB_COPILOT_DRY_RUN)) {
  console.log(`github-copilot e2e dry-run ok: credential_source=${rawCredentials.source || "none"} upstream=${upstreamBaseURL} model=${upstreamModel}`);
  process.exit(0);
}

const credentials = await ensureCopilotAccessToken(rawCredentials);
if (!credentials.accessToken) {
  throw new Error(
    "GitHub Copilot credentials are not available. Set E2E_GITHUB_COPILOT_ACCESS_TOKEN " +
      "or set E2E_GITHUB_COPILOT_GITHUB_TOKEN plus E2E_GITHUB_COPILOT_ALLOW_TOKEN_EXCHANGE=1. " +
      "This runner does not launch GitHub login.",
  );
}

const adminLogin = await request("POST", "/api/admin/v1/auth/login", null, {
  email: env("OWNER_EMAIL", "owner@example.com"),
  password: env("OWNER_PASSWORD", "change-me-please-32-chars"),
});
const adminToken = adminLogin.data.session.session_token;

const modelName = `github-copilot-e2e-${run}`;
await request("POST", "/api/admin/v1/models", adminToken, {
  model_name: modelName,
  display_name: "GitHub Copilot E2E Model",
  endpoint_capabilities: ["chat"],
  request_usd: "0",
  min_charge_usd: "0",
  public_visible: true,
});

const provider = await request("POST", "/api/admin/v1/providers", adminToken, {
  name: `GitHub Copilot E2E Provider ${run}`,
  provider_type: "github_copilot",
});
const providerId = provider.data.id;

const providerClient = await request("POST", "/api/admin/v1/provider-clients", adminToken, {
  provider_id: providerId,
  name: `GitHub Copilot E2E Client ${run}`,
  client_type: "oauth_app",
  metadata: githubCopilotMetadata(),
});
const providerClientId = providerClient.data.id;

const channel = await request("POST", "/api/admin/v1/channels", adminToken, {
  provider_id: providerId,
  name: `GitHub Copilot E2E Channel ${run}`,
  base_url: upstreamBaseURL,
  abilities: [{
    model_name: modelName,
    endpoint: "chat",
    upstream_model: upstreamModel,
    transform_capability: { mode: "native", lossless: true },
  }],
  metadata: {
    e2e_run_id: run,
    upstream_model: upstreamModel,
    ...githubCopilotMetadata(),
  },
});
const channelId = channel.data.id;

const portalRegister = await request("POST", "/api/portal/v1/auth/register", null, {
  email: `github-copilot-e2e-${run}@example.com`,
  password: `github-copilot-e2e-password-${run}`,
  display_name: "GitHub Copilot E2E User",
});
const portalToken = portalRegister.data.session.session_token;
const userId = portalRegister.data.user.id;

const account = await request("POST", "/api/portal/v1/oauth/accounts", portalToken, {
  provider_id: providerId,
  channel_id: channelId,
  provider_client_id: providerClientId,
  name: `GitHub Copilot E2E Account ${run}`,
  auth_mode: "github_device",
  token_bundle: githubCopilotTokenBundle(credentials),
});
const accountId = account.data.id;
assert(account.data.auth_status === "active", "GitHub Copilot account should start active when token_bundle is supplied");

const explain = await request("GET", `/api/admin/v1/runtime/route-explain?model=${encodeURIComponent(modelName)}&endpoint=chat&routing_mode=byo&user_id=${encodeURIComponent(userId)}`, adminToken);
assert(explain.data.available === true, "GitHub Copilot BYO account should route");
assert(explain.data.selected.account_id === accountId, "route explain should select the GitHub Copilot account");
assert(explain.data.selected.auth_status === "active", "route explain should show active GitHub Copilot auth");

const apiKey = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `github-copilot-e2e-key-${run}`,
  routing_mode: "byo",
  model_scope: [modelName],
});

const chatRequestID = `github-copilot-e2e-chat-${run}`;
const chat = await rawRequest("POST", "/v1/chat/completions", apiKey.data.secret, {
  model: modelName,
  messages: [{ role: "user", content: "Say ok." }],
  max_tokens: 8,
}, { "x-request-id": chatRequestID, "x-elucid-relay-session": chatRequestID });

const usage = await request("GET", "/api/portal/v1/usage?limit=20", portalToken);
const chatUsage = usage.data.find((row) => row.request_id === chatRequestID);
assert(chatUsage, "GitHub Copilot usage row should be recorded");
assert(Number(chatUsage.actual_cost) === 0, "GitHub Copilot BYO usage should cost zero");

if (!chat.response.ok) {
  if (!allowExpectedAuthError || !isExpectedGitHubCopilotAuthError(chat.response.status, chat.body, chat.bodyText)) {
    throw new Error(`GitHub Copilot request failed ${chat.response.status}: ${safeBody(chat.bodyText)}`);
  }
  assert(chatUsage.status === "failed", "expected-auth-error mode should record a failed usage row");
  console.log(`github-copilot e2e partial: credential reached Copilot but was rejected as expected (${chat.response.status}) account=${accountId} source=${credentials.source}`);
  process.exit(0);
}

assertRecognizedChatResponse(chat.body);
assert(chatUsage.status === "success", "GitHub Copilot usage should be success");

const streamRequestID = `github-copilot-e2e-stream-${run}`;
const stream = await rawRequest("POST", "/v1/chat/completions", apiKey.data.secret, {
  model: modelName,
  stream: true,
  stream_options: { include_usage: true },
  messages: [{ role: "user", content: "Say ok." }],
  max_tokens: 8,
}, {
  accept: "text/event-stream",
  "x-request-id": streamRequestID,
  "x-elucid-relay-session": streamRequestID,
});
if (!stream.response.ok) {
  throw new Error(`GitHub Copilot stream request failed ${stream.response.status}: ${safeBody(stream.bodyText)}`);
}
assert(stream.bodyText.includes("data:"), "GitHub Copilot stream should return SSE data");

const usageAfterStream = await request("GET", "/api/portal/v1/usage?limit=20", portalToken);
const streamUsage = usageAfterStream.data.find((row) => row.request_id === streamRequestID);
assert(streamUsage?.status === "success", "GitHub Copilot stream usage should be success");
assert(Number(streamUsage.actual_cost) === 0, "GitHub Copilot stream BYO usage should cost zero");

console.log(`github-copilot e2e ok: account=${accountId} upstream=${upstreamBaseURL} model=${upstreamModel} credential_source=${credentials.source}`);

async function resolveGitHubCopilotCredentials() {
  if (process.env.E2E_GITHUB_COPILOT_ACCESS_TOKEN || process.env.GITHUB_COPILOT_ACCESS_TOKEN) {
    return {
      source: process.env.E2E_GITHUB_COPILOT_ACCESS_TOKEN ? "E2E_GITHUB_COPILOT_ACCESS_TOKEN" : "GITHUB_COPILOT_ACCESS_TOKEN",
      accessToken: process.env.E2E_GITHUB_COPILOT_ACCESS_TOKEN || process.env.GITHUB_COPILOT_ACCESS_TOKEN,
      githubToken: process.env.E2E_GITHUB_COPILOT_GITHUB_TOKEN || "",
      expiresAt: normalizeExpiresAt(process.env.E2E_GITHUB_COPILOT_EXPIRES_AT),
      subject: process.env.E2E_GITHUB_COPILOT_SUBJECT || "",
    };
  }
  if (process.env.E2E_GITHUB_COPILOT_GITHUB_TOKEN) {
    return {
      source: "E2E_GITHUB_COPILOT_GITHUB_TOKEN",
      accessToken: "",
      githubToken: process.env.E2E_GITHUB_COPILOT_GITHUB_TOKEN,
      expiresAt: "",
      subject: process.env.E2E_GITHUB_COPILOT_SUBJECT || "",
    };
  }
  return { source: "", accessToken: "", githubToken: "", expiresAt: "", subject: "" };
}

async function ensureCopilotAccessToken(credentials) {
  if (credentials.accessToken) return credentials;
  if (!credentials.githubToken) return credentials;
  if (!truthy(process.env.E2E_GITHUB_COPILOT_ALLOW_TOKEN_EXCHANGE)) {
    throw new Error(
      "Only a GitHub OAuth token is available. Set E2E_GITHUB_COPILOT_ALLOW_TOKEN_EXCHANGE=1 " +
        "to exchange it for a Copilot API token. This runner will not exchange tokens implicitly.",
    );
  }
  const response = await fetch(`${env("E2E_GITHUB_COPILOT_GITHUB_API_BASE_URL", "https://api.github.com").replace(/\/$/, "")}/copilot_internal/v2/token`, {
    headers: githubTokenExchangeHeaders(credentials.githubToken),
  });
  const text = await response.text();
  const data = parseJSON(text);
  if (!response.ok || !data.token) {
    throw new Error(`GitHub Copilot token exchange failed ${response.status}: ${safeBody(text)}`);
  }
  return {
    ...credentials,
    source: `${credentials.source}:copilot_token_exchange`,
    accessToken: data.token,
    expiresAt: data.expires_at ? new Date(Number(data.expires_at) * 1000).toISOString() : credentials.expiresAt,
    copilotExpiresAtEpoch: Number(data.expires_at || 0),
    copilotRefreshIn: Number(data.refresh_in || 0),
  };
}

function githubTokenExchangeHeaders(githubToken) {
  return {
    accept: "application/json",
    "content-type": "application/json",
    authorization: `token ${githubToken}`,
    "editor-version": `vscode/${githubCopilotVSCodeVersion()}`,
    "editor-plugin-version": `copilot-chat/${githubCopilotClientVersion()}`,
    "user-agent": githubCopilotUserAgent(),
    "x-github-api-version": githubCopilotAPIVersion(),
    "x-vscode-user-agent-library-version": githubCopilotFetchLibraryVersion(),
  };
}

function githubCopilotTokenBundle(credentials) {
  return {
    type: "oauth",
    access_token: credentials.accessToken,
    refresh_token: credentials.githubToken || "",
    expires_at: credentials.expiresAt || "",
    scopes: ["read:user"],
    provider: "github_copilot",
    auth_scheme: "bearer",
    subject: credentials.subject || "",
    metadata: {
      ...githubCopilotMetadata(),
      source: credentials.source,
      github_token_type: credentials.githubToken ? "bearer" : "",
      copilot_base_url: upstreamBaseURL,
      copilot_api_base_url: env("E2E_GITHUB_COPILOT_GITHUB_API_BASE_URL", "https://api.github.com"),
      copilot_refresh_in: Number(credentials.copilotRefreshIn || 0),
      copilot_expires_at_epoch: Number(credentials.copilotExpiresAtEpoch || 0),
    },
  };
}

function githubCopilotMetadata() {
  return {
    wrapper_strategy: "github_copilot",
    auth_mode: "github_device",
    client_id: env("GITHUB_COPILOT_CLIENT_ID", "Iv1.b507a08c87ecfe98"),
    scopes: ["read:user"],
    client_version: githubCopilotClientVersion(),
    vscode_version: githubCopilotVSCodeVersion(),
    user_agent: githubCopilotUserAgent(),
    api_version: githubCopilotAPIVersion(),
    account_type: accountType,
    copilot_integration_id: "vscode-chat",
    openai_intent: env("E2E_GITHUB_COPILOT_OPENAI_INTENT", "conversation-panel"),
    interaction_type: env("E2E_GITHUB_COPILOT_INTERACTION_TYPE", env("E2E_GITHUB_COPILOT_OPENAI_INTENT", "conversation-panel")),
    vscode_user_agent_library_version: githubCopilotFetchLibraryVersion(),
  };
}

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
  return { response, bodyText, body: parseJSON(bodyText) };
}

function assertRecognizedChatResponse(body) {
  assert(body && typeof body === "object", "chat response should be a JSON object");
  assert(Array.isArray(body.choices), "chat response schema was not recognized");
}

function isExpectedGitHubCopilotAuthError(status, body, bodyText) {
  const text = JSON.stringify(body?.error || body || {}) + " " + String(bodyText || "");
  return [401, 403, 429].includes(status) &&
    /(auth|token|unauthorized|forbidden|rate|quota|subscription|copilot|access|abuse|exceeded)/i.test(text);
}

function githubCopilotBaseURL(type) {
  if (type === "business") return "https://api.business.githubcopilot.com";
  if (type === "enterprise") return "https://api.enterprise.githubcopilot.com";
  return "https://api.githubcopilot.com";
}

function githubCopilotAccountType() {
  const value = String(process.env.E2E_GITHUB_COPILOT_ACCOUNT_TYPE || process.env.GITHUB_COPILOT_ACCOUNT_TYPE || "individual").trim().toLowerCase();
  if (["business", "enterprise"].includes(value)) return value;
  return "individual";
}

function githubCopilotClientVersion() {
  return env("GITHUB_COPILOT_CLIENT_VERSION", "0.44.0");
}

function githubCopilotVSCodeVersion() {
  return env("GITHUB_COPILOT_VSCODE_VERSION", "1.109.3");
}

function githubCopilotUserAgent() {
  return env("GITHUB_COPILOT_USER_AGENT", `GitHubCopilotChat/${githubCopilotClientVersion()}`);
}

function githubCopilotAPIVersion() {
  return env("GITHUB_COPILOT_API_VERSION", "2025-05-01");
}

function githubCopilotFetchLibraryVersion() {
  return env("GITHUB_COPILOT_FETCH_LIBRARY_VERSION", "electron-fetch");
}

function parseJSON(text) {
  if (!text) return {};
  try {
    return JSON.parse(text);
  } catch {
    return { raw: text };
  }
}

function normalizeExpiresAt(value) {
  if (!value) return "";
  if (typeof value === "number") return new Date(value).toISOString();
  if (/^\d+$/.test(String(value))) return new Date(Number(value)).toISOString();
  return String(value);
}

function safeBody(value) {
  return String(value || "")
    .replace(/gh[opsu]_[A-Za-z0-9_]+/g, "[REDACTED]")
    .replace(/Bearer\s+[A-Za-z0-9._-]+/gi, "Bearer [REDACTED]")
    .replace(/token\s+[A-Za-z0-9._-]+/gi, "token [REDACTED]")
    .slice(0, 700);
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
