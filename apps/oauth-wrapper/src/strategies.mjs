import crypto from "node:crypto";
import fs from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const CODEX_ISSUER = "https://auth.openai.com";
const CODEX_CLIENT_ID = "app_EMoamEEZ73f0CkXaXp7hrann";
const CLAUDE_CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
const CLAUDE_AUTH_URL = "https://claude.com/cai/oauth/authorize";
const CLAUDE_TOKEN_URL = "https://platform.claude.com/v1/oauth/token";
const CLAUDE_MANUAL_REDIRECT_URL = "https://platform.claude.com/oauth/code/callback";
const CLAUDE_OAUTH_SCOPES = [
  "org:create_api_key",
  "user:profile",
  "user:inference",
  "user:sessions:claude_code",
  "user:mcp_servers",
  "user:file_upload",
];
const CLAUDE_OAUTH_BETA = "oauth-2025-04-20";
const CODEX_CLI_VERSION = "0.0.0";
const CLAUDE_CODE_VERSION = "2.1.104";
const GITHUB_COPILOT_API_BASE_URL = "https://api.github.com";
const GITHUB_COPILOT_BASE_URL = "https://api.githubcopilot.com";
const GITHUB_COPILOT_CLIENT_VERSION = "0.44.0";
const GITHUB_COPILOT_VSCODE_VERSION = "1.109.3";
const GEMINI_OAUTH_CLIENT_ID = "";
const GEMINI_OAUTH_CLIENT_SECRET = "";
const GEMINI_AUTH_URL = "https://accounts.google.com/o/oauth2/v2/auth";
const GEMINI_TOKEN_URL = "https://oauth2.googleapis.com/token";
const GEMINI_USERINFO_URL = "https://www.googleapis.com/oauth2/v2/userinfo";
const GEMINI_CODE_ASSIST_ENDPOINT = "https://cloudcode-pa.googleapis.com";
const GEMINI_CODE_ASSIST_API_VERSION = "v1internal";
const GEMINI_CLI_VERSION = "0.42.0-nightly.20260428.g59b2dea0e";
const GEMINI_GENAI_SDK_CLIENT = "google-genai-sdk/1.41.0 gl-node/v22.19.0";
const GEMINI_OAUTH_SCOPES = [
  "https://www.googleapis.com/auth/cloud-platform",
  "https://www.googleapis.com/auth/userinfo.email",
  "https://www.googleapis.com/auth/userinfo.profile",
];
const ANTIGRAVITY_OAUTH_CLIENT_ID = "";
const ANTIGRAVITY_OAUTH_CLIENT_SECRET = "";
const ANTIGRAVITY_CLIENT_VERSION = "1.20.5";
const ANTIGRAVITY_CODE_ASSIST_ENDPOINT = "https://daily-cloudcode-pa.googleapis.com";
const ANTIGRAVITY_CODE_ASSIST_FALLBACK_ENDPOINT = "https://cloudcode-pa.googleapis.com";
const ANTIGRAVITY_OAUTH_SCOPES = [
  "https://www.googleapis.com/auth/cloud-platform",
  "https://www.googleapis.com/auth/userinfo.email",
  "https://www.googleapis.com/auth/userinfo.profile",
  "https://www.googleapis.com/auth/cclog",
  "https://www.googleapis.com/auth/experimentsandconfigs",
];
const KIRO_API_REGION = "us-east-1";
const KIRO_CLIENT_VERSION = "0.7.45";
const WINDSURF_IDE_VERSION = "1.20.9";
const WINDSURF_EXTENSION_VERSION = "1.20.9";
const WINDSURF_LANGUAGE_SERVER_VERSION = "1.20.9";

export class TerminalJobError extends Error {
  constructor(message, authStatus = "reauth_required") {
    super(message);
    this.name = "TerminalJobError";
    this.terminal = true;
    this.authStatus = authStatus;
  }
}

export async function executeJob(job, options = {}) {
  const jobType = normalized(job.job_type);
  const authMode = normalized(job.auth_mode);
  const strategy = normalized(job.payload?.strategy || providerMetadata(job).wrapper_strategy || providerMetadata(job).strategy);
  const externalMode = authMode.endsWith("_cli") || strategy === "external_cli";
  const mockMode = authMode === "mock" || strategy === "mock";

  if (authMode === "codex_cli" || strategy === "codex_cli") return codexCli(job, options);
  if (authMode === "claude_cli" || strategy === "claude_cli") return claudeCli(job, options);
  if (authMode === "kiro" || strategy === "kiro") return kiroCli(job);
  if (authMode === "windsurf_cli" || strategy === "windsurf_cli" || strategy === "codeium_cli") return windsurfCli(job);
  if (jobType === "revoke" && externalMode) return externalCli(job, options);
  if (jobType === "revoke") return revoke(job, options);
  if (mockMode) return mock(job);
  if (jobType === "refresh" && externalMode) return externalCli(job, options);
  if (jobType === "refresh") return refresh(job, options);

  const payloadBundle = objectValue(job.payload?.token_bundle);
  if (payloadBundle?.access_token) {
    return completeWithBundle(job, payloadBundle, "payload_token_bundle");
  }

  const metadataBundle = objectValue(providerMetadata(job).token_bundle);
  if (metadataBundle?.access_token) {
    return completeWithBundle(job, metadataBundle, "metadata_token_bundle");
  }

  if (authMode === "github_device" || strategy === "device") return deviceFlow(job, options);
  if (authMode === "google_pkce" || strategy === "pkce") return pkceFlow(job, options);
  if (externalMode) return externalCli(job, options);

  if (currentBundle(job).refresh_token && tokenUrl(job)) return refresh(job, options);
  throw new TerminalJobError(`No executable OAuth strategy configured for auth_mode=${job.auth_mode}.`);
}

async function refresh(job, _options) {
  const bundle = currentBundle(job);
  if (isGitHubCopilotJob(job)) {
    return githubCopilotRefresh(job, bundle);
  }
  if (isKiroJob(job)) {
    return kiroRefresh(job, bundle);
  }
  if (!bundle.refresh_token) {
    throw new TerminalJobError("Current token bundle has no refresh_token.");
  }
  const url = tokenUrl(job);
  if (!url) {
    throw new TerminalJobError("provider_client.metadata.token_url is required for refresh.");
  }

  const metadata = providerMetadata(job);
  const response = await tokenRequest(url, {
    grant_type: "refresh_token",
    refresh_token: bundle.refresh_token,
    client_id: clientId(job),
    client_secret: clientSecret(job),
  }, metadata);
  const refreshed = bundleFromTokenResponse(job, response, bundle);
  if (isGoogleGeminiJob(job)) {
    return completeWithBundle(job, await googleGeminiBundleFromOAuth(job, response, refreshed, "google_gemini_refresh"), "google_gemini_refresh");
  }
  if (isGoogleAntigravityJob(job)) {
    return completeWithBundle(job, await googleAntigravityBundleFromOAuth(job, response, refreshed, "google_antigravity_refresh"), "google_antigravity_refresh");
  }
  return completeWithBundle(job, refreshed, "oauth_refresh");
}

async function revoke(job, _options) {
  const metadata = providerMetadata(job);
  const url = stringValue(metadata.revoke_url);
  if (url) {
    const bundle = currentBundle(job);
    const token = stringValue(job.payload?.token || bundle.refresh_token || bundle.access_token);
    if (!token) throw new TerminalJobError("No token is available for revoke.", "revoked");
    await formPost(url, {
      token,
      client_id: clientId(job),
      client_secret: clientSecret(job),
    }, metadata);
  }
  return { auth_status: "revoked", result: { strategy: "oauth_revoke" } };
}

async function codexCli(job, options) {
  if (normalized(job.job_type) === "revoke") {
    await codexRevoke(job);
    return { auth_status: "revoked", result: { strategy: "codex_cli_revoke" } };
  }

  if (normalized(job.job_type) === "refresh" && currentBundle(job).refresh_token) {
    return codexRefresh(job);
  }

  const metadata = providerMetadata(job);
  const payload = objectValue(job.payload) || {};
  const authFile = codexAuthFile(metadata, payload);
  let auth = await readCodexAuth(authFile);
  if (!auth?.tokens?.access_token && (payload.login_command || metadata.login_command)) {
    await runCommand(payload.login_command || metadata.login_command, envForCommand(job, metadata, payload), Number(metadata.command_timeout_ms || payload.command_timeout_ms || 120000));
    auth = await readCodexAuth(authFile);
  }
  if (!auth?.tokens?.access_token) {
    return codexDeviceLogin(job, options);
  }
  const codexHome = path.dirname(authFile);
  const installationId = await resolveCodexInstallationId(codexHome);

  return completeWithBundle(job, codexBundleFromTokens(auth.tokens, {
    source: "codex_auth_file",
    auth_mode: auth.auth_mode || "",
    installation_id: installationId,
  }), "codex_cli");
}

async function claudeCli(job, options) {
  const jobType = normalized(job.job_type);
  if (jobType === "revoke") {
    return { auth_status: "revoked", result: { strategy: "claude_cli_revoke" } };
  }
  if (jobType === "refresh" && currentBundle(job).refresh_token) {
    return claudeRefresh(job);
  }

  const credentials = await readClaudeCredentials(job);
  if (credentials?.access_token) {
    return completeWithBundle(job, credentials, "claude_credentials_file");
  }
  return claudeManualPkceLogin(job, options);
}

async function codexRefresh(job) {
  const bundle = currentBundle(job);
  const response = await jsonPost(codexTokenUrl(job), {
    client_id: codexClientId(job),
    grant_type: "refresh_token",
    refresh_token: bundle.refresh_token,
  });
  return completeWithBundle(job, codexBundleFromTokens({
    access_token: response.access_token,
    refresh_token: response.refresh_token || bundle.refresh_token,
    id_token: response.id_token || bundle.metadata?.id_token || "",
  }, { source: "codex_refresh" }), "codex_refresh");
}

