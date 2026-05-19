# Provider Compatibility

This file is the support-status source for v1 provider behavior.

| Provider family | Status | Runtime path | Notes |
| --- | --- | --- | --- |
| OpenAI | production-ready | OpenAI-compatible adapter | Supports chat, responses, embeddings, SSE streaming, and realtime WebSocket relay when upstream supports the same paths. |
| OpenAI-compatible | production-ready | OpenAI-compatible adapter | Use for providers exposing `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, and compatible auth. |
| Anthropic | production-ready | Anthropic adapter | Supports `/v1/messages` style relay with `x-api-key`, `anthropic-version`, and stream usage parsing. |
| Claude-compatible | production-ready | Anthropic adapter | Same adapter as Anthropic. Configure base URL to the compatible provider root. |
| GitHub Copilot | production-ready OAuth path, provider-dependent models | GitHub Copilot adapter | `github_device` can exchange a GitHub OAuth token for a Copilot API token and relay `/v1/chat/completions`, `/v1/responses`, `/v1/embeddings`, and `/v1/models` with Copilot/VS Code style headers, including request/interaction correlation headers. |
| Gemini OpenAI-compatible | production-ready | Gemini OpenAI-compatible adapter | Uses OpenAI-compatible request/usage semantics against `https://generativelanguage.googleapis.com/v1beta/openai`; Google OAuth routes use bearer auth plus Gemini CLI-style client headers. |
| Gemini CLI / Code Assist | chat translator ready, native passthrough ready | Gemini CLI adapter | Supports official Google OAuth metadata, OpenAI-compatible `/v1/chat/completions` translation to Cloud Code Assist `v1internal:generateContent` / `v1internal:streamGenerateContent?alt=sse`, OpenAI-style non-stream/SSE response conversion, usage metering, and native `v1internal:*` path/header passthrough. |
| Antigravity / Code Assist | chat translator ready, native passthrough ready | Antigravity adapter | Uses Antigravity Google OAuth client defaults, Code Assist `daily-cloudcode-pa` endpoint metadata, `antigravity/<version> <platform>/<arch>` user agent, `requestType=agent`, `requestId=agent-*`, `sessionId`, project headers, Claude/Gemini model aliases, Antigravity tool-call/tool-result shape, and OpenAI-compatible Code Assist response/SSE conversion. |
| Kiro / Amazon Q Developer | chat translator ready, stream conversion ready | Kiro adapter | Converts OpenAI chat requests to Kiro `generateAssistantResponse`, synthesizes Kiro/AWS CodeWhisperer desktop headers, imports desktop/Builder ID local credentials when configured, supports Kiro Desktop and AWS SSO OIDC refresh-token exchange, maps images/tool uses/tool results into Kiro `conversationState`, and converts AWS event-stream responses back to OpenAI JSON/SSE. |
| Windsurf / Codeium | credential/header passthrough ready | Windsurf Codeium adapter | Reads Codeium/Windsurf local `api_key` config when available and injects Windsurf/Codeium IDE metadata headers. The wrapper also stores Codeium language-server/chat-client metadata. The closed Codeium language-server protocol is not reimplemented; native HTTP compatibility depends on the configured upstream endpoint. |
| Gemini native | experimental | Gemini native adapter | Uses `X-Goog-Api-Key` for API-key routes and bearer auth plus Gemini CLI-style client headers for Google OAuth routes. Native request/response transform is intentionally later. |
| Rerank-compatible | experimental | OpenAI-compatible adapter if provider path matches `/v1/rerank` | Real provider coverage is not complete. |
| Realtime WebSocket | production-ready relay, provider-dependent | Adapter WebSocket builder | Relay, header safety, frame stats, fallback settlement, and known usage event parsing are implemented. |

## OAuth Support Matrix

