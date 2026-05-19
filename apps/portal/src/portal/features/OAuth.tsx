import { useEffect, useMemo, useState } from "react";
import { Link } from "lucide-react";
import { api } from "../../api";
import { DataTable } from "../../components/DataTable";
import { authModeLabel, oauthProgress, optionLabel, type OAuthOptions } from "../shared";

export function OAuthView() {
  const [accounts, setAccounts] = useState<any[]>([]);
  const [options, setOptions] = useState<OAuthOptions>({ providers: [], channels: [], provider_clients: [], auth_modes: [] });
  const [providerId, setProviderId] = useState("");
  const [channelId, setChannelId] = useState("");
  const [providerClientId, setProviderClientId] = useState("");
  const [name, setName] = useState("");
  const [authMode, setAuthMode] = useState("codex_cli");
  const [tokenBundle, setTokenBundle] = useState("");
  const [payload, setPayload] = useState("");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [jobInputs, setJobInputs] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");

  async function load() {
    setAccounts(await api.request<any[]>("/api/portal/v1/oauth/accounts"));
    setOptions(await api.request<OAuthOptions>("/api/portal/v1/oauth/options"));
  }

  useEffect(() => { void load(); }, []);

  const providerChannels = useMemo(() => options.channels.filter((item) => item.provider_id === providerId), [options.channels, providerId]);
  const providerClients = useMemo(() => options.provider_clients.filter((item) => item.provider_id === providerId), [options.provider_clients, providerId]);

  useEffect(() => {
    if (!providerId && options.providers[0]) {
      setProviderId(options.providers[0].id);
    }
  }, [options.providers, providerId]);

  useEffect(() => {
    if (!providerChannels.some((item) => item.id === channelId)) {
      setChannelId(providerChannels[0]?.id ?? "");
    }
    if (!providerClients.some((item) => item.id === providerClientId)) {
      setProviderClientId(providerClients[0]?.id ?? "");
    }
  }, [providerId, channelId, providerClientId, providerChannels, providerClients]);

  async function createAccount() {
    setError("");
    setMessage("");
    try {
      await api.request("/api/portal/v1/oauth/accounts", { method: "POST", body: JSON.stringify({ provider_id: providerId, channel_id: channelId, provider_client_id: providerClientId, name, auth_mode: authMode, token_bundle: tokenBundle ? JSON.parse(tokenBundle) : {}, metadata: payload ? JSON.parse(payload) : {} }) });
      setName("");
      setTokenBundle("");
      setPayload("");
      setMessage("账号已添加");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function reauth(row: any) {
    setError("");
    setMessage("");
    try {
      const clientId = providerClientId || options.provider_clients.find((item) => item.provider_id === row.provider_id)?.id || "";
      const mode = row.auth?.auth_mode || authMode;
      await api.request(`/api/portal/v1/oauth/accounts/${row.id}/reauth`, { method: "POST", body: JSON.stringify({ provider_client_id: clientId, auth_mode: mode, payload: payload ? JSON.parse(payload) : {} }) });
      setMessage("已加入重新授权队列");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function revoke(accountId: string) {
    setError("");
    setMessage("");
    try {
      await api.request(`/api/portal/v1/oauth/accounts/${accountId}/revoke`, { method: "POST" });
      setMessage("账号已撤销");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function submitJobInput(jobId: string) {
    const authorizationCode = jobInputs[jobId]?.trim();
    if (!authorizationCode) return;
    setError("");
    setMessage("");
    try {
      await api.request(`/api/portal/v1/oauth/jobs/${jobId}/input`, { method: "POST", body: JSON.stringify({ authorization_code: authorizationCode }) });
      setJobInputs((current) => ({ ...current, [jobId]: "" }));
      setMessage("授权码已提交");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  const rows = accounts.map((account) => {
    const progress = oauthProgress(account);
    return {
      ...account,
      auth_mode: account.auth?.auth_mode,
      auth_status: account.auth?.auth_status,
      provider_subject: account.auth?.provider_subject,
      last_error: account.auth?.last_error,
      latest_job: account.latest_job,
      job_status: account.latest_job?.status,
      oauth_progress: Object.keys(progress).length ? progress : "",
    };
  });

  return (
    <div className="stack">
      <section className="panel">
        <h2><Link size={18} /> 添加自带账号</h2>
        <div className="form-grid">
          <select value={providerId} onChange={(event) => setProviderId(event.target.value)}>
            <option value="">供应商</option>
            {options.providers.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
          <select value={channelId} onChange={(event) => setChannelId(event.target.value)}>
            <option value="">通道</option>
            {providerChannels.map((item) => <option key={item.id} value={item.id}>{optionLabel(item)}</option>)}
          </select>
          <select value={providerClientId} onChange={(event) => setProviderClientId(event.target.value)}>
            <option value="">无 OAuth 客户端</option>
            {providerClients.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}
          </select>
          <select value={authMode} onChange={(event) => setAuthMode(event.target.value)}>
            {(options.auth_modes.length ? options.auth_modes : ["codex_cli", "oauth"]).map((item) => <option key={item} value={item}>{authModeLabel(item)}</option>)}
          </select>
          <input value={name} onChange={(event) => setName(event.target.value)} placeholder="账号名称" />
          <button type="button" className="ghost" onClick={() => setShowAdvanced((value) => !value)}>
            {showAdvanced ? "收起高级配置" : "高级配置"}
          </button>
          {showAdvanced && (
            <>
              <textarea value={tokenBundle} onChange={(event) => setTokenBundle(event.target.value)} placeholder="令牌包 JSON" rows={4} />
              <textarea value={payload} onChange={(event) => setPayload(event.target.value)} placeholder="请求载荷 JSON" rows={4} />
            </>
          )}
          <button onClick={createAccount} disabled={!providerId || !channelId || !name}>添加账号</button>
        </div>
        {error && <div className="error">{error}</div>}
        {message && <div className="success">{message}</div>}
      </section>
      <section className="panel">
        <h2>自带账号</h2>
        <DataTable rows={rows} columns={["name", "status", "auth_mode", "auth_status", "job_status", "oauth_progress", "provider_subject", "last_error", "created_at"]} action={(row) => {
          const progress = oauthProgress(row);
          const jobId = row.latest_job?.id;
          return (
            <div className="actions">
              {progress.authorization_url && <a href={progress.authorization_url} target="_blank" rel="noreferrer">授权</a>}
              {progress.user_code && <code>{progress.user_code}</code>}
              {jobId && progress.input === "authorization_code" && (
                <>
                  <input value={jobInputs[jobId] ?? ""} onChange={(event) => setJobInputs((current) => ({ ...current, [jobId]: event.target.value }))} placeholder="授权码" />
                  <button onClick={() => submitJobInput(jobId)} disabled={!jobInputs[jobId]?.trim()}>提交</button>
                </>
              )}
              <button onClick={() => reauth(row)}>重新授权</button>
              <button onClick={() => revoke(row.id)}>撤销</button>
            </div>
          );
        }} />
      </section>
    </div>
  );
}
