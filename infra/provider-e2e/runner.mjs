#!/usr/bin/env node
import crypto from "node:crypto";

const args = new Set(process.argv.slice(2));
const providerArg = valueAfter("--provider") || "all";
const baseURL = env("E2E_BASE_URL", "http://localhost:18080").replace(/\/$/, "");
const adminEmail = env("E2E_ADMIN_EMAIL", "owner@example.com");
const adminPassword = env("E2E_ADMIN_PASSWORD", "change-me-please-32-chars");
const runID = `${Date.now()}-${crypto.randomBytes(3).toString("hex")}`;

const providers = {
  openai: {
    providerType: "openai_compatible",
    envKey: "E2E_OPENAI_API_KEY",
    baseURL: env("E2E_OPENAI_BASE_URL", "https://api.openai.com"),
    model: env("E2E_OPENAI_MODEL", "gpt-4.1-mini"),
    abilities: ["chat", "responses", "embeddings"],
    checks: ["chat", "responses", "embeddings", "stream"],
  },
  anthropic: {
    providerType: "anthropic",
    envKey: "E2E_ANTHROPIC_API_KEY",
    baseURL: env("E2E_ANTHROPIC_BASE_URL", "https://api.anthropic.com"),
    model: env("E2E_ANTHROPIC_MODEL", "claude-3-5-haiku-latest"),
    abilities: ["messages"],
    checks: ["messages", "stream"],
  },
  "gemini-openai-compatible": {
    providerType: "gemini_openai_compatible",
    envKey: "E2E_GEMINI_API_KEY",
    baseURL: env("E2E_GEMINI_BASE_URL", "https://generativelanguage.googleapis.com/v1beta/openai"),
    model: env("E2E_GEMINI_MODEL", "gemini-2.0-flash"),
    abilities: ["chat", "embeddings"],
    checks: ["chat", "embeddings"],
  },
};

const selected = providerArg === "all" ? Object.keys(providers) : [providerArg];
let ran = 0;
for (const name of selected) {
  const spec = providers[name];
  if (!spec) {
    throw new Error(`unknown provider ${name}`);
  }
  if (!process.env[spec.envKey]) {
    console.log(`skip ${name}: ${spec.envKey} is not set`);
    continue;
  }
  ran++;
  await runProvider(name, spec);
}

if (ran === 0) {
  console.log("provider e2e skipped: no selected provider API key is set");
}

async function runProvider(name, spec) {
  const prefix = `e2e-${name}-${runID}`;
  const modelName = `${prefix}-model`;
  const userEmail = `${prefix}@example.com`;
  const password = `e2e-password-${runID}`;

  const admin = await request("/api/admin/v1/auth/login", {
    method: "POST",
    body: { email: adminEmail, password: adminPassword },
  });
  const adminToken = admin.session.session_token;

  await request("/api/admin/v1/models", {
    method: "POST",
    token: adminToken,
    body: {
      model_name: modelName,
      display_name: modelName,
      endpoint_capabilities: spec.abilities,
      request_usd: "0",
      min_charge_usd: "0.000001",
      public_visible: true,
    },
  });

  const provider = await request("/api/admin/v1/providers", {
    method: "POST",
    token: adminToken,
    body: { name: `${prefix} provider`, provider_type: spec.providerType },
  });
  const channel = await request("/api/admin/v1/channels", {
    method: "POST",
    token: adminToken,
    body: {
      provider_id: provider.id,
      name: `${prefix} channel`,
      base_url: spec.baseURL,
      abilities: spec.abilities.map((endpoint) => ({
        model_name: modelName,
        endpoint,
        upstream_model: spec.model,
        transform_capability: { mode: "native", lossless: true },
      })),
      metadata: { e2e_run_id: runID },
    },
  });
  const account = await request("/api/admin/v1/accounts", {
    method: "POST",
    token: adminToken,
    body: {
      provider_id: provider.id,
      channel_id: channel.id,
      name: `${prefix} account`,
      api_key: process.env[spec.envKey],
      max_concurrency: 2,
      metadata: { e2e_run_id: runID },
    },
  });
  await request("/api/admin/v1/account-quota-windows", {
    method: "POST",
    token: adminToken,
    body: {
      account_id: account.id,
      window_type: "requests",
      remaining: "50",
      metadata: { limit: 50, e2e_run_id: runID },
    },
  });

  const portal = await request("/api/portal/v1/auth/register", {
    method: "POST",
    body: { email: userEmail, password, display_name: prefix },
  });
  const portalToken = portal.session.session_token;
  await request(`/api/admin/v1/users/${portal.user.id}/wallet/adjustments`, {
    method: "POST",
    token: adminToken,
    body: { entry_type: "credit", amount: "1.00", reason: "provider e2e" },
  });
  const apiKey = await request("/api/portal/v1/api-keys", {
    method: "POST",
    token: portalToken,
    body: { name: prefix, model_scope: [modelName] },
  });

  for (const check of spec.checks) {
    const requestID = `${prefix}-${check}`;
    const response = await northbound(check, modelName, apiKey.secret, requestID);
    assertResponse(check, response);
    const usage = await request(`/api/admin/v1/usage?request_id=${encodeURIComponent(requestID)}&limit=1`, {
      token: adminToken,
    });
    if (!usage[0] || usage[0].status !== "success" || usage[0].requested_model !== modelName) {
      throw new Error(`${name} ${check}: usage record missing or not successful`);
    }
  }
  console.log(`ok ${name}: ${modelName}`);
}