| Provider family | Auth mode | Wrapper responsibility | Runtime injection |
| --- | --- | --- | --- |
| OpenAI / Codex | `openai_cli`, `codex_cli`, `oauth` | External official CLI command strategy plus generic refresh/revoke from provider-client metadata | `Authorization: Bearer <access_token>` |
| Claude Code / Anthropic | `claude_cli`, `oauth` | External official CLI command strategy plus generic refresh/revoke from provider-client metadata | `X-API-Key: <access_token>` |
| Google / Gemini | `google_pkce`, `oauth` | Installed-app OAuth / PKCE using Gemini CLI client defaults when provider metadata selects Gemini; generic refresh/revoke still support metadata overrides | OpenAI-compatible bearer, Gemini native bearer/key header, or Code Assist passthrough headers by provider type |
| GitHub / GitHub Copilot | `github_device` | Device flow plus optional Copilot token exchange when provider/client metadata selects `github_copilot` | `Authorization: Bearer <copilot_token>` with Copilot/VS Code style headers for `github_copilot`; OpenAI-compatible bearer for generic GitHub-compatible upstreams |
| Google / Antigravity | `google_pkce`, `oauth` | Installed-app OAuth / PKCE using Antigravity OAuth client defaults and scopes, then optional Code Assist project discovery with `metadata.ideType=ANTIGRAVITY` | `Authorization: Bearer <access_token>` plus Antigravity Code Assist headers |
| Kiro | `kiro`, `oauth` | Imports configured JSON/SQLite Kiro credentials, refreshes Kiro Desktop tokens through `https://prod.<region>.auth.desktop.kiro.dev/refreshToken`, and refreshes Builder ID/AWS SSO OIDC tokens through `https://oidc.<region>.amazonaws.com/token` when client registration metadata exists | `Authorization: Bearer <access_token>` plus AWS SDK/Kiro desktop headers |
| Windsurf / Codeium | `windsurf_cli`, `codeium_cli` | Reads configured Codeium/Windsurf config files and normalizes `api_key` as the upstream token | `Authorization: Bearer <api_key>` plus Codeium IDE metadata headers |
| Local smoke | `mock` | Deterministic wrapper completion for local E2E and CI | OpenAI-compatible bearer |
| Static upstream key | `api_key` | None | Provider adapter injects the stored access token as the upstream key |

All supported modes are enabled by default at the gateway data model level. Emergency disable should happen by disabling provider clients, account auth states, or wrapper workers.

`apps/oauth-wrapper` is the out-of-process worker. It supports:

- `mock` for local and CI E2E.
- `codex_cli` reads `CODEX_AUTH_FILE`, `provider_clients.metadata_json.auth_file`, or `$CODEX_HOME/auth.json` after `codex login` and converts the official Codex CLI credentials into the normalized token bundle.
- Generic OAuth refresh/revoke using `provider_clients.metadata_json.token_url` and `revoke_url`.
- Generic device flow using `device_authorization_url`, `token_url`, `client_id`, and `scopes`.
- GitHub Copilot mode when `provider_type`, `wrapper_strategy`, `strategy`, or `token_target` is `github_copilot`; the wrapper stores the Copilot API token as `access_token`, the original GitHub OAuth token as `refresh_token` for later Copilot token refresh, and VS Code/Copilot Chat client metadata such as client version, editor version, API version, account type, intent, interaction type, and Copilot base URL.
- Generic PKCE using `auth_url`, `token_url`, `client_id`, and localhost callback metadata.
- Google Gemini PKCE defaults to the official Gemini CLI installed-app OAuth client, `/oauth2callback`, `access_type=offline`, Cloud Code scopes, and stores Gemini CLI/Code Assist metadata for gateway header synthesis.
- Google Antigravity PKCE defaults to the Antigravity installed-app OAuth client, `/oauth-callback`, `access_type=offline`, Cloud Code plus `cclog` and `experimentsandconfigs` scopes, and stores Antigravity Code Assist metadata for gateway header synthesis.
- Kiro mode reads configured credential JSON files or common `kiro-cli` / CodeWhisperer SQLite stores, exchanges Kiro Desktop or Builder ID/AWS SSO OIDC refresh tokens for access tokens, and stores region, API host, client version, agent mode, opt-out, auth type, credential source, client registration metadata, and profile ARN when available.
- Windsurf/Codeium CLI mode reads `api_key` from configured files or common Codeium/Windsurf config paths and stores IDE/extension, API server, language-server, and chat-client query metadata for request/header synthesis.
- External CLI mode through JSON-array command specs such as `login_command`, `refresh_command`, `revoke_command`, `token_bundle_command`, or `token_bundle_file`.

