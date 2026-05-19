import { useEffect, useMemo, useState } from "react";
import { AlertTriangle, CheckCircle2, CircleDashed, RefreshCcw } from "lucide-react";
import type { AdminTarget, SetupChecklistItem, SetupChecklistResponse } from "./types";
import { adminApi } from "../adminApi";
import { SectionHeader } from "../components/Primitives";

const checkDescriptions: Record<string, string> = {
  site: "站点 URL、开放注册和基础安全项。",
  smtp: "SMTP 能发送注册邮箱验证码。",
  registration_email: "开启后普通用户注册才需要邮箱验证码。",
  payment: "配置支付总开关、支付方式和至少一个可用服务商。",
  provider: "至少接入一个供应商。",
  channel: "至少有一个活动通道承载模型请求。",
  model: "至少公开一个模型给用户门户。",
  account: "至少有一个可用账号进入共享池或 BYO 路由。",
};

export function SetupChecklist({ onNavigate, compact = false }: { onNavigate?: (target: AdminTarget) => void; compact?: boolean }) {
  const [items, setItems] = useState<SetupChecklistItem[]>([]);
  const [configStatus, setConfigStatus] = useState<Record<string, unknown>>({});
  const [testing, setTesting] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const data = await adminApi.request<SetupChecklistResponse>("/api/admin/v1/setup/checklist");
    setItems(data.items ?? []);
    setConfigStatus(data.config_status ?? {});
  }

  useEffect(() => {
    void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"));
  }, []);

  const done = items.filter((item) => item.complete).length;
  const total = items.length;
  const percent = total ? Math.round((done / total) * 100) : 0;
  const paymentStatus = configStatus.payment && typeof configStatus.payment === "object" ? configStatus.payment as Record<string, unknown> : {};
  const authStatus = configStatus.auth && typeof configStatus.auth === "object" ? configStatus.auth as Record<string, unknown> : {};
  const securityStatus = configStatus.security && typeof configStatus.security === "object" ? configStatus.security as Record<string, unknown> : {};
  const summary = useMemo(() => [
    { label: "SMTP", value: authStatus.smtp_configured ? "已配置" : "未配置" },
    { label: "注册验证", value: authStatus.registration_email_verification_enabled ? "已开启" : "未开启" },
    { label: "支付", value: paymentStatus.configured ? "已配置" : "未配置" },
    { label: "Vault Key", value: securityStatus.vault_key_configured ? "已配置" : "未配置" },
  ], [authStatus, paymentStatus, securityStatus]);

  async function test(item: SetupChecklistItem) {
    setTesting(item.id);
    setError("");
    setMessage("");
    try {
      const result = await adminApi.request<SetupChecklistItem>(`/api/admin/v1/setup/checks/${encodeURIComponent(item.id)}/test`, { method: "POST" });
      setItems((current) => current.map((entry) => entry.id === result.id ? result : entry));
      setMessage(result.complete ? `${result.label} 已通过` : `${result.label} 仍未完成`);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setTesting("");
    }
  }

  return (
    <section className={compact ? "panel setup-panel compact" : "panel setup-panel"}>
      <SectionHeader
        title="生产启用清单"
        description="不阻断操作，但会把部署前必须补齐的入口集中展示。"
        action={<button onClick={() => load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"))}><RefreshCcw size={15} /> 刷新</button>}
      />
      <div className="setup-progress">
        <div>
          <strong>{percent}%</strong>
          <span>{done}/{total} 项完成</span>
        </div>
        <div className="setup-progress-bar"><span style={{ width: `${percent}%` }} /></div>
      </div>
      {(error || message) && <div className="inline-feedback">{error && <div className="error">{error}</div>}{message && <div className="success">{message}</div>}</div>}
      <div className="setup-summary">
        {summary.map((item) => <div key={item.label}><span>{item.label}</span><strong>{item.value}</strong></div>)}
      </div>
      <div className="setup-steps">
        {items.map((item) => (
          <div key={item.id} className={`setup-step ${item.complete ? "complete" : "pending"}`}>
            <span className="setup-step-icon">{item.complete ? <CheckCircle2 size={18} /> : <CircleDashed size={18} />}</span>
            <div>
              <strong>{item.label}</strong>
              <small>{checkDescriptions[item.id] ?? "打开目标页面完成该项配置。"}</small>
            </div>
            <div className="actions">
              <button onClick={() => onNavigate?.({ view: normalizeView(item.target_view) })}>打开</button>
              <button onClick={() => test(item)} disabled={testing === item.id}>{testing === item.id ? "检测中" : "检测"}</button>
            </div>
          </div>
        ))}
        {!items.length && <div className="empty"><AlertTriangle size={16} /> 暂无清单数据</div>}
      </div>
    </section>
  );
}

function normalizeView(value: string): AdminTarget["view"] {
  const allowed = new Set(["overview", "users", "redeem", "models", "pool", "upstream", "proxies", "oauth", "billing", "controls", "usage", "content", "groups", "risk", "public", "audit"]);
  return allowed.has(value) ? value as AdminTarget["view"] : "overview";
}
