#!/usr/bin/env node
import { configFromEnv, runLoop, runOnce } from "./runner.mjs";

const command = process.argv[2] || "run";

if (command === "version") {
  console.log("oauth-wrapper 0.1.0");
  process.exit(0);
}

const config = configFromEnv();

if (command === "once") {
  const processed = await runOnce(config);
  process.exit(processed ? 0 : 2);
}

if (command === "run") {
  await runLoop(config);
} else {
  console.error("Usage: oauth-wrapper [run|once|version]");
  process.exit(64);
}
