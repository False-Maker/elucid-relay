#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import zlib from "node:zlib";
import { execFile, spawn, spawnSync } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const repoRoot = path.resolve(new URL("..", import.meta.url).pathname);
const captureRoot = path.join(repoRoot, "infra", "fixtures", "oauth-official");
const codexRoot = process.env.CODEX_SOURCE_DIR || "/home/elucid/projects/web/codex/codex-rs";
const claudeRoot = process.env.CLAUDE_CODE_SOURCE_DIR || "/home/elucid/projects/claude-code-source-code";
const redacted = "[REDACTED]";
const timeoutMs = Number(process.env.OAUTH_CAPTURE_TIMEOUT_MS || 240000);

const geminiRoot = process.env.GEMINI_CLI_SOURCE_DIR || "/tmp/gemini-cli";
const targetArg = (process.argv[2] || "all").toLowerCase();
const target = ({ github: "github_copilot", copilot: "github_copilot" })[targetArg] || targetArg;

if (!["all", "codex", "claude", "gemini", "github_copilot"].includes(target)) {
  console.error("usage: node infra/oauth-official-capture.mjs [all|codex|claude|gemini|github_copilot|copilot]");
  process.exit(2);
}

await fs.mkdir(captureRoot, { recursive: true });

const results = [];
if (target === "all" || target === "codex") results.push(await captureCodex());
if (target === "all" || target === "claude") results.push(await captureClaude());
if (target === "all" || target === "gemini") results.push(await captureGemini());
if (target === "all" || target === "github_copilot") results.push(await captureGitHubCopilot());

for (const result of results) {
  const status = result.ok ? "ok" : "failed";
  console.log(`${result.name} ${status}: ${result.outputPath} (${result.requests} request(s))`);
  if (!result.ok && result.error) console.log(`  ${result.error}`);
}

process.exitCode = results.some((result) => !result.ok) ? 1 : 0;

async function captureCodex() {
  const name = "codex";
  const outputPath = path.join(captureRoot, "codex.json");
  const server = await startRecorder({ flavor: "codex" });
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "elucid-codex-capture-"));
  const codexHome = path.join(tmp, "codex-home");
  await fs.mkdir(codexHome, { recursive: true });
  await fs.writeFile(path.join(codexHome, "auth.json"), JSON.stringify(fakeCodexAuth(), null, 2));

  const { file, argsPrefix } = await codexCommand();
  const args = [
    ...argsPrefix,
    "exec",
    "--skip-git-repo-check",
    "-C",
    repoRoot,
    "-c",
    `openai_base_url="${server.baseUrl}/v1"`,
    "-c",
    "model=\"gpt-5.1-codex-max\"",
    "-c",
    "model_reasoning_effort=\"low\"",
    "Say ok.",
  ];

  try {
    const run = await runCommand(file, args, {
      cwd: codexRoot,
      env: {
        ...process.env,
        CODEX_HOME: codexHome,
        RUST_BACKTRACE: process.env.RUST_BACKTRACE || "0",
        OPENAI_API_KEY: "",
      },
      timeout: Math.max(timeoutMs, 600000),
    });
    await writeCapture(outputPath, {
      name,
      ok: run.exitCode === 0,
      command: redactCommand([file, ...args]),
      stdout: trimOutput(run.stdout),
      stderr: trimOutput(run.stderr),
      exitCode: run.exitCode,
      signal: run.signal,
      recorder: server.snapshot(),
    });
    return { name, ok: run.exitCode === 0, outputPath, requests: server.requests.length, error: run.exitCode === 0 ? "" : trimOutput(run.stderr || run.stdout) };
  } catch (error) {
    await writeFailure(outputPath, name, error, server);
    return { name, ok: false, outputPath, requests: server.requests.length, error: error.message };
  } finally {
    await server.close();
    await fs.rm(tmp, { recursive: true, force: true });
  }
}

async function captureClaude() {
  const name = "claude";
  const outputPath = path.join(captureRoot, "claude.json");
  const server = await startRecorder({ flavor: "claude" });
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "elucid-claude-capture-"));

  try {
    const command = await claudeCommand();
    const args = [
      ...command.argsPrefix,
      "-p",
      "--output-format",
      "json",
      "--no-session-persistence",
      "Say ok.",
    ];
    const run = await runCommand(command.file, args, {
      cwd: command.cwd,
      env: {
        ...process.env,
        ANTHROPIC_BASE_URL: server.baseUrl,
        ANTHROPIC_API_KEY: "",
        ANTHROPIC_AUTH_TOKEN: "",
        CLAUDE_CODE_OAUTH_TOKEN: "claude-fake-access-token",
        CLAUDE_CONFIG_DIR: path.join(tmp, "config"),
        CLAUDE_CODE_ENTRYPOINT: "cli",
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
        DISABLE_TELEMETRY: "1",
        USER_TYPE: "external",
      },
      timeout: timeoutMs,
    });
    await writeCapture(outputPath, {
      name,
      ok: run.exitCode === 0,
      command: redactCommand([command.file, ...args]),
      stdout: trimOutput(run.stdout),
      stderr: trimOutput(run.stderr),
      exitCode: run.exitCode,
      signal: run.signal,
      recorder: server.snapshot(),
    });
    return { name, ok: run.exitCode === 0, outputPath, requests: server.requests.length, error: run.exitCode === 0 ? "" : trimOutput(run.stderr || run.stdout) };
  } catch (error) {
    await writeFailure(outputPath, name, error, server);
    return { name, ok: false, outputPath, requests: server.requests.length, error: error.message };
  } finally {
    await server.close();
    await fs.rm(tmp, { recursive: true, force: true });
  }
}

