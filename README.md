# Elucid Relay

面向个人用户的自托管 AI API 中转站。

## 快速开始

环境要求：

- Docker 和 Docker Compose。

启动本地服务：

```bash
docker compose up --build
```

检查网关 API：

```bash
curl -fsS http://localhost:18080/healthz
```

预期返回：

```text
ok
```

手动执行数据库迁移：

```bash
docker compose run --rm gateway-api migrate up
```

常用命令：

```bash
make build
make up
make healthz
make migrate-up
make smoke-api
make down
```

本地入口：

- Web 控制台：<http://localhost:18081>
- 开发模式 Web 控制台：<http://localhost:5173>
- 网关 API：<http://localhost:18080>

Web 控制台是唯一的前端入口。个人用户页面、管理员页面和公开信息页面都在 `apps/portal` 中，没有单独的 Admin 或 Public 前端应用。首次使用空数据库时，打开控制台并在初始化页面创建第一个平台管理员；系统中存在 `platform_owner` 后，初始化接口会关闭。

## 生产部署

生产环境从 `.env.production.example` 和生产 Compose 覆盖文件开始：

```bash
cp .env.production.example .env.production
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml run --rm gateway-api migrate up
docker compose --env-file .env.production -f docker-compose.yml -f docker-compose.prod.yml up -d --build
```

启动后检查两个探针：

```bash
curl -fsS http://localhost:18080/healthz
curl -fsS http://localhost:18080/readyz
```

对外开放流量前检查：

- 设置足够长且随机的 `VAULT_KEY`；轮换该值需要重新加密上游账号凭据。
- HTTPS 后方部署时设置 `COOKIE_SECURE=true`，并收窄 `CORS_ALLOWED_ORIGINS`。
- 设置 `PORTAL_BASE_URL`、`PUBLIC_GATEWAY_API_URL`、`SMTP_HOST`、`SMTP_FROM` 以及 SMTP 鉴权和 TLS 配置，用于密码重置和邮箱验证。
- 在管理员控制台 `商业化闭环 -> 支付设置` 配置支付提供商。Stripe 仍可使用 `STRIPE_SECRET_KEY`、`STRIPE_WEBHOOK_SECRET`、`BILLING_SUCCESS_URL` 和 `BILLING_CANCEL_URL`；支付宝、微信支付和 EasyPay 凭据会加密存储在数据库中。
- 如果迁移由部署系统管理，保持 `MIGRATE_ON_START=false`。
- 将 Postgres 和 Redis 放在私有网络中，并配置持久化和备份。
- 在迁移前和日常运维中运行 `infra/backup-postgres.sh`。
- 使用管理员控制台 `运维` 页面查看内置 readiness、流量、延迟、账号池和事件监控。

## OAuth 客户端配置

仓库不内置 Google OAuth `client_id` 或 `client_secret`，避免触发 GitHub secret scanning，也避免把真实凭据写入代码。

Gemini Code Assist 和 Antigravity 的 OAuth 刷新需要通过 provider client metadata 或环境变量显式配置：

```text
GEMINI_OAUTH_CLIENT_ID
GEMINI_OAUTH_CLIENT_SECRET
ANTIGRAVITY_OAUTH_CLIENT_ID
ANTIGRAVITY_OAUTH_CLIENT_SECRET
```

不要提交真实 OAuth client secret、API key、refresh token、access token 或 `.env` 文件。真实运行配置放在本地 `.env`、部署平台 secret manager，或管理员控制台的加密配置中。

## 已实现的 v1 能力

后端：

- Portal 认证：注册、登录、登出、当前用户信息。
- Admin 认证：登录、登出、当前管理员信息。
- 美元钱包余额、流水、管理员手动增减和调整。
- 兑换码批量生成和个人领取。
- 个人 API key 创建、一次性复制 secret、哈希存储、吊销、禁用和更新。
- 模型目录和端点能力。
- Provider、client、channel、account、proxy 管理。
- 账号运行态、并发 checkout、冷却、额度窗口过滤和路由解释。
- 使用 `VAULT_KEY` 加密存储上游账号 API key。
- 基于 Redis 的认证、敏感 Portal 操作和北向调用限流。
- 北向 `/v1/models`，以及 chat、responses、messages、embeddings、images、audio、realtime、rerank 代理处理。
- 用量记录、钱包 reserve/release/debit 流水、重复 request id 保护和审计日志。
- 用户组策略、模型 allow/deny、价格倍率、RPM 和月度美元限额。
- Stripe、支付宝、微信支付和 EasyPay 的统一支付提供商模型；订单、订阅、退款、分佣返利和财务汇总。
- 风控规则和风控事件记录。
- Channel 模型发现、同步任务和公开 channel 状态。
- 账号池导入导出、额度刷新、健康检查、质量评分、唤醒任务、平台策略和批量操作。

