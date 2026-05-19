#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { runOnce } from "../apps/oauth-wrapper/src/runner.mjs";

const enabled = truthy(process.env.E2E_CLAUDE_CODE_REMOTE);
if (!enabled) {
  console.log("claude-code remote e2e skipped: set E2E_CLAUDE_CODE_REMOTE=1");
  process.exit(0);
}

const base = env("BASE_URL", "http://localhost:18080").replace(/\/$/, "");
const wrapperToken = env("OAUTH_WRAPPER_BEARER_TOKEN", "local-oauth-wrapper-token");
const upstreamBaseURL = env("E2E_CLAUDE_CODE_BASE_URL", await defaultClaudeBaseURL()).replace(/\/$/, "");
const run = `${Date.now()}-${crypto.randomBytes(3).toString("hex")}`;

const credentials = await resolveClaudeCredentials();
assert(credentials.accessToken, "Claude credentials are not available from CLAUDE_CODE_OAUTH_TOKEN, CLAUDE_CREDENTIALS_FILE, ~/.claude/.credentials.json, or ~/.claude/settings.json.");
const authTokenSource = credentials.source === "claude_settings_auth_token" ? "ANTHROPIC_AUTH_TOKEN" : credentials.source;

const adminLogin = await request("POST", "/api/admin/v1/auth/login", null, {
  email: env("OWNER_EMAIL", "owner@example.com"),
  password: env("OWNER_PASSWORD", "change-me-please-32-chars"),
});
const adminToken = adminLogin.data.session.session_token;

const provider = await request("POST", "/api/admin/v1/providers", adminToken, {
  name: `Claude Code Remote E2E Provider ${run}`,
  provider_type: "anthropic",
});
const providerId = provider.data.id;

const providerClient = await request("POST", "/api/admin/v1/provider-clients", adminToken, {
  provider_id: providerId,
  name: `Claude Code Remote E2E Client ${run}`,
  client_type: "oauth_app",
  metadata: { wrapper_strategy: "claude_cli", credentials_file: credentials.credentialsFile || "" },
});
const providerClientId = providerClient.data.id;

const channel = await request("POST", "/api/admin/v1/channels", adminToken, {
  provider_id: providerId,
  name: `Claude Code Remote E2E Channel ${run}`,
  base_url: upstreamBaseURL,
  abilities: [],
  metadata: { e2e_run_id: run },
});
const channelId = channel.data.id;

const portalRegister = await request("POST", "/api/portal/v1/auth/register", null, {
  email: `claude-code-remote-e2e-${run}@example.com`,
  password: `claude-code-remote-e2e-password-${run}`,
  display_name: "Claude Code Remote E2E User",
});
const portalToken = portalRegister.data.session.session_token;

const account = await request("POST", "/api/portal/v1/oauth/accounts", portalToken, {
  provider_id: providerId,
  channel_id: channelId,
  provider_client_id: providerClientId,
  name: `Claude Code Remote E2E Account ${run}`,
  auth_mode: "claude_cli",
});
const accountId = account.data.id;
assert(account.data.auth_status === "pending", "Claude Code account should start pending");