async function captureGemini() {
  const name = "gemini";
  const outputPath = path.join(captureRoot, "gemini.json");
  const server = await startRecorder({ flavor: "gemini" });
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "elucid-gemini-capture-"));
  const script = path.join(tmp, "capture-gemini-code-assist.mjs");

  try {
    await fs.writeFile(script, geminiCaptureScript(), { mode: 0o600 });
    const run = await runCommand(process.execPath, [script], {
      cwd: repoRoot,
      env: {
        ...process.env,
        CODE_ASSIST_ENDPOINT: server.baseUrl,
        CODE_ASSIST_API_VERSION: "v1internal",
        GEMINI_CAPTURE_REPO_ROOT: repoRoot,
        GEMINI_CAPTURE_SOURCE_DIR: geminiRoot,
        GEMINI_CAPTURE_ACCESS_TOKEN: "google-gemini-capture-access-token",
        GEMINI_CAPTURE_PROJECT: "gemini-capture-project",
        GEMINI_CAPTURE_SESSION_ID: "gemini-capture-session",
        GEMINI_CAPTURE_USER_PROMPT_ID: "gemini-capture-prompt",
        GEMINI_CAPTURE_MODEL: "gemini-2.5-pro",
        GEMINI_CAPTURE_VERSION: "0.42.0-nightly.20260428.g59b2dea0e",
        GEMINI_CAPTURE_DISABLE_REAL_NETWORK: "1",
      },
      timeout: timeoutMs,
    });
    await writeCapture(outputPath, {
      name,
      ok: run.exitCode === 0,
      command: redactCommand([process.execPath, script]),
      stdout: trimOutput(run.stdout),
      stderr: trimOutput(run.stderr),
      exitCode: run.exitCode,
      signal: run.signal,
      recorder: server.snapshot(),
    });
    return { name, ok: run.exitCode === 0, outputPath, requests: server.requests.length, error: run.exitCode === 0 ? "" : trimOutput(run.stderr || run.stdout) };
  } catch (error) {
    await writeFailure(outputPath, name, error, server);
    return { name, ok: false, outputPath, requests: server.requests.length, error: error.message };
  } finally {
    await server.close();
    await fs.rm(tmp, { recursive: true, force: true });
  }
}

async function captureGitHubCopilot() {
  const name = "github-copilot";
  const outputPath = path.join(captureRoot, "github-copilot.json");
  const server = await startRecorder({ flavor: "github_copilot" });
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "elucid-github-copilot-capture-"));
  const script = path.join(tmp, "capture-github-copilot.mjs");

  try {
    await fs.writeFile(script, githubCopilotCaptureScript(), { mode: 0o600 });
    const run = await runCommand(process.execPath, [script], {
      cwd: repoRoot,
      env: {
        ...process.env,
        GITHUB_COPILOT_CAPTURE_ENDPOINT: server.baseUrl,
        GITHUB_COPILOT_CAPTURE_ACCESS_TOKEN: "github-copilot-capture-token",
        GITHUB_COPILOT_CAPTURE_MODEL: "gpt-5.1",
        GITHUB_COPILOT_CAPTURE_CLIENT_VERSION: "0.44.0",
        GITHUB_COPILOT_CAPTURE_VSCODE_VERSION: "1.109.3",
        GITHUB_COPILOT_CAPTURE_API_VERSION: "2025-05-01",
        GITHUB_COPILOT_CAPTURE_DISABLE_REAL_NETWORK: "1",
      },
      timeout: timeoutMs,
    });
    await writeCapture(outputPath, {
      name,
      ok: run.exitCode === 0,
      command: redactCommand([process.execPath, script]),
      stdout: trimOutput(run.stdout),
      stderr: trimOutput(run.stderr),
      exitCode: run.exitCode,
      signal: run.signal,
      recorder: server.snapshot(),
    });
    return { name, ok: run.exitCode === 0, outputPath, requests: server.requests.length, error: run.exitCode === 0 ? "" : trimOutput(run.stderr || run.stdout) };
  } catch (error) {
    await writeFailure(outputPath, name, error, server);
    return { name, ok: false, outputPath, requests: server.requests.length, error: error.message };
  } finally {
    await server.close();
    await fs.rm(tmp, { recursive: true, force: true });
  }
}

