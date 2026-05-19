import assert from "node:assert/strict";
import fs from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { executeJob } from "../src/strategies.mjs";
import { runOnce } from "../src/runner.mjs";

test("mock strategy completes with normalized token bundle", async () => {
  const result = await executeJob({
    id: "job-1",
    account_id: "account-1",
    job_type: "onboarding",
    auth_mode: "mock",
    provider: { name: "mock-provider" },
    provider_client: { metadata: { scopes: ["chat"] } },
    payload: {},
  });

  assert.equal(result.auth_status, "active");
  assert.equal(result.token_bundle.type, "oauth");
  assert.equal(result.token_bundle.access_token, "mock-access-job-1");
  assert.deepEqual(result.token_bundle.scopes, ["chat"]);
});

test("refresh strategy exchanges refresh token", async () => {
  const server = await listen((req, res) => {
    assert.equal(req.method, "POST");
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      const params = new URLSearchParams(body);
      assert.equal(params.get("grant_type"), "refresh_token");
      assert.equal(params.get("refresh_token"), "refresh-old");
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ access_token: "access-new", expires_in: 3600, scope: "chat profile" }));
    });
  });
  try {
    const result = await executeJob({
      id: "job-2",
      account_id: "account-2",
      job_type: "refresh",
      auth_mode: "oauth",
      provider: { name: "provider" },
      provider_client: { metadata: { token_url: server.url, client_id: "client" } },
      token_bundle: { type: "oauth", access_token: "access-old", refresh_token: "refresh-old" },
      payload: {},
    });
    assert.equal(result.token_bundle.access_token, "access-new");
    assert.equal(result.token_bundle.refresh_token, "refresh-old");
    assert.deepEqual(result.token_bundle.scopes, ["chat", "profile"]);
  } finally {
    await server.close();
  }
});

test("google_pkce Gemini refresh uses configured OAuth client metadata", async () => {
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      res.setHeader("content-type", "application/json");
      if (req.url === "/token") {
        const params = new URLSearchParams(body);
        assert.equal(params.get("grant_type"), "refresh_token");
        assert.equal(params.get("refresh_token"), "refresh-gemini");
        assert.equal(params.get("client_id"), "gid");
        assert.equal(params.get("client_secret"), "gsec");
        res.end(JSON.stringify({ access_token: "access-gemini", expires_in: 3600, token_type: "Bearer" }));
        return;
      }
      if (req.url === "/userinfo") {
        assert.equal(req.headers.authorization, "Bearer access-gemini");
        res.end(JSON.stringify({ id: "google-user-1", email: "gemini-user@example.com", verified_email: true }));
        return;
      }
      res.writeHead(404).end("{}");
    });
  });

  try {
    const result = await executeJob({
      id: "job-gemini-refresh",
      account_id: "relay-account-gemini",
      job_type: "refresh",
      auth_mode: "google_pkce",
      provider: { name: "Gemini CLI", provider_type: "gemini_cli" },
      provider_client: {
        metadata: {
          wrapper_strategy: "gemini_cli",
          token_url: `${server.url}/token`,
          userinfo_url: `${server.url}/userinfo`,
          client_version: "0.42.0-test",
          model: "gemini-2.5-pro",
          client_id: "gid",
          client_secret: "gsec",
        },
      },
      token_bundle: { type: "oauth", access_token: "old-gemini", refresh_token: "refresh-gemini" },
      payload: {},
    });

    assert.equal(result.auth_status, "active");
    assert.equal(result.result.strategy, "google_gemini_refresh");
    assert.equal(result.token_bundle.provider, "google_gemini");
    assert.equal(result.token_bundle.access_token, "access-gemini");
    assert.equal(result.token_bundle.refresh_token, "refresh-gemini");
    assert.equal(result.token_bundle.auth_scheme, "bearer");
    assert.equal(result.token_bundle.subject, "gemini-user@example.com");
    assert.deepEqual(result.token_bundle.scopes, [
      "https://www.googleapis.com/auth/cloud-platform",
      "https://www.googleapis.com/auth/userinfo.email",
      "https://www.googleapis.com/auth/userinfo.profile",
    ]);
    assert.equal(result.token_bundle.metadata.client_version, "0.42.0-test");
    assert.equal(result.token_bundle.metadata.oauth_client_id, "gid");
    assert.equal(result.token_bundle.metadata.code_assist_endpoint, "https://cloudcode-pa.googleapis.com");
    assert.equal(result.token_bundle.metadata.code_assist_api_version, "v1internal");
    assert.equal(result.token_bundle.metadata.code_assist_base_url, "https://cloudcode-pa.googleapis.com/v1internal");
    assert.match(result.token_bundle.metadata.user_agent, /^GeminiCLI\/0\.42\.0-test\/gemini-2\.5-pro \(/);
    assert.equal(result.token_bundle.metadata.google_email, "gemini-user@example.com");
    assert.equal(result.token_bundle.metadata.google_verified_email, true);
  } finally {
    await server.close();
  }
});

