#!/usr/bin/env node
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

const enabled = truthy(process.env.E2E_GEMINI_CODEASSIST);
if (!enabled) {
  console.log("gemini-codeassist e2e skipped: set E2E_GEMINI_CODEASSIST=1");
  process.exit(0);
}
autoEnableNodeEnvProxy();

const base = env("BASE_URL", "http://localhost:18080").replace(/\/$/, "");
const upstreamBaseURL = env("E2E_GEMINI_CODEASSIST_BASE_URL", "https://cloudcode-pa.googleapis.com/v1internal").replace(/\/$/, "");
const upstreamModel = env("E2E_GEMINI_CODEASSIST_MODEL", "gemini-2.5-pro");
const configuredProject = env("E2E_GEMINI_CODEASSIST_PROJECT", env("GOOGLE_CLOUD_PROJECT", env("GOOGLE_CLOUD_PROJECT_ID", "")));
const allowExpectedAuthError = truthy(process.env.E2E_GEMINI_CODEASSIST_ALLOW_EXPECTED_AUTH_ERROR);
const run = `${Date.now()}-${crypto.randomBytes(3).toString("hex")}`;

const credentials = await ensureGeminiAccessToken(await resolveGeminiCredentials());
if (!credentials.accessToken) {
  throw new Error(
    "Gemini Code Assist credentials are not available. Set GOOGLE_CLOUD_ACCESS_TOKEN, " +
      "E2E_GEMINI_CODEASSIST_ACCESS_TOKEN, or GEMINI_OAUTH_CREDS_FILE. " +
      "This runner does not launch Google login.",
  );
}
let project = normalizeProjectId(configuredProject || credentials.project || "");
if (truthy(process.env.E2E_GEMINI_CODEASSIST_DRY_RUN)) {
  console.log(`gemini-codeassist e2e dry-run ok: credential_source=${credentials.source} upstream=${upstreamBaseURL} model=${upstreamModel} project=${project || "auto"}`);
  process.exit(0);
}

const discovery = await discoverGeminiCodeAssistProject(credentials, project);
if (discovery.project) project = discovery.project;
if (discovery.error) {
  const classification = classifyGoogleCodeAssistError(discovery.status, discovery.error);
  if (allowExpectedAuthError && classification.expected) {
    console.log(`gemini-codeassist e2e partial: project discovery reached Google but was rejected as expected (${discovery.status} ${classification.code}) source=${credentials.source}`);
    process.exit(0);
  }
  throw new Error(`Gemini Code Assist project discovery failed ${discovery.status}: ${safeBody(discovery.bodyText)}`);
}

const adminLogin = await request("POST", "/api/admin/v1/auth/login", null, {
  email: env("OWNER_EMAIL", "owner@example.com"),
  password: env("OWNER_PASSWORD", "change-me-please-32-chars"),
});
const adminToken = adminLogin.data.session.session_token;

const modelName = `gemini-codeassist-e2e-${run}`;
await request("POST", "/api/admin/v1/models", adminToken, {
  model_name: modelName,
  display_name: "Gemini Code Assist E2E Model",
  endpoint_capabilities: ["chat"],
  request_usd: "0",
  min_charge_usd: "0",
  public_visible: true,
});

const provider = await request("POST", "/api/admin/v1/providers", adminToken, {
  name: `Gemini Code Assist E2E Provider ${run}`,
  provider_type: "gemini_cli",
});
const providerId = provider.data.id;

const providerClient = await request("POST", "/api/admin/v1/provider-clients", adminToken, {
  provider_id: providerId,
  name: `Gemini Code Assist E2E Client ${run}`,
  client_type: "oauth_app",
  metadata: {
    wrapper_strategy: "gemini_cli",
    auth_mode: "google_pkce",
    client_version: geminiClientVersion(),
    code_assist_endpoint: "https://cloudcode-pa.googleapis.com",
    code_assist_api_version: "v1internal",
    model: upstreamModel,
    ...(project ? { project_id: project } : {}),
  },
});
const providerClientId = providerClient.data.id;

