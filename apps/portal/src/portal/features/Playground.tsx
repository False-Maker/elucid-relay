import { useEffect, useState } from "react";
import type { ModelRecord } from "@elucid-relay/contracts";
import { RELAY_API_BASE, api } from "../../api";
import { PageNotice, SectionHeader } from "../../components/Primitives";

type PlaygroundModel = {
  model_name: string;
};

export function PlaygroundView({ onOpenKeys }: { onOpenKeys?: () => void }) {
  const [models, setModels] = useState<PlaygroundModel[]>([]);
  const [personalKey, setPersonalKey] = useState("");
  const [model, setModel] = useState("");
  const [endpoint, setEndpoint] = useState("chat");
  const [prompt, setPrompt] = useState("hello");
  const [result, setResult] = useState("");
  const [error, setError] = useState("");
  const [modelSource, setModelSource] = useState<"portal" | "relay">("portal");
  const [loadingModels, setLoadingModels] = useState(false);

  useEffect(() => {
    void api.request<ModelRecord[]>("/api/portal/v1/models").then((rows) => {
      setModels(rows.map((row) => ({ model_name: row.model_name })));
      setModel(rows[0]?.model_name ?? "");
    }).catch(() => undefined);
  }, []);

  async function loadRelayModels(keyValue = personalKey) {
    const key = keyValue.trim();
    if (!key) {
      setError("请先粘贴个人 API Key，再加载测试地址模型。");
      return;
    }
    setError("");
    setLoadingModels(true);
    try {
      const response = await fetch(`${RELAY_API_BASE}/v1/models`, {
        headers: { Authorization: `Bearer ${key}` },
      });
      const text = await response.text();
      if (!response.ok) throw new Error(playgroundErrorMessage(text, response.status));
      const rows = relayModels(text);
      setModels(rows);
      setModel(rows[0]?.model_name ?? "");
      setModelSource("relay");
      if (!rows.length) setError("测试地址没有返回可用模型。");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setLoadingModels(false);
    }
  }

  async function send() {
    setError("");
    setResult("");
    const key = personalKey.trim();
    if (!key) {
      setError("请先粘贴个人 API Key。登录态不能直接调用 /v1 接口。");
      return;
    }
    if (!model) {
      setError("请选择模型。");
      return;
    }
    const path = endpoint === "responses" ? "/v1/responses" : "/v1/chat/completions";
    const payload = endpoint === "responses"
      ? { model, input: prompt }
      : { model, messages: [{ role: "user", content: prompt }] };
    try {
      const response = await fetch(`${RELAY_API_BASE}${path}`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Authorization: `Bearer ${key}`,
        },
        body: JSON.stringify(payload),
      });
      const text = await response.text();
      if (!response.ok) throw new Error(playgroundErrorMessage(text, response.status));
      setResult(text);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  return (
    <div className="stack">
      <section className="panel">
        <SectionHeader title="API Playground" />
        <dl className="kv"><dt>测试地址</dt><dd>{RELAY_API_BASE}</dd></dl>
        <PageNotice
          error={error}
          message={!models.length ? "先粘贴测试 Key 并加载测试地址模型。" : personalKey ? `密钥只在浏览器内用于这次请求；当前模型来源：${modelSource === "relay" ? "测试地址" : "本地门户"}` : "先粘贴个人 API Key，再选择模型发送测试请求。"}
          action={!personalKey.trim() && onOpenKeys ? <button onClick={onOpenKeys}>打开 API 密钥</button> : undefined}
        />
        <div className="form-grid">
          <input value={personalKey} onChange={(event) => setPersonalKey(event.target.value)} onBlur={(event) => { if (event.target.value.trim() && modelSource !== "relay") void loadRelayModels(event.target.value); }} placeholder="sk-relay_...（不是登录 token）" type="password" autoComplete="off" />
          <button onClick={() => loadRelayModels()} disabled={!personalKey.trim() || loadingModels}>{loadingModels ? "加载中" : "加载测试模型"}</button>
          <input value={model} onChange={(event) => setModel(event.target.value)} placeholder="模型名，例如 codex-auto-review" list="playground-model-list" />
          <datalist id="playground-model-list">
            {models.map((item) => <option key={item.model_name} value={item.model_name}>{item.model_name}</option>)}
          </datalist>
          <select value={endpoint} onChange={(event) => setEndpoint(event.target.value)}>
            <option value="chat">Chat Completions</option>
            <option value="responses">Responses</option>
          </select>
          <textarea value={prompt} onChange={(event) => setPrompt(event.target.value)} rows={5} />
          <button onClick={send} disabled={!personalKey.trim() || !model || !prompt.trim()}>发送</button>
        </div>
        {result && <pre>{result}</pre>}
      </section>
    </div>
  );
}

function relayModels(text: string): PlaygroundModel[] {
  const payload = JSON.parse(text);
  const rows = Array.isArray(payload.data) ? payload.data : Array.isArray(payload) ? payload : [];
  return rows
    .map((row: any) => row?.model_name || row?.id || row?.name || "")
    .filter((name: string) => name)
    .map((name: string) => ({ model_name: name }));
}

function playgroundErrorMessage(text: string, status: number) {
  const fallback = text || `HTTP ${status}`;
  try {
    const payload = JSON.parse(text);
    const code = payload?.code ?? payload?.error?.code;
    const message = payload?.message ?? payload?.error?.message;
    if (code === "API_KEY_REQUIRED") {
      return "需要个人 API Key：请在 API 密钥页面创建密钥，并把完整密钥粘贴到 Playground。登录态不能直接调用 /v1 接口。";
    }
    if (code === "INVALID_API_KEY") return "API Key 无效或已停用，请重新创建或启用密钥。";
    if (message) return String(message);
  } catch {
    return fallback;
  }
  return fallback;
}