test("google_pkce Antigravity refresh stores Code Assist metadata", async () => {
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      res.setHeader("content-type", "application/json");
      if (req.url === "/token") {
        const params = new URLSearchParams(body);
        assert.equal(params.get("grant_type"), "refresh_token");
        assert.equal(params.get("refresh_token"), "refresh-antigravity");
        assert.equal(params.get("client_id"), "gid");
        assert.equal(params.get("client_secret"), "gsec");
        res.end(JSON.stringify({ access_token: "access-antigravity", expires_in: 3600, token_type: "Bearer" }));
        return;
      }
      if (req.url === "/userinfo") {
        assert.equal(req.headers.authorization, "Bearer access-antigravity");
        res.end(JSON.stringify({ id: "google-user-ag", email: "ag-user@example.com", verified_email: true }));
        return;
      }
      if (req.url === "/v1internal:loadCodeAssist") {
        assert.equal(req.headers.authorization, "Bearer access-antigravity");
        assert.match(req.headers["user-agent"], /^antigravity\/1\.20\.5-test /);
        const payload = JSON.parse(body);
        assert.equal(payload.metadata.ideType, "ANTIGRAVITY");
        res.end(JSON.stringify({ cloudaicompanionProject: "ag-project" }));
        return;
      }
      res.writeHead(404).end("{}");
    });
  });

  try {
    const result = await executeJob({
      id: "job-antigravity-refresh",
      account_id: "relay-account-ag",
      job_type: "refresh",
      auth_mode: "google_pkce",
      provider: { name: "Antigravity", provider_type: "antigravity" },
      provider_client: {
        metadata: {
          wrapper_strategy: "antigravity",
          token_url: `${server.url}/token`,
          userinfo_url: `${server.url}/userinfo`,
          code_assist_endpoint: server.url,
          code_assist_fallback_endpoint: server.url,
          client_version: "1.20.5-test",
          client_id: "gid",
          client_secret: "gsec",
        },
      },
      token_bundle: { type: "oauth", access_token: "old-antigravity", refresh_token: "refresh-antigravity" },
      payload: {},
    });

    assert.equal(result.auth_status, "active");
    assert.equal(result.result.strategy, "google_antigravity_refresh");
    assert.equal(result.token_bundle.provider, "google_antigravity");
    assert.equal(result.token_bundle.access_token, "access-antigravity");
    assert.equal(result.token_bundle.subject, "ag-user@example.com");
    assert.deepEqual(result.token_bundle.scopes, [
      "https://www.googleapis.com/auth/cloud-platform",
      "https://www.googleapis.com/auth/userinfo.email",
      "https://www.googleapis.com/auth/userinfo.profile",
      "https://www.googleapis.com/auth/cclog",
      "https://www.googleapis.com/auth/experimentsandconfigs",
    ]);
    assert.equal(result.token_bundle.metadata.client_version, "1.20.5-test");
    assert.equal(result.token_bundle.metadata.oauth_client_id, "gid");
    assert.equal(result.token_bundle.metadata.cloudaicompanion_project, "ag-project");
    assert.equal(result.token_bundle.metadata.code_assist_base_url, `${server.url}/v1internal`);
    assert.equal(result.token_bundle.metadata.code_assist_fallback_base_url, `${server.url}/v1internal`);
    assert.equal(result.token_bundle.metadata.ide_type, "ANTIGRAVITY");
  } finally {
    await server.close();
  }
});

test("kiro refresh exchanges desktop refresh token", async () => {
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      assert.equal(req.method, "POST");
      assert.equal(req.headers["content-type"], "application/json");
      assert.deepEqual(JSON.parse(body), { refreshToken: "refresh-kiro" });
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ accessToken: "access-kiro", expiresIn: 3600, profileArn: "arn:aws:codewhisperer:us-east-1:123:profile/ABC" }));
    });
  });

  try {
    const result = await executeJob({
      id: "job-kiro-refresh",
      account_id: "relay-account-kiro",
      job_type: "refresh",
      auth_mode: "oauth",
      provider: { name: "Kiro", provider_type: "kiro" },
      provider_client: { metadata: { wrapper_strategy: "kiro", token_url: server.url, client_version: "0.7.45-test" } },
      token_bundle: { type: "oauth", access_token: "old-kiro", refresh_token: "refresh-kiro" },
      payload: {},
    });

    assert.equal(result.result.strategy, "kiro_refresh");
    assert.equal(result.token_bundle.provider, "kiro");
    assert.equal(result.token_bundle.access_token, "access-kiro");
    assert.equal(result.token_bundle.metadata.client_version, "0.7.45-test");
    assert.equal(result.token_bundle.metadata.profile_arn, "arn:aws:codewhisperer:us-east-1:123:profile/ABC");
    assert.equal(result.token_bundle.metadata.api_host, "https://q.us-east-1.amazonaws.com");
  } finally {
    await server.close();
  }
});