const channel = await request("POST", "/api/admin/v1/channels", adminToken, {
  provider_id: providerId,
  name: `Gemini Code Assist E2E Channel ${run}`,
  base_url: upstreamBaseURL,
  abilities: [{
    model_name: modelName,
    endpoint: "chat",
    upstream_model: upstreamModel,
    transform_capability: { mode: "native", lossless: false },
  }],
  metadata: {
    e2e_run_id: run,
    upstream_model: upstreamModel,
    client_version: geminiClientVersion(),
    ...(project ? { project_id: project } : {}),
  },
});
const channelId = channel.data.id;

const portalRegister = await request("POST", "/api/portal/v1/auth/register", null, {
  email: `gemini-codeassist-e2e-${run}@example.com`,
  password: `gemini-codeassist-e2e-password-${run}`,
  display_name: "Gemini Code Assist E2E User",
});
const portalToken = portalRegister.data.session.session_token;
const userId = portalRegister.data.user.id;

const account = await request("POST", "/api/portal/v1/oauth/accounts", portalToken, {
  provider_id: providerId,
  channel_id: channelId,
  provider_client_id: providerClientId,
  name: `Gemini Code Assist E2E Account ${run}`,
  auth_mode: "google_pkce",
  token_bundle: geminiTokenBundle(credentials),
});
const accountId = account.data.id;
assert(account.data.auth_status === "active", "Gemini account should start active when token_bundle is supplied");

const explain = await request("GET", `/api/admin/v1/runtime/route-explain?model=${encodeURIComponent(modelName)}&endpoint=chat&routing_mode=byo&user_id=${encodeURIComponent(userId)}`, adminToken);
assert(explain.data.available === true, "Gemini Code Assist BYO account should route");
assert(explain.data.selected.account_id === accountId, "route explain should select the Gemini Code Assist account");
assert(explain.data.selected.auth_status === "active", "route explain should show active Gemini auth");

const apiKey = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `gemini-codeassist-e2e-key-${run}`,
  routing_mode: "byo",
  model_scope: [modelName],
});

const chatRequestID = `gemini-codeassist-e2e-chat-${run}`;
const chat = await rawRequest("POST", "/v1/chat/completions", apiKey.data.secret, {
  model: modelName,
  messages: [{ role: "user", content: "Say ok." }],
  max_tokens: 8,
  metadata: { session_id: chatRequestID },
}, { "x-request-id": chatRequestID, "x-elucid-relay-session": chatRequestID });

const usage = await request("GET", "/api/portal/v1/usage?limit=20", portalToken);
const chatUsage = usage.data.find((row) => row.request_id === chatRequestID);
assert(chatUsage, "Gemini Code Assist usage row should be recorded");
assert(Number(chatUsage.actual_cost) === 0, "Gemini Code Assist BYO usage should cost zero");

if (!chat.response.ok) {
  const classification = classifyGoogleCodeAssistError(chat.response.status, chat.body);
  if (!allowExpectedAuthError || !classification.expected) {
    throw new Error(`Gemini Code Assist request failed ${chat.response.status}: ${safeBody(chat.bodyText)}`);
  }
  assert(chatUsage.status === "failed", "expected-auth-error mode should record a failed usage row");
  console.log(`gemini-codeassist e2e partial: credential reached Google but was rejected as expected (${chat.response.status} ${classification.code}) account=${accountId} source=${credentials.source}`);
  process.exit(0);
}

assertRecognizedChatResponse(chat.body);
assert(chatUsage.status === "success", "Gemini Code Assist usage should be success");

const streamRequestID = `gemini-codeassist-e2e-stream-${run}`;
const stream = await rawRequest("POST", "/v1/chat/completions", apiKey.data.secret, {
  model: modelName,
  stream: true,
  messages: [{ role: "user", content: "Say ok." }],
  max_tokens: 8,
  metadata: { session_id: streamRequestID },
}, {
  accept: "text/event-stream",
  "x-request-id": streamRequestID,
  "x-elucid-relay-session": streamRequestID,
});
if (!stream.response.ok) {
  const classification = classifyGoogleCodeAssistError(stream.response.status, stream.body);
  throw new Error(`Gemini Code Assist stream request failed ${stream.response.status}${classification.code ? ` ${classification.code}` : ""}: ${safeBody(stream.bodyText)}`);
}
assert(stream.bodyText.includes("data:"), "Gemini Code Assist stream should return SSE data");

