#!/usr/bin/env node
import fs from "node:fs/promises";
import path from "node:path";

const repoRoot = path.resolve(new URL("..", import.meta.url).pathname);
const fixtureRoot = path.join(repoRoot, "infra", "fixtures", "oauth-official");
const testdataRoot = path.join(repoRoot, "services", "gateway-api", "internal", "httpserver", "testdata", "oauth-official");
const replayFixtures = ["codex.json", "gemini.json", "github-copilot.json", "claude.json"];

await fs.mkdir(testdataRoot, { recursive: true });

for (const fixture of replayFixtures) {
  const source = path.join(fixtureRoot, fixture);
  const destination = path.join(testdataRoot, fixture);
  await fs.copyFile(source, destination);
  console.log(`${path.relative(repoRoot, source)} -> ${path.relative(repoRoot, destination)}`);
}
