#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs/promises";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";

const repoRoot = path.resolve(new URL("..", import.meta.url).pathname);
const captureRoot = path.join(repoRoot, "infra", "fixtures", "oauth-official");
const codexRoot = process.env.CODEX_SOURCE_DIR || "/home/elucid/projects/web/codex/codex-rs";
const claudeRoot = process.env.CLAUDE_CODE_SOURCE_DIR || "/home/elucid/projects/claude-code-source-code";
const timeoutMs = Number(process.env.TLS_CAPTURE_TIMEOUT_MS || 120000);
const captureHost = process.env.TLS_CAPTURE_HOST || "localhost";
const target = (process.argv[2] || "codex").toLowerCase();
const execFileAsync = promisify(execFile);

if (!["codex", "claude"].includes(target)) {
  console.error("usage: node infra/tls-fingerprint-capture.mjs [codex|claude]");
  process.exit(2);
}

await fs.mkdir(captureRoot, { recursive: true });

const result = target === "codex" ? await captureCodexTLS() : await captureClaudeTLS();
console.log(`${result.name} ${result.ok ? "ok" : "failed"}: ${result.outputPath}`);
if (!result.ok && result.error) console.log(`  ${result.error}`);
process.exitCode = result.ok ? 0 : 1;

async function captureCodexTLS() {
  const name = "codex-tls";
  const outputPath = path.join(captureRoot, "codex-tls.json");
  const listener = await startClientHelloListener();
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "elucid-codex-tls-capture-"));
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
    `openai_base_url="${listener.baseUrl}/v1"`,
    "-c",
    "model=\"gpt-5.1-codex-max\"",
    "-c",
    "model_reasoning_effort=\"low\"",
    "Say ok.",
  ];

  try {
    const [run, hello] = await Promise.all([
      runCommand(file, args, {
        cwd: codexRoot,
        env: {
          ...process.env,
          CODEX_HOME: codexHome,
          RUST_BACKTRACE: process.env.RUST_BACKTRACE || "0",
          OPENAI_API_KEY: "",
        },
        timeout: timeoutMs,
      }),
      listener.capture,
    ]);

    await writeJSON(outputPath, {
      name,
      ok: true,
      command: redactCommand([file, ...args]),
      stdout: trimOutput(run.stdout),
      stderr: trimOutput(run.stderr),
      exitCode: run.exitCode,
      signal: run.signal,
      capture: hello,
    });
    return { name, ok: true, outputPath };
  } catch (error) {
    await writeJSON(outputPath, {
      name,
      ok: false,
      error: String(error?.message || error),
      capture: listener.snapshot(),
    });
    return { name, ok: false, outputPath, error: String(error?.message || error) };
  } finally {
    await listener.close();
    await fs.rm(tmp, { recursive: true, force: true });
  }
}

async function captureClaudeTLS() {
  const name = "claude-tls";
  const outputPath = path.join(captureRoot, "claude-tls.json");
  const listener = await startClientHelloListener();
  const tmp = await fs.mkdtemp(path.join(os.tmpdir(), "elucid-claude-tls-capture-"));
  const configDir = path.join(tmp, "config");
  await fs.mkdir(configDir, { recursive: true });

  const command = await claudeCommand();
  const args = [
    ...command.argsPrefix,
    "-p",
    "--output-format",
    "json",
    "--no-session-persistence",
    "Say ok.",
  ];

  try {
    const [run, hello] = await Promise.all([
      runCommand(command.file, args, {
        cwd: command.cwd,
        env: {
          ...process.env,
          ANTHROPIC_BASE_URL: listener.baseUrl,
          ANTHROPIC_API_KEY: "",
          ANTHROPIC_AUTH_TOKEN: "",
          CLAUDE_CODE_OAUTH_TOKEN: "claude-fake-access-token",
          CLAUDE_CONFIG_DIR: configDir,
          CLAUDE_CODE_ENTRYPOINT: "cli",
          CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
          DISABLE_TELEMETRY: "1",
          USER_TYPE: "external",
        },
        timeout: timeoutMs,
      }),
      listener.capture,
    ]);

    await writeJSON(outputPath, {
      name,
      ok: true,
      command: redactCommand([command.file, ...args]),
      stdout: trimOutput(run.stdout),
      stderr: trimOutput(run.stderr),
      exitCode: run.exitCode,
      signal: run.signal,
      capture: hello,
    });
    return { name, ok: true, outputPath };
  } catch (error) {
    await writeJSON(outputPath, {
      name,
      ok: false,
      error: String(error?.message || error),
      capture: listener.snapshot(),
    });
    return { name, ok: false, outputPath, error: String(error?.message || error) };
  } finally {
    await listener.close();
    await fs.rm(tmp, { recursive: true, force: true });
  }
}