const usageAfterStream = await request("GET", "/api/portal/v1/usage?limit=20", portalToken);
const streamUsage = usageAfterStream.data.find((row) => row.request_id === streamRequestID);
assert(streamUsage?.status === "success", "Gemini Code Assist stream usage should be success");
assert(Number(streamUsage.actual_cost) === 0, "Gemini Code Assist stream BYO usage should cost zero");

console.log(`gemini-codeassist e2e ok: account=${accountId} upstream=${upstreamBaseURL} model=${upstreamModel} credential_source=${credentials.source}`);

async function resolveGeminiCredentials() {
  if (process.env.E2E_GEMINI_CODEASSIST_ACCESS_TOKEN || process.env.GOOGLE_CLOUD_ACCESS_TOKEN) {
    return {
      source: process.env.E2E_GEMINI_CODEASSIST_ACCESS_TOKEN ? "E2E_GEMINI_CODEASSIST_ACCESS_TOKEN" : "GOOGLE_CLOUD_ACCESS_TOKEN",
      accessToken: process.env.E2E_GEMINI_CODEASSIST_ACCESS_TOKEN || process.env.GOOGLE_CLOUD_ACCESS_TOKEN,
      refreshToken: process.env.E2E_GEMINI_CODEASSIST_REFRESH_TOKEN || "",
      expiresAt: normalizeExpiresAt(process.env.E2E_GEMINI_CODEASSIST_EXPIRES_AT),
      scopes: normalizeList(process.env.E2E_GEMINI_CODEASSIST_SCOPES, defaultGeminiScopes()),
      subject: process.env.E2E_GEMINI_CODEASSIST_SUBJECT || "",
    };
  }

  for (const filePath of geminiCredentialPaths()) {
    const parsed = await readJSONFile(filePath);
    if (!parsed) continue;
    if (parsed.access_token || parsed.refresh_token) {
      return {
        source: filePath,
        accessToken: parsed.access_token || "",
        refreshToken: parsed.refresh_token || "",
        expiresAt: normalizeExpiresAt(parsed.expiry_date || parsed.expires_at),
        scopes: normalizeList(parsed.scope || parsed.scopes, defaultGeminiScopes()),
        subject: parsed.subject || "",
        project: normalizeProjectId(parsed.project_id || parsed.cloudaicompanion_project || parsed.metadata?.project_id || parsed.metadata?.cloudaicompanion_project),
      };
    }
    if (parsed.type === "authorized_user" && parsed.refresh_token) {
      return {
        source: filePath,
        accessToken: "",
        refreshToken: parsed.refresh_token,
        expiresAt: "",
        scopes: defaultGeminiScopes(),
        subject: parsed.client_email || "",
        clientId: parsed.client_id || "",
        clientSecret: parsed.client_secret || "",
        project: normalizeProjectId(parsed.project_id || parsed.quota_project_id),
      };
    }
  }

  return { source: "", accessToken: "", refreshToken: "", expiresAt: "", scopes: [] };
}