async function codexRevoke(job) {
  const bundle = currentBundle(job);
  const token = stringValue(job.payload?.token || bundle.refresh_token || bundle.access_token);
  if (!token) return;
  await jsonPost(codexRevokeUrl(job), {
    token,
    token_type_hint: bundle.refresh_token ? "refresh_token" : "access_token",
    client_id: codexClientId(job),
  });
}

async function codexDeviceLogin(job, options) {
  const issuer = codexIssuer(job);
  const device = await jsonPost(`${issuer}/api/accounts/deviceauth/usercode`, { client_id: codexClientId(job) });
  const verificationUrl = device.verification_url || `${issuer}/codex/device`;
  const expiresAt = Date.now() + 15 * 60 * 1000;
  await reportProgress(options, {
    provider: "openai_codex",
    mode: "device",
    status: "authorization_pending",
    authorization_url: verificationUrl,
    user_code: stringValue(device.user_code),
    expires_at: new Date(expiresAt).toISOString(),
  });
  options.log?.(`Authorize ${job.id}: ${verificationUrl} code=${device.user_code}`);

  const intervalMs = Math.max(1, Number(device.interval || 5)) * 1000;
  while (Date.now() < expiresAt) {
    await sleep(intervalMs);
    const polled = await jsonPost(`${issuer}/api/accounts/deviceauth/token`, {
      device_auth_id: device.device_auth_id,
      user_code: device.user_code,
    }, { allowStatuses: [403, 404] });
    if (polled.authorization_code) {
      const response = await formPost(codexTokenUrl(job), {
        grant_type: "authorization_code",
        code: polled.authorization_code,
        redirect_uri: `${issuer}/deviceauth/callback`,
        client_id: codexClientId(job),
        code_verifier: polled.code_verifier,
      }, providerMetadata(job));
      await reportProgress(options, {
        provider: "openai_codex",
        mode: "device",
        status: "token_received",
      });
      const metadata = providerMetadata(job);
      const payload = objectValue(job.payload) || {};
      const installationId = await resolveCodexInstallationId(path.dirname(codexAuthFile(metadata, payload)));
      return completeWithBundle(job, codexBundleFromTokens({
        access_token: response.access_token,
        refresh_token: response.refresh_token,
        id_token: response.id_token,
      }, { source: "codex_device", installation_id: installationId }), "codex_device");
    }
  }
  throw new TerminalJobError("Codex device authorization expired.");
}

async function claudeRefresh(job) {
  const bundle = currentBundle(job);
  const response = await jsonPost(claudeTokenUrl(job), {
    grant_type: "refresh_token",
    refresh_token: bundle.refresh_token,
    client_id: claudeClientId(job),
    scope: claudeScopes(job).join(" "),
  });
  return completeWithBundle(job, claudeBundleFromTokenResponse(job, response, bundle, "claude_refresh"), "claude_refresh");
}

async function claudeManualPkceLogin(job, options) {
  const verifier = base64Url(crypto.randomBytes(32));
  const challenge = base64Url(crypto.createHash("sha256").update(verifier).digest());
  const state = base64Url(crypto.randomBytes(24));
  const authUrl = new URL(claudeAuthUrl(job));
  authUrl.searchParams.set("code", "true");
  authUrl.searchParams.set("client_id", claudeClientId(job));
  authUrl.searchParams.set("response_type", "code");
  authUrl.searchParams.set("redirect_uri", claudeRedirectUrl(job));
  authUrl.searchParams.set("scope", claudeScopes(job).join(" "));
  authUrl.searchParams.set("code_challenge", challenge);
  authUrl.searchParams.set("code_challenge_method", "S256");
  authUrl.searchParams.set("state", state);

  await reportProgress(options, {
    provider: "anthropic_claude",
    mode: "manual_pkce",
    status: "authorization_code_required",
    authorization_url: authUrl.toString(),
    input: "authorization_code",
    state,
  });
  options.log?.(`Authorize ${job.id}: ${authUrl.toString()}`);

  const input = await waitForManualInput(job, options);
  const code = stringValue(input.authorization_code || input.code);
  if (!code) throw new TerminalJobError("Claude authorization_code was not submitted.");
  if (input.state && input.state !== state) {
    throw new TerminalJobError("Claude authorization_code state does not match.");
  }

  const response = await jsonPost(claudeTokenUrl(job), {
    grant_type: "authorization_code",
    code,
    redirect_uri: claudeRedirectUrl(job),
    client_id: claudeClientId(job),
    code_verifier: verifier,
    state,
  });
  await reportProgress(options, {
    provider: "anthropic_claude",
    mode: "manual_pkce",
    status: "token_received",
  });
  return completeWithBundle(job, claudeBundleFromTokenResponse(job, response, {}, "claude_manual_pkce"), "claude_manual_pkce");
}

async function mock(job) {
  const payloadBundle = objectValue(job.payload?.token_bundle);
  const metadataBundle = objectValue(providerMetadata(job).mock_token_bundle);
  const selected = payloadBundle?.access_token ? payloadBundle : metadataBundle?.access_token ? metadataBundle : {};
  const expiresAt = selected.expires_at || new Date(Date.now() + 60 * 60 * 1000).toISOString();
  return completeWithBundle(job, {
    type: selected.type || "oauth",
    access_token: selected.access_token || `mock-access-${job.id}`,
    refresh_token: selected.refresh_token || `mock-refresh-${job.id}`,
    expires_at: expiresAt,
    scopes: selected.scopes || scopes(job),
    provider: selected.provider || normalized(job.provider?.name) || "mock",
    subject: selected.subject || `mock-subject-${job.account_id}`,
    metadata: selected.metadata || {},
  }, "mock");
}

async function deviceFlow(job, options) {
  const metadata = providerMetadata(job);
  const defaults = deviceDefaults(job.auth_mode);
  const deviceUrl = stringValue(metadata.device_authorization_url || defaults.device_authorization_url);
  const pollingUrl = stringValue(metadata.token_url || defaults.token_url);
  const id = clientId(job);
  if (!deviceUrl || !pollingUrl || !id) {
    throw new TerminalJobError("device_authorization_url, token_url, and client_id are required for device flow.");
  }

  const device = await formPost(deviceUrl, { client_id: id, scope: scopes(job).join(" ") }, metadata);
  const authorizeUrl = device.verification_uri_complete || `${device.verification_uri || device.verification_url} code=${device.user_code}`;
  options.log?.(`Authorize ${job.id}: ${authorizeUrl}`);

  const expiresAt = Date.now() + Number(device.expires_in || 900) * 1000;
  let intervalMs = Number(device.interval || metadata.device_poll_interval_seconds || 5) * 1000;
  while (Date.now() < expiresAt) {
    await sleep(intervalMs);
    const response = await formPost(pollingUrl, {
      grant_type: "urn:ietf:params:oauth:grant-type:device_code",
      device_code: device.device_code,
      client_id: id,
      client_secret: clientSecret(job),
    }, metadata, { allowOAuthError: true });
    if (response.access_token) {
      const bundle = bundleFromTokenResponse(job, response, currentBundle(job));
      if (isGitHubCopilotJob(job)) {
        return completeWithBundle(job, await githubCopilotBundleFromGitHubToken(job, response.access_token, bundle), "github_copilot_device");
      }
      return completeWithBundle(job, bundle, "oauth_device");
    }
    if (response.error === "authorization_pending") continue;
    if (response.error === "slow_down") {
      intervalMs += 5000;
      continue;
    }
    throw new TerminalJobError(response.error_description || response.error || "Device authorization failed.");
  }
  throw new TerminalJobError("Device authorization expired.");
}

async function pkceFlow(job, options) {
  const metadata = providerMetadata(job);
  const authUrl = authUrlForJob(job);
  const url = tokenUrl(job);
  const id = clientId(job);
  if (!authUrl || !url || !id) {
    throw new TerminalJobError("auth_url, token_url, and client_id are required for PKCE flow.");
  }

  const verifier = base64Url(crypto.randomBytes(32));
  const challenge = base64Url(crypto.createHash("sha256").update(verifier).digest());
  const state = base64Url(crypto.randomBytes(24));
  const redirectPath = isGoogleGeminiJob(job) ? "/oauth2callback" : isGoogleAntigravityJob(job) ? "/oauth-callback" : "/callback";
  const redirectUri = stringValue(metadata.redirect_uri) || `http://127.0.0.1:${Number(metadata.redirect_port || 18765)}${stringValue(metadata.redirect_path) || redirectPath}`;
  const callback = waitForCallback(redirectUri, state, Number(metadata.callback_timeout_seconds || 300));

  const query = new URLSearchParams({
    response_type: "code",
    client_id: id,
    redirect_uri: redirectUri,
    scope: scopes(job).join(" "),
    state,
    code_challenge: challenge,
    code_challenge_method: "S256",
  });
  if ((isGoogleGeminiJob(job) || isGoogleAntigravityJob(job)) && !query.has("access_type")) {
    query.set("access_type", "offline");
  }
  for (const [key, value] of Object.entries(objectValue(metadata.extra_auth_params) || {})) {
    query.set(key, String(value));
  }
  options.log?.(`Authorize ${job.id}: ${authUrl}?${query.toString()}`);
  const { code } = await callback;
  const response = await tokenRequest(url, {
    grant_type: "authorization_code",
    code,
    redirect_uri: redirectUri,
    client_id: id,
    client_secret: clientSecret(job),
    code_verifier: verifier,
  }, metadata);
  const bundle = bundleFromTokenResponse(job, response, currentBundle(job));
  if (isGoogleGeminiJob(job)) {
    return completeWithBundle(job, await googleGeminiBundleFromOAuth(job, response, bundle, "google_gemini_pkce"), "google_gemini_pkce");
  }
  if (isGoogleAntigravityJob(job)) {
    return completeWithBundle(job, await googleAntigravityBundleFromOAuth(job, response, bundle, "google_antigravity_pkce"), "google_antigravity_pkce");
  }
  return completeWithBundle(job, bundle, "oauth_pkce");
}