test("kiro strategy imports local desktop credentials file before refresh", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "kiro-desktop-"));
  const credentialsFile = path.join(dir, "kiro-auth.json");
  await fs.writeFile(credentialsFile, JSON.stringify({
    refreshToken: "refresh-kiro-file",
    profileArn: "arn:aws:codewhisperer:us-east-1:123:profile/OLD",
    region: "us-east-1",
  }));
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      assert.equal(req.method, "POST");
      assert.equal(req.headers["content-type"], "application/json");
      assert.match(req.headers["user-agent"], /^KiroIDE-0\.7\.45-test-/);
      assert.deepEqual(JSON.parse(body), { refreshToken: "refresh-kiro-file" });
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({
        accessToken: "access-kiro-file",
        refreshToken: "refresh-kiro-file-next",
        expiresIn: 3600,
        profileArn: "arn:aws:codewhisperer:us-east-1:123:profile/NEW",
      }));
    });
  });

  try {
    const result = await executeJob({
      id: "job-kiro-file",
      account_id: "relay-account-kiro",
      job_type: "onboarding",
      auth_mode: "kiro",
      provider: { name: "Kiro", provider_type: "kiro" },
      provider_client: { metadata: { credentials_file: credentialsFile, token_url: server.url, client_version: "0.7.45-test" } },
      payload: {},
    });

    assert.equal(result.result.strategy, "kiro_refresh");
    assert.equal(result.token_bundle.access_token, "access-kiro-file");
    assert.equal(result.token_bundle.refresh_token, "refresh-kiro-file-next");
    assert.equal(result.token_bundle.metadata.source, "kiro_desktop_refresh");
    assert.equal(result.token_bundle.metadata.credential_source, "kiro_credentials_file");
    assert.equal(result.token_bundle.metadata.credentials_file, credentialsFile);
    assert.equal(result.token_bundle.metadata.auth_type, "kiro_desktop");
    assert.equal(result.token_bundle.metadata.profile_arn, "arn:aws:codewhisperer:us-east-1:123:profile/NEW");
  } finally {
    await server.close();
  }
});

test("kiro strategy imports Builder ID credentials file before AWS SSO OIDC refresh", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "kiro-builder-id-"));
  const credentialsFile = path.join(dir, "kiro-builder-id.json");
  await fs.writeFile(credentialsFile, JSON.stringify({
    refresh_token: "refresh-builder",
    client_id: "builder-client",
    client_secret: "builder-secret",
    idcRegion: "us-west-2",
    profile_arn: "arn:aws:codewhisperer:us-west-2:123:profile/BUILDER",
    authMethod: "builder-id",
    provider: "kiro",
    startUrl: "https://view.awsapps.com/start",
  }));
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      assert.equal(req.method, "POST");
      assert.equal(req.headers["content-type"], "application/json");
      assert.match(req.headers["user-agent"], /^KiroIDE-0\.7\.45-test-/);
      assert.deepEqual(JSON.parse(body), {
        grantType: "refresh_token",
        clientId: "builder-client",
        clientSecret: "builder-secret",
        refreshToken: "refresh-builder",
      });
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ accessToken: "access-builder", refreshToken: "refresh-builder-next", expiresIn: 7200 }));
    });
  });

  try {
    const result = await executeJob({
      id: "job-kiro-builder",
      account_id: "relay-account-kiro",
      job_type: "onboarding",
      auth_mode: "kiro",
      provider: { name: "Kiro", provider_type: "kiro" },
      provider_client: { metadata: { credentials_file: credentialsFile, token_url: server.url, client_version: "0.7.45-test" } },
      payload: {},
    });

    assert.equal(result.result.strategy, "kiro_refresh");
    assert.equal(result.token_bundle.access_token, "access-builder");
    assert.equal(result.token_bundle.refresh_token, "refresh-builder-next");
    assert.equal(result.token_bundle.metadata.source, "kiro_aws_sso_oidc_refresh");
    assert.equal(result.token_bundle.metadata.credential_source, "kiro_credentials_file");
    assert.equal(result.token_bundle.metadata.auth_type, "aws_sso_oidc");
    assert.equal(result.token_bundle.metadata.sso_region, "us-west-2");
    assert.equal(result.token_bundle.metadata.auth_method, "builder-id");
    assert.equal(result.token_bundle.metadata.start_url, "https://view.awsapps.com/start");
    assert.equal(result.token_bundle.metadata.client_id, "builder-client");
    assert.equal(result.token_bundle.metadata.profile_arn, "arn:aws:codewhisperer:us-west-2:123:profile/BUILDER");
  } finally {
    await server.close();
  }
});