async function ensureGeminiAccessToken(credentials) {
  if (credentials.accessToken) return credentials;
  if (!credentials.refreshToken) return credentials;
  if (!truthy(process.env.E2E_GEMINI_CODEASSIST_ALLOW_REFRESH)) {
    throw new Error(
      "Gemini credentials only contain a refresh token. Set E2E_GEMINI_CODEASSIST_ALLOW_REFRESH=1 " +
        "to exchange it for an access token. This runner will not refresh tokens implicitly.",
    );
  }
  const clientId = credentials.clientId || env("GEMINI_OAUTH_CLIENT_ID", "");
  const clientSecret = credentials.clientSecret || env("GEMINI_OAUTH_CLIENT_SECRET", "");
  assert(clientId, "GEMINI_OAUTH_CLIENT_ID is required to refresh Gemini credentials.");
  assert(clientSecret, "GEMINI_OAUTH_CLIENT_SECRET is required to refresh Gemini credentials.");
  const response = await fetch(env("GEMINI_TOKEN_URL", "https://oauth2.googleapis.com/token"), {
    method: "POST",
    headers: { accept: "application/json", "content-type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({
      grant_type: "refresh_token",
      refresh_token: credentials.refreshToken,
      client_id: clientId,
      client_secret: clientSecret,
    }),
  });
  const text = await response.text();
  const data = parseJSON(text);
  if (!response.ok) {
    throw new Error(`Gemini OAuth refresh failed ${response.status}: ${safeBody(text)}`);
  }
  return {
    ...credentials,
    source: `${credentials.source}:refresh_token`,
    accessToken: data.access_token || "",
    expiresAt: data.expires_in ? new Date(Date.now() + Number(data.expires_in) * 1000).toISOString() : credentials.expiresAt,
    scopes: normalizeList(data.scope, credentials.scopes),
  };
}

async function discoverGeminiCodeAssistProject(credentials, projectHint) {
  if (truthy(process.env.E2E_GEMINI_CODEASSIST_SKIP_PROJECT_DISCOVERY)) {
    return { project: normalizeProjectId(projectHint) };
  }
  const setupMetadata = geminiSetupMetadata(projectHint);
  const loadBody = {
    ...(projectHint ? { cloudaicompanionProject: projectHint, mode: "HEALTH_CHECK" } : {}),
    metadata: setupMetadata,
  };
  const load = await codeAssistJSON("loadCodeAssist", credentials, geminiAuxiliaryHeaderModel(), loadBody);
  if (!load.ok) return { status: load.status, error: load.body, bodyText: load.bodyText };
  const loadedProject = normalizeProjectId(load.body.cloudaicompanionProject);
  if (loadedProject) return { project: loadedProject };

  if (!truthy(process.env.E2E_GEMINI_CODEASSIST_ALLOW_ONBOARD)) {
    return { project: normalizeProjectId(projectHint) };
  }
  const tierId = defaultTierID(load.body.allowedTiers);
  if (!tierId) return { project: normalizeProjectId(projectHint) };
  const onboard = await codeAssistJSON("onboardUser", credentials, geminiAuxiliaryHeaderModel(), {
    tierId,
    metadata: setupMetadata,
  });
  if (!onboard.ok) return { status: onboard.status, error: onboard.body, bodyText: onboard.bodyText };
  return { project: normalizeProjectId(onboard.body?.response?.cloudaicompanionProject || onboard.body?.cloudaicompanionProject || projectHint) };
}

async function codeAssistJSON(method, credentials, model, body) {
  const response = await fetch(`${upstreamBaseURL}:${method}`, {
    method: "POST",
    headers: geminiCodeAssistHeaders(credentials, model),
    body: JSON.stringify(body || {}),
  });
  const bodyText = await response.text();
  return {
    ok: response.ok,
    status: response.status,
    bodyText,
    body: parseJSON(bodyText),
  };
}

function geminiCodeAssistHeaders(credentials, model) {
  return {
    authorization: `Bearer ${credentials.accessToken}`,
    "content-type": "application/json",
    accept: "application/json",
    "user-agent": geminiUserAgent(model),
    "x-goog-api-client": env("GEMINI_CODEASSIST_API_CLIENT", "gl-node/25.8.1"),
  };
}

function geminiSetupMetadata(projectID = "") {
  const project = normalizeProjectId(projectID);
  return {
    ideType: "IDE_UNSPECIFIED",
    platform: "PLATFORM_UNSPECIFIED",
    pluginType: "GEMINI",
    ...(project ? { duetProject: project } : {}),
  };
}

function defaultTierID(allowedTiers) {
  if (!Array.isArray(allowedTiers)) return "";
  const selected = allowedTiers.find((tier) => tier?.isDefault) || allowedTiers[0];
  return String(selected?.id || "");
}