function geminiCaptureScript() {
  return String.raw`import http from "node:http";
import https from "node:https";
import { Readable } from "node:stream";

const endpoint = process.env.CODE_ASSIST_ENDPOINT;
const version = process.env.CODE_ASSIST_API_VERSION || "v1internal";
const repoRoot = process.env.GEMINI_CAPTURE_REPO_ROOT || process.cwd();
const model = process.env.GEMINI_CAPTURE_MODEL || "gemini-2.5-pro";
const project = process.env.GEMINI_CAPTURE_PROJECT || "gemini-capture-project";
const sessionId = process.env.GEMINI_CAPTURE_SESSION_ID || "gemini-capture-session";
const userPromptId = process.env.GEMINI_CAPTURE_USER_PROMPT_ID || "gemini-capture-prompt";
const versionText = process.env.GEMINI_CAPTURE_VERSION || "0.42.0-nightly.20260428.g59b2dea0e";
const accessToken = process.env.GEMINI_CAPTURE_ACCESS_TOKEN || "google-gemini-capture-access-token";

if (!endpoint) throw new Error("CODE_ASSIST_ENDPOINT is required");
if (process.env.GEMINI_CAPTURE_DISABLE_REAL_NETWORK === "1") {
  const allowedEndpoint = new URL(endpoint);
  const allowedOrigin = allowedEndpoint.origin;
  const isAllowed = (candidate) => candidate && candidate.origin === allowedOrigin;
  const blocked = (candidate) => {
    const href = candidate && candidate.href ? candidate.href : String(candidate);
    throw new Error("blocked non-recorder request: " + href);
  };
  const normalizeURL = (input, options = {}) => {
    if (typeof input === "string" || input instanceof URL) return new URL(String(input), endpoint);
    if (input && typeof input.url === "string") return new URL(input.url, endpoint);
    if (input && typeof input.href === "string") return new URL(input.href, endpoint);
    const protocol = options.protocol || input?.protocol || allowedEndpoint.protocol;
    const hostname = options.hostname || options.host || input?.hostname || input?.host;
    const port = options.port || input?.port;
    const requestPath = options.path || input?.path || "/";
    if (!hostname) return null;
    return new URL(protocol + "//" + hostname + (port ? ":" + port : "") + requestPath);
  };
  const patchRequests = (mod) => {
    const originalRequest = mod.request.bind(mod);
    mod.request = function patchedRequest(input, options, callback) {
      const candidate = normalizeURL(input, options);
      if (!isAllowed(candidate)) blocked(candidate);
      return originalRequest(input, options, callback);
    };
    mod.get = function patchedGet(input, options, callback) {
      const req = mod.request(input, options, callback);
      req.end();
      return req;
    };
  };
  if (typeof globalThis.fetch === "function") {
    const originalFetch = globalThis.fetch.bind(globalThis);
    globalThis.fetch = function patchedFetch(input, init) {
      const candidate = normalizeURL(input, init);
      if (!isAllowed(candidate)) blocked(candidate);
      return originalFetch(input, init);
    };
  }
  patchRequests(http);
  patchRequests(https);
}

const request = {
  model,
  contents: [{ role: "user", parts: [{ text: "Say ok." }] }],
  config: {
    temperature: 1,
    topP: 0.95,
    topK: 64,
    maxOutputTokens: 8,
    thinkingConfig: { includeThoughts: true, thinkingBudget: 8192 },
  },
};

const headers = {
  Authorization: "Bearer " + accessToken,
  "Content-Type": "application/json",
  "User-Agent": "GeminiCLI/" + versionText + "/" + model + " (linux; x64; terminal)",
};

const body = JSON.stringify(toGenerateContentRequest(request, userPromptId, project, sessionId));
await codeAssistPost("generateContent", body, false);
await codeAssistPost("streamGenerateContent", body, true);

async function codeAssistPost(method, body, stream) {
  const url = new URL(endpoint + "/" + version + ":" + method);
  if (stream) url.searchParams.set("alt", "sse");
  const response = await fetch(url, {
    method: "POST",
    headers,
    body,
  });
  if (stream) {
    for await (const _line of Readable.fromWeb(response.body)) {}
    return;
  }
  await response.text();
}

function toGenerateContentRequest(req, userPromptId, project, sessionId) {
  return {
    model: req.model,
    project,
    user_prompt_id: userPromptId,
    request: {
      contents: req.contents,
      generationConfig: req.config,
      session_id: sessionId,
    },
  };
}
`;
}