Real provider E2E remains opt-in because it requires actual provider accounts and official client/CLI configuration.

## OAuth Official Capture Regression

Offline replay fixtures live in `infra/fixtures/oauth-official/`. After refreshing a capture, sync the checked-in Go replay testdata and run the gateway conformance tests:

```bash
npm run oauth:capture -- codex
npm run oauth:capture -- gemini
npm run oauth:capture -- github_copilot
npm run oauth:capture -- claude
npm run oauth:sync-fixtures
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23 sh -lc '/usr/local/go/bin/gofmt -w internal/httpserver/provider_adapters.go internal/httpserver/gemini_cli_transform.go internal/httpserver/provider_conformance_test.go && /usr/local/go/bin/go test ./internal/httpserver -run "Test.*OfficialRequestReplayConformance"'
```

`npm run oauth:capture -- all` can refresh all four fixtures. The offline replay compares prepared upstream method, path, query, headers, and JSON body for Codex, Gemini Code Assist, GitHub Copilot, and Claude Code. Sensitive headers are compared as redacted values; volatile transport headers such as host, content length, connection, fetch mode, accept encoding, and WebSocket handshake fields are ignored. Real account E2E remains separate and must be enabled explicitly with provider token environment variables.

## Runtime Route Metadata

Channel, account, and token metadata are read on each route checkout, so metadata changes apply to new requests without restarting the gateway.

`request_headers` supports controlled official-client header alignment:

```json
{
  "request_headers": {
    "pass": ["X-Trace-Id"],
    "set": {
      "X-Client-Feature": "feature-a",
      "X-From-Client": "{client_header:X-Trace-Id}",
      "X-Route-Account": "{route:account_id}"
    },
    "remove": ["OpenAI-Beta"]
  }
}
```

`{request_header:Name}` is accepted as an alias for `{client_header:Name}`. `{route:account_id}`, `{route:channel_id}`, `{route:provider_type}`, `{route:upstream_model}`, and `{route:base_url}` expose the selected route. Unsafe transport and credential headers remain blocked unless the adapter itself owns them.
Even when a route opts into `pass: ["*"]`, forwarded/CDN identity headers such as `X-Forwarded-For`, `X-Real-IP`, `Forwarded`, `Via`, `Cf-Ray`, and `Cf-Connecting-IP` are not passed upstream.

Failover attempts and retryable statuses can be tuned per channel, ability, or account:

```json
{
  "retry": {
    "max_attempts": 3,
    "retryable_statuses": [408, 429, 500, 502, 503, 504],
    "non_retryable_statuses": [400, 401, 403]
  }
}
```

The default is 2 attempts and the runtime clamps configured values to 1-4 attempts. `non_retryable_statuses` wins over `retryable_statuses`; if neither list mentions a status, the default retry rule is 429 or 5xx. This applies to normal HTTP, SSE stream opening, WebSocket opening, and Claude Code sidecar HTTP calls. Retry events emit a structured `upstream retrying` log without request bodies or credentials, and downstream failures are surfaced in the admin `运维` page through recent failed requests and error distribution.

For SSE streams, the relay delays downstream header/body commit until the first valid upstream output is available. Connection, decode, or Responses-SSE framing failures before any downstream byte is written use the same route retry policy; once a byte has been written, the relay does not retry and instead emits the existing terminal stream error event. `reverse_proxy.client_like.stream_keep_alive_seconds` can be set to a positive value to emit SSE comment heartbeats after the stream has been committed; the default `0` keeps this disabled.

OpenAI/Codex `/v1/responses` SSE streams are normalized before forwarding: split events and missing event separators are repaired, invalid JSON data before commit is treated as a bootstrap failure, and empty `response.completed.response.output` arrays can be filled from earlier `response.output_item.done` events.

Circuit breaker behavior can be tuned per channel, ability, or account:

```json
{
  "circuit_breaker": {
    "failure_threshold": 3,
    "open_seconds": 120,
    "status_open_seconds": {
      "429": 600
    }
  }
}
```