test("windsurf_cli reads Codeium config api_key", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "windsurf-codeium-"));
  const configFile = path.join(dir, "config.json");
  await fs.writeFile(configFile, JSON.stringify({ api_key: "cwkey", email: "windsurf@example.com" }));

  const result = await executeJob({
    id: "job-windsurf",
    account_id: "relay-account-windsurf",
    job_type: "onboarding",
    auth_mode: "windsurf_cli",
    provider: { name: "Windsurf", provider_type: "windsurf_codeium" },
    provider_client: { metadata: { config_file: configFile, ide_version: "1.2.3", extension_version: "1.2.4" } },
    payload: {},
  });

  assert.equal(result.result.strategy, "windsurf_cli");
  assert.equal(result.token_bundle.type, "api_key");
  assert.equal(result.token_bundle.access_token, "cwkey");
  assert.equal(result.token_bundle.provider, "windsurf_codeium");
  assert.equal(result.token_bundle.subject, "windsurf@example.com");
  assert.equal(result.token_bundle.metadata.ide_version, "1.2.3");
  assert.equal(result.token_bundle.metadata.extension_version, "1.2.4");
  assert.equal(result.token_bundle.metadata.api_server_url, "https://server.codeium.com:443/");
  assert.equal(result.token_bundle.metadata.api_host, "server.codeium.com");
  assert.equal(result.token_bundle.metadata.api_port, "443");
  assert.equal(result.token_bundle.metadata.has_enterprise_extension, "false");
  assert.equal(result.token_bundle.metadata.request_metadata.ide_name, "windsurf");
  assert.equal(result.token_bundle.metadata.request_metadata.request_id, "");
  assert.equal(result.token_bundle.metadata.chat_client_query.extension_name, "windsurf");
  assert.equal(result.token_bundle.metadata.chat_client_query.has_index_service, "true");
  assert.equal(result.token_bundle.metadata.chat_client_query.has_enterprise_extension, "false");
});

test("google_pkce refresh without Gemini provider keeps configured client", async () => {
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      const params = new URLSearchParams(body);
      assert.equal(params.get("grant_type"), "refresh_token");
      assert.equal(params.get("refresh_token"), "refresh-google");
      assert.equal(params.get("client_id"), "client-google");
      assert.equal(params.get("client_secret"), "secret-google");
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ access_token: "access-google", expires_in: 3600, scope: "email profile" }));
    });
  });

  try {
    const result = await executeJob({
      id: "job-google-refresh",
      account_id: "relay-account-google",
      job_type: "refresh",
      auth_mode: "google_pkce",
      provider: { name: "Google Generic", provider_type: "google" },
      provider_client: { metadata: { token_url: server.url, client_id: "client-google", client_secret: "secret-google" } },
      token_bundle: { type: "oauth", access_token: "old-google", refresh_token: "refresh-google" },
      payload: {},
    });

    assert.equal(result.result.strategy, "oauth_refresh");
    assert.equal(result.token_bundle.provider, "Google Generic");
    assert.equal(result.token_bundle.access_token, "access-google");
    assert.deepEqual(result.token_bundle.scopes, ["email", "profile"]);
  } finally {
    await server.close();
  }
});

test("mock strategy can satisfy refresh jobs", async () => {
  const result = await executeJob({
    id: "job-refresh-mock",
    account_id: "account-mock",
    job_type: "refresh",
    auth_mode: "mock",
    provider: { name: "mock-provider" },
    provider_client: { metadata: { wrapper_strategy: "mock" } },
    token_bundle: { type: "oauth", access_token: "old", refresh_token: "old-refresh" },
    payload: {},
  });

  assert.equal(result.auth_status, "active");
  assert.equal(result.token_bundle.access_token, "mock-access-job-refresh-mock");
});

