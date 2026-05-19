#!/usr/bin/env bash
set -euo pipefail

node infra/provider-e2e/runner.mjs --provider all
node infra/codex-openai-e2e.mjs
node infra/codex-chatgpt-responses-e2e.mjs
node infra/gemini-codeassist-e2e.mjs
node infra/github-copilot-e2e.mjs
node infra/claude-code-remote-e2e.mjs
