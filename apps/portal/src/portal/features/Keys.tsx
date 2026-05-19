import { useEffect, useMemo, useState } from "react";
import { KeyRound, Loader2, Plus, RefreshCw } from "lucide-react";
import type { ModelRecord } from "@elucid-relay/contracts";
import { api } from "../../api";
import { DataTable } from "../../components/DataTable";
import { CopyButton, PageNotice, SectionHeader } from "../../components/Primitives";
import { EmptyState, splitCSV, type ApiKeyRecord } from "../shared";

const DEFAULT_KEY_NAME = "默认共享池密钥";

export function KeysView() {
  const [keys, setKeys] = useState<ApiKeyRecord[]>([]);
  const [models, setModels] = useState<ModelRecord[]>([]);
  const [name, setName] = useState("");
  const [modelScope, setModelScope] = useState("");
  const [ipAllowlist, setIPAllowlist] = useState("");
  const [expiresAt, setExpiresAt] = useState("");
  const [secret, setSecret] = useState("");
  const [routingMode, setRoutingMode] = useState("pool");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [actionId, setActionId] = useState("");

  async function load() {
    setLoading(true);
    setError("");
    try {
      const [nextKeys, nextModels] = await Promise.all([
        api.request<ApiKeyRecord[]>("/api/portal/v1/api-keys"),
        api.request<ModelRecord[]>("/api/portal/v1/models"),
      ]);
      setKeys(nextKeys);
      setModels(nextModels);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const activeModels = useMemo(() => models.filter((model) => model.status === "active" && model.public_visible !== false), [models]);

  async function createKey(quick = false) {
    setError("");
    setMessage("");
    setCreating(true);
    try {
      const keyName = (name || (quick ? DEFAULT_KEY_NAME : "API Key")).trim();
      const data = await api.request<ApiKeyRecord & { secret: string }>("/api/portal/v1/api-keys", {
        method: "POST",
        body: JSON.stringify(quick
          ? { name: keyName, routing_mode: "pool" }
          : {
              name: keyName,
              routing_mode: routingMode,
              expires_at: expiresAt || undefined,
              ip_allowlist: splitCSV(ipAllowlist),
              model_scope: splitCSV(modelScope),
            }),
      });
      setSecret(data.secret);
      setMessage("密钥已创建，请立即复制保存。");
      setName("");
      setModelScope("");
      setIPAllowlist("");
      setExpiresAt("");
      setRoutingMode("pool");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setCreating(false);
    }
  }

  async function updateStatus(id: string, status: string) {
    setError("");
    setActionId(`status:${id}`);
    try {
      await api.request(`/api/portal/v1/api-keys/${id}`, { method: "PATCH", body: JSON.stringify({ status }) });
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setActionId("");
    }
  }

  async function revoke(id: string) {
    setError("");
    setActionId(`revoke:${id}`);
    try {
      await api.request(`/api/portal/v1/api-keys/${id}`, { method: "DELETE" });
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setActionId("");
    }
  }

  return (
    <div className="stack">
      <PageNotice error={error} message={message} />
      <section className="panel">
        <SectionHeader
          title="新建密钥"
          action={(
            <button className="primary" onClick={() => createKey(true)} disabled={creating}>
              {creating ? <Loader2 className="spin" size={15} /> : <Plus size={15} />}
              快速创建
            </button>
          )}
        />
        <div className="form-grid relaxed">
          <label className="form-field">
            <span>名称</span>
            <input value={name} onChange={(event) => setName(event.target.value)} placeholder={DEFAULT_KEY_NAME} />
          </label>
          <label className="form-field">
            <span>路由</span>
            <select value={routingMode} onChange={(event) => setRoutingMode(event.target.value)}>
              <option value="pool">共享池</option>
              <option value="byo">自带账号池</option>
            </select>
          </label>
          <details className="advanced-details wide">
            <summary>访问限制</summary>
            <div className="form-grid relaxed">
              <label className="form-field">
                <span>模型范围</span>
                <input value={modelScope} onChange={(event) => setModelScope(event.target.value)} placeholder="gpt-4.1, claude-3-7-sonnet" list="model-list" />
              </label>
              <label className="form-field">
                <span>IP 白名单</span>
                <input value={ipAllowlist} onChange={(event) => setIPAllowlist(event.target.value)} placeholder="203.0.113.10, 2001:db8::1" />
              </label>
              <label className="form-field">
                <span>过期时间</span>
                <input value={expiresAt} onChange={(event) => setExpiresAt(event.target.value)} placeholder="2026-12-31T23:59:59Z" />
              </label>
              <label className="form-field">
                <span>可用模型</span>
                <select value="" onChange={(event) => setModelScope((current) => current ? `${current}, ${event.target.value}` : event.target.value)}>
                  <option value="">选择后追加到模型范围</option>
                  {activeModels.map((model) => <option key={model.model_name} value={model.model_name}>{model.model_name}</option>)}
                </select>
              </label>
            </div>
          </details>
          <datalist id="model-list">{activeModels.map((model) => <option key={model.model_name}>{model.model_name}</option>)}</datalist>
          <div className="actions wide">
            <button onClick={() => createKey(false)} disabled={creating}>
              {creating ? <Loader2 className="spin" size={15} /> : <KeyRound size={15} />}
              按配置创建
            </button>
            <button onClick={() => { setModelScope(""); setIPAllowlist(""); setExpiresAt(""); setRoutingMode("pool"); }}>清空限制</button>
          </div>
        </div>
      </section>
      {secret && (
        <section className="secret-block">
          <div className="secret-header">
            <div>
              <strong>新密钥只显示一次</strong>
              <small>创建后立即保存。</small>
            </div>
            <CopyButton value={secret} label="复制密钥" />
          </div>
          <pre className="secret">{secret}</pre>
        </section>
      )}
      <section className="panel">
        <SectionHeader title="密钥" action={<button onClick={() => load()} disabled={loading}><RefreshCw size={15} /> {loading ? "刷新中" : "刷新"}</button>} />
        {keys.length === 0 && !loading
          ? <EmptyState title="还没有 API Key" detail="创建共享池密钥后即可测试请求。" action={<button className="primary" onClick={() => createKey(true)} disabled={creating}><Plus size={15} /> 快速创建</button>} />
          : <DataTable rows={keys} columns={["name", "routing_mode", "display_prefix", "status", "model_scope", "ip_allowlist", "expires_at", "last_used_at"]} action={(row) => {
              const nextStatus = row.status === "active" ? "disabled" : "active";
              return (
                <div className="actions">
                  <button onClick={() => updateStatus(row.id, nextStatus)} disabled={Boolean(actionId)}>
                    {actionId === `status:${row.id}` ? "处理中" : row.status === "active" ? "停用" : "启用"}
                  </button>
                  <button onClick={() => revoke(row.id)} disabled={Boolean(actionId)}>{actionId === `revoke:${row.id}` ? "撤销中" : "撤销"}</button>
                </div>
              );
            }} />}
      </section>
    </div>
  );
}
