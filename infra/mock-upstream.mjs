import crypto from "node:crypto";
import http from "node:http";

const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (chunk) => chunks.push(chunk));
  req.on("end", () => {
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString("utf8")) : {};

    if (req.url === "/v1/models" && req.method === "GET") {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ object: "list", data: [{ id: "mock-model", object: "model", created: 0, owned_by: "mock" }] }));
      return;
    }

    if (!hasUpstreamAuth(req, res)) {
      return;
    }

    if (body.stream === true) {
      res.writeHead(200, {
        "content-type": "text/event-stream",
        "cache-control": "no-cache",
        "x-mock-upstream-auth": "bearer",
      });
      res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: "ok" } }] })}\n\n`);
      res.write(`data: ${JSON.stringify({ usage: { prompt_tokens: 11, completion_tokens: 7 } })}\n\n`);
      res.end("data: [DONE]\n\n");
      return;
    }

    if (req.url === "/v1/embeddings") {
      res.setHeader("content-type", "application/json");
      res.setHeader("x-mock-upstream-auth", "bearer");
      res.end(JSON.stringify({
        object: "list",
        data: [{ object: "embedding", embedding: [0.1, 0.2, 0.3], index: 0 }],
        model: body.model || "mock-model",
        usage: { prompt_tokens: 8, total_tokens: 8 },
      }));
      return;
    }

    res.setHeader("content-type", "application/json");
    res.setHeader("x-mock-upstream-auth", "bearer");
    res.end(JSON.stringify({
      id: "chatcmpl_mock",
      object: "chat.completion",
      model: body.model || "mock-model",
      choices: [{ index: 0, message: { role: "assistant", content: "ok" }, finish_reason: "stop" }],
      usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
    }));
  });
});

function hasUpstreamAuth(req, res) {
  const authorization = String(req.headers.authorization || "");
  if (!authorization.startsWith("Bearer ")) {
    res.writeHead(401, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { code: "missing_upstream_auth" } }));
    return false;
  }
  if (authorization.startsWith("Bearer sk-relay_")) {
    res.writeHead(401, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { code: "downstream_key_leaked" } }));
    return false;
  }
  return true;
}

function websocketFrame(text) {
  const payload = Buffer.from(text, "utf8");
  if (payload.length > 125) {
    throw new Error("mock websocket payload is too large");
  }
  return Buffer.concat([Buffer.from([0x81, payload.length]), payload]);
}

server.on("upgrade", (req, socket) => {
  if (!req.url.startsWith("/v1/realtime")) {
    socket.destroy();
    return;
  }
  if (!req.headers.authorization?.startsWith("Bearer ") || req.headers.authorization.startsWith("Bearer sk-relay_")) {
    socket.write("HTTP/1.1 401 Unauthorized\r\nConnection: close\r\n\r\n");
    socket.destroy();
    return;
  }

  const key = req.headers["sec-websocket-key"];
  if (!key) {
    socket.destroy();
    return;
  }
  const accept = crypto
    .createHash("sha1")
    .update(`${key}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
    .digest("base64");

  socket.write(
    [
      "HTTP/1.1 101 Switching Protocols",
      "Upgrade: websocket",
      "Connection: Upgrade",
      `Sec-WebSocket-Accept: ${accept}`,
      "X-Mock-Upstream: realtime",
      "\r\n",
    ].join("\r\n"),
  );
  socket.write(websocketFrame("realtime-ok"));
  setTimeout(() => socket.end(), 50);
});

server.listen(8090, "0.0.0.0");