const wrapperEnv = {
  CLAUDE_CODE_OAUTH_TOKEN: credentials.accessToken,
  CLAUDE_CODE_OAUTH_REFRESH_TOKEN: credentials.refreshToken || "",
  CLAUDE_CODE_OAUTH_EXPIRES_AT: credentials.expiresAt || "",
  CLAUDE_CODE_OAUTH_SCOPES: credentials.scopes.join(" "),
};
if (authTokenSource === "ANTHROPIC_AUTH_TOKEN") {
  wrapperEnv.ANTHROPIC_AUTH_TOKEN = credentials.accessToken;
  wrapperEnv.ANTHROPIC_AUTH_TOKEN_SCOPES = credentials.scopes.join(" ");
  delete wrapperEnv.CLAUDE_CODE_OAUTH_TOKEN;
  delete wrapperEnv.CLAUDE_CODE_OAUTH_REFRESH_TOKEN;
  delete wrapperEnv.CLAUDE_CODE_OAUTH_EXPIRES_AT;
  delete wrapperEnv.CLAUDE_CODE_OAUTH_SCOPES;
}
const previousEnv = snapshotEnv(Object.keys(wrapperEnv));
Object.assign(process.env, wrapperEnv);
try {
  const processed = await runOnce({
    baseUrl: base,
    token: wrapperToken,
    leaseOwner: `claude-code-remote-e2e-${run}`,
    leaseSeconds: 300,
    supportedModes: ["claude_cli"],
    authMode: "claude_cli",
  }, { logger: silentLogger(), failFast: true });
  assert(processed === true, "wrapper should process the Claude Code OAuth job");
} finally {
  restoreEnv(previousEnv);
}

const states = await request("GET", `/api/admin/v1/account-auth-states?account_id=${accountId}`, adminToken);
assert(states.data[0]?.auth_status === "active", "Claude Code wrapper should activate account auth state");

const apiKey = await request("POST", "/api/portal/v1/api-keys", portalToken, {
  name: `claude-code-remote-e2e-key-${run}`,
  routing_mode: "byo",
});

const profile = await fetchClaudeProfile(apiKey.data.secret, run, credentials.source);
const organizationUUID = firstNonEmpty(
  env("E2E_CLAUDE_CODE_ORGANIZATION_UUID", ""),
  profile.body?.organization?.uuid,
  profile.body?.organization_uuid,
);
const accountUUID = firstNonEmpty(
  env("E2E_CLAUDE_CODE_ACCOUNT_UUID", ""),
  profile.body?.account?.uuid,
  profile.body?.account_uuid,
  profile.body?.account?.account_uuid,
);
if (!organizationUUID || !accountUUID) {
  console.log(`claude-code remote e2e partial: profile unavailable for credential_source=${credentials.source}; set E2E_CLAUDE_CODE_ACCOUNT_UUID and E2E_CLAUDE_CODE_ORGANIZATION_UUID to continue session/environment validation`);
  process.exit(0);
}

const providerRequestId = `claude-code-remote-providers-${run}`;
const providers = await rawRequest("GET", "/v1/environment_providers", apiKey.data.secret, undefined, {
  "x-request-id": providerRequestId,
  ...orgHeader(organizationUUID),
});
assert(providers.response.ok, `/v1/environment_providers failed ${providers.response.status}: ${safeBody(providers.bodyText)}`);

let environmentId = firstEnvironmentId(providers.body);
let createRequestId = "";
if (!environmentId && truthy(process.env.E2E_CLAUDE_CODE_CREATE_ENVIRONMENT)) {
  createRequestId = `claude-code-remote-env-create-${run}`;
  const created = await rawRequest("POST", "/v1/environment_providers/cloud/create", apiKey.data.secret, defaultEnvironmentBody(run), {
    "x-request-id": createRequestId,
    ...orgHeader(organizationUUID),
  });
  assert(created.response.ok, `/v1/environment_providers/cloud/create failed ${created.response.status}: ${safeBody(created.bodyText)}`);
  environmentId = created.body?.environment_id || created.body?.id || "";
}
if (!environmentId && !truthy(process.env.E2E_CLAUDE_CODE_REQUIRE_SESSION)) {
  console.log(`claude-code remote e2e partial: no environment returned for source=${credentials.source}; profile and providers succeeded`);
  process.exit(0);
}
assert(environmentId, "No Claude environment was returned; set E2E_CLAUDE_CODE_CREATE_ENVIRONMENT=1 to create a default environment.");