async function externalCli(job, _options) {
  const metadata = providerMetadata(job);
  const payload = objectValue(job.payload) || {};
  const command = commandFor(job, metadata, payload);
  if (command) await runCommand(command, envForCommand(job, metadata, payload), Number(metadata.command_timeout_ms || payload.command_timeout_ms || 120000));
  if (normalized(job.job_type) === "revoke") {
    return { auth_status: "revoked", result: { strategy: "external_cli" } };
  }

  const bundleCommand = payload.token_bundle_command || metadata.token_bundle_command;
  const bundleFile = stringValue(payload.token_bundle_file || metadata.token_bundle_file);
  let bundle;
  if (bundleCommand) {
    const output = await runCommand(bundleCommand, envForCommand(job, metadata, payload), Number(metadata.command_timeout_ms || payload.command_timeout_ms || 120000));
    bundle = JSON.parse(output.trim());
  } else if (bundleFile) {
    bundle = JSON.parse(await fs.readFile(bundleFile, "utf8"));
  }
  if (!bundle?.access_token) {
    throw new TerminalJobError("external_cli strategy requires token_bundle_command or token_bundle_file.");
  }
  return completeWithBundle(job, bundle, "external_cli");
}

async function windsurfCli(job) {
  const metadata = providerMetadata(job);
  const payload = objectValue(job.payload) || {};
  const files = normalizeList(payload.config_file || payload.token_file || metadata.config_file || metadata.token_file || process.env.WINDSURF_CODEIUM_CONFIG_FILE);
  const candidates = files.length ? files : windsurfCodeiumConfigFiles();
  for (const file of candidates) {
    const config = await readJSONFile(file).catch(() => null);
    const apiKey = stringValue(config?.api_key || config?.apiKey || config?.access_token || config?.token);
    if (!apiKey) continue;
    return completeWithBundle(job, {
      type: "api_key",
      access_token: apiKey,
      provider: "windsurf_codeium",
      auth_scheme: "bearer",
      subject: stringValue(config?.email || config?.user || config?.user_id),
      metadata: {
        ...windsurfCodeiumClientMetadata(metadata),
        source: "windsurf_codeium_config_file",
        config_file: file,
      },
    }, "windsurf_cli");
  }
  throw new TerminalJobError("Windsurf/Codeium config file with api_key was not found.");
}

async function kiroCli(job) {
  const jobType = normalized(job.job_type);
  if (jobType === "revoke") {
    return { auth_status: "revoked", result: { strategy: "kiro_revoke" } };
  }
  const current = currentBundle(job);
  const local = await kiroLocalCredentialsBundle(job);
  const bundle = local?.refresh_token || local?.access_token ? local : current;
  if (jobType === "refresh" || bundle.refresh_token) {
    return kiroRefresh(job, bundle);
  }
  if (bundle.access_token) {
    return completeWithBundle(job, bundle, local ? "kiro_credentials_file" : "kiro_token_bundle");
  }
  throw new TerminalJobError("Kiro credentials were not found. Configure credentials_file, sqlite_file, or a token_bundle with refresh_token.");
}

async function kiroLocalCredentialsBundle(job) {
  const metadata = providerMetadata(job);
  const payload = objectValue(job.payload) || {};
  const files = normalizeList(payload.credentials_file || payload.token_file || metadata.credentials_file || metadata.token_file || process.env.KIRO_CREDENTIALS_FILE);
  for (const file of files) {
    const config = await readJSONFile(file).catch(() => null);
    const bundle = kiroBundleFromLocalCredentials(job, config, { source: "kiro_credentials_file", credentials_file: file });
    if (bundle) return bundle;
  }
  const sqliteFiles = normalizeList(payload.sqlite_file || metadata.sqlite_file || process.env.KIRO_SQLITE_FILE);
  for (const file of sqliteFiles.length ? sqliteFiles : kiroSQLiteCredentialFiles()) {
    const bundle = await kiroBundleFromSQLite(job, file).catch(() => null);
    if (bundle) return bundle;
  }
  return null;
}

function kiroBundleFromLocalCredentials(job, data, extraMetadata = {}) {
  if (!data || typeof data !== "object") return null;
  const refreshToken = stringValue(data.refreshToken || data.refresh_token);
  const accessToken = stringValue(data.accessToken || data.access_token || data.token || data.idToken || data.id_token);
  if (!refreshToken && !accessToken) return null;
  const metadata = providerMetadata(job);
  const region = stringValue(data.idcRegion || data.idc_region || data.region || data.sso_region || metadata.region || metadata.kiro_region || process.env.KIRO_REGION || KIRO_API_REGION);
  const profileArn = stringValue(data.profileArn || data.profile_arn || data.profileARN || metadata.profile_arn);
  const clientId = stringValue(data.clientId || data.client_id || metadata.client_id);
  const clientSecret = stringValue(data.clientSecret || data.client_secret || metadata.client_secret);
  const authMethod = stringValue(data.authMethod || data.auth_method);
  const upstreamProvider = stringValue(data.provider);
  const startUrl = stringValue(data.startUrl || data.start_url);
  return {
    type: "oauth",
    access_token: accessToken || "kiro-refresh-pending",
    refresh_token: refreshToken,
    expires_at: stringValue(data.expiresAt || data.expires_at || data.expiry || data.expires),
    scopes: normalizeList(data.scopes),
    provider: "kiro",
    auth_scheme: "bearer",
    subject: profileArn,
    metadata: {
      ...kiroClientMetadata({ ...metadata, region }),
      ...extraMetadata,
      profile_arn: profileArn,
      client_id: clientId,
      client_secret: clientSecret,
      auth_type: clientId && clientSecret ? "aws_sso_oidc" : "kiro_desktop",
      sso_region: region,
      auth_method: authMethod,
      upstream_provider: upstreamProvider,
      start_url: startUrl,
    },
  };
}

async function kiroBundleFromSQLite(job, file) {
  if (!file) return null;
  const expanded = expandHome(file);
  try {
    await fs.access(expanded);
  } catch {
    return null;
  }
  const tokenRaw = await sqliteJSONValue(expanded, "auth_kv", [
    "kirocli:social:token",
    "kirocli:odic:token",
    "codewhisperer:odic:token",
  ]);
  const registrationRaw = await sqliteJSONValue(expanded, "auth_kv", [
    "kirocli:odic:device-registration",
    "codewhisperer:odic:device-registration",
  ]);
  const profileRaw = await sqliteJSONValue(expanded, "state", ["api.codewhisperer.profile"]).catch(() => null);
  const tokenData = tokenRaw ? JSON.parse(tokenRaw) : null;
  const registrationData = registrationRaw ? JSON.parse(registrationRaw) : {};
  const profileData = profileRaw ? JSON.parse(profileRaw) : {};
  if (!tokenData) return null;
  return kiroBundleFromLocalCredentials(job, {
    ...tokenData,
    client_id: registrationData.client_id,
    client_secret: registrationData.client_secret,
    profile_arn: tokenData.profile_arn || profileData.arn,
  }, { source: "kiro_sqlite", sqlite_file: expanded });
}

async function sqliteJSONValue(file, table, keys) {
  const query = `SELECT value FROM ${table} WHERE key = ? LIMIT 1`;
  for (const key of keys) {
    try {
      const result = await execFileAsync("sqlite3", ["-readonly", "-json", file, query, key], { timeout: 5000, maxBuffer: 1024 * 1024 });
      const rows = JSON.parse(result.stdout || "[]");
      if (rows?.[0]?.value) return rows[0].value;
    } catch {
      continue;
    }
  }
  return null;
}

function kiroSQLiteCredentialFiles() {
  return [
    path.join(os.homedir(), ".local", "share", "kiro-cli", "data.sqlite3"),
    path.join(os.homedir(), ".local", "share", "amazon-q", "data.sqlite3"),
  ];
}

function commandFor(job, metadata, payload) {
  const type = normalized(job.job_type);
  if (type === "refresh") return payload.refresh_command || metadata.refresh_command;
  if (type === "revoke") return payload.revoke_command || metadata.revoke_command;
  return payload.login_command || metadata.login_command;
}

async function runCommand(commandSpec, env, timeout) {
  if (!Array.isArray(commandSpec) || commandSpec.length === 0) {
    throw new TerminalJobError("Command specs must be JSON arrays, for example [\"codex\", \"login\"].");
  }
  const [file, ...args] = commandSpec.map(String);
  const result = await execFileAsync(file, args, {
    env: { ...process.env, ...env },
    timeout,
    maxBuffer: 1024 * 1024,
  });
  return result.stdout;
}

function envForCommand(job, metadata, payload) {
  return {
    OAUTH_JOB_ID: job.id,
    OAUTH_ACCOUNT_ID: job.account_id,
    OAUTH_AUTH_MODE: job.auth_mode,
    OAUTH_PROVIDER_CLIENT_ID: job.provider_client?.id || "",
    OAUTH_PROVIDER_CLIENT_SECRET: clientSecret(job),
    ...(objectValue(metadata.command_env) || {}),
    ...(objectValue(payload.command_env) || {}),
  };
}

