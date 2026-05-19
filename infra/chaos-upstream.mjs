#!/usr/bin/env node
import crypto from "node:crypto";
import http from "node:http";

const mode = process.env.CHAOS_MODE || "ok";
const server = http.createServer((req, res) => {
  const chunks = [];
  req.on("data", (chunk) => chunks.push(chunk));
  req.on("end", () => {
    const body = chunks.length ? Buffer.concat(chunks).toString("utf8") : "";
    const payload = body ? JSON.parse(body) : {};

    if (mode === "ok") {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify(okResponse(req.url, payload)));
      return;
    }
    if (mode === "429") {
      res.writeHead(429, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: { message: "rate limited" } }));
      return;
    }
    if (mode === "500") {
      res.writeHead(500, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: { message: "upstream error" } }));
      return;
    }
    if (mode === "slow") {
      setTimeout(() => {
        res.setHeader("content-type", "application/json");
        res.end(JSON.stringify(okResponse(req.url, payload)));
      }, 1500);
      return;
    }
    if (mode === "sse-half") {
      res.writeHead(200, { "content-type": "text/event-stream" });
      res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: "part" } }] })}\n\n`);
      return;
    }
    if (mode === "sse-no-usage") {
      res.writeHead(200, { "content-type": "text/event-stream" });
      res.write(`data: ${JSON.stringify({ choices: [{ delta: { content: "ok" } }] })}\n\n`);
      res.end("data: [DONE]\n\n");
      return;
    }
    if (mode === "big") {
      res.setHeader("content-type", "application/json");
      res.end(JSON.stringify({ data: [{ blob: crypto.randomBytes(128).toString("hex") }] }));
      return;
    }
    res.writeHead(500, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { message: `unknown chaos mode ${mode}` } }));
  });
});

function okResponse(path, payload) {
  if (path === "/v1/embeddings") {
    return {
      object: "list",
      data: [{ object: "embedding", embedding: [0.1, 0.2], index: 0 }],
      model: payload.model || "chaos-model",
      usage: { prompt_tokens: 5, total_tokens: 5 },
    };
  }
  return {
    id: "chatcmpl_chaos",
    object: "chat.completion",
    model: payload.model || "chaos-model",
    choices: [{ index: 0, message: { role: "assistant", content: "ok" }, finish_reason: "stop" }],
    usage: { prompt_tokens: 5, completion_tokens: 2, total_tokens: 7 },
  };
}

server.listen(8091, "0.0.0.0");