function githubCopilotCaptureScript() {
  return String.raw`import crypto from "node:crypto";
import http from "node:http";
import https from "node:https";

const endpoint = process.env.GITHUB_COPILOT_CAPTURE_ENDPOINT;
const accessToken = process.env.GITHUB_COPILOT_CAPTURE_ACCESS_TOKEN || "github-copilot-capture-token";
const model = process.env.GITHUB_COPILOT_CAPTURE_MODEL || "gpt-5.1";
const clientVersion = process.env.GITHUB_COPILOT_CAPTURE_CLIENT_VERSION || "0.44.0";
const vscodeVersion = process.env.GITHUB_COPILOT_CAPTURE_VSCODE_VERSION || "1.109.3";
const apiVersion = process.env.GITHUB_COPILOT_CAPTURE_API_VERSION || "2025-05-01";

if (!endpoint) throw new Error("GITHUB_COPILOT_CAPTURE_ENDPOINT is required");
if (process.env.GITHUB_COPILOT_CAPTURE_DISABLE_REAL_NETWORK === "1") {
  const allowedEndpoint = new URL(endpoint);
  const allowedOrigin = allowedEndpoint.origin;
  const isAllowed = (candidate) => candidate && candidate.origin === allowedOrigin;
  const blocked = (candidate) => {
    const href = candidate && candidate.href ? candidate.href : String(candidate);
    throw new Error("blocked non-recorder request: " + href);
  };
  const normalizeURL = (input, options = {}) => {
    if (typeof input === "string" || input instanceof URL) return new URL(String(input), endpoint);
    if (input && typeof input.url === "string") return new URL(input.url, endpoint);
    if (input && typeof input.href === "string") return new URL(input.href, endpoint);
    const protocol = options.protocol || input?.protocol || allowedEndpoint.protocol;
    const hostname = options.hostname || options.host || input?.hostname || input?.host;
    const port = options.port || input?.port;
    const requestPath = options.path || input?.path || "/";
    if (!hostname) return null;
    return new URL(protocol + "//" + hostname + (port ? ":" + port : "") + requestPath);
  };
  const patchRequests = (mod) => {
    const originalRequest = mod.request.bind(mod);
    mod.request = function patchedRequest(input, options, callback) {
      const candidate = normalizeURL(input, options);
      if (!isAllowed(candidate)) blocked(candidate);
      return originalRequest(input, options, callback);
    };
    mod.get = function patchedGet(input, options, callback) {
      const req = mod.request(input, options, callback);
      req.end();
      return req;
    };
  };
  if (typeof globalThis.fetch === "function") {
    const originalFetch = globalThis.fetch.bind(globalThis);
    globalThis.fetch = function patchedFetch(input, init) {
      const candidate = normalizeURL(input, init);
      if (!isAllowed(candidate)) blocked(candidate);
      return originalFetch(input, init);
    };
  }
  patchRequests(http);
  patchRequests(https);
}

await get("/models", "conversation-panel");
await post("/chat/completions", {
  model,
  messages: [{ role: "user", content: "Say ok." }],
  max_tokens: 8,
  n: 1,
  stream: false,
}, "conversation-panel", "user");
await post("/chat/completions", {
  model,
  messages: [{ role: "user", content: "Say ok." }],
  max_tokens: 8,
  stream: true,
  stream_options: { include_usage: true },
}, "conversation-panel", "user", { accept: "text/event-stream" });
await post("/responses", {
  model,
  input: [{ role: "user", content: [{ type: "input_text", text: "Say ok." }] }],
  stream: true,
  store: false,
}, "responses-proxy", "user", { accept: "text/event-stream" });
await post("/embeddings", {
  model: "copilot-text-embedding-ada-002",
  input: "hello",
}, "conversation-panel", "user");

async function get(path, intent) {
  const response = await fetch(endpoint + path, {
    method: "GET",
    headers: officialHeaders(intent, "user", "GET"),
  });
  await response.text();
}

async function post(path, body, intent, initiator, extraHeaders = {}) {
  const response = await fetch(endpoint + path, {
    method: "POST",
    headers: { ...officialHeaders(intent, initiator, "POST"), ...extraHeaders },
    body: JSON.stringify(body),
  });
  await response.text();
}

function officialHeaders(intent, initiator, method) {
  const requestId = crypto.randomUUID();
  const headers = {
    Authorization: "Bearer " + accessToken,
    Accept: "application/json",
    "Content-Type": "application/json",
    "Copilot-Integration-Id": "vscode-chat",
    "Editor-Version": "vscode/" + vscodeVersion,
    "Editor-Plugin-Version": "copilot-chat/" + clientVersion,
    "User-Agent": "GitHubCopilotChat/" + clientVersion,
    "OpenAI-Intent": intent,
    "X-GitHub-Api-Version": apiVersion,
    "X-Request-Id": requestId,
    "X-VSCode-User-Agent-Library-Version": "electron-fetch",
  };
  if (method !== "GET") {
    headers["X-Interaction-Id"] = requestId;
    headers["X-Interaction-Type"] = intent;
    headers["X-Agent-Task-Id"] = requestId;
    headers["X-Initiator"] = initiator;
  }
  return headers;
}
`;
}

async function claudeCommand() {
  const rootCli = path.join(claudeRoot, "cli.js");
  const distCli = path.join(claudeRoot, "dist", "cli.js");
  if (await exists(rootCli)) return { file: "node", argsPrefix: [rootCli], cwd: claudeRoot };
  if (await exists(distCli)) return { file: "node", argsPrefix: [distCli], cwd: claudeRoot };

  if (process.env.CLAUDE_CODE_SOURCE_ONLY !== "1") {
    const installed = await resolveCommand(process.env.CLAUDE_COMMAND || "claude");
    if (installed) return { file: installed, argsPrefix: [], cwd: repoRoot };
  }

  const packageLock = path.join(claudeRoot, "package-lock.json");
  if (!(await exists(path.join(claudeRoot, "node_modules")))) {
    const installArgs = (await exists(packageLock)) ? ["ci"] : ["install"];
    await execFileAsync("npm", installArgs, { cwd: claudeRoot, timeout: 180000, maxBuffer: 8 * 1024 * 1024 });
  }

  await execFileAsync("npm", ["run", "build"], { cwd: claudeRoot, timeout: 180000, maxBuffer: 8 * 1024 * 1024 });
  if (await exists(distCli)) return { file: "node", argsPrefix: [distCli], cwd: claudeRoot };
  throw new Error(`Claude Code CLI was not built and no installed claude command was found: ${distCli}`);
}

async function codexCommand() {
  const debugBinary = path.join(codexRoot, "target", "debug", "codex");
  const releaseBinary = path.join(codexRoot, "target", "release", "codex");
  if (await exists(debugBinary)) return { file: debugBinary, argsPrefix: [] };
  if (await exists(releaseBinary)) return { file: releaseBinary, argsPrefix: [] };
  return {
    file: "cargo",
    argsPrefix: [
      "run",
      "--manifest-path",
      path.join(codexRoot, "Cargo.toml"),
      "-p",
      "codex-cli",
      "--bin",
      "codex",
      "--",
    ],
  };
}