The default opens an account circuit after 3 consecutive upstream failures. Open duration defaults to the existing cooldown class: 30 seconds for generic failures, 2 minutes for 5xx, and 10 minutes for 429. When the open window expires, the next checkout becomes a single half-open probe; success closes the circuit and resets the counter, while failure opens it again.

`status_code_mapping` changes only the client-facing HTTP status; retry, circuit breaker, quota, and usage still use the original upstream status.

```json
{
  "status_code_mapping": {
    "529": 429
  }
}
```

`param_override.operations` provides a narrow request JSON/header rewrite layer for route compatibility. It supports `op` or `mode` with `set`, `delete`, `copy`, `move`, `append`, `prepend`, `replace`, `regex_replace`, `set_header`, `delete_header`, `copy_header`, `move_header`, and `pass_headers`; `model` is protected unless `allow_model_override` is true. Header operations run after adapter defaults and route `request_headers`, but blocked transport and credential headers remain protected.

```json
{
  "param_override": {
    "operations": [
      {"op": "set", "path": "stream_options.include_usage", "value": true},
      {"op": "set_header", "path": "X-Trace", "value": "{request_header:X-Trace-Id}"},
      {"op": "pass_headers", "value": ["traceparent", "tracestate"]},
      {"op": "delete", "path": "service_tier"},
      {"op": "move", "from": "max_tokens", "to": "max_completion_tokens"}
    ]
  }
}
```

`response_rewrite` is an opt-in response URL rewrite layer for providers that return absolute upstream URLs in JSON/text bodies or redirect headers. It rewrites `Location` by default and can rewrite additional headers. Body rewrites only run for UTF-8 text-like content types unless `content_types` is configured.

```json
{
  "response_rewrite": {
    "replace": {
      "{route:base_url}": "{request:origin}/upstream",
      "https://files.example.com": "{request:origin}/files"
    },
    "headers": ["Location", "X-Asset-Url"],
    "content_types": ["application/json", "text/*"]
  }
}
```

`system_prompt` is a route-level semantic shortcut for OpenAI chat `messages` and Anthropic messages `system`. Supported modes are `if_absent`, `prepend`, and `replace`.

```json
{
  "system_prompt": {
    "mode": "if_absent",
    "text": "You are a helpful assistant."
  }
}
```

Route profiles can be expressed as tags on channel or account metadata:

```json
{
  "route_tags": ["think", "long-context"]
}
```

Clients can request one of those profiles with `X-Elucid-Relay-Route-Tag`, `X-Elucid-Relay-Route-Profile`, `route_tag`, `route_profile`, or JSON `metadata.route_tags`. Tags are normalized to lowercase, limited to four values, and only `[a-z0-9_.:-]` is accepted. Tagged requests still use the normal model scope, pool/BYO, quota, cooldown, concurrency, priority, and weight checks. Route affinity is skipped for tagged requests so a previous session pin does not override the requested profile.

## Recommended Configurations

OpenAI-compatible:

```json
{
  "provider_type": "openai_compatible",
  "base_url": "https://api.openai.com",
  "abilities": [
    { "model_name": "gpt-4.1-mini", "endpoint": "chat" },
    { "model_name": "gpt-4.1-mini", "endpoint": "responses" },
    { "model_name": "text-embedding-3-small", "endpoint": "embeddings" }
  ]
}
```

Anthropic:

```json
{
  "provider_type": "anthropic",
  "base_url": "https://api.anthropic.com",
  "abilities": [
    { "model_name": "claude-3-5-haiku-latest", "endpoint": "messages" }
  ]
}
```

Gemini OpenAI-compatible:

```json
{
  "provider_type": "gemini_openai_compatible",
  "base_url": "https://generativelanguage.googleapis.com/v1beta/openai",
  "abilities": [
    { "model_name": "gemini-2.0-flash", "endpoint": "chat" },
    { "model_name": "gemini-embedding-exp", "endpoint": "embeddings" }
  ]
}
```

Gemini CLI / Cloud Code Assist:

```json
{
  "provider_type": "gemini_cli",
  "base_url": "https://cloudcode-pa.googleapis.com/v1internal",
  "abilities": [
    { "model_name": "gemini-2.5-pro", "endpoint": "chat" }
  ],
  "provider_client_metadata": {
    "wrapper_strategy": "gemini_cli",
    "auth_mode": "google_pkce"
  }
}
```