前端：

- 单一 Portal 应用，覆盖个人仪表盘、钱包、账单、API key、用量、模型、公开信息和管理员操作。

## 验证

常用验证命令：

```bash
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23-alpine go test ./...
docker run --rm -v "$PWD/services/gateway-api:/src" -w /src golang:1.23-alpine go vet ./...
npm run build
npm run smoke:browser
bash infra/smoke-api.sh
npm run e2e:providers
npm run e2e:stripe
```

`infra/smoke-api.sh` 会启动 Compose 的 `mock-upstream` profile，并验证 provider/client/channel/account 设置、模型同步、公开 channel 状态、用户组计费策略、Stripe-like webhook 事件、退款、订阅、分佣结算、风控事件、真实 `/v1/chat/completions` relay 调用、重复 `X-Request-Id` 拒绝、用量记录和钱包结算。

`npm run e2e:providers` 和 `npm run e2e:stripe` 是可选门禁。Provider 检查在没有提供商凭据时会跳过。Stripe 检查需要设置 `E2E_STRIPE_TESTMODE=1`、Stripe 测试 secret、webhook secret，并启动带匹配 Stripe 环境变量的 gateway。

## 产品定位

Elucid Relay 是面向个人用户的自托管 AI API 中转平台。它提供公开注册、美元钱包余额、兑换码和支付提供商充值、订阅、个人 API key、用量计费、用户组策略、风控，以及用于模型、channel 和账号池管理的运维后台。

本项目独立于 `elucid-gateway`。旧企业版概念不属于当前产品面：

- 不包含企业申请流程。
- 不包含项目、团队、成员模型。
- 不包含企业 SSO。
- 不包含 SaaS 营销站。

## 参考项目

- `sub2api`：用户自助、额度分发、兑换码和个人 API key 流程。
- `new-api`：AI 中转站产品形态、模型和 channel 定价、用户侧用量与 token 管理。
- `CLIProxyAPI`：CLI/OAuth 上游账号池、多账号调度、额度窗口、OpenAI/Gemini/Claude/Codex 兼容路由。

实现时只复用思路，不复制代码。

## 初始架构

```text
elucid-relay/
├── apps/
│   └── portal/          # 单一 Web 控制台，覆盖用户、管理员和公开信息
├── services/
│   └── gateway-api/     # Go 网关、Portal/Admin API、北向 /v1 API
├── packages/
│   └── contracts/       # 共享 API 类型
└── infra/               # Docker、部署和脚本
```

## v1 范围

- 公开用户注册和登录。
- 个人美元钱包。
- 管理员手动充值、兑换码充值、Stripe checkout 充值、支付宝/微信/EasyPay 充值，以及订阅钱包额度发放。
- 个人 API key 创建、列表、禁用和吊销。
- 完整北向 API 面：`/v1/models`、chat、responses、messages、embeddings、images、audio、realtime、rerank。
- 用量记录和钱包流水结算。
- 管理员管理用户、余额、兑换码、账单、退款、分佣、用户组、风控、模型目录、定价、channel、上游账号、proxy、模型同步，以及账号池健康和自动化。

## v1 非目标

- 企业账单、税务、发票、优惠券或 Stripe Customer Portal。
- 企业项目、团队、SSO 或多组织租户。
- 公开 marketplace 或提供商转售入驻。
- 从 `sub2api`、`new-api` 或 `CLIProxyAPI` 导入运行时代码。

## 本地文档

设计文档默认放在本地 `docs/`，该目录已加入 `.gitignore`，不随远程仓库发布。