async function startRecorder({ flavor }) {
  const requests = [];
  const sockets = new Set();
  const server = http.createServer(async (req, res) => {
    const body = await readBody(req);
    const decodedBody = decodeBody(body, req.headers["content-encoding"]);
    const url = new URL(req.url || "/", "http://127.0.0.1");
    const textBody = decodedBody.toString("utf8");
    const jsonBody = parseJSON(textBody);

    pushCapturedRequest(requests, {
      at: new Date().toISOString(),
      method: req.method,
      url: req.url,
      path: url.pathname,
      query: Object.fromEntries(url.searchParams.entries()),
      headers: redactHeaders(req.headers),
      bodyText: redactTextBody(textBody),
      jsonBody: jsonBody === undefined ? undefined : redactValue(jsonBody),
      rawBodyBytes: body.length,
      decodedBodyBytes: decodedBody.length,
    });

    routeResponse(req, res, url.pathname, jsonBody, flavor);
  });
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
  });
  server.on("upgrade", (req, socket, head) => {
    handleWebSocketUpgrade(req, socket, head, requests, flavor);
  });

  await new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });

  const { port } = server.address();
  return {
    baseUrl: `http://127.0.0.1:${port}`,
    requests,
    snapshot: () => ({ baseUrl: `http://127.0.0.1:${port}`, requests }),
    close: () => closeRecorder(server, sockets),
  };
}

function closeRecorder(server, sockets) {
  for (const socket of sockets) {
    socket.destroy();
  }
  return new Promise((resolve) => {
    server.close(() => resolve());
  });
}

function pushCapturedRequest(requests, entry) {
  const captured = {
    index: requests.length + 1,
    ...entry,
  };
  requests.push(captured);
  return captured;
}

function handleWebSocketUpgrade(req, socket, head, requests, flavor) {
  const url = new URL(req.url || "/", "http://127.0.0.1");
  const upgradeEntry = pushCapturedRequest(requests, {
    at: new Date().toISOString(),
    method: req.method,
    url: req.url,
    path: url.pathname,
    query: Object.fromEntries(url.searchParams.entries()),
    headers: redactHeaders(req.headers),
    bodyText: "",
    rawBodyBytes: 0,
    decodedBodyBytes: 0,
    websocket: { upgrade: true },
  });

  if (!url.pathname.endsWith("/responses") || flavor !== "codex") {
    socket.write("HTTP/1.1 404 Not Found\r\nConnection: close\r\n\r\n");
    socket.destroy();
    return;
  }

  const key = req.headers["sec-websocket-key"];
  if (!key) {
    socket.write("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n");
    socket.destroy();
    return;
  }

  const accept = crypto
    .createHash("sha1")
    .update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
    .digest("base64");
  socket.write([
    "HTTP/1.1 101 Switching Protocols",
    "Upgrade: websocket",
    "Connection: Upgrade",
    `Sec-WebSocket-Accept: ${accept}`,
    "\r\n",
  ].join("\r\n"));

  let buffer = Buffer.from(head || []);
  let sequence = 0;
  const consume = () => {
    for (;;) {
      const parsed = parseWebSocketFrame(buffer);
      if (!parsed) return;
      buffer = buffer.subarray(parsed.consumed);
      if (parsed.opcode === 0x8) {
        socket.end(encodeWebSocketFrame(Buffer.alloc(0), 0x8));
        return;
      }
      if (parsed.opcode === 0x9) {
        socket.write(encodeWebSocketFrame(parsed.payload, 0xA));
        continue;
      }
      if (parsed.opcode !== 0x1 && parsed.opcode !== 0x2) continue;

      const textBody = parsed.payload.toString("utf8");
      const jsonBody = parseJSON(textBody);
      pushCapturedRequest(requests, {
        at: new Date().toISOString(),
        method: "WEBSOCKET",
        url: req.url,
        path: url.pathname,
        query: Object.fromEntries(url.searchParams.entries()),
        headers: redactHeaders(req.headers),
        bodyText: redactTextBody(textBody),
        jsonBody: jsonBody === undefined ? undefined : redactValue(jsonBody),
        rawBodyBytes: parsed.payload.length,
        decodedBodyBytes: Buffer.byteLength(textBody),
        websocket: {
          parent_index: upgradeEntry.index,
          direction: "client_to_server",
          opcode: parsed.opcode,
          fin: parsed.fin,
        },
      });

      sequence += 1;
      const model = typeof jsonBody?.model === "string" ? jsonBody.model : "gpt-5.1-codex-max";
      for (const event of openAIResponsesEvents(`resp_capture_ws_${sequence}`, model)) {
        socket.write(encodeWebSocketFrame(Buffer.from(JSON.stringify(event)), 0x1));
      }
    }
  };

  socket.on("data", (chunk) => {
    buffer = Buffer.concat([buffer, chunk]);
    consume();
  });
  socket.on("error", () => {});
  consume();
}