function completeWithBundle(job, bundle, strategy) {
  const bundleScopes = normalizeList(bundle.scopes);
  const normalizedBundle = {
    type: bundle.type || "oauth",
    access_token: stringValue(bundle.access_token),
    refresh_token: stringValue(bundle.refresh_token),
    expires_at: expiresAt(bundle),
    scopes: bundleScopes.length ? bundleScopes : scopes(job),
    provider: stringValue(bundle.provider || job.provider?.name),
    auth_scheme: stringValue(bundle.auth_scheme || (normalized(bundle.type) === "api_key" ? "api_key" : "bearer")),
    subject: stringValue(bundle.subject || bundle.provider_subject),
    metadata: objectValue(bundle.metadata) || {},
  };
  if (!normalizedBundle.access_token) throw new TerminalJobError("Token bundle has no access_token.");
  return {
    auth_status: "active",
    token_bundle: normalizedBundle,
    provider_subject: normalizedBundle.subject,
    scopes: normalizedBundle.scopes,
    result: { strategy },
  };
}

function bundleFromTokenResponse(job, response, current = {}) {
  if (!response.access_token) throw new TerminalJobError(response.error_description || response.error || "Token endpoint did not return access_token.");
  const scopeText = stringValue(response.scope);
  return {
    type: response.token_type ? normalized(response.token_type) : "oauth",
    access_token: response.access_token,
    refresh_token: response.refresh_token || current.refresh_token || "",
    expires_at: response.expires_at || (response.expires_in ? new Date(Date.now() + Number(response.expires_in) * 1000).toISOString() : current.expires_at || ""),
    scopes: scopeText ? scopeText.split(/\s+/).filter(Boolean) : current.scopes || scopes(job),
    provider: current.provider || job.provider?.name || "",
    auth_scheme: current.auth_scheme || "bearer",
    subject: response.id_token || current.subject || "",
    metadata: { token_type: response.token_type || "" },
  };
}

async function githubCopilotRefresh(job, current) {
  const githubToken = stringValue(current.refresh_token || current.metadata?.github_token);
  if (!githubToken) {
    throw new TerminalJobError("GitHub Copilot refresh requires the original GitHub OAuth token.");
  }
  return completeWithBundle(job, await githubCopilotBundleFromGitHubToken(job, githubToken, current), "github_copilot_refresh");
}

async function githubCopilotBundleFromGitHubToken(job, githubToken, current = {}) {
  const metadata = providerMetadata(job);
  const copilot = await githubCopilotTokenRequest(job, githubToken);
  const user = await githubUserRequest(job, githubToken).catch(() => ({}));
  return {
    type: "oauth",
    access_token: copilot.token,
    refresh_token: githubToken,
    expires_at: expiresAtFromValue(copilot.expires_at),
    scopes: current.scopes || scopes(job),
    provider: "github_copilot",
    auth_scheme: "bearer",
    subject: stringValue(user.login || current.subject),
    metadata: {
      ...githubCopilotClientMetadata(metadata),
      source: "github_device",
      github_token_type: stringValue(current.metadata?.token_type || current.token_type || "bearer"),
      github_scope: normalizeList(current.scopes || current.scope || scopes(job)).join(" "),
      github_login: stringValue(user.login),
      copilot_base_url: githubCopilotBaseURL(job),
      copilot_api_base_url: githubCopilotAPIBaseURL(job),
      copilot_refresh_in: Number(copilot.refresh_in || 0),
      copilot_expires_at_epoch: Number(copilot.expires_at || 0),
    },
  };
}