OpenAI-compatible `/v1/chat/completions` requests are converted to native Code Assist bodies with `model`, optional `project`, `user_prompt_id`, and nested `request.contents`, `systemInstruction`, `tools`, `toolConfig`, `generationConfig`, `safetySettings`, `labels`, and `session_id`. Streaming chat is sent to `v1internal:streamGenerateContent?alt=sse` and converted back to OpenAI SSE chunks. Code Assist OAuth requests use the Gemini CLI-style `User-Agent` plus bearer auth and intentionally do not synthesize GenAI SDK API-key headers. Native Code Assist paths such as `/v1internal:countTokens`, `/v1internal:loadCodeAssist`, and `/v1internal:onboardUser` remain passthrough.

Antigravity:

```json
{
  "provider_type": "antigravity",
  "base_url": "https://daily-cloudcode-pa.googleapis.com/v1internal",
  "abilities": [
    { "model_name": "claude-sonnet-4-5", "endpoint": "chat" }
  ],
  "provider_client_metadata": {
    "wrapper_strategy": "google_pkce",
    "auth_mode": "google_pkce",
    "token_target": "antigravity",
    "client_version": "1.20.5"
  }
}
```

OpenAI-compatible chat requests are converted to Antigravity's Code Assist wrapper body with `model`, `userAgent`, `requestType`, `requestId`, optional `project`, and nested `request.contents`, `systemInstruction`, `tools`, `toolConfig`, `generationConfig`, `safetySettings`, and `sessionId`. Claude-family Antigravity tools use the observed `parameters` schema field, tool calls/results preserve ids, and image/function-call parts include the thought-signature skip marker used by open-source Antigravity reverse-proxy implementations. Streaming is sent to `v1internal:streamGenerateContent?alt=sse` and converted back to OpenAI SSE chunks.

Kiro:

```json
{
  "provider_type": "kiro",
  "base_url": "https://q.us-east-1.amazonaws.com",
  "abilities": [
    { "model_name": "claude-sonnet-4.5", "endpoint": "chat" }
  ],
  "provider_client_metadata": {
    "wrapper_strategy": "kiro",
    "auth_mode": "oauth",
    "region": "us-east-1"
  }
}
```

OpenAI-compatible chat requests are converted to Kiro `conversationState` payloads and sent to `/generateAssistantResponse`. User images are placed directly under `userInputMessage.images`; assistant tool calls map to `assistantResponseMessage.toolUses`; tool messages map to `userInputMessageContext.toolResults`; synthetic empty turns are inserted when needed to keep user/assistant alternation. Kiro AWS event-stream responses are parsed into OpenAI non-stream JSON or OpenAI SSE. `credentials_file`, `token_file`, or `sqlite_file` can be supplied for local credential import. JSON credentials support Kiro Desktop fields such as `refreshToken`, `accessToken`, `profileArn`, `region` and Builder ID/AWS SSO OIDC fields such as `clientId`, `clientSecret`, `refreshToken`, `idcRegion`/`region`; SQLite import checks common `kirocli:*` and legacy `codewhisperer:*` token/registration keys. `profile_arn`, `client_version`, `fingerprint`, `agent_mode`, `codewhisperer_optout`, `x_amz_user_agent`, and `user_agent` can be supplied in provider, channel, account, or token metadata.

Windsurf / Codeium:

```json
{
  "provider_type": "windsurf_codeium",
  "base_url": "https://server.codeium.com",
  "abilities": [
    { "model_name": "codeium-chat", "endpoint": "chat" }
  ],
  "provider_client_metadata": {
    "wrapper_strategy": "windsurf_cli",
    "auth_mode": "windsurf_cli"
  }
}
```

This adapter preserves the configured upstream path/body and applies Codeium/Windsurf token and client metadata headers. The wrapper imports `api_key` plus API server, language-server, and chat-client metadata from Windsurf/Codeium-style config. Use it for endpoints that already accept the configured request format.

GitHub Copilot:

```json
{
  "provider_type": "github_copilot",
  "base_url": "https://api.githubcopilot.com",
  "abilities": [
    { "model_name": "gpt-5.1", "endpoint": "chat" },
    { "model_name": "gpt-5.1", "endpoint": "responses" },
    { "model_name": "copilot-text-embedding-ada-002", "endpoint": "embeddings" }
  ],
  "provider_client_metadata": {
    "wrapper_strategy": "github_copilot",
    "client_id": "Iv1.b507a08c87ecfe98",
    "scopes": ["read:user"]
  }
}
```

GitHub Copilot routes synthesize `Authorization`, `Copilot-Integration-Id`, `Editor-Version`, `Editor-Plugin-Version`, `User-Agent`, `OpenAI-Intent`, `X-GitHub-Api-Version`, `X-Request-Id`, `X-VSCode-User-Agent-Library-Version`, `X-Interaction-Id`, `X-Interaction-Type`, `X-Agent-Task-Id`, `X-Initiator`, and `Copilot-Vision-Request` when relevant. Defaults track the checked-in VS Code Copilot Chat source shape and can be overridden with provider client, channel, account, or token metadata. `account_type` can be `individual`, `business`, or `enterprise` when deriving the Copilot base URL during token exchange.

## Real Provider E2E

Real tests are opt-in and skip unless the provider key exists.

```bash
npm run e2e:providers
E2E_OPENAI_API_KEY=sk-... node infra/provider-e2e/runner.mjs --provider openai
E2E_ANTHROPIC_API_KEY=sk-ant-... node infra/provider-e2e/runner.mjs --provider anthropic
E2E_GEMINI_API_KEY=... node infra/provider-e2e/runner.mjs --provider gemini-openai-compatible
E2E_GEMINI_CODEASSIST=1 E2E_GEMINI_CODEASSIST_ACCESS_TOKEN=... node infra/gemini-codeassist-e2e.mjs
E2E_GITHUB_COPILOT=1 E2E_GITHUB_COPILOT_ACCESS_TOKEN=... node infra/github-copilot-e2e.mjs
```

`npm run e2e:providers` is the unified local gate. It runs the OpenAI/Anthropic/Gemini OpenAI-compatible runner plus Codex OpenAI, Codex ChatGPT Responses, Gemini Code Assist, GitHub Copilot, and Claude Code Remote checks. Each individual runner keeps its own opt-in skip behavior.

Default environment:

- `E2E_BASE_URL=http://localhost:18080`
- `E2E_ADMIN_EMAIL=owner@example.com`
- `E2E_ADMIN_PASSWORD=change-me-please-32-chars`
- `E2E_OPENAI_BASE_URL=https://api.openai.com`
- `E2E_ANTHROPIC_BASE_URL=https://api.anthropic.com`
- `E2E_GEMINI_BASE_URL=https://generativelanguage.googleapis.com/v1beta/openai`
- `E2E_GEMINI_CODEASSIST_BASE_URL=https://cloudcode-pa.googleapis.com/v1internal`

`infra/gemini-codeassist-e2e.mjs` intentionally does not launch Google login. It accepts `E2E_GEMINI_CODEASSIST_ACCESS_TOKEN`, `GOOGLE_CLOUD_ACCESS_TOKEN`, `GEMINI_OAUTH_CREDS_FILE`, `GOOGLE_APPLICATION_CREDENTIALS` when it is an authorized-user credential, or `~/.gemini/oauth_creds.json`, calls `v1internal:loadCodeAssist` to discover `cloudaicompanionProject`, and sends one non-stream plus one stream request through BYO routing. If proxy environment variables are present, the runner restarts itself with `NODE_USE_ENV_PROXY=1` so Node `fetch` uses the configured proxy.

Antigravity, Kiro, and Windsurf real-provider E2E are not run by default. Antigravity can reuse an authorized Google access token with Antigravity scopes. If Google returns `cloud_code_private_api_disabled`, the selected project has not enabled `cloudcode-pa.googleapis.com`. Kiro requires a real Kiro Desktop or Builder ID/AWS SSO OIDC refresh/access token plus profile ARN. Windsurf/Codeium requires a valid Codeium API key and an upstream endpoint that accepts the selected request format.