function parseWebSocketFrame(buffer) {
  if (buffer.length < 2) return null;
  const first = buffer[0];
  const second = buffer[1];
  const fin = (first & 0x80) !== 0;
  const opcode = first & 0x0f;
  const masked = (second & 0x80) !== 0;
  let offset = 2;
  let length = second & 0x7f;
  if (length === 126) {
    if (buffer.length < offset + 2) return null;
    length = buffer.readUInt16BE(offset);
    offset += 2;
  } else if (length === 127) {
    if (buffer.length < offset + 8) return null;
    const bigLength = buffer.readBigUInt64BE(offset);
    if (bigLength > BigInt(Number.MAX_SAFE_INTEGER)) {
      throw new Error("WebSocket frame is too large to capture.");
    }
    length = Number(bigLength);
    offset += 8;
  }
  const maskOffset = offset;
  if (masked) offset += 4;
  if (buffer.length < offset + length) return null;
  const payload = Buffer.from(buffer.subarray(offset, offset + length));
  if (masked) {
    const mask = buffer.subarray(maskOffset, maskOffset + 4);
    for (let i = 0; i < payload.length; i += 1) {
      payload[i] ^= mask[i % 4];
    }
  }
  return { fin, opcode, payload, consumed: offset + length };
}

function encodeWebSocketFrame(payload, opcode = 0x1) {
  const body = Buffer.isBuffer(payload) ? payload : Buffer.from(payload);
  let header;
  if (body.length < 126) {
    header = Buffer.from([0x80 | opcode, body.length]);
  } else if (body.length <= 0xffff) {
    header = Buffer.alloc(4);
    header[0] = 0x80 | opcode;
    header[1] = 126;
    header.writeUInt16BE(body.length, 2);
  } else {
    header = Buffer.alloc(10);
    header[0] = 0x80 | opcode;
    header[1] = 127;
    header.writeBigUInt64BE(BigInt(body.length), 2);
  }
  return Buffer.concat([header, body]);
}

function routeResponse(req, res, pathname, jsonBody, flavor) {
  if (req.method === "GET" && pathname.endsWith("/models")) {
    if (flavor === "github_copilot") {
      writeJSON(res, 200, githubCopilotModelsResponse());
      return;
    }
    writeJSON(res, 200, {
      models: [
        {
          slug: "gpt-5.1-codex-max",
          display_name: "gpt-5.1-codex-max",
          description: "Capture model",
          context_window: 272000,
          max_context_window: 272000,
          supported_in_api: true,
          supports_parallel_tool_calls: true,
          supports_reasoning_summaries: true,
          default_reasoning_level: "low",
          supported_reasoning_levels: [{ effort: "low", description: "Capture" }],
          shell_type: "shell_command",
          input_modalities: ["text"],
          priority: 1,
          upgrade: null,
          base_instructions: "",
          model_messages: null,
          default_reasoning_summary: "auto",
          support_verbosity: false,
          default_verbosity: null,
          apply_patch_tool_type: null,
          truncation_policy: { mode: "tokens", limit: 272000 },
          experimental_supported_tools: [],
          visibility: "list",
        },
        {
          slug: "claude-sonnet-4-5-20250929",
          display_name: "claude-sonnet-4-5-20250929",
          description: "Capture model",
          context_window: 200000,
          max_context_window: 200000,
          supported_in_api: true,
          supported_reasoning_levels: [],
          shell_type: "shell_command",
          input_modalities: ["text"],
          priority: 2,
          upgrade: null,
          base_instructions: "",
          model_messages: null,
          supports_reasoning_summaries: false,
          default_reasoning_summary: "auto",
          support_verbosity: false,
          default_verbosity: null,
          apply_patch_tool_type: null,
          truncation_policy: { mode: "tokens", limit: 200000 },
          supports_parallel_tool_calls: false,
          experimental_supported_tools: [],
          visibility: "list",
        },
      ],
    });
    return;
  }

  if (pathname.endsWith("/responses")) {
    writeSSE(res, openAIResponsesEvents());
    return;
  }

  if (flavor === "github_copilot" && pathname.endsWith("/chat/completions")) {
    if (jsonBody?.stream === true || req.headers.accept === "text/event-stream") {
      writeSSE(res, openAIChatCompletionEvents());
      return;
    }
    writeJSON(res, 200, openAIChatCompletionResponse());
    return;
  }

  if (flavor === "github_copilot" && pathname.endsWith("/embeddings")) {
    writeJSON(res, 200, openAIEmbeddingsResponse());
    return;
  }

  if (flavor === "gemini" && pathname.endsWith(":generateContent")) {
    writeJSON(res, 200, geminiGenerateContentResponse());
    return;
  }

  if (flavor === "gemini" && pathname.endsWith(":streamGenerateContent")) {
    writeGeminiSSE(res, geminiGenerateContentEvents());
    return;
  }

  if (pathname.endsWith("/messages")) {
    if (jsonBody?.stream === true || req.headers.accept === "text/event-stream") {
      writeSSE(res, anthropicMessagesEvents());
      return;
    }
    writeJSON(res, 200, anthropicMessageResponse());
    return;
  }

  if (pathname.includes("/organizations") || pathname.includes("/settings") || pathname.includes("/profile") || pathname.includes("/oauth") || flavor === "claude") {
    writeJSON(res, 200, { ok: true });
    return;
  }

  writeJSON(res, 200, { ok: true });
}

