# Reference Mapping

## sub2api

Use as reference for:

- Personal user self-service.
- API key distribution.
- Wallet/redeem-code top-up.
- Quota and billing-oriented product language.
- Admin views for users, accounts, monitoring, and payment-adjacent flows.

Do not copy:

- Vue implementation details.
- Payment providers in v1.
- Sponsor/relay marketplace assumptions.

## new-api

Use as reference for:

- Relay station product model.
- Model/channel/pricing administration.
- User token/key management.
- Usage analytics and model visibility.
- Multi-provider AI gateway conventions.

Do not copy:

- One API compatibility constraints unless they simplify migration.
- Full online payment scope in v1.
- React/Semi UI implementation details by default.

## CLIProxyAPI

Use as reference for:

- OAuth-backed upstream accounts for Claude Code, Codex, Gemini, and similar CLI products.
- Multi-account scheduling.
- Quota window and cooldown behavior.
- Provider-specific protocol paths and compatibility notes.
- Localhost-sensitive management security assumptions.

Do not copy:

- CLI-first UX as the main product surface.
- Config-file-only operations model.
- Go SDK embedding unless a later phase needs it.

## Sub-Router

Use as reference for:

- Conversation/session-to-account affinity so upstream cache and context behavior stay stable.
- Safe reverse-proxy defaults with explicit internal header stripping.
- Streaming/WebSocket proxy discipline: only retry before bytes are written, and avoid leaking router metadata upstream.
- Quota-headroom-aware scheduling ideas for future account pool tuning.

Do not copy:

- Local coding-agent account manager UX.
- Team/self-reported teammate observability semantics.
- Raw transcript persistence by default, because it stores sensitive request and response bodies.

## Elucid Gateway

Reuse as reference for:

- Runtime provider executor patterns.
- Northbound endpoint families.
- Model catalog and endpoint-aware channel abilities.
- Metering and pricing snapshots.
- Route explain and account pool monitoring.

Avoid carrying over:

- Enterprise-only projects/applications/team membership.
- Organization SSO.
- Enterprise registration review.
