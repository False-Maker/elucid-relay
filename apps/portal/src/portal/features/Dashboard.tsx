import { useEffect, useState } from "react";
import { CheckCircle2, Database, KeyRound, PlayCircle, WalletCards } from "lucide-react";
import type { ModelRecord, RelayUser, Wallet } from "@elucid-relay/contracts";
import { RELAY_API_BASE, api } from "../../api";
import { DataTable, Metric } from "../../components/DataTable";
import { CopyButton, PageNotice, SectionHeader } from "../../components/Primitives";
import { available, EmptyState, type ApiKeyRecord, type PortalNavTarget } from "../shared";

export function Dashboard({ user, onNavigate }: { user: RelayUser; onNavigate: (view: PortalNavTarget) => void }) {
  const [wallet, setWallet] = useState<Wallet | null>(null);
  const [keys, setKeys] = useState<ApiKeyRecord[]>([]);
  const [usage, setUsage] = useState<any[]>([]);
  const [models, setModels] = useState<ModelRecord[]>([]);
  const [error, setError] = useState("");

  async function load() {
    const [nextWallet, nextKeys, nextUsage, nextModels] = await Promise.all([
      api.request<Wallet>("/api/portal/v1/wallet"),
      api.request<ApiKeyRecord[]>("/api/portal/v1/api-keys"),
      api.request<any[]>("/api/portal/v1/usage?limit=5"),
      api.request<ModelRecord[]>("/api/portal/v1/models"),
    ]);
    setWallet(nextWallet);
    setKeys(nextKeys);
    setUsage(nextUsage);
    setModels(nextModels);
  }

  useEffect(() => {
    void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"));
  }, []);

  const activeKeys = keys.filter((key) => key.status === "active").length;
  const availableBalance = Number(available(wallet));
  const publicModels = models.filter((model) => model.status === "active" && model.public_visible !== false).length;
  const baseURL = `${RELAY_API_BASE}/v1`;
  const steps = [
    { id: "wallet", title: "准备余额", detail: availableBalance > 0 ? `可用 $${available(wallet)}` : "充值或兑换后才能稳定调用", complete: availableBalance > 0, icon: WalletCards, action: () => onNavigate("billing") },
    { id: "key", title: "创建 API Key", detail: activeKeys > 0 ? `${activeKeys} 个启用密钥` : "密钥只显示一次，创建后立即保存", complete: activeKeys > 0, icon: KeyRound, action: () => onNavigate("keys") },
    { id: "model", title: "确认模型", detail: publicModels > 0 ? `${publicModels} 个可用模型` : "等待管理员发布模型", complete: publicModels > 0, icon: Database, action: () => onNavigate("models") },
    { id: "test", title: "发起测试", detail: usage.length > 0 ? "已有请求记录" : "用 Playground 验证网关", complete: usage.length > 0, icon: PlayCircle, action: () => onNavigate("playground") },
  ];

  return (
    <div className="stack">
      <PageNotice error={error} />
      <div className="workflow-grid user-flow">
        {steps.map((step) => {
          const Icon = step.icon;
          return (
            <button key={step.id} className={`workflow-tile ${step.complete ? "complete" : ""}`} onClick={step.action}>
              <span>{step.complete ? <CheckCircle2 size={17} /> : <Icon size={17} />}</span>
              <strong>{step.title}</strong>
              <small>{step.detail}</small>
            </button>
          );
        })}
      </div>
      <section className="panel quick-console">
        <SectionHeader
          title="快速接入"
          action={<button onClick={() => onNavigate("docs")}>打开文档</button>}
        />
        <div className="connect-grid">
          <div className="copy-row">
            <span>Base URL</span>
            <code>{baseURL}</code>
            <CopyButton value={baseURL} />
          </div>
          <div className="copy-row">
            <span>Authorization</span>
            <code>Bearer YOUR_API_KEY</code>
            <CopyButton value="Authorization: Bearer YOUR_API_KEY" />
          </div>
          <div className="quick-actions">
            <button className="primary" onClick={() => onNavigate("keys")}><KeyRound size={15} /> 创建 Key</button>
            <button onClick={() => onNavigate("billing")}><WalletCards size={15} /> 充值/订阅</button>
            <button onClick={() => onNavigate("playground")}><PlayCircle size={15} /> 测试请求</button>
          </div>
        </div>
      </section>
      <div className="grid three">
        <Metric label="余额" value={`$${wallet?.balance ?? "0.00"}`} detail={`可用余额 $${available(wallet)}`} />
        <Metric label="已预留" value={`$${wallet?.reserved_balance ?? "0.00"}`} detail="请求完成后按实际用量结算" />
        <Metric label="启用密钥" value={String(activeKeys)} detail={`${keys.length} 个密钥总数`} />
        <section className="panel wide">
          <SectionHeader title="账号信息" />
          <DataTable rows={[user as any]} columns={["email", "display_name", "user_type", "status", "email_verified_at"]} />
        </section>
        <section className="panel wide">
          <SectionHeader title="近期用量" action={<button onClick={() => onNavigate("usage")}>查看全部</button>} />
          {usage.length === 0
            ? <EmptyState title="还没有请求记录" detail="创建 API Key 后到 Playground 发起一次测试，请求完成后这里会显示模型、成本和状态。" action={<button onClick={() => onNavigate("playground")}><PlayCircle size={15} /> 去测试</button>} />
            : <DataTable rows={usage} columns={["request_id", "requested_model", "endpoint", "actual_cost", "status"]} />}
        </section>
      </div>
    </div>
  );
}
