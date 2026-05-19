import { useMemo } from "react";
import { RELAY_API_BASE } from "../../api";
import { DataTable } from "../../components/DataTable";

export function DocsView() {
  const examples = useMemo(() => ({
    models: `curl ${RELAY_API_BASE}/v1/models \\\n  -H "Authorization: Bearer sk-relay_..."`,
    chat: `curl ${RELAY_API_BASE}/v1/chat/completions \\\n  -H "Authorization: Bearer sk-relay_..." \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}]}'`,
    responses: `curl ${RELAY_API_BASE}/v1/responses \\\n  -H "Authorization: Bearer sk-relay_..." \\\n  -H "Content-Type: application/json" \\\n  -d '{"model":"gpt-4o-mini","input":"hello"}'`,
  }), []);
  const endpoints = [
    { id: "models", method: "GET", path: "/v1/models", family: "模型" },
    { id: "model", method: "GET", path: "/v1/models/{model}", family: "模型详情" },
    { id: "chat", method: "POST", path: "/v1/chat/completions", family: "OpenAI Chat" },
    { id: "responses", method: "POST", path: "/v1/responses", family: "OpenAI Responses" },
    { id: "responses-ws", method: "GET", path: "/v1/responses", family: "Responses 实时连接" },
    { id: "messages", method: "POST", path: "/v1/messages", family: "Anthropic Messages" },
    { id: "claude-files", method: "GET/POST", path: "/v1/files*", family: "Claude Code 文件" },
    { id: "claude-mcp", method: "GET", path: "/v1/mcp_servers*", family: "Claude Code MCP" },
    { id: "claude-sessions", method: "GET/POST/PATCH", path: "/v1/sessions*", family: "Claude Code 会话" },
    { id: "claude-sessions-ws", method: "GET", path: "/v1/sessions/ws/*", family: "Claude Code WebSocket" },
    { id: "claude-code-sessions", method: "GET/POST", path: "/v1/code/sessions*", family: "Claude Code 执行会话" },
    { id: "claude-session-ingress", method: "GET", path: "/v1/session_ingress/*", family: "Claude Code 入口" },
    { id: "claude-environments", method: "GET/POST/DELETE", path: "/v1/environments*", family: "Claude Code 环境" },
    { id: "claude-environment-providers", method: "GET/POST", path: "/v1/environment_providers*", family: "Claude Code 环境供应商" },
    { id: "claude-oauth", method: "GET/POST/PATCH", path: "/api/oauth/*", family: "Claude Code OAuth" },
    { id: "claude-cli-profile", method: "GET", path: "/api/claude_cli_profile", family: "Claude CLI 配置" },
    { id: "embeddings", method: "POST", path: "/v1/embeddings", family: "向量" },
    { id: "images", method: "POST", path: "/v1/images/generations", family: "图片" },
    { id: "audio-transcriptions", method: "POST", path: "/v1/audio/transcriptions", family: "音频转写" },
    { id: "audio-translations", method: "POST", path: "/v1/audio/translations", family: "音频翻译" },
    { id: "audio-speech", method: "POST", path: "/v1/audio/speech", family: "语音合成" },
    { id: "realtime-session", method: "POST", path: "/v1/realtime/sessions", family: "实时会话" },
    { id: "realtime-ws", method: "GET", path: "/v1/realtime", family: "实时 WebSocket" },
    { id: "rerank", method: "POST", path: "/v1/rerank", family: "重排" },
  ];
  return (
    <div className="stack">
      <section className="panel">
        <h2>API</h2>
        <dl className="kv"><dt>基础地址</dt><dd>{RELAY_API_BASE}</dd><dt>认证</dt><dd>Authorization: Bearer &lt;personal_api_key&gt;</dd></dl>
        <div className="settings-note">门户/管理员登录态只用于控制台；所有 /v1 接口都必须使用 API 密钥，不接受登录 session token。</div>
        <DataTable rows={endpoints} columns={["method", "path", "family"]} />
      </section>
      <section className="panel">
        <h2>请求示例</h2>
        <pre>{examples.models}</pre>
        <pre>{examples.chat}</pre>
        <pre>{examples.responses}</pre>
      </section>
    </div>
  );
}