async function startClientHelloListener() {
  let captured;
  let resolveCapture;
  let rejectCapture;
  const capture = new Promise((resolve, reject) => {
    resolveCapture = resolve;
    rejectCapture = reject;
  });

  const server = net.createServer((socket) => {
    let buffer = Buffer.alloc(0);
    socket.on("data", (chunk) => {
      if (captured) return;
      buffer = Buffer.concat([buffer, chunk]);
      const parsed = parseTLSClientHello(buffer);
      if (!parsed) return;
      captured = {
        at: new Date().toISOString(),
        remoteAddress: socket.remoteAddress,
        remotePort: socket.remotePort,
        ...parsed,
      };
      resolveCapture(captured);
      socket.destroy();
    });
    socket.on("error", () => {});
  });

  await new Promise((resolve, reject) => {
    server.on("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });

  const timeout = setTimeout(() => {
    rejectCapture(new Error("Timed out waiting for TLS ClientHello."));
  }, timeoutMs);
  capture.finally(() => clearTimeout(timeout)).catch(() => {});

  const { port } = server.address();
  return {
    baseUrl: `https://${captureHost}:${port}`,
    capture,
    snapshot: () => captured || null,
    close: () => new Promise((resolve) => server.close(resolve)),
  };
}

function parseTLSClientHello(buffer) {
  if (buffer.length < 5) return null;
  const recordType = buffer.readUInt8(0);
  const recordVersion = buffer.readUInt16BE(1);
  const recordLength = buffer.readUInt16BE(3);
  if (recordType !== 22) throw new Error(`Expected TLS handshake record, got type ${recordType}.`);
  if (buffer.length < 5 + recordLength) return null;

  let offset = 5;
  const handshakeType = buffer.readUInt8(offset);
  const handshakeLength = buffer.readUIntBE(offset + 1, 3);
  offset += 4;
  if (handshakeType !== 1) throw new Error(`Expected ClientHello, got handshake type ${handshakeType}.`);
  if (offset + handshakeLength > buffer.length) return null;

  const clientVersion = buffer.readUInt16BE(offset);
  offset += 2 + 32;

  const sessionIDLength = buffer.readUInt8(offset);
  offset += 1 + sessionIDLength;

  const cipherSuiteLength = buffer.readUInt16BE(offset);
  offset += 2;
  const cipherSuites = [];
  for (let end = offset + cipherSuiteLength; offset < end; offset += 2) {
    cipherSuites.push(buffer.readUInt16BE(offset));
  }

  const compressionMethodLength = buffer.readUInt8(offset);
  offset += 1 + compressionMethodLength;

  const extensionLength = offset < buffer.length ? buffer.readUInt16BE(offset) : 0;
  offset += 2;
  const extensions = [];
  let supportedGroups = [];
  let ecPointFormats = [];
  let supportedVersions = [];
  let signatureAlgorithms = [];
  let keyShareGroups = [];
  let alpn = [];
  let sni = "";

  for (let end = offset + extensionLength; offset + 4 <= end; ) {
    const type = buffer.readUInt16BE(offset);
    const length = buffer.readUInt16BE(offset + 2);
    const dataStart = offset + 4;
    const dataEnd = dataStart + length;
    const data = buffer.subarray(dataStart, dataEnd);
    extensions.push(type);

    if (type === 0) sni = parseSNI(data);
    if (type === 10) supportedGroups = parseUint16Vector(data);
    if (type === 11) ecPointFormats = [...data.subarray(1)];
    if (type === 13) signatureAlgorithms = parseUint16Vector(data);
    if (type === 16) alpn = parseALPN(data);
    if (type === 43) supportedVersions = parseSupportedVersions(data);
    if (type === 51) keyShareGroups = parseKeyShareGroups(data);

    offset = dataEnd;
  }

  const ja3Parts = [
    clientVersion,
    cipherSuites.filter((value) => !isGrease(value)).join("-"),
    extensions.filter((value) => !isGrease(value)).join("-"),
    supportedGroups.filter((value) => !isGrease(value)).join("-"),
    ecPointFormats.join("-"),
  ];
  const ja3 = ja3Parts.join(",");

  return {
    recordVersion,
    clientVersion,
    ja3,
    ja3Hash: crypto.createHash("md5").update(ja3).digest("hex"),
    cipherSuites: cipherSuites.map(formatUint16),
    extensions: extensions.map(formatUint16),
    supportedGroups: supportedGroups.map(formatUint16),
    ecPointFormats,
    supportedVersions: supportedVersions.map(formatUint16),
    signatureAlgorithms: signatureAlgorithms.map(formatUint16),
    keyShareGroups: keyShareGroups.map(formatUint16),
    alpn,
    sni,
    rawBytes: buffer.subarray(0, 5 + recordLength).toString("base64"),
  };
}

function parseUint16Vector(data) {
  if (data.length < 2) return [];
  const length = data.readUInt16BE(0);
  const values = [];
  for (let offset = 2, end = Math.min(2 + length, data.length); offset + 1 < end; offset += 2) {
    values.push(data.readUInt16BE(offset));
  }
  return values;
}

function parseSupportedVersions(data) {
  if (data.length < 1) return [];
  const length = data.readUInt8(0);
  const values = [];
  for (let offset = 1, end = Math.min(1 + length, data.length); offset + 1 < end; offset += 2) {
    values.push(data.readUInt16BE(offset));
  }
  return values;
}

function parseKeyShareGroups(data) {
  if (data.length < 2) return [];
  const length = data.readUInt16BE(0);
  const values = [];
  for (let offset = 2, end = Math.min(2 + length, data.length); offset + 3 < end; ) {
    const group = data.readUInt16BE(offset);
    const keyLength = data.readUInt16BE(offset + 2);
    values.push(group);
    offset += 4 + keyLength;
  }
  return values;
}

function parseALPN(data) {
  if (data.length < 2) return [];
  const length = data.readUInt16BE(0);
  const protocols = [];
  for (let offset = 2, end = Math.min(2 + length, data.length); offset < end; ) {
    const itemLength = data.readUInt8(offset);
    offset += 1;
    protocols.push(data.subarray(offset, offset + itemLength).toString("ascii"));
    offset += itemLength;
  }
  return protocols;
}

function parseSNI(data) {
  if (data.length < 5) return "";
  let offset = 2;
  const nameType = data.readUInt8(offset);
  const nameLength = data.readUInt16BE(offset + 1);
  offset += 3;
  if (nameType !== 0 || offset + nameLength > data.length) return "";
  return data.subarray(offset, offset + nameLength).toString("utf8");
}

function isGrease(value) {
  return (value & 0x0f0f) === 0x0a0a && ((value >> 8) & 0xff) === (value & 0xff);
}

function formatUint16(value) {
  return `0x${value.toString(16).padStart(4, "0")}`;
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

async function claudeCommand() {
  if (process.env.CLAUDE_CODE_SOURCE_ONLY !== "1") {
    const installed = await resolveCommand(process.env.CLAUDE_COMMAND || "claude");
    if (installed) return { file: installed, argsPrefix: [], cwd: repoRoot };
  }

  const rootCli = path.join(claudeRoot, "cli.js");
  const distCli = path.join(claudeRoot, "dist", "cli.js");
  if (await exists(rootCli)) return { file: "node", argsPrefix: [rootCli], cwd: claudeRoot };
  if (await exists(distCli)) return { file: "node", argsPrefix: [distCli], cwd: claudeRoot };
  throw new Error(`Claude Code CLI was not found. Set CLAUDE_COMMAND or build ${distCli}.`);
}

function fakeCodexAuth() {
  const now = Math.floor(Date.now() / 1000);
  const payload = Buffer.from(
    JSON.stringify({
      sub: "user-codex-capture",
      "https://api.openai.com/auth": {
        chatgpt_account_id: "acc-capture",
        user_id: "user-codex-capture",
      },
      exp: now + 3600,
    }),
  ).toString("base64url");

  return {
    OPENAI_API_KEY: null,
    tokens: {
      id_token: `header.${payload}.signature`,
      access_token: "codex-fake-access-token",
      refresh_token: "codex-fake-refresh-token",
      account_id: "acc-capture",
    },
    last_refresh: new Date().toISOString(),
  };
}

function runCommand(file, args, options) {
  return new Promise((resolve, reject) => {
    const child = spawn(file, args, {
      cwd: options.cwd,
      env: options.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    let stdout = "";
    let stderr = "";
    const timer = setTimeout(() => child.kill("SIGKILL"), options.timeout);
    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", (error) => {
      clearTimeout(timer);
      reject(error);
    });
    child.on("close", (exitCode, signal) => {
      clearTimeout(timer);
      resolve({ exitCode, signal, stdout, stderr });
    });
  });
}

async function exists(file) {
  try {
    await fs.access(file);
    return true;
  } catch {
    return false;
  }
}

async function writeJSON(file, value) {
  await fs.writeFile(file, `${JSON.stringify(value, null, 2)}\n`);
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

function redactCommand(parts) {
  return parts.map((part) => part.replace(/https:\/\/[^":]+:\d+/g, `https://${captureHost}:<port>`));
}

function trimOutput(value) {
  value = String(value || "");
  if (value.length <= 4000) return value;
  return `${value.slice(0, 4000)}\n...[truncated ${value.length - 4000} chars]`;
}