function geminiCredentialPaths() {
  return [
    process.env.GEMINI_OAUTH_CREDS_FILE,
    process.env.GOOGLE_APPLICATION_CREDENTIALS,
    path.join(os.homedir(), ".gemini", "oauth_creds.json"),
  ].filter(Boolean);
}

function geminiTokenBundle(credentials) {
  return {
    type: "oauth",
    access_token: credentials.accessToken,
    refresh_token: credentials.refreshToken || "",
    expires_at: credentials.expiresAt || "",
    scopes: credentials.scopes.length ? credentials.scopes : defaultGeminiScopes(),
    provider: "google_gemini",
    auth_scheme: "bearer",
    subject: credentials.subject || "",
    metadata: {
      source: credentials.source,
      client_version: geminiClientVersion(),
      user_agent: geminiUserAgent(),
      code_assist_endpoint: "https://cloudcode-pa.googleapis.com",
      code_assist_api_version: "v1internal",
      token_type: "Bearer",
      ...(project ? { project_id: project } : {}),
    },
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

async function readJSONFile(filePath) {
  try {
    return parseJSON(await fs.readFile(filePath, "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

function assertRecognizedChatResponse(body) {
  assert(body && typeof body === "object", "chat response should be a JSON object");
  assert(body.id || Array.isArray(body.choices), "chat response schema was not recognized");
}

function classifyGoogleCodeAssistError(status, body) {
  const error = body?.error || {};
  const text = JSON.stringify(error);
  if (/Cloud Code Private API has not been used|cloudcode-pa\.googleapis\.com/i.test(text)) {
    return { expected: [400, 401, 403, 429].includes(status), code: "cloud_code_private_api_disabled" };
  }
  const code = String(error.code || error.status || "");
  return {
    expected: [400, 401, 403, 429].includes(status) &&
      /(invalid|unauthorized|permission|quota|precondition|validation|required|forbidden|denied|exhausted|billing|disabled)/i.test(text),
    code: code || "google_codeassist_error",
  };
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

function normalizeList(value, fallback = []) {
  if (Array.isArray(value)) return value.map(String).filter(Boolean);
  if (!value) return fallback;
  return String(value).split(/[,\s]+/).map((item) => item.trim()).filter(Boolean);
}

function defaultGeminiScopes() {
  return [
    "https://www.googleapis.com/auth/cloud-platform",
    "https://www.googleapis.com/auth/userinfo.email",
    "https://www.googleapis.com/auth/userinfo.profile",
  ];
}

function geminiClientVersion() {
  return env("GEMINI_CLI_VERSION", "0.42.0-nightly.20260428.g59b2dea0e");
}

function geminiAuxiliaryHeaderModel() {
  return env("E2E_GEMINI_CODEASSIST_AUX_MODEL", "gemini-3-flash-preview");
}

function geminiUserAgent(model = upstreamModel) {
  return env("GEMINI_CLI_USER_AGENT", `GeminiCLI/${geminiClientVersion()}/${model} (${process.platform}; ${process.arch}; terminal) google-api-nodejs-client/9.15.1`);
}

function safeBody(value) {
  return String(value || "").replace(/ya29\.[A-Za-z0-9._-]+/g, "[REDACTED]").slice(0, 700);
}

function normalizeProjectId(value) {
  return String(value || "").trim().replace(/^projects\//, "");
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

function autoEnableNodeEnvProxy() {
  if (truthy(process.env.NODE_USE_ENV_PROXY) || truthy(process.env.E2E_DISABLE_AUTO_ENV_PROXY)) return;
  const hasProxy = ["HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"].some((name) => String(process.env[name] || "").trim());
  if (!hasProxy) return;
  const result = spawnSync(process.execPath, process.argv.slice(1), {
    stdio: "inherit",
    env: { ...process.env, NODE_USE_ENV_PROXY: "1" },
  });
  if (result.error) throw result.error;
  process.exit(result.status === null ? 1 : result.status);
}