async function northbound(check, model, token, requestID) {
  if (check === "messages") {
    return rawJSON("/v1/messages", token, requestID, {
      model,
      max_tokens: 16,
      messages: [{ role: "user", content: "Say ok." }],
    }, { "anthropic-version": "2023-06-01" });
  }
  if (check === "responses") {
    return rawJSON("/v1/responses", token, requestID, {
      model,
      input: "Say ok.",
      max_output_tokens: 16,
    });
  }
  if (check === "embeddings") {
    return rawJSON("/v1/embeddings", token, requestID, {
      model,
      input: "embedding probe",
    });
  }
  if (check === "stream") {
    return rawStream("/v1/chat/completions", token, requestID, {
      model,
      stream: true,
      messages: [{ role: "user", content: "Say ok." }],
    });
  }
  return rawJSON("/v1/chat/completions", token, requestID, {
    model,
    messages: [{ role: "user", content: "Say ok." }],
  });
}

async function rawJSON(path, token, requestID, body, extraHeaders = {}) {
  const response = await fetch(`${baseURL}${path}`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
      "x-request-id": requestID,
      ...extraHeaders,
    },
    body: JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`${path} ${response.status}: ${text}`);
  }
  return JSON.parse(text);
}

async function rawStream(path, token, requestID, body) {
  const response = await fetch(`${baseURL}${path}`, {
    method: "POST",
    headers: {
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
      accept: "text/event-stream",
      "x-request-id": requestID,
    },
    body: JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) {
    throw new Error(`${path} ${response.status}: ${text}`);
  }
  if (!text.includes("data:")) {
    throw new Error("stream response did not contain SSE data");
  }
  return { stream: text };
}

function assertResponse(check, response) {
  if (check === "stream") return;
  if (!response || typeof response !== "object") {
    throw new Error(`${check}: response is not JSON object`);
  }
  if (check === "embeddings" && !Array.isArray(response.data)) {
    throw new Error("embeddings response missing data array");
  }
  if (check !== "embeddings" && !response.id && !response.content && !response.output && !response.choices) {
    throw new Error(`${check}: response schema was not recognized`);
  }
}

async function request(path, options = {}) {
  const headers = { "content-type": "application/json" };
  if (options.token) headers.authorization = `Bearer ${options.token}`;
  const response = await fetch(`${baseURL}${path}`, {
    method: options.method || "GET",
    headers,
    body: options.body ? JSON.stringify(options.body) : undefined,
  });
  const text = await response.text();
  let parsed = {};
  if (text) parsed = JSON.parse(text);
  if (!response.ok || parsed.error) {
    throw new Error(`${path} ${response.status}: ${text}`);
  }
  return parsed.data;
}

function valueAfter(name) {
  const index = process.argv.indexOf(name);
  if (index >= 0) return process.argv[index + 1];
  for (const arg of args) {
    if (arg.startsWith(`${name}=`)) return arg.slice(name.length + 1);
  }
  return "";
}

function env(name, fallback) {
  return process.env[name] || fallback;
}
