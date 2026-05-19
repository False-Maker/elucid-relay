import { useEffect, useMemo, useState } from "react";
import { Activity, CreditCard, Database, Network, ShieldAlert, Users } from "lucide-react";
import type { AdminOverview } from "@elucid-relay/contracts";
import type { AdminTarget } from "./types";
import { SetupChecklist } from "./SetupChecklist";
import { adminApi } from "../adminApi";
import { DataTable, Metric } from "../components/DataTable";
import { PageNotice, SectionHeader } from "../components/Primitives";

export function OverviewDashboard({ onNavigate }: { onNavigate: (target: AdminTarget) => void }) {
  const [overview, setOverview] = useState<AdminOverview | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    void adminApi.request<AdminOverview>("/api/admin/v1/overview").then(setOverview).catch((err) => setError(err instanceof Error ? err.message : "请求失败。"));
  }, []);

  const metrics = overview?.metrics ?? {};
  const healthCards = useMemo(() => [
    { label: "用户规模", value: metrics.total_users ?? "0", detail: `${metrics.active_users ?? "0"} 个活跃，${metrics.new_users_24h ?? "0"} 个 24h 新增`, icon: Users },
    { label: "24h 请求", value: metrics.requests_24h ?? "0", detail: `${metrics.failed_requests_24h ?? "0"} 失败，${metrics.rejected_requests_24h ?? "0"} 拒绝`, icon: Activity },
    { label: "24h 成本", value: `$${metrics.cost_24h ?? "0"}`, detail: `钱包余额 $${metrics.wallet_balance ?? "0"}`, icon: CreditCard },
    { label: "模型发布", value: metrics.public_model_count ?? "0", detail: `${metrics.model_count ?? "0"} 个模型总数`, icon: Database },
    { label: "上游能力", value: `${metrics.active_channels ?? "0"} / ${metrics.active_accounts ?? "0"}`, detail: "活动通道 / 活动账号", icon: Network },
    { label: "风险事件", value: metrics.risk_events_24h ?? "0", detail: "最近 24 小时", icon: ShieldAlert },
  ], [metrics]);

  return (
    <div className="stack">
      <PageNotice error={error} />
      <div className="metric-grid">
        {healthCards.map((item) => {
          const Icon = item.icon;
          return (
            <section key={item.label} className="metric metric-card">
              <span><Icon size={15} /> {item.label}</span>
              <strong>{item.value}</strong>
              <small>{item.detail}</small>
            </section>
          );
        })}
      </div>
      <SetupChecklist onNavigate={onNavigate} />
      <div className="dashboard-list">
        <section className="panel">
          <SectionHeader title="运营指标" />
          <DataTable rows={overviewRows(metrics)} columns={["metric", "value"]} />
        </section>
      </div>
    </div>
  );
}

function overviewRows(metrics: Record<string, string>) {
  const labels: Record<string, string> = {
    new_users_24h: "24h 新增用户",
    disabled_users: "停用用户",
    operator_users: "操作员",
    owner_users: "平台所有者",
    rejected_requests_24h: "24h 拒绝请求",
    provider_count: "供应商",
    channel_count: "通道总数",
    account_count: "账号总数",
    proxy_count: "代理总数",
    model_count: "模型总数",
  };
  return Object.entries(labels).map(([key, label]) => ({ id: key, metric: label, value: metrics[key] ?? "0" }));
}