let sessionRequestId = "";
let archiveRequestId = "";
if (environmentId) {
  sessionRequestId = `claude-code-remote-session-${run}`;
  const session = await rawRequest("POST", "/v1/sessions", apiKey.data.secret, sessionBody(environmentId, run), {
    "x-request-id": sessionRequestId,
    ...orgHeader(organizationUUID),
  });
  assert(session.response.ok, `/v1/sessions failed ${session.response.status}: ${safeBody(session.bodyText)}`);
  const sessionId = session.body?.id || "";
  assert(sessionId, "/v1/sessions response had no id");

  archiveRequestId = `claude-code-remote-archive-${run}`;
  const archived = await rawRequest("POST", `/v1/sessions/${encodeURIComponent(sessionId)}/archive`, apiKey.data.secret, {}, {
    "x-request-id": archiveRequestId,
    ...orgHeader(organizationUUID),
  });
  assert([200, 201, 204, 409].includes(archived.response.status), `/v1/sessions/{id}/archive failed ${archived.response.status}: ${safeBody(archived.bodyText)}`);
}

const usage = await request("GET", "/api/portal/v1/usage?limit=20", portalToken);
for (const requestId of [profileRequestId, providerRequestId, createRequestId, sessionRequestId, archiveRequestId].filter(Boolean)) {
  const row = usage.data.find((item) => item.request_id === requestId);
  assert(row?.status === "success", `usage row should be success for ${requestId}`);
  assert(Number(row.actual_cost) === 0, `BYO usage should cost zero for ${requestId}`);
}

console.log(`claude-code remote e2e ok: account=${accountId} upstream=${upstreamBaseURL} credential_source=${credentials.source}`);

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

async function fetchClaudeProfile(apiToken, runID, source) {
  const profileRequestId = `claude-code-remote-profile-${runID}`;
  const endpoints = source === "claude_settings_auth_token"
    ? [
        { path: "/api/claude_cli_profile?account_uuid=unknown", headers: { "x-request-id": profileRequestId } },
        { path: "/api/oauth/profile", headers: { "x-request-id": profileRequestId } },
      ]
    : [
        { path: "/api/oauth/profile", headers: { "x-request-id": profileRequestId } },
        { path: "/api/claude_cli_profile?account_uuid=unknown", headers: { "x-request-id": profileRequestId } },
      ];

  let lastError = "";
  for (const endpoint of endpoints) {
    const profile = await rawRequest("GET", endpoint.path, apiToken, undefined, endpoint.headers);
    if (profile.response.ok) {
      return profile;
    }
    lastError = `${endpoint.path} failed ${profile.response.status}: ${safeBody(profile.bodyText)}`;
  }
  if (truthy(process.env.E2E_CLAUDE_CODE_ALLOW_PROFILE_MISS)) {
    console.log(`claude-code remote e2e profile miss: ${lastError}`);
    return { response: { ok: false, status: 404 }, bodyText: "", body: {} };
  }
  throw new Error(`Claude profile could not be fetched via either profile endpoint: ${lastError}`);
}

async function resolveClaudeCredentials() {
  if (process.env.CLAUDE_CODE_OAUTH_TOKEN) {
    return {
      source: "CLAUDE_CODE_OAUTH_TOKEN",
      accessToken: process.env.CLAUDE_CODE_OAUTH_TOKEN,
      refreshToken: process.env.CLAUDE_CODE_OAUTH_REFRESH_TOKEN || "",
      expiresAt: normalizeExpiresAt(process.env.CLAUDE_CODE_OAUTH_EXPIRES_AT),
      scopes: normalizeList(process.env.CLAUDE_CODE_OAUTH_SCOPES, defaultClaudeScopes()),
    };
  }

  for (const file of claudeCredentialFiles()) {
    const data = await readJSON(file);
    const oauth = data?.claudeAiOauth;
    if (!oauth?.accessToken) continue;
    return {
      source: "claude_credentials_file",
      credentialsFile: file,
      accessToken: oauth.accessToken,
      refreshToken: oauth.refreshToken || "",
      expiresAt: normalizeExpiresAt(oauth.expiresAt),
      scopes: normalizeList(oauth.scopes, defaultClaudeScopes()),
    };
  }

  const settings = await readJSON(path.join(os.homedir(), ".claude", "settings.json"));
  const authToken = settings?.env?.ANTHROPIC_AUTH_TOKEN;
  if (authToken) {
    return {
      source: "claude_settings_auth_token",
      accessToken: authToken,
      refreshToken: "",
      expiresAt: "",
      scopes: defaultClaudeScopes(),
    };
  }

  return { source: "none", accessToken: "", refreshToken: "", expiresAt: "", scopes: defaultClaudeScopes() };
}