test("codex_cli strategy reads Codex auth.json", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "codex-auth-"));
  const authFile = path.join(dir, "auth.json");
  const installationFile = path.join(dir, "installation_id");
  const installationId = "11111111-1111-4111-8111-111111111111";
  await fs.writeFile(installationFile, installationId);
  const accessClaims = {
    exp: Math.floor(Date.now() / 1000) + 3600,
    scp: ["openid", "offline_access"],
    sub: "subject-1",
    "https://api.openai.com/auth": { chatgpt_account_id: "account-1" },
  };
  const accessToken = `header.${Buffer.from(JSON.stringify(accessClaims)).toString("base64url")}.sig`;
  const idToken = jwt({
    sub: "chatgpt-user-1",
    "https://api.openai.com/auth": {
      chatgpt_account_id: "account-1",
      chatgpt_user_id: "chatgpt-user-1",
      chatgpt_plan_type: "plus",
      chatgpt_account_is_fedramp: true,
    },
  });
  await fs.writeFile(authFile, JSON.stringify({
    auth_mode: "chatgpt",
    tokens: {
      access_token: accessToken,
      refresh_token: "refresh-1",
      account_id: "account-1",
      id_token: idToken,
    },
  }));

  const result = await executeJob({
    id: "job-codex",
    account_id: "relay-account-1",
    job_type: "onboarding",
    auth_mode: "codex_cli",
    provider: { name: "OpenAI Codex" },
    provider_client: { metadata: { auth_file: authFile } },
    payload: {},
  });

  assert.equal(result.auth_status, "active");
  assert.equal(result.token_bundle.provider, "openai_codex");
  assert.equal(result.token_bundle.access_token, accessToken);
  assert.equal(result.token_bundle.refresh_token, "refresh-1");
  assert.equal(result.token_bundle.subject, "account-1");
  assert.deepEqual(result.token_bundle.scopes, ["openid", "offline_access"]);
  assert.equal(result.token_bundle.metadata.originator, "codex_exec");
  assert.equal(result.token_bundle.metadata.client_version, "0.0.0");
  assert.equal(result.token_bundle.metadata.chatgpt_account_id, "account-1");
  assert.equal(result.token_bundle.metadata.chatgpt_user_id, "chatgpt-user-1");
  assert.equal(result.token_bundle.metadata.chatgpt_plan_type, "plus");
  assert.equal(result.token_bundle.metadata.chatgpt_account_is_fedramp, true);
  assert.equal(result.token_bundle.metadata.installation_id, installationId);
});

test("codex_cli strategy generates installation_id when missing", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "codex-install-"));
  const authFile = path.join(dir, "auth.json");
  const accessClaims = {
    exp: Math.floor(Date.now() / 1000) + 3600,
    scp: ["openid", "offline_access"],
    sub: "subject-2",
    "https://api.openai.com/auth": { chatgpt_account_id: "account-2" },
  };
  const accessToken = `header.${Buffer.from(JSON.stringify(accessClaims)).toString("base64url")}.sig`;
  await fs.writeFile(authFile, JSON.stringify({
    auth_mode: "chatgpt",
    tokens: {
      access_token: accessToken,
      refresh_token: "refresh-2",
      account_id: "account-2",
    },
  }));

  const result = await executeJob({
    id: "job-codex-install",
    account_id: "relay-account-2",
    job_type: "onboarding",
    auth_mode: "codex_cli",
    provider: { name: "OpenAI Codex" },
    provider_client: { metadata: { auth_file: authFile } },
    payload: {},
  });

  const installationId = result.token_bundle.metadata.installation_id;
  assert.match(installationId, /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i);
  const persisted = await fs.readFile(path.join(dir, "installation_id"), "utf8");
  assert.equal(persisted.trim(), installationId);
});