function githubCopilotModelsResponse() {
  return {
    object: "list",
    data: [
      {
        id: "gpt-5.1",
        object: "model",
        owned_by: "github-copilot",
        vendor: "OpenAI",
        supported_endpoints: ["/chat/completions", "/responses"],
      },
      {
        id: "copilot-text-embedding-ada-002",
        object: "model",
        owned_by: "github-copilot",
        vendor: "OpenAI",
        supported_endpoints: ["/embeddings"],
      },
    ],
  };
}

function openAIChatCompletionResponse() {
  return {
    id: "chatcmpl_capture_1",
    object: "chat.completion",
    created: Math.floor(Date.now() / 1000),
    model: "gpt-5.1",
    choices: [{ index: 0, message: { role: "assistant", content: "ok" }, finish_reason: "stop" }],
    usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
  };
}

function openAIChatCompletionEvents() {
  return [
    {
      id: "chatcmpl_capture_1",
      object: "chat.completion.chunk",
      created: Math.floor(Date.now() / 1000),
      model: "gpt-5.1",
      choices: [{ index: 0, delta: { role: "assistant" }, finish_reason: null }],
    },
    {
      id: "chatcmpl_capture_1",
      object: "chat.completion.chunk",
      created: Math.floor(Date.now() / 1000),
      model: "gpt-5.1",
      choices: [{ index: 0, delta: { content: "ok" }, finish_reason: null }],
    },
    {
      id: "chatcmpl_capture_1",
      object: "chat.completion.chunk",
      created: Math.floor(Date.now() / 1000),
      model: "gpt-5.1",
      choices: [{ index: 0, delta: {}, finish_reason: "stop" }],
      usage: { prompt_tokens: 1, completion_tokens: 1, total_tokens: 2 },
    },
  ];
}

function openAIEmbeddingsResponse() {
  return {
    object: "list",
    data: [{ object: "embedding", index: 0, embedding: [0.1, 0.2, 0.3] }],
    model: "copilot-text-embedding-ada-002",
    usage: { prompt_tokens: 1, total_tokens: 1 },
  };
}

function geminiGenerateContentResponse() {
  return {
    response: {
      modelVersion: "gemini-2.5-pro-001",
      candidates: [{
        index: 0,
        content: { role: "model", parts: [{ text: "ok" }] },
        finishReason: "STOP",
      }],
      usageMetadata: {
        promptTokenCount: 1,
        candidatesTokenCount: 1,
        totalTokenCount: 2,
      },
    },
    traceId: "gemini_capture_trace",
  };
}

function geminiGenerateContentEvents() {
  return [geminiGenerateContentResponse()];
}

function openAIResponsesEvents(id = "resp_capture_1", model = "gpt-5.1-codex-max") {
  return [
    {
      type: "response.created",
      response: {
        id,
        object: "response",
        created_at: Math.floor(Date.now() / 1000),
        status: "in_progress",
        model,
        output: [],
      },
    },
    {
      type: "response.output_item.done",
      item: {
        type: "message",
        role: "assistant",
        id: `msg_${id}`,
        content: [{ type: "output_text", text: "ok" }],
      },
    },
    {
      type: "response.completed",
      response: {
        id,
        object: "response",
        status: "completed",
        output: [],
        usage: {
          input_tokens: 1,
          input_tokens_details: null,
          output_tokens: 1,
          output_tokens_details: null,
          total_tokens: 2,
        },
      },
    },
  ];
}

function anthropicMessagesEvents() {
  return [
    {
      type: "message_start",
      message: {
        id: "msg_capture_1",
        type: "message",
        role: "assistant",
        model: "claude-sonnet-4-5-20250929",
        content: [],
        stop_reason: null,
        stop_sequence: null,
        usage: { input_tokens: 1, output_tokens: 0 },
      },
    },
    { type: "content_block_start", index: 0, content_block: { type: "text", text: "" } },
    { type: "content_block_delta", index: 0, delta: { type: "text_delta", text: "ok" } },
    { type: "content_block_stop", index: 0 },
    { type: "message_delta", delta: { stop_reason: "end_turn", stop_sequence: null }, usage: { output_tokens: 1 } },
    { type: "message_stop" },
  ];
}

function anthropicMessageResponse() {
  return {
    id: "msg_capture_1",
    type: "message",
    role: "assistant",
    model: "claude-sonnet-4-5-20250929",
    content: [{ type: "text", text: "ok" }],
    stop_reason: "end_turn",
    stop_sequence: null,
    usage: { input_tokens: 1, output_tokens: 1 },
  };
}

async function runCommand(file, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, {
      cwd: options.cwd,
      env: options.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => {
      child.kill("SIGTERM");
      setTimeout(() => child.kill("SIGKILL"), 3000).unref();
    }, options.timeout).unref();

    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", (error) => {
      clearTimeout(timer);
      error.stdout = stdout;
      error.stderr = stderr;
      reject(error);
    });
    child.on("close", (exitCode, signal) => {
      clearTimeout(timer);
      resolve({ exitCode, signal, stdout, stderr });
    });
  });
}

