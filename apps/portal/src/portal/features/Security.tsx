import { useEffect, useMemo, useState } from "react";
import { ShieldCheck } from "lucide-react";
import type { RelayUser } from "@elucid-relay/contracts";
import { api } from "../../api";
import { DataTable } from "../../components/DataTable";

export function SecurityView({ user }: { user: RelayUser }) {
  const [limits, setLimits] = useState<any>(null);
  const [verificationToken, setVerificationToken] = useState("");
  const fragmentParams = useMemo(() => new URLSearchParams(window.location.hash.replace(/^#/, "")), []);
  const [confirmToken, setConfirmToken] = useState(fragmentParams.get("verification_token") ?? "");
  const [apiKeyId, setAPIKeyId] = useState("");
  const [dailyUSD, setDailyUSD] = useState("");
  const [monthlyUSD, setMonthlyUSD] = useState("");
  const [dailyRequests, setDailyRequests] = useState("");
  const [monthlyRequests, setMonthlyRequests] = useState("");
  const [message, setMessage] = useState("");

  async function load() {
    setLimits(await api.request<any>("/api/portal/v1/spend-limits"));
  }

  useEffect(() => {
    void load();
  }, []);

  async function requestVerification() {
    const data = await api.request<any>("/api/portal/v1/me/email-verification", { method: "POST" });
    setVerificationToken(data.verification_token ?? "");
    setMessage(data.verified ? "邮箱已验证" : "已发送验证请求");
  }

  async function confirmVerification() {
    await api.request("/api/portal/v1/auth/email-verification/confirm", { method: "POST", body: JSON.stringify({ token: confirmToken }) });
    setConfirmToken("");
    setMessage("邮箱已验证");
  }

  async function saveKeyLimit() {
    await api.request(`/api/portal/v1/api-keys/${apiKeyId}/spend-limit`, {
      method: "PUT",
      body: JSON.stringify({
        daily_usd_limit: dailyUSD || null,
        monthly_usd_limit: monthlyUSD || null,
        daily_request_limit: dailyRequests ? Number(dailyRequests) : null,
        monthly_request_limit: monthlyRequests ? Number(monthlyRequests) : null,
      }),
    });
    setMessage("限制已保存");
    await load();
  }

  return (
    <div className="stack">
      <section className="panel">
        <h2><ShieldCheck size={18} /> 账号安全</h2>
        <DataTable rows={[user as any]} columns={["email", "email_verified_at", "status"]} />
        <div className="row">
          <button onClick={requestVerification}>请求验证</button>
          <input value={confirmToken} onChange={(event) => setConfirmToken(event.target.value)} placeholder="验证令牌" />
          <button onClick={confirmVerification} disabled={!confirmToken}>确认</button>
        </div>
        {verificationToken && <pre>{verificationToken}</pre>}
        {message && <div className="success">{message}</div>}
      </section>
      <section className="panel">
        <h2>消费限制</h2>
        <DataTable rows={limits?.user ? [limits.user] : []} columns={["target_type", "daily_usd_limit", "monthly_usd_limit", "daily_request_limit", "monthly_request_limit", "status"]} />
        <div className="form-grid">
          <input value={apiKeyId} onChange={(event) => setAPIKeyId(event.target.value)} placeholder="API 密钥 ID" />
          <input value={dailyUSD} onChange={(event) => setDailyUSD(event.target.value)} placeholder="每日 USD" />
          <input value={monthlyUSD} onChange={(event) => setMonthlyUSD(event.target.value)} placeholder="每月 USD" />
          <input value={dailyRequests} onChange={(event) => setDailyRequests(event.target.value)} placeholder="每日请求数" />
          <input value={monthlyRequests} onChange={(event) => setMonthlyRequests(event.target.value)} placeholder="每月请求数" />
          <button onClick={saveKeyLimit} disabled={!apiKeyId}>保存密钥限制</button>
        </div>
        <DataTable rows={limits?.api_keys ?? []} columns={["target_id", "daily_usd_limit", "monthly_usd_limit", "daily_request_limit", "monthly_request_limit", "status"]} />
      </section>
    </div>
  );
}