test("codex_cli strategy performs device login when auth.json is missing", async () => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "codex-device-"));
  const authFile = path.join(dir, "auth.json");
  const accessToken = jwt({
    exp: Math.floor(Date.now() / 1000) + 3600,
    scp: ["openid", "offline_access"],
    "https://api.openai.com/auth": { chatgpt_account_id: "codex-account" },
  });
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      res.setHeader("content-type", "application/json");
      if (req.url === "/api/accounts/deviceauth/usercode") {
        assert.equal(JSON.parse(body).client_id, "client-codex");
        res.end(JSON.stringify({ user_code: "ABCD-EFGH", device_auth_id: "device-1", interval: 1 }));
        return;
      }
      if (req.url === "/api/accounts/deviceauth/token") {
        const data = JSON.parse(body);
        assert.equal(data.device_auth_id, "device-1");
        res.end(JSON.stringify({ authorization_code: "auth-code", code_verifier: "verifier" }));
        return;
      }
      if (req.url === "/oauth/token") {
        const params = new URLSearchParams(body);
        assert.equal(params.get("grant_type"), "authorization_code");
        assert.equal(params.get("code"), "auth-code");
        res.end(JSON.stringify({ access_token: accessToken, refresh_token: "refresh-codex", id_token: "id-token" }));
        return;
      }
      res.writeHead(404).end("{}");
    });
  });
  const progress = [];
  try {
    const result = await executeJob({
      id: "job-codex-device",
      account_id: "relay-account-codex",
      job_type: "onboarding",
      auth_mode: "codex_cli",
      provider: { name: "OpenAI Codex" },
      provider_client: { metadata: { issuer: server.url, client_id: "client-codex", auth_file: authFile } },
      payload: {},
    }, { progress: (item) => progress.push(item) });

    assert.equal(result.token_bundle.provider, "openai_codex");
    assert.equal(result.token_bundle.access_token, accessToken);
    assert.equal(result.token_bundle.refresh_token, "refresh-codex");
    assert.equal(result.token_bundle.auth_scheme, "bearer");
    assert.equal(progress[0].user_code, "ABCD-EFGH");
  } finally {
    await server.close();
  }
});

test("claude_cli strategy reads Claude .credentials.json", async () => {
  const previousOauthToken = process.env.CLAUDE_CODE_OAUTH_TOKEN;
  const previousAuthToken = process.env.ANTHROPIC_AUTH_TOKEN;
  delete process.env.CLAUDE_CODE_OAUTH_TOKEN;
  delete process.env.ANTHROPIC_AUTH_TOKEN;
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "claude-creds-"));
  const credentialsFile = path.join(dir, ".credentials.json");
  await fs.writeFile(credentialsFile, JSON.stringify({
    claudeAiOauth: {
      accessToken: "claude-access",
      refreshToken: "claude-refresh",
      expiresAt: Date.now() + 3600_000,
      scopes: ["user:profile", "user:inference"],
      subscriptionType: "pro",
      rateLimitTier: "default",
      tokenAccount: { uuid: "claude-account-file" },
    },
  }));

  let result;
  try {
    result = await executeJob({
      id: "job-claude-file",
      account_id: "relay-account-claude",
      job_type: "onboarding",
      auth_mode: "claude_cli",
      provider: { name: "Claude Code" },
      provider_client: { metadata: { credentials_file: credentialsFile } },
      payload: {},
    });
  } finally {
    if (previousOauthToken === undefined) {
      delete process.env.CLAUDE_CODE_OAUTH_TOKEN;
    } else {
      process.env.CLAUDE_CODE_OAUTH_TOKEN = previousOauthToken;
    }
    if (previousAuthToken === undefined) {
      delete process.env.ANTHROPIC_AUTH_TOKEN;
    } else {
      process.env.ANTHROPIC_AUTH_TOKEN = previousAuthToken;
    }
  }

  assert.equal(result.token_bundle.provider, "anthropic_claude");
  assert.equal(result.token_bundle.access_token, "claude-access");
  assert.equal(result.token_bundle.refresh_token, "claude-refresh");
  assert.equal(result.token_bundle.auth_scheme, "bearer");
  assert.equal(result.token_bundle.subject, "claude-account-file");
  assert.deepEqual(result.token_bundle.scopes, ["user:profile", "user:inference"]);
  assert.equal(result.token_bundle.metadata.client_version, "2.1.104");
  assert.equal(result.token_bundle.metadata.entrypoint, "cli");
  assert.equal(result.token_bundle.metadata.user_type, "external");
  assert.equal(result.token_bundle.metadata.account_uuid, "claude-account-file");
});