async function githubCopilotTokenRequest(job, githubToken) {
  const response = await fetch(`${githubCopilotAPIBaseURL(job)}/copilot_internal/v2/token`, {
    headers: githubAuthHeaders(job, githubToken),
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok || !data.token) {
    throw new TerminalJobError(data.message || data.error || `GitHub Copilot token HTTP ${response.status}`);
  }
  return data;
}

async function githubUserRequest(job, githubToken) {
  const response = await fetch(`${githubCopilotAPIBaseURL(job)}/user`, {
    headers: githubAuthHeaders(job, githubToken),
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(data.message || data.error || `GitHub user HTTP ${response.status}`);
  }
  return data;
}

function githubAuthHeaders(job, githubToken) {
  const metadata = providerMetadata(job);
  return {
    accept: "application/json",
    "content-type": "application/json",
    authorization: `token ${githubToken}`,
    "editor-version": `vscode/${githubCopilotVSCodeVersion(metadata)}`,
    "editor-plugin-version": `copilot-chat/${githubCopilotClientVersion(metadata)}`,
    "user-agent": githubCopilotUserAgent(metadata),
    "x-github-api-version": githubCopilotAPIVersion(metadata),
    "x-vscode-user-agent-library-version": githubCopilotFetchLibraryVersion(metadata),
  };
}

function isGitHubCopilotJob(job) {
  const metadata = providerMetadata(job);
  return normalized(job.auth_mode) === "github_copilot" ||
    normalized(job.payload?.strategy) === "github_copilot" ||
    normalized(metadata.wrapper_strategy) === "github_copilot" ||
    normalized(metadata.strategy) === "github_copilot" ||
    normalized(metadata.token_target) === "github_copilot" ||
    normalized(job.provider?.provider_type) === "github_copilot" ||
    normalized(job.provider?.name).includes("copilot");
}

function githubCopilotClientMetadata(metadata = {}) {
  return {
    client_version: githubCopilotClientVersion(metadata),
    vscode_version: githubCopilotVSCodeVersion(metadata),
    user_agent: githubCopilotUserAgent(metadata),
    api_version: githubCopilotAPIVersion(metadata),
    account_type: githubCopilotAccountType(metadata),
    copilot_integration_id: stringValue(metadata.copilot_integration_id || metadata.integration_id || "vscode-chat"),
    openai_intent: stringValue(metadata.openai_intent || metadata.intent || "conversation-panel"),
    interaction_type: stringValue(metadata.interaction_type || metadata.x_interaction_type || metadata.openai_intent || metadata.intent || "conversation-panel"),
    vscode_user_agent_library_version: githubCopilotFetchLibraryVersion(metadata),
  };
}

function githubCopilotClientVersion(metadata = {}) {
  return stringValue(metadata.client_version || metadata.copilot_version || process.env.GITHUB_COPILOT_CLIENT_VERSION || GITHUB_COPILOT_CLIENT_VERSION);
}

function githubCopilotVSCodeVersion(metadata = {}) {
  return stringValue(metadata.vscode_version || metadata.editor_version || process.env.GITHUB_COPILOT_VSCODE_VERSION || GITHUB_COPILOT_VSCODE_VERSION);
}

function githubCopilotUserAgent(metadata = {}) {
  return stringValue(metadata.user_agent || process.env.GITHUB_COPILOT_USER_AGENT || `GitHubCopilotChat/${githubCopilotClientVersion(metadata)}`);
}

function githubCopilotAPIVersion(metadata = {}) {
  return stringValue(metadata.api_version || metadata.github_api_version || process.env.GITHUB_COPILOT_API_VERSION || "2025-05-01");
}

function githubCopilotFetchLibraryVersion(metadata = {}) {
  return stringValue(metadata.vscode_user_agent_library_version || metadata.fetch_library_version || "electron-fetch");
}

function githubCopilotAccountType(metadata = {}) {
  const value = normalized(metadata.account_type || metadata.copilot_account_type || process.env.GITHUB_COPILOT_ACCOUNT_TYPE || "individual");
  if (["business", "enterprise"].includes(value)) return value;
  return "individual";
}

function githubCopilotAPIBaseURL(job) {
  return stringValue(job.payload?.github_api_base_url || providerMetadata(job).github_api_base_url || process.env.GITHUB_API_BASE_URL || GITHUB_COPILOT_API_BASE_URL).replace(/\/+$/, "");
}

function githubCopilotBaseURL(job) {
  const explicit = stringValue(job.payload?.copilot_base_url || providerMetadata(job).copilot_base_url || process.env.GITHUB_COPILOT_BASE_URL);
  if (explicit) return explicit.replace(/\/+$/, "");
  switch (githubCopilotAccountType(providerMetadata(job))) {
    case "business":
      return "https://api.business.githubcopilot.com";
    case "enterprise":
      return "https://api.enterprise.githubcopilot.com";
    default:
      return GITHUB_COPILOT_BASE_URL;
  }
}

async function googleGeminiBundleFromOAuth(job, response, current = {}, source = "google_gemini_oauth") {
  const metadata = providerMetadata(job);
  const user = await googleGeminiUserInfoRequest(job, current.access_token).catch(() => ({}));
  return {
    type: "oauth",
    access_token: current.access_token || response.access_token,
    refresh_token: current.refresh_token || response.refresh_token || "",
    expires_at: current.expires_at || expiresAt(response),
    scopes: normalizeList(current.scopes).length ? normalizeList(current.scopes) : scopes(job),
    provider: "google_gemini",
    auth_scheme: "bearer",
    subject: stringValue(user.email || user.id || current.subject),
    metadata: {
      ...geminiClientMetadata(metadata),
      source,
      token_type: stringValue(response.token_type || current.metadata?.token_type || "Bearer"),
      google_user_id: stringValue(user.id),
      google_email: stringValue(user.email),
      code_assist_base_url: geminiCodeAssistBaseURL(job),
      ...(user.verified_email === undefined ? {} : { google_verified_email: Boolean(user.verified_email) }),
    },
  };
}

async function googleGeminiUserInfoRequest(job, accessToken) {
  if (!accessToken) return {};
  const response = await fetch(geminiUserInfoURL(job), {
    headers: {
      accept: "application/json",
      authorization: `Bearer ${accessToken}`,
    },
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(data.error_description || data.error || `Google userinfo HTTP ${response.status}`);
  }
  return data;
}

function isGoogleGeminiJob(job) {
  const metadata = providerMetadata(job);
  const providerType = normalized(job.provider?.provider_type);
  const providerName = normalized(job.provider?.name);
  return normalized(job.payload?.strategy) === "google_gemini" ||
    normalized(job.payload?.strategy) === "gemini_cli" ||
    normalized(metadata.wrapper_strategy) === "google_gemini" ||
    normalized(metadata.wrapper_strategy) === "gemini_cli" ||
    normalized(metadata.strategy) === "google_gemini" ||
    normalized(metadata.strategy) === "gemini_cli" ||
    normalized(metadata.token_target) === "google_gemini" ||
    normalized(metadata.token_target) === "gemini_cli" ||
    ["gemini", "gemini_openai_compatible", "gemini_compatible", "gemini_cli", "google_gemini", "google_gemini_cli"].includes(providerType) ||
    providerName.includes("gemini");
}

function geminiClientMetadata(metadata = {}) {
  const model = stringValue(metadata.model || metadata.default_model || "gemini-2.5-pro");
  return {
    client_version: geminiClientVersion(metadata),
    user_agent: stringValue(metadata.user_agent || process.env.GEMINI_CLI_USER_AGENT || `GeminiCLI/${geminiClientVersion(metadata)}/${model} (${process.platform}; ${process.arch}; terminal)`),
    api_client: geminiAPIClientHeader(metadata),
    x_goog_api_client: geminiAPIClientHeader(metadata),
    genai_sdk_client: geminiAPIClientHeader(metadata),
    code_assist_endpoint: stringValue(metadata.code_assist_endpoint || process.env.CODE_ASSIST_ENDPOINT || GEMINI_CODE_ASSIST_ENDPOINT),
    code_assist_api_version: stringValue(metadata.code_assist_api_version || process.env.CODE_ASSIST_API_VERSION || GEMINI_CODE_ASSIST_API_VERSION),
    oauth_client_id: geminiClientId(metadata),
    scopes: geminiScopes(metadata),
    surface: stringValue(metadata.surface || "terminal"),
  };
}

function geminiClientVersion(metadata = {}) {
  return stringValue(metadata.client_version || metadata.gemini_cli_version || process.env.GEMINI_CLI_VERSION || GEMINI_CLI_VERSION);
}

function geminiAPIClientHeader(metadata = {}) {
  return stringValue(metadata.api_client || metadata.x_goog_api_client || metadata.genai_sdk_client || process.env.GEMINI_GENAI_SDK_CLIENT || GEMINI_GENAI_SDK_CLIENT);
}

function geminiClientId(metadata = {}) {
  return stringValue(metadata.client_id || process.env.GEMINI_OAUTH_CLIENT_ID || GEMINI_OAUTH_CLIENT_ID);
}

function geminiClientSecret(metadata = {}) {
  return stringValue(metadata.client_secret || process.env.GEMINI_OAUTH_CLIENT_SECRET || GEMINI_OAUTH_CLIENT_SECRET);
}

function geminiScopes(metadata = {}) {
  const configured = normalizeList(metadata.scopes || process.env.GEMINI_OAUTH_SCOPES);
  return configured.length ? configured : GEMINI_OAUTH_SCOPES;
}

function geminiUserInfoURL(job) {
  return stringValue(job.payload?.userinfo_url || providerMetadata(job).userinfo_url || process.env.GEMINI_USERINFO_URL || GEMINI_USERINFO_URL);
}

function geminiCodeAssistBaseURL(job) {
  const metadata = providerMetadata(job);
  const endpoint = stringValue(job.payload?.code_assist_endpoint || metadata.code_assist_endpoint || process.env.CODE_ASSIST_ENDPOINT || GEMINI_CODE_ASSIST_ENDPOINT).replace(/\/+$/, "");
  const version = stringValue(job.payload?.code_assist_api_version || metadata.code_assist_api_version || process.env.CODE_ASSIST_API_VERSION || GEMINI_CODE_ASSIST_API_VERSION).replace(/^\/+|\/+$/g, "");
  return `${endpoint}/${version}`;
}

async function googleAntigravityBundleFromOAuth(job, response, current = {}, source = "google_antigravity_oauth") {
  const metadata = providerMetadata(job);
  const accessToken = current.access_token || response.access_token;
  const user = await googleAntigravityUserInfoRequest(job, accessToken).catch(() => ({}));
  const project = await googleAntigravityProjectRequest(job, accessToken).catch(() => "");
  return {
    type: "oauth",
    access_token: accessToken,
    refresh_token: current.refresh_token || response.refresh_token || "",
    expires_at: current.expires_at || expiresAt(response),
    scopes: normalizeList(current.scopes).length ? normalizeList(current.scopes) : scopes(job),
    provider: "google_antigravity",
    auth_scheme: "bearer",
    subject: stringValue(user.email || user.id || current.subject),
    metadata: {
      ...antigravityClientMetadata(metadata),
      source,
      token_type: stringValue(response.token_type || current.metadata?.token_type || "Bearer"),
      google_user_id: stringValue(user.id),
      google_email: stringValue(user.email),
      cloudaicompanion_project: project,
      code_assist_base_url: antigravityCodeAssistBaseURL(job),
      code_assist_fallback_base_url: antigravityFallbackCodeAssistBaseURL(job),
      ...(user.verified_email === undefined ? {} : { google_verified_email: Boolean(user.verified_email) }),
    },
  };
}

async function googleAntigravityUserInfoRequest(job, accessToken) {
  if (!accessToken) return {};
  const response = await fetch(antigravityUserInfoURL(job), {
    headers: {
      accept: "application/json",
      authorization: `Bearer ${accessToken}`,
    },
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(data.error_description || data.error || `Google userinfo HTTP ${response.status}`);
  }
  return data;
}

async function googleAntigravityProjectRequest(job, accessToken) {
  if (!accessToken) return "";
  const response = await fetch(`${antigravityFallbackCodeAssistBaseURL(job)}:loadCodeAssist`, {
    method: "POST",
    headers: {
      accept: "application/json",
      authorization: `Bearer ${accessToken}`,
      "content-type": "application/json",
      "user-agent": antigravityUserAgent(providerMetadata(job)),
    },
    body: JSON.stringify({ metadata: { ideType: "ANTIGRAVITY" } }),
  });
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok) {
    throw new Error(data.error_description || data.error || data.message || `Antigravity project HTTP ${response.status}`);
  }
  return stringValue(data.cloudaicompanionProject || data.project || data.projectId);
}

function isGoogleAntigravityJob(job) {
  const metadata = providerMetadata(job);
  const providerType = normalized(job.provider?.provider_type);
  const providerName = normalized(job.provider?.name);
  return normalized(job.payload?.strategy) === "google_antigravity" ||
    normalized(job.payload?.strategy) === "antigravity" ||
    normalized(metadata.wrapper_strategy) === "google_antigravity" ||
    normalized(metadata.wrapper_strategy) === "antigravity" ||
    normalized(metadata.strategy) === "google_antigravity" ||
    normalized(metadata.strategy) === "antigravity" ||
    normalized(metadata.token_target) === "google_antigravity" ||
    normalized(metadata.token_target) === "antigravity" ||
    ["antigravity", "google_antigravity", "google_antigravity_cli"].includes(providerType) ||
    providerName.includes("antigravity");
}

function antigravityClientMetadata(metadata = {}) {
  return {
    client_version: antigravityClientVersion(metadata),
    user_agent: antigravityUserAgent(metadata),
    oauth_client_id: antigravityClientId(metadata),
    scopes: antigravityScopes(metadata),
    code_assist_endpoint: stringValue(metadata.code_assist_endpoint || process.env.ANTIGRAVITY_CODE_ASSIST_ENDPOINT || ANTIGRAVITY_CODE_ASSIST_ENDPOINT),
    code_assist_fallback_endpoint: stringValue(metadata.code_assist_fallback_endpoint || process.env.ANTIGRAVITY_CODE_ASSIST_FALLBACK_ENDPOINT || ANTIGRAVITY_CODE_ASSIST_FALLBACK_ENDPOINT),
    code_assist_api_version: stringValue(metadata.code_assist_api_version || process.env.ANTIGRAVITY_CODE_ASSIST_API_VERSION || GEMINI_CODE_ASSIST_API_VERSION),
    surface: stringValue(metadata.surface || "desktop"),
    ide_type: "ANTIGRAVITY",
  };
}

function antigravityClientVersion(metadata = {}) {
  return stringValue(metadata.client_version || metadata.antigravity_version || process.env.ANTIGRAVITY_CLIENT_VERSION || ANTIGRAVITY_CLIENT_VERSION);
}

function antigravityUserAgent(metadata = {}) {
  const platform = process.platform === "darwin" ? "macos" : process.platform;
  return stringValue(metadata.user_agent || process.env.ANTIGRAVITY_USER_AGENT || `antigravity/${antigravityClientVersion(metadata)} ${platform}/${process.arch}`);
}

function antigravityClientId(metadata = {}) {
  return stringValue(metadata.client_id || process.env.ANTIGRAVITY_OAUTH_CLIENT_ID || ANTIGRAVITY_OAUTH_CLIENT_ID);
}

function antigravityClientSecret(metadata = {}) {
  return stringValue(metadata.client_secret || process.env.ANTIGRAVITY_OAUTH_CLIENT_SECRET || ANTIGRAVITY_OAUTH_CLIENT_SECRET);
}

function antigravityScopes(metadata = {}) {
  const configured = normalizeList(metadata.scopes || process.env.ANTIGRAVITY_OAUTH_SCOPES);
  return configured.length ? configured : ANTIGRAVITY_OAUTH_SCOPES;
}

function antigravityUserInfoURL(job) {
  return stringValue(job.payload?.userinfo_url || providerMetadata(job).userinfo_url || process.env.ANTIGRAVITY_USERINFO_URL || GEMINI_USERINFO_URL);
}

function antigravityCodeAssistBaseURL(job) {
  const metadata = providerMetadata(job);
  const endpoint = stringValue(job.payload?.code_assist_endpoint || metadata.code_assist_endpoint || process.env.ANTIGRAVITY_CODE_ASSIST_ENDPOINT || ANTIGRAVITY_CODE_ASSIST_ENDPOINT).replace(/\/+$/, "");
  const version = stringValue(job.payload?.code_assist_api_version || metadata.code_assist_api_version || process.env.ANTIGRAVITY_CODE_ASSIST_API_VERSION || GEMINI_CODE_ASSIST_API_VERSION).replace(/^\/+|\/+$/g, "");
  return `${endpoint}/${version}`;
}

function antigravityFallbackCodeAssistBaseURL(job) {
  const metadata = providerMetadata(job);
  const endpoint = stringValue(job.payload?.code_assist_fallback_endpoint || metadata.code_assist_fallback_endpoint || process.env.ANTIGRAVITY_CODE_ASSIST_FALLBACK_ENDPOINT || ANTIGRAVITY_CODE_ASSIST_FALLBACK_ENDPOINT).replace(/\/+$/, "");
  const version = stringValue(job.payload?.code_assist_api_version || metadata.code_assist_api_version || process.env.ANTIGRAVITY_CODE_ASSIST_API_VERSION || GEMINI_CODE_ASSIST_API_VERSION).replace(/^\/+|\/+$/g, "");
  return `${endpoint}/${version}`;
}

async function kiroRefresh(job, current) {
  const refreshToken = stringValue(current.refresh_token || current.metadata?.refresh_token);
  if (!refreshToken) {
    throw new TerminalJobError("Kiro token bundle has no refresh_token.");
  }
  const metadata = providerMetadata(job);
  const currentMetadata = objectValue(current.metadata) || {};
  const clientId = stringValue(job.payload?.client_id || metadata.client_id || current.client_id || currentMetadata.client_id);
  const clientSecret = stringValue(job.payload?.client_secret || metadata.client_secret || current.client_secret || currentMetadata.client_secret);
  const region = stringValue(job.payload?.region || metadata.region || currentMetadata.region || metadata.kiro_region || currentMetadata.kiro_region || process.env.KIRO_REGION || KIRO_API_REGION);
  const ssoRegion = stringValue(job.payload?.sso_region || metadata.sso_region || currentMetadata.sso_region || region);
  const fingerprint = stringValue(job.payload?.fingerprint || metadata.fingerprint || currentMetadata.fingerprint || crypto.createHash("sha256").update(os.hostname()).digest("hex"));
  const clientVersion = stringValue(metadata.client_version || currentMetadata.client_version || process.env.KIRO_CLIENT_VERSION || KIRO_CLIENT_VERSION);
  const useSsoOidc = Boolean(clientId && clientSecret);
  const url = useSsoOidc ?
    stringValue(job.payload?.token_url || metadata.token_url || currentMetadata.token_url || process.env.KIRO_TOKEN_URL || `https://oidc.${ssoRegion}.amazonaws.com/token`) :
    stringValue(job.payload?.token_url || metadata.token_url || currentMetadata.token_url || process.env.KIRO_TOKEN_URL || `https://prod.${region}.auth.desktop.kiro.dev/refreshToken`);
  const response = await jsonPost(url, useSsoOidc ? {
    grantType: "refresh_token",
    clientId,
    clientSecret,
    refreshToken,
  } : { refreshToken }, {
    headers: { "user-agent": `KiroIDE-${clientVersion}-${fingerprint}` },
  });
  const accessToken = stringValue(response.accessToken || response.access_token);
  if (!accessToken) throw new TerminalJobError("Kiro refresh endpoint did not return accessToken.");
  const source = useSsoOidc ? "kiro_aws_sso_oidc_refresh" : "kiro_desktop_refresh";
  const priorSource = stringValue(currentMetadata.source);
  return completeWithBundle(job, {
    type: "oauth",
    access_token: accessToken,
    refresh_token: stringValue(response.refreshToken || response.refresh_token || refreshToken),
    expires_at: response.expiresAt || response.expires_at || (response.expiresIn || response.expires_in ? new Date(Date.now() + Number(response.expiresIn || response.expires_in) * 1000).toISOString() : current.expires_at || ""),
    provider: "kiro",
    auth_scheme: "bearer",
    subject: stringValue(response.userId || response.user_id || current.subject),
    metadata: {
      ...kiroClientMetadata({ ...metadata, ...currentMetadata, region }),
      ...currentMetadata,
      source,
      ...(priorSource ? { credential_source: priorSource } : {}),
      auth_type: useSsoOidc ? "aws_sso_oidc" : "kiro_desktop",
      region,
      sso_region: ssoRegion,
      client_id: clientId,
      client_secret: clientSecret,
      fingerprint,
      profile_arn: stringValue(response.profileArn || response.profile_arn || currentMetadata.profile_arn),
    },
  }, "kiro_refresh");
}

function isKiroJob(job) {
  const metadata = providerMetadata(job);
  const providerType = normalized(job.provider?.provider_type);
  const providerName = normalized(job.provider?.name);
  return normalized(job.payload?.strategy) === "kiro" ||
    normalized(metadata.wrapper_strategy) === "kiro" ||
    normalized(metadata.strategy) === "kiro" ||
    normalized(metadata.token_target) === "kiro" ||
    ["kiro", "aws_kiro", "amazon_q_kiro", "kiro_compatible"].includes(providerType) ||
    providerName.includes("kiro");
}

function kiroClientMetadata(metadata = {}) {
  const region = stringValue(metadata.region || metadata.kiro_region || process.env.KIRO_REGION || KIRO_API_REGION);
  return {
    client_version: stringValue(metadata.client_version || metadata.kiro_version || process.env.KIRO_CLIENT_VERSION || KIRO_CLIENT_VERSION),
    region,
    api_host: stringValue(metadata.api_host || process.env.KIRO_API_HOST || `https://q.${region}.amazonaws.com`),
    q_host: stringValue(metadata.q_host || process.env.KIRO_Q_HOST || `https://q.${region}.amazonaws.com`),
    agent_mode: stringValue(metadata.agent_mode || "vibe"),
    codewhisperer_optout: stringValue(metadata.codewhisperer_optout || metadata.optout || "true"),
  };
}

function windsurfCodeiumClientMetadata(metadata = {}) {
  const apiServerURL = windsurfCodeiumAPIServerURL(metadata);
  const extensionName = stringValue(metadata.extension_name || process.env.WINDSURF_EXTENSION_NAME || "windsurf");
  const extensionVersion = stringValue(metadata.extension_version || process.env.WINDSURF_EXTENSION_VERSION || WINDSURF_EXTENSION_VERSION);
  const ideName = stringValue(metadata.ide_name || process.env.WINDSURF_IDE_NAME || "windsurf");
  const ideVersion = stringValue(metadata.ide_version || metadata.client_version || process.env.WINDSURF_IDE_VERSION || WINDSURF_IDE_VERSION);
  const hasEnterpriseExtension = booleanString(metadata.has_enterprise_extension || process.env.WINDSURF_HAS_ENTERPRISE_EXTENSION || "false");
  return {
    ide_name: ideName,
    ide_version: ideVersion,
    extension_name: extensionName,
    extension_version: extensionVersion,
    request_metadata: {
      ide_name: ideName,
      ide_version: ideVersion,
      extension_name: extensionName,
      extension_version: extensionVersion,
      request_id: stringValue(metadata.request_id || process.env.WINDSURF_REQUEST_ID || ""),
    },
    app_name: stringValue(metadata.app_name || process.env.WINDSURF_APP_NAME || extensionName),
    language_server_version: stringValue(metadata.language_server_version || process.env.WINDSURF_LANGUAGE_SERVER_VERSION || WINDSURF_LANGUAGE_SERVER_VERSION),
    api_server_url: apiServerURL,
    api_host: windsurfCodeiumAPIHost(metadata),
    api_port: windsurfCodeiumAPIPort(metadata),
    api_path: windsurfCodeiumAPIPath(metadata),
    portal_url: stringValue(metadata.portal_url || process.env.WINDSURF_PORTAL_URL || "https://codeium.com"),
    has_enterprise_extension: hasEnterpriseExtension,
    chat_client_query: {
      ide_name: ideName,
      ide_version: ideVersion,
      app_name: stringValue(metadata.app_name || process.env.WINDSURF_APP_NAME || extensionName),
      extension_name: extensionName,
      extension_version: extensionVersion,
      ide_telemetry_enabled: stringValue(metadata.ide_telemetry_enabled || process.env.WINDSURF_IDE_TELEMETRY_ENABLED || "true"),
      has_index_service: stringValue(metadata.has_index_service || process.env.WINDSURF_HAS_INDEX_SERVICE || "true"),
      locale: stringValue(metadata.locale || process.env.WINDSURF_LOCALE || "en_US"),
      has_enterprise_extension: hasEnterpriseExtension,
    },
  };
}

function windsurfCodeiumAPIHost(metadata = {}) {
  return stringValue(metadata.api_host || process.env.WINDSURF_API_HOST || "server.codeium.com");
}

function windsurfCodeiumAPIPort(metadata = {}) {
  return stringValue(metadata.api_port || process.env.WINDSURF_API_PORT || "443");
}

function windsurfCodeiumAPIPath(metadata = {}) {
  return stringValue(metadata.api_path || process.env.WINDSURF_API_PATH || "/");
}

function windsurfCodeiumAPIServerURL(metadata = {}) {
  if (metadata.api_server_url || process.env.WINDSURF_API_SERVER_URL) {
    return stringValue(metadata.api_server_url || process.env.WINDSURF_API_SERVER_URL);
  }
  const host = windsurfCodeiumAPIHost(metadata);
  const port = windsurfCodeiumAPIPort(metadata);
  const rawPath = windsurfCodeiumAPIPath(metadata);
  const normalizedPath = rawPath && rawPath !== "/" ? `/${rawPath.replace(/^\/+/, "")}` : "/";
  return `https://${host}:${port}${normalizedPath}`;
}

function booleanString(value) {
  if (typeof value === "boolean") {
    return value ? "true" : "false";
  }
  const text = stringValue(value).trim().toLowerCase();
  if (["1", "true", "yes", "on"].includes(text)) return "true";
  if (["0", "false", "no", "off"].includes(text)) return "false";
  return text || "false";
}

function windsurfCodeiumConfigFiles() {
  return [
    path.join(os.homedir(), ".cache", "codeium", "config.json"),
    path.join(os.homedir(), ".cache", "windsurf", "config.json"),
    path.join(os.homedir(), ".config", "Codeium", "config.json"),
    path.join(os.homedir(), ".config", "codeium", "config.json"),
    path.join(os.homedir(), ".config", "Windsurf", "User", "globalStorage", "codeium.codeium", "config.json"),
    path.join(os.homedir(), ".windsurf", "codeium", "config.json"),
  ];
}

async function tokenRequest(url, fields, metadata) {
  return formPost(url, fields, metadata);
}

async function formPost(url, fields, metadata, options = {}) {
  const body = new URLSearchParams();
  for (const [key, value] of Object.entries(fields)) {
    if (value !== undefined && value !== null && String(value) !== "") body.set(key, String(value));
  }
  const headers = { accept: "application/json", "content-type": "application/x-www-form-urlencoded" };
  if (metadata.token_request_auth === "basic" && fields.client_id && fields.client_secret) {
    headers.authorization = `Basic ${Buffer.from(`${fields.client_id}:${fields.client_secret}`).toString("base64")}`;
    body.delete("client_secret");
  }
  const response = await fetch(url, { method: "POST", headers, body });
  const text = await response.text();
  const data = text ? JSON.parse(text) : {};
  if (!response.ok && !(options.allowOAuthError && data.error)) {
    throw new Error(data.error_description || data.error || `OAuth HTTP ${response.status}`);
  }
  return data;
}

async function jsonPost(url, fields, options = {}) {
  const response = await fetch(url, {
    method: "POST",
    headers: { accept: "application/json", "content-type": "application/json", ...(objectValue(options.headers) || {}) },
    body: JSON.stringify(fields || {}),
  });
  const text = await response.text();
  let data = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch (error) {
    if (!(options.allowStatuses || []).includes(response.status)) throw error;
  }
  if (!response.ok && !(options.allowStatuses || []).includes(response.status)) {
    throw new Error(data.error_description || data.error || data.message || `OAuth HTTP ${response.status}`);
  }
  return data;
}

function waitForCallback(redirectUri, state, timeoutSeconds) {
  const parsed = new URL(redirectUri);
  if (!["127.0.0.1", "localhost"].includes(parsed.hostname)) {
    throw new TerminalJobError("PKCE redirect_uri must use localhost or 127.0.0.1.");
  }
  return new Promise((resolve, reject) => {
    const server = http.createServer((req, res) => {
      const url = new URL(req.url, redirectUri);
      if (url.pathname !== parsed.pathname) {
        res.writeHead(404).end();
        return;
      }
      if (url.searchParams.get("state") !== state) {
        res.writeHead(400).end("invalid state");
        return;
      }
      const code = url.searchParams.get("code");
      if (!code) {
        res.writeHead(400).end("missing code");
        return;
      }
      res.writeHead(200, { "content-type": "text/plain" }).end("OAuth complete. You can close this tab.");
      server.close();
      resolve({ code });
    });
    server.on("error", reject);
    server.listen(Number(parsed.port), parsed.hostname);
    setTimeout(() => {
      server.close();
      reject(new TerminalJobError("PKCE callback timed out."));
    }, timeoutSeconds * 1000).unref();
  });
}

function deviceDefaults(authMode) {
  if (normalized(authMode) === "github_device") {
    return {
      device_authorization_url: "https://github.com/login/device/code",
      token_url: "https://github.com/login/oauth/access_token",
    };
  }
  return {};
}

function codexAuthFile(metadata, payload) {
  const configured = stringValue(payload.auth_file || metadata.auth_file || process.env.CODEX_AUTH_FILE);
  if (configured) return configured;
  const codexHome = stringValue(process.env.CODEX_HOME) || path.join(os.homedir(), ".codex");
  return path.join(codexHome, "auth.json");
}

async function readCodexAuth(authFile) {
  try {
    return JSON.parse(await fs.readFile(authFile, "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

async function resolveCodexInstallationId(codexHome) {
  const installationPath = path.join(codexHome, "installation_id");
  await fs.mkdir(codexHome, { recursive: true });
  try {
    const existing = (await fs.readFile(installationPath, "utf8")).trim();
    if (isUUID(existing)) return existing.toLowerCase();
  } catch (error) {
    if (error?.code !== "ENOENT") throw error;
  }
  const generated = crypto.randomUUID();
  await fs.writeFile(installationPath, generated, { mode: 0o644 });
  return generated;
}

function isUUID(value) {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(String(value || ""));
}

function codexIssuer(job) {
  const metadata = providerMetadata(job);
  return stringValue(job.payload?.issuer || metadata.issuer || metadata.auth_issuer || CODEX_ISSUER).replace(/\/+$/, "");
}

function codexClientId(job) {
  return stringValue(job.payload?.client_id || providerMetadata(job).client_id || CODEX_CLIENT_ID);
}

function codexTokenUrl(job) {
  return stringValue(job.payload?.token_url || providerMetadata(job).token_url || `${codexIssuer(job)}/oauth/token`);
}

function codexRevokeUrl(job) {
  return stringValue(job.payload?.revoke_url || providerMetadata(job).revoke_url || `${codexIssuer(job)}/oauth/revoke`);
}

function codexBundleFromTokens(tokens, metadata = {}) {
  const accessClaims = decodeJwt(tokens.access_token);
  const idClaims = decodeJwt(tokens.id_token);
  const authClaims = objectValue(accessClaims?.["https://api.openai.com/auth"]) || {};
  const idAuthClaims = objectValue(idClaims?.["https://api.openai.com/auth"]) || {};
  const scopes = normalizeList(accessClaims?.scp);
  const expiresAt = Number(accessClaims?.exp) > 0 ? new Date(Number(accessClaims.exp) * 1000).toISOString() : "";
  const accountID = stringValue(tokens.account_id || authClaims.chatgpt_account_id || idAuthClaims.chatgpt_account_id || accessClaims?.sub || idClaims?.sub);
  return {
    type: "oauth",
    access_token: tokens.access_token,
    refresh_token: tokens.refresh_token || "",
    expires_at: expiresAt,
    scopes,
    provider: "openai_codex",
    auth_scheme: "bearer",
    subject: accountID,
    metadata: {
      ...codexClientMetadata(metadata),
      ...metadata,
      account_id: accountID,
      chatgpt_account_id: accountID,
      chatgpt_user_id: stringValue(authClaims.chatgpt_user_id || idAuthClaims.chatgpt_user_id || accessClaims?.sub || idClaims?.sub),
      chatgpt_plan_type: stringValue(authClaims.chatgpt_plan_type || idAuthClaims.chatgpt_plan_type),
      chatgpt_account_is_fedramp: Boolean(authClaims.chatgpt_account_is_fedramp || idAuthClaims.chatgpt_account_is_fedramp),
      id_token: tokens.id_token || "",
    },
  };
}

function codexClientMetadata(metadata = {}) {
  return {
    originator: stringValue(metadata.originator || process.env.CODEX_ORIGINATOR || "codex_exec"),
    client_version: stringValue(metadata.client_version || process.env.CODEX_CLI_VERSION || CODEX_CLI_VERSION),
    user_agent: stringValue(metadata.user_agent || process.env.CODEX_USER_AGENT),
  };
}

async function readClaudeCredentials(job) {
  const envToken = stringValue(process.env.CLAUDE_CODE_OAUTH_TOKEN);
  if (envToken) {
    const envScopes = normalizeList(process.env.CLAUDE_CODE_OAUTH_SCOPES);
    return {
      type: "oauth",
      access_token: envToken,
      refresh_token: stringValue(process.env.CLAUDE_CODE_OAUTH_REFRESH_TOKEN),
      expires_at: expiresAtFromValue(process.env.CLAUDE_CODE_OAUTH_EXPIRES_AT),
      scopes: envScopes.length ? envScopes : CLAUDE_OAUTH_SCOPES,
      provider: "anthropic_claude",
      auth_scheme: "bearer",
      metadata: claudeClientMetadata({ source: "CLAUDE_CODE_OAUTH_TOKEN", oauth_beta: CLAUDE_OAUTH_BETA }),
    };
  }

  const authToken = stringValue(process.env.ANTHROPIC_AUTH_TOKEN);
  if (authToken) {
    const authTokenScopes = normalizeList(process.env.ANTHROPIC_AUTH_TOKEN_SCOPES);
    return {
      type: "oauth",
      access_token: authToken,
      refresh_token: "",
      expires_at: "",
      scopes: authTokenScopes,
      provider: "anthropic_claude",
      auth_scheme: "bearer",
      metadata: claudeClientMetadata({ source: "ANTHROPIC_AUTH_TOKEN", token_source: "ANTHROPIC_AUTH_TOKEN" }),
    };
  }

  for (const file of claudeCredentialFiles(job)) {
    const data = await readJSONFile(file);
    const oauth = objectValue(data?.claudeAiOauth);
    if (!oauth?.accessToken) continue;
    const fileScopes = normalizeList(oauth.scopes);
    return {
      type: "oauth",
      access_token: oauth.accessToken,
      refresh_token: stringValue(oauth.refreshToken),
      expires_at: expiresAtFromValue(oauth.expiresAt),
      scopes: fileScopes.length ? fileScopes : CLAUDE_OAUTH_SCOPES,
      provider: "anthropic_claude",
      auth_scheme: "bearer",
      subject: stringValue(oauth.tokenAccount?.uuid || oauth.account?.uuid || oauth.account_id),
      metadata: claudeClientMetadata({
        source: "claude_credentials_file",
        credentials_file: file,
        account_uuid: stringValue(oauth.tokenAccount?.uuid || oauth.account?.uuid || oauth.account_id),
        subscription_type: stringValue(oauth.subscriptionType),
        rate_limit_tier: stringValue(oauth.rateLimitTier),
        oauth_beta: CLAUDE_OAUTH_BETA,
      }),
    };
  }
  return null;
}

function claudeCredentialFiles(job) {
  const metadata = providerMetadata(job);
  const payload = objectValue(job.payload) || {};
  const files = normalizeList(payload.credentials_file || metadata.credentials_file || process.env.CLAUDE_CREDENTIALS_FILE);
  const configDir = stringValue(process.env.CLAUDE_CONFIG_DIR) || path.join(os.homedir(), ".claude");
  files.push(path.join(configDir, ".credentials.json"));
  return [...new Set(files.filter(Boolean))];
}

async function readJSONFile(file) {
  try {
    return JSON.parse(await fs.readFile(expandHome(file), "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw error;
  }
}

function expandHome(file) {
  const value = stringValue(file);
  if (value === "~") return os.homedir();
  if (value.startsWith("~/")) return path.join(os.homedir(), value.slice(2));
  return value;
}

function claudeAuthUrl(job) {
  return stringValue(job.payload?.auth_url || providerMetadata(job).auth_url || CLAUDE_AUTH_URL);
}

function claudeTokenUrl(job) {
  return stringValue(job.payload?.token_url || providerMetadata(job).token_url || CLAUDE_TOKEN_URL);
}

function claudeRedirectUrl(job) {
  return stringValue(job.payload?.redirect_uri || providerMetadata(job).redirect_uri || CLAUDE_MANUAL_REDIRECT_URL);
}

function claudeClientId(job) {
  return stringValue(job.payload?.client_id || providerMetadata(job).client_id || CLAUDE_CLIENT_ID);
}

function claudeScopes(job) {
  const configured = normalizeList(job.payload?.scopes || providerMetadata(job).scopes || currentBundle(job).scopes);
  return configured.length ? configured : CLAUDE_OAUTH_SCOPES;
}

function claudeBundleFromTokenResponse(job, response, current = {}, source = "claude_oauth") {
  if (!response.access_token) throw new TerminalJobError(response.error_description || response.error || "Claude token endpoint did not return access_token.");
  const expires = response.expires_at || (response.expires_in ? new Date(Date.now() + Number(response.expires_in) * 1000).toISOString() : current.expires_at || "");
  const account = objectValue(response.account) || {};
  const organization = objectValue(response.organization) || {};
  const nextScopes = normalizeList(response.scope || current.scopes);
  return {
    type: "oauth",
    access_token: response.access_token,
    refresh_token: response.refresh_token || current.refresh_token || "",
    expires_at: expires,
    scopes: nextScopes.length ? nextScopes : claudeScopes(job),
    provider: "anthropic_claude",
    auth_scheme: "bearer",
    subject: stringValue(account.uuid || current.subject),
    metadata: claudeClientMetadata({
      source,
      oauth_beta: CLAUDE_OAUTH_BETA,
      account_uuid: stringValue(account.uuid),
      account_email: stringValue(account.email_address),
      organization_uuid: stringValue(organization.uuid),
      token_type: stringValue(response.token_type),
    }),
  };
}

function claudeClientMetadata(metadata = {}) {
  return {
    client_version: stringValue(metadata.client_version || process.env.CLAUDE_CODE_VERSION || CLAUDE_CODE_VERSION),
    entrypoint: stringValue(metadata.entrypoint || process.env.CLAUDE_CODE_ENTRYPOINT || "cli"),
    user_type: stringValue(metadata.user_type || process.env.USER_TYPE || "external"),
    ...metadata,
  };
}

async function waitForManualInput(job, options) {
  const timeoutSeconds = Number(job.payload?.manual_input_timeout_seconds || providerMetadata(job).manual_input_timeout_seconds || 900);
  const intervalMs = Number(job.payload?.manual_input_poll_ms || providerMetadata(job).manual_input_poll_ms || 2000);
  const expiresAt = Date.now() + timeoutSeconds * 1000;
  while (Date.now() < expiresAt) {
    const data = await options.input?.();
    const input = objectValue(data?.input) || {};
    if (input.authorization_code || input.code) return input;
    await sleep(intervalMs);
  }
  throw new TerminalJobError("Manual OAuth authorization_code was not submitted before timeout.");
}

async function reportProgress(options, progress) {
  if (!options.progress) return;
  await options.progress(progress);
}

function expiresAtFromValue(value) {
  if (value === null || value === undefined || value === "") return "";
  if (typeof value === "number") return new Date(value > 1e12 ? value : value * 1000).toISOString();
  const text = String(value).trim();
  if (/^\d+$/.test(text)) {
    const numeric = Number(text);
    return new Date(numeric > 1e12 ? numeric : numeric * 1000).toISOString();
  }
  return text;
}

function decodeJwt(token) {
  const parts = String(token || "").split(".");
  if (parts.length < 2) return null;
  try {
    return JSON.parse(Buffer.from(parts[1], "base64url").toString("utf8"));
  } catch {
    return null;
  }
}

function providerMetadata(job) {
  return objectValue(job.provider_client?.metadata) || {};
}

function currentBundle(job) {
  return objectValue(job.token_bundle) || {};
}

function authUrlForJob(job) {
  const metadata = providerMetadata(job);
  if (isGoogleGeminiJob(job)) {
    return stringValue(job.payload?.auth_url || metadata.auth_url || process.env.GEMINI_AUTH_URL || GEMINI_AUTH_URL);
  }
  if (isGoogleAntigravityJob(job)) {
    return stringValue(job.payload?.auth_url || metadata.auth_url || process.env.ANTIGRAVITY_AUTH_URL || GEMINI_AUTH_URL);
  }
  return stringValue(job.payload?.auth_url || metadata.auth_url);
}

function tokenUrl(job) {
  const metadata = providerMetadata(job);
  if (isGoogleGeminiJob(job)) {
    return stringValue(job.payload?.token_url || metadata.token_url || process.env.GEMINI_TOKEN_URL || GEMINI_TOKEN_URL);
  }
  if (isGoogleAntigravityJob(job)) {
    return stringValue(job.payload?.token_url || metadata.token_url || process.env.ANTIGRAVITY_TOKEN_URL || GEMINI_TOKEN_URL);
  }
  return stringValue(job.payload?.token_url || metadata.token_url);
}

function clientId(job) {
  const metadata = providerMetadata(job);
  if (isGoogleGeminiJob(job)) {
    return stringValue(job.payload?.client_id || geminiClientId(metadata));
  }
  if (isGoogleAntigravityJob(job)) {
    return stringValue(job.payload?.client_id || antigravityClientId(metadata));
  }
  return stringValue(job.payload?.client_id || metadata.client_id);
}

function clientSecret(job) {
  const metadata = providerMetadata(job);
  if (isGoogleGeminiJob(job)) {
    return stringValue(job.payload?.client_secret || metadata.client_secret || job.provider_client?.credential || geminiClientSecret(metadata));
  }
  if (isGoogleAntigravityJob(job)) {
    return stringValue(job.payload?.client_secret || metadata.client_secret || job.provider_client?.credential || antigravityClientSecret(metadata));
  }
  return stringValue(job.payload?.client_secret || metadata.client_secret || job.provider_client?.credential);
}

function scopes(job) {
  const metadata = providerMetadata(job);
  const configured = normalizeList(job.payload?.scopes || metadata.scopes || currentBundle(job).scopes);
  if (configured.length) return configured;
  if (isGoogleGeminiJob(job)) return GEMINI_OAUTH_SCOPES;
  if (isGoogleAntigravityJob(job)) return ANTIGRAVITY_OAUTH_SCOPES;
  return [];
}

function expiresAt(bundle) {
  if (bundle.expires_at) return expiresAtFromValue(bundle.expires_at);
  if (bundle.expires_in) return new Date(Date.now() + Number(bundle.expires_in) * 1000).toISOString();
  return "";
}

function objectValue(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? value : null;
}

function stringValue(value) {
  return value === undefined || value === null ? "" : String(value).trim();
}

function normalized(value) {
  return stringValue(value).toLowerCase();
}

function normalizeList(value) {
  if (Array.isArray(value)) return value.map(stringValue).filter(Boolean);
  if (typeof value === "string") return value.split(/[,\s]+/).map(stringValue).filter(Boolean);
  return [];
}

function base64Url(buffer) {
  return Buffer.from(buffer).toString("base64").replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
