import { GatewayClient } from "./client.mjs";
import { executeJob, TerminalJobError } from "./strategies.mjs";

export function configFromEnv(env = process.env) {
  return {
    baseUrl: env.GATEWAY_BASE_URL || env.BASE_URL || "http://localhost:18080",
    token: env.OAUTH_WRAPPER_BEARER_TOKEN || "",
    leaseOwner: env.OAUTH_WRAPPER_LEASE_OWNER || `oauth-wrapper-${process.pid}`,
    leaseSeconds: Number(env.OAUTH_WRAPPER_LEASE_SECONDS || 300),
    intervalMs: Number(env.OAUTH_WRAPPER_INTERVAL_MS || 5000),
    supportedModes: splitList(env.OAUTH_WRAPPER_SUPPORTED_MODES || "oauth,openai_cli,codex_cli,claude_cli,google_pkce,github_device,kiro,windsurf_cli,codeium_cli,mock"),
    providerName: env.OAUTH_WRAPPER_PROVIDER_NAME || "",
    providerType: env.OAUTH_WRAPPER_PROVIDER_TYPE || "",
    authMode: env.OAUTH_WRAPPER_AUTH_MODE || "",
  };
}

export async function runOnce(config, deps = {}) {
  const client = deps.client || new GatewayClient(config);
  const logger = deps.logger || console;
  const leaseOwner = config.leaseOwner;
  const data = await client.claim({
    lease_owner: leaseOwner,
    lease_seconds: config.leaseSeconds,
    provider_name: config.providerName,
    provider_type: config.providerType,
    auth_mode: config.authMode,
    supported_modes: config.supportedModes,
  });
  const job = data.job;
  if (!job) {
    logger.log?.("oauth-wrapper: no job");
    return false;
  }

  try {
    const result = await executeJob(job, {
      log: (message) => logger.error?.(message) || logger.log?.(message),
      progress: (progress) => client.progress?.(job.id, { lease_owner: leaseOwner, progress }),
      input: () => client.input?.(job.id, leaseOwner),
    });
    await client.complete(job.id, {
      lease_owner: leaseOwner,
      token_bundle: result.token_bundle || {},
      provider_subject: result.provider_subject || "",
      scopes: result.scopes || [],
      auth_status: result.auth_status || "active",
      result: result.result || {},
    });
    logger.log?.(`oauth-wrapper: completed ${job.id} ${job.job_type} ${job.auth_mode}`);
    return true;
  } catch (error) {
    const terminal = error instanceof TerminalJobError || error.terminal === true;
    await client.fail(job.id, {
      lease_owner: leaseOwner,
      error: error instanceof Error ? error.message : String(error),
      terminal,
      auth_status: error.authStatus || "reauth_required",
    });
    logger.error?.(`oauth-wrapper: failed ${job.id}: ${error instanceof Error ? error.message : String(error)}`);
    if (deps.failFast) throw error;
    return true;
  }
}

export async function runLoop(config, deps = {}) {
  const stopAfter = deps.stopAfter || Number.POSITIVE_INFINITY;
  let iterations = 0;
  while (iterations < stopAfter) {
    await runOnce(config, deps);
    iterations += 1;
    await sleep(config.intervalMs);
  }
}

function splitList(value) {
  return String(value).split(/[,\s]+/).map((item) => item.trim()).filter(Boolean);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