test("claude_cli strategy reads ANTHROPIC_AUTH_TOKEN", async () => {
  const previousOauthToken = process.env.CLAUDE_CODE_OAUTH_TOKEN;
  const previousAuthToken = process.env.ANTHROPIC_AUTH_TOKEN;
  const previousAuthTokenScopes = process.env.ANTHROPIC_AUTH_TOKEN_SCOPES;
  delete process.env.CLAUDE_CODE_OAUTH_TOKEN;
  process.env.ANTHROPIC_AUTH_TOKEN = "anthropic-auth-token";
  process.env.ANTHROPIC_AUTH_TOKEN_SCOPES = "user:inference";

  try {
    const result = await executeJob({
      id: "job-claude-auth-token",
      account_id: "relay-account-claude",
      job_type: "onboarding",
      auth_mode: "claude_cli",
      provider: { name: "Claude Code" },
      provider_client: { metadata: {} },
      payload: {},
    });

    assert.equal(result.token_bundle.provider, "anthropic_claude");
    assert.equal(result.token_bundle.access_token, "anthropic-auth-token");
    assert.equal(result.token_bundle.auth_scheme, "bearer");
    assert.deepEqual(result.token_bundle.scopes, ["user:inference"]);
    assert.equal(result.token_bundle.metadata.source, "ANTHROPIC_AUTH_TOKEN");
    assert.equal(result.token_bundle.metadata.token_source, "ANTHROPIC_AUTH_TOKEN");
  } finally {
    if (previousOauthToken === undefined) {
      delete process.env.CLAUDE_CODE_OAUTH_TOKEN;
    } else {
      process.env.CLAUDE_CODE_OAUTH_TOKEN = previousOauthToken;
    }
    if (previousAuthToken === undefined) {
      delete process.env.ANTHROPIC_AUTH_TOKEN;
    } else {
      process.env.ANTHROPIC_AUTH_TOKEN = previousAuthToken;
    }
    if (previousAuthTokenScopes === undefined) {
      delete process.env.ANTHROPIC_AUTH_TOKEN_SCOPES;
    } else {
      process.env.ANTHROPIC_AUTH_TOKEN_SCOPES = previousAuthTokenScopes;
    }
  }
});

test("claude_cli strategy performs manual PKCE flow", async () => {
  const claudeConfigDir = await fs.mkdtemp(path.join(os.tmpdir(), "claude-empty-"));
  const previousConfigDir = process.env.CLAUDE_CONFIG_DIR;
  const previousOauthToken = process.env.CLAUDE_CODE_OAUTH_TOKEN;
  const previousAuthToken = process.env.ANTHROPIC_AUTH_TOKEN;
  process.env.CLAUDE_CONFIG_DIR = claudeConfigDir;
  delete process.env.CLAUDE_CODE_OAUTH_TOKEN;
  delete process.env.ANTHROPIC_AUTH_TOKEN;
  const server = await listen((req, res) => {
    assert.equal(req.method, "POST");
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      const data = JSON.parse(body);
      assert.equal(data.grant_type, "authorization_code");
      assert.equal(data.code, "claude-code");
      assert.equal(data.client_id, "client-claude");
      assert.ok(data.code_verifier);
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({
        access_token: "claude-access-new",
        refresh_token: "claude-refresh-new",
        expires_in: 3600,
        scope: "user:profile user:inference",
        account: { uuid: "claude-account", email_address: "user@example.com" },
        organization: { uuid: "org-1" },
      }));
    });
  });
  const progress = [];
  try {
    const result = await executeJob({
      id: "job-claude-manual",
      account_id: "relay-account-claude",
      job_type: "onboarding",
      auth_mode: "claude_cli",
      provider: { name: "Claude Code" },
      provider_client: { metadata: { auth_url: "https://claude.test/oauth/authorize", token_url: server.url, client_id: "client-claude", manual_input_poll_ms: 1, manual_input_timeout_seconds: 5 } },
      payload: {},
    }, {
      progress: (item) => progress.push(item),
      input: () => ({ input: { authorization_code: "claude-code", state: progress[0].state } }),
    });

    assert.equal(progress[0].status, "authorization_code_required");
    assert.equal(new URL(progress[0].authorization_url).searchParams.get("client_id"), "client-claude");
    assert.equal(result.token_bundle.provider, "anthropic_claude");
    assert.equal(result.token_bundle.access_token, "claude-access-new");
    assert.equal(result.token_bundle.subject, "claude-account");
    assert.equal(result.token_bundle.auth_scheme, "bearer");
    assert.equal(result.token_bundle.metadata.client_version, "2.1.104");
    assert.equal(result.token_bundle.metadata.account_uuid, "claude-account");
  } finally {
    if (previousConfigDir === undefined) {
      delete process.env.CLAUDE_CONFIG_DIR;
    } else {
      process.env.CLAUDE_CONFIG_DIR = previousConfigDir;
    }
    if (previousOauthToken === undefined) {
      delete process.env.CLAUDE_CODE_OAUTH_TOKEN;
    } else {
      process.env.CLAUDE_CODE_OAUTH_TOKEN = previousOauthToken;
    }
    if (previousAuthToken === undefined) {
      delete process.env.ANTHROPIC_AUTH_TOKEN;
    } else {
      process.env.ANTHROPIC_AUTH_TOKEN = previousAuthToken;
    }
    await server.close();
  }
});

