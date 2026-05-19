#!/usr/bin/env node
const baseURL = (process.env.BASE_URL || "http://localhost:18080").replace(/\/$/, "");
const apiKey = process.env.API_KEY_SECRET;
const model = process.env.MODEL_NAME;
const concurrency = Number(process.env.CONCURRENCY || "10");
const requests = Number(process.env.REQUESTS || "50");

if (!apiKey || !model) {
  console.error("API_KEY_SECRET and MODEL_NAME are required");
  process.exit(2);
}

let next = 0;
let ok = 0;
let failed = 0;
const startedAt = Date.now();

await Promise.all(Array.from({ length: concurrency }, async () => {
  for (;;) {
    const index = next++;
    if (index >= requests) return;
    try {
      const response = await fetch(`${baseURL}/v1/chat/completions`, {
        method: "POST",
        headers: {
          authorization: `Bearer ${apiKey}`,
          "content-type": "application/json",
          "x-request-id": `load-${Date.now()}-${index}`,
        },
        body: JSON.stringify({
          model,
          messages: [{ role: "user", content: "ping" }],
        }),
      });
      if (response.ok) ok++;
      else failed++;
      await response.text();
    } catch {
      failed++;
    }
  }
}));

const elapsed = (Date.now() - startedAt) / 1000;
console.log(JSON.stringify({
  requests,
  concurrency,
  ok,
  failed,
  seconds: elapsed,
  rps: requests / elapsed,
}, null, 2));

if (failed > 0) process.exit(1);