function claudeCredentialFiles() {
  const configured = normalizeList(process.env.CLAUDE_CREDENTIALS_FILE, []);
  const configDir = process.env.CLAUDE_CONFIG_DIR || path.join(os.homedir(), ".claude");
  configured.push(path.join(configDir, ".credentials.json"));
  return [...new Set(configured.filter(Boolean))];
}

function firstEnvironmentId(body) {
  const environments = Array.isArray(body?.environments) ? body.environments : Array.isArray(body?.data) ? body.data : [];
  const preferred = environments.find((item) => item?.kind === "anthropic_cloud") || environments.find((item) => item?.kind !== "bridge") || environments[0];
  return preferred?.environment_id || preferred?.id || "";
}

function defaultEnvironmentBody(runID) {
  return {
    name: `Elucid Relay E2E ${runID}`,
    kind: "anthropic_cloud",
    description: "",
    config: {
      environment_type: "anthropic",
      cwd: "/home/user",
      init_script: null,
      environment: {},
      languages: [
        { name: "python", version: "3.11" },
        { name: "node", version: "20" },
      ],
      network_config: {
        allowed_hosts: [],
        allow_default_hosts: true,
      },
    },
  };
}

function sessionBody(environmentId, runID) {
  return {
    title: `Elucid Relay E2E ${runID}`,
    events: [],
    session_context: {
      sources: [],
      outcomes: [],
      model: env("E2E_CLAUDE_CODE_MODEL", "sonnet"),
    },
    environment_id: environmentId,
    source: "remote-control",
  };
}

function orgHeader(organizationUUID) {
  return organizationUUID ? { "x-organization-uuid": organizationUUID } : {};
}

async function defaultClaudeBaseURL() {
  const settings = await readJSON(path.join(os.homedir(), ".claude", "settings.json"));
  return settings?.env?.ANTHROPIC_BASE_URL || "https://api.anthropic.com";
}

async function readJSON(file) {
  try {
    return JSON.parse(await fs.readFile(file, "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

function defaultClaudeScopes() {
  return [
    "org:create_api_key",
    "user:profile",
    "user:inference",
    "user:sessions:claude_code",
    "user:mcp_servers",
    "user:file_upload",
  ];
}

function normalizeList(value, fallback) {
  if (Array.isArray(value)) return value.map(String).map((item) => item.trim()).filter(Boolean);
  const items = String(value || "").split(/[,\s]+/).map((item) => item.trim()).filter(Boolean);
  return items.length ? items : fallback;
}

function normalizeExpiresAt(value) {
  if (!value) return "";
  if (typeof value === "number") return new Date(value > 10_000_000_000 ? value : value * 1000).toISOString();
  const numeric = Number(value);
  if (Number.isFinite(numeric)) return normalizeExpiresAt(numeric);
  return String(value);
}

function snapshotEnv(keys) {
  return Object.fromEntries(keys.map((key) => [key, process.env[key]]));
}

function restoreEnv(snapshot) {
  for (const [key, value] of Object.entries(snapshot)) {
    if (value === undefined) delete process.env[key];
    else process.env[key] = value;
  }
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

function firstNonEmpty(...values) {
  for (const value of values) {
    const text = String(value || "").trim();
    if (text) return text;
  }
  return "";
}