test("github_device strategy exchanges GitHub token for Copilot token", async () => {
  const server = await listen((req, res) => {
    let body = "";
    req.on("data", (chunk) => body += chunk);
    req.on("end", () => {
      res.setHeader("content-type", "application/json");
      if (req.url === "/login/device/code") {
        const params = new URLSearchParams(body);
        assert.equal(params.get("client_id"), "client-github");
        res.end(JSON.stringify({ device_code: "device-gh", user_code: "GH-CODE", verification_uri: "https://github.test/login/device", expires_in: 900, interval: 1 }));
        return;
      }
      if (req.url === "/login/oauth/access_token") {
        const params = new URLSearchParams(body);
        assert.equal(params.get("grant_type"), "urn:ietf:params:oauth:grant-type:device_code");
        assert.equal(params.get("device_code"), "device-gh");
        res.end(JSON.stringify({ access_token: "ghtok", token_type: "bearer", scope: "read:user" }));
        return;
      }
      if (req.url === "/copilot_internal/v2/token") {
        assert.equal(req.headers.authorization, "token ghtok");
        assert.equal(req.headers["editor-plugin-version"], "copilot-chat/0.99.0");
        assert.equal(req.headers["x-github-api-version"], "2025-05-01");
        res.end(JSON.stringify({ token: "cptok", expires_at: Math.floor(Date.now() / 1000) + 3600, refresh_in: 1500 }));
        return;
      }
      if (req.url === "/user") {
        assert.equal(req.headers.authorization, "token ghtok");
        res.end(JSON.stringify({ login: "octocat" }));
        return;
      }
      res.writeHead(404).end("{}");
    });
  });

  try {
    const result = await executeJob({
      id: "job-github-copilot",
      account_id: "relay-account-github",
      job_type: "onboarding",
      auth_mode: "github_device",
      provider: { name: "GitHub Copilot", provider_type: "github_copilot" },
      provider_client: {
        metadata: {
          wrapper_strategy: "github_copilot",
          device_authorization_url: `${server.url}/login/device/code`,
          token_url: `${server.url}/login/oauth/access_token`,
          github_api_base_url: server.url,
          client_id: "client-github",
          scopes: ["read:user"],
          client_version: "0.99.0",
          account_type: "business",
        },
      },
      payload: {},
    });

    assert.equal(result.auth_status, "active");
    assert.equal(result.token_bundle.provider, "github_copilot");
    assert.equal(result.token_bundle.access_token, "cptok");
    assert.equal(result.token_bundle.refresh_token, "ghtok");
    assert.equal(result.token_bundle.auth_scheme, "bearer");
    assert.equal(result.token_bundle.subject, "octocat");
    assert.deepEqual(result.token_bundle.scopes, ["read:user"]);
    assert.equal(result.token_bundle.metadata.client_version, "0.99.0");
    assert.equal(result.token_bundle.metadata.api_version, "2025-05-01");
    assert.equal(result.token_bundle.metadata.account_type, "business");
    assert.equal(result.token_bundle.metadata.interaction_type, "conversation-panel");
    assert.equal(result.token_bundle.metadata.github_login, "octocat");
    assert.equal(result.token_bundle.metadata.copilot_base_url, "https://api.business.githubcopilot.com");
    assert.equal(result.result.strategy, "github_copilot_device");
  } finally {
    await server.close();
  }
});

test("runner claims and completes a job", async () => {
  const calls = [];
  const client = {
    async claim(body) {
      calls.push(["claim", body]);
      return {
        job: {
          id: "job-3",
          account_id: "account-3",
          job_type: "onboarding",
          auth_mode: "mock",
          provider: { name: "mock" },
          provider_client: { metadata: {} },
          payload: {},
        },
      };
    },
    async complete(jobId, body) {
      calls.push(["complete", jobId, body]);
      return {};
    },
    async fail() {
      throw new Error("fail should not be called");
    },
  };

  const processed = await runOnce({
    leaseOwner: "test-worker",
    leaseSeconds: 60,
    supportedModes: ["mock"],
  }, { client, logger: silentLogger() });

  assert.equal(processed, true);
  assert.equal(calls[0][0], "claim");
  assert.equal(calls[1][0], "complete");
  assert.equal(calls[1][1], "job-3");
  assert.equal(calls[1][2].token_bundle.access_token, "mock-access-job-3");
});

function listen(handler) {
  const server = http.createServer(handler);
  return new Promise((resolve) => {
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      resolve({
        url: `http://127.0.0.1:${address.port}`,
        close: () => new Promise((done) => server.close(done)),
      });
    });
  });
}

function jwt(claims) {
  return `header.${Buffer.from(JSON.stringify(claims)).toString("base64url")}.sig`;
}

function silentLogger() {
  return { log() {}, error() {} };
}