function fakeCodexAuth() {
  const now = Math.floor(Date.now() / 1000);
  const authClaims = {
    chatgpt_account_id: "acc-capture",
    chatgpt_user_id: "user-capture",
    chatgpt_plan_type: "plus",
    chatgpt_account_is_fedramp: false,
  };
  const accessToken = fakeJWT({
    iss: "https://auth.openai.com",
    aud: "https://api.openai.com/v1",
    sub: "user-capture",
    exp: now + 3600,
    iat: now,
    scp: ["codex", "user:inference"],
    "https://api.openai.com/auth": authClaims,
  });
  const idToken = fakeJWT({
    iss: "https://auth.openai.com",
    aud: "app_EMoamEEZ73f0CkXaXp7hrann",
    sub: "user-capture",
    exp: now + 3600,
    iat: now,
    "https://api.openai.com/auth": authClaims,
  });
  return {
    auth_mode: "chatgpt",
    OPENAI_API_KEY: null,
    tokens: {
      id_token: idToken,
      access_token: accessToken,
      refresh_token: "refresh-capture",
      account_id: "acc-capture",
    },
    last_refresh: new Date().toISOString(),
  };
}

function fakeJWT(payload) {
  const header = { alg: "none", typ: "JWT" };
  return `${base64URL(JSON.stringify(header))}.${base64URL(JSON.stringify(payload))}.${base64URL("sig")}`;
}

function base64URL(value) {
  return Buffer.from(value).toString("base64url");
}

async function readBody(req) {
  const chunks = [];
  for await (const chunk of req) chunks.push(chunk);
  return Buffer.concat(chunks);
}

function decodeBody(body, encoding) {
  const normalized = String(encoding || "").toLowerCase();
  try {
    if (normalized.includes("gzip")) return zlib.gunzipSync(body);
    if (normalized.includes("deflate")) return zlib.inflateSync(body);
    if (normalized.includes("zstd")) {
      const decoded = spawnSync("zstd", ["-d", "-c"], { input: body, maxBuffer: 128 * 1024 * 1024 });
      if (decoded.status === 0) return decoded.stdout;
    }
  } catch {
    return body;
  }
  return body;
}

function parseJSON(text) {
  if (!text.trim()) return undefined;
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function writeSSE(res, events) {
  res.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache",
    connection: "keep-alive",
  });
  for (const event of events) {
    res.write(`event: ${event.type}\n`);
    res.write(`data: ${JSON.stringify(event)}\n\n`);
  }
  res.end();
}

function writeGeminiSSE(res, events) {
  res.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-cache",
    connection: "keep-alive",
  });
  for (const event of events) {
    res.write(`data: ${JSON.stringify(event)}\n\n`);
  }
  res.end();
}

function writeJSON(res, status, body) {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(body));
}

function redactHeaders(headers) {
  const out = {};
  for (const [key, value] of Object.entries(headers)) {
    out[key] = sensitiveKey(key) ? redacted : value;
  }
  return out;
}

function redactTextBody(text) {
  const parsed = parseJSON(text);
  if (parsed === undefined) return text.length > 4096 ? `${text.slice(0, 4096)}...` : text;
  return JSON.stringify(redactValue(parsed));
}

function redactValue(value, key = "") {
  if (Array.isArray(value)) return value.map((item) => redactValue(item, key));
  if (value && typeof value === "object") {
    const out = {};
    for (const [key, child] of Object.entries(value)) {
      out[key] = sensitiveKey(key) ? redacted : redactValue(child, key);
    }
    return out;
  }
  if (typeof value === "string") return redactStringValue(value, key);
  return value;
}

function redactStringValue(value, key) {
  if (longTextKey(key) && value.length > 240) return `[REDACTED_TEXT:${value.length}]`;
  if (value.length > 4096) return `${value.slice(0, 512)}...[TRUNCATED:${value.length}]`;
  return value;
}

function longTextKey(key) {
  return /^(text|content|prompt|instructions|system_prompt)$/i.test(key);
}

function sensitiveKey(key) {
  return /^(authorization|proxy-authorization|cookie|set-cookie|x-api-key|api-key|apikey)$/i.test(key) ||
    /(^|_)(access_token|refresh_token|id_token|api_key|client_secret|token)(_|$)/i.test(key) ||
    /^x-claude-code-ide-authorization$/i.test(key);
}

function redactCommand(command) {
  return command.map((part) => {
    if (/token|secret|key/i.test(part)) return redacted;
    return part;
  });
}

async function writeCapture(filePath, payload) {
  const stable = {
    generatedAt: new Date().toISOString(),
    ...payload,
  };
  await fs.writeFile(filePath, `${JSON.stringify(stable, null, 2)}\n`);
}

async function writeFailure(filePath, name, error, server) {
  await writeCapture(filePath, {
    name,
    ok: false,
    error: error.message,
    stdout: trimOutput(error.stdout || ""),
    stderr: trimOutput(error.stderr || ""),
    recorder: server.snapshot(),
  });
}

function trimOutput(value) {
  const text = String(value || "").trim();
  if (text.length <= 12000) return text;
  return `${text.slice(0, 5000)}\n...[truncated ${text.length - 10000} chars]...\n${text.slice(-5000)}`;
}

async function exists(filePath) {
  try {
    await fs.access(filePath);
    return true;
  } catch {
    return false;
  }
}

async function resolveCommand(command) {
  if (command.includes(path.sep) && await exists(command)) return command;
  try {
    const { stdout } = await execFileAsync("bash", ["-lc", `command -v ${shellQuote(command)}`], {
      cwd: repoRoot,
      timeout: 5000,
      maxBuffer: 1024 * 1024,
    });
    return stdout.trim().split(/\r?\n/)[0] || "";
  } catch {
    return "";
  }
}

function shellQuote(value) {
  return `'${String(value).replaceAll("'", "'\\''")}'`;
}
