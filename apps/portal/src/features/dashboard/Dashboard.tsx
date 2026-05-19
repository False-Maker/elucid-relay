import { useMemo } from "react";
import { Wallet, Key, Zap, Database, ArrowRight, CheckCircle2, Circle, CreditCard, PlayCircle, KeyRound, ListChecks } from "lucide-react";
import { motion } from "framer-motion";
import type { RelayUser } from "@elucid-relay/contracts";
import { useWallet } from "@/lib/queries/wallet";
import { useApiKeys } from "@/lib/queries/keys";
import { useUsage, type UsageRecord } from "@/lib/queries/usage";
import { useModels } from "@/lib/queries/models";
import { Skeleton, MetricCard, StatusBadge, EmptyState, CopyButton } from "@/components/shared";
import { RELAY_API_BASE } from "@/lib/api-client";
import { cn } from "@/lib/utils";

type PortalView = "keys" | "billing" | "models" | "playground" | "usage";

interface DashboardProps {
  user: RelayUser;
  onNavigate: (view: PortalView) => void;
}

export function Dashboard({ user, onNavigate }: DashboardProps) {
  const wallet = useWallet();
  const keys = useApiKeys();
  const usage = useUsage({ limit: 5 });
  const models = useModels();

  const balance = wallet.data ? parseFloat(wallet.data.balance) : 0;
  const reserved = wallet.data ? parseFloat(wallet.data.reserved_balance) : 0;
  const available = balance - reserved;
  const activeKeys = keys.data?.filter((k) => k.status === "active").length ?? 0;
  const publicModels = models.data?.filter((m) => m.status === "active" && m.public_visible).length ?? 0;
  const hasUsage = (usage.data?.length ?? 0) > 0;

  const loading = wallet.isLoading || keys.isLoading;

  // Quick setup steps
  const steps: SetupStep[] = [
    {
      label: "充值余额",
      detail: available > 0 ? `$${available.toFixed(2)} 可用` : "前往充值",
      done: available > 0,
      action: () => onNavigate("billing"),
      icon: CreditCard,
    },
    {
      label: "创建密钥",
      detail: activeKeys > 0 ? `${activeKeys} 个活跃密钥` : "创建第一个密钥",
      done: activeKeys > 0,
      action: () => onNavigate("keys"),
      icon: KeyRound,
    },
    {
      label: "确认模型",
      detail: publicModels > 0 ? `${publicModels} 个可用模型` : "查看模型目录",
      done: publicModels > 0,
      action: () => onNavigate("models"),
      icon: Database,
    },
    {
      label: "测试请求",
      detail: hasUsage ? "已完成首次调用" : "前往 Playground",
      done: hasUsage,
      action: () => onNavigate("playground"),
      icon: PlayCircle,
    },
  ];
  const doneCount = steps.filter((s) => s.done).length;

  return (
    <motion.div
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.25 }}
      className="space-y-6"
    >
      {/* Greeting */}
      <div>
        <h2 className="text-base font-semibold">
          Hi，{user.display_name || user.email.split("@")[0]}
        </h2>
        <p className="mt-0.5 text-sm text-muted">
          {doneCount < 4
            ? `完成以下 ${4 - doneCount} 步即可开始使用`
            : "一切就绪，开始使用吧"}
        </p>
      </div>

      {/* Metric Cards */}
      <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
        <MetricCard
          label="可用余额"
          value={loading ? undefined : `$${available.toFixed(2)}`}
          detail={reserved > 0 ? `预留 $${reserved.toFixed(2)}` : undefined}
          loading={loading}
        />
        <MetricCard
          label="活跃密钥"
          value={loading ? undefined : activeKeys}
          detail={keys.data ? `共 ${keys.data.length} 个` : undefined}
          loading={loading}
        />
        <MetricCard
          label="近期请求"
          value={usage.isLoading ? undefined : usage.data?.length ?? 0}
          detail="最近 5 条"
          loading={usage.isLoading}
        />
        <MetricCard
          label="可用模型"
          value={models.isLoading ? undefined : publicModels}
          loading={models.isLoading}
        />
      </div>

      {/* Quick Setup */}
      {doneCount < 4 && (
        <div className="rounded-xl border border-border bg-surface p-4">
          <div className="mb-3 flex items-center justify-between">
            <h3 className="text-sm font-semibold">快速开始</h3>
            <span className="text-xs text-muted">{doneCount}/4</span>
          </div>
          {/* Progress bar */}
          <div className="mb-4 h-1.5 overflow-hidden rounded-full bg-surface-2">
            <motion.div
              className="h-full rounded-full bg-bronze"
              initial={{ width: 0 }}
              animate={{ width: `${(doneCount / 4) * 100}%` }}
              transition={{ duration: 0.5, ease: "easeOut" }}
            />
          </div>
          <div className="grid gap-2 sm:grid-cols-2">
            {steps.map((step, i) => (
              <SetupStepCard key={i} step={step} index={i + 1} />
            ))}
          </div>
        </div>
      )}

      {/* Quick Access */}
      <div className="rounded-xl border border-border bg-surface p-4">
        <h3 className="mb-3 text-sm font-semibold">快速接入</h3>
        <div className="space-y-2">
          <div className="flex items-center justify-between rounded-lg bg-surface-2 px-3 py-2">
            <span className="font-mono text-xs text-muted">Base URL</span>
            <div className="flex items-center gap-2">
              <code className="text-xs">{RELAY_API_BASE}/v1</code>
              <CopyButton value={`${RELAY_API_BASE}/v1`} />
            </div>
          </div>
          <div className="flex items-center justify-between rounded-lg bg-surface-2 px-3 py-2">
            <span className="font-mono text-xs text-muted">Authorization</span>
            <div className="flex items-center gap-2">
              <code className="text-xs">Bearer sk-relay_...</code>
              <CopyButton value="Bearer sk-relay_YOUR_KEY" label="复制格式" />
            </div>
          </div>
        </div>
      </div>

      {/* Recent Usage */}
      <div className="rounded-xl border border-border bg-surface p-4">
        <div className="mb-3 flex items-center justify-between">
          <h3 className="text-sm font-semibold">近期请求</h3>
          <button
            onClick={() => onNavigate("usage")}
            className="flex items-center gap-1 text-xs text-bronze hover:text-bronze-deep transition-colors"
          >
            查看全部 <ArrowRight size={12} />
          </button>
        </div>
        {usage.isLoading ? (
          <div className="space-y-2">
            {Array.from({ length: 3 }).map((_, i) => (
              <Skeleton key={i} className="h-10 w-full" />
            ))}
          </div>
        ) : !usage.data?.length ? (
          <EmptyState title="暂无请求记录" description="创建密钥后发起第一次请求" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-xs">
              <thead>
                <tr className="border-b border-border text-left text-muted">
                  <th className="pb-2 pr-4 font-medium">模型</th>
                  <th className="pb-2 pr-4 font-medium">端点</th>
                  <th className="pb-2 pr-4 font-medium text-right">费用</th>
                  <th className="pb-2 pr-4 font-medium text-right">耗时</th>
                  <th className="pb-2 font-medium">状态</th>
                </tr>
              </thead>
              <tbody>
                {usage.data.map((row) => (
                  <tr key={row.id} className="border-b border-border/50 last:border-0">
                    <td className="py-2 pr-4 font-mono">{row.requested_model}</td>
                    <td className="py-2 pr-4 text-muted">{row.endpoint}</td>
                    <td className="py-2 pr-4 text-right">${row.actual_cost}</td>
                    <td className="py-2 pr-4 text-right text-muted">{row.duration_ms}ms</td>
                    <td className="py-2"><StatusBadge status={row.status} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </motion.div>
  );
}

/* ─── Setup Step Card ─── */

type SetupStep = {
  label: string;
  detail: string;
  done: boolean;
  action: () => void;
  icon: typeof Wallet;
};

function SetupStepCard({ step, index }: { step: SetupStep; index: number }) {
  const Icon = step.icon;
  return (
    <button
      onClick={step.action}
      className={cn(
        "flex items-center gap-3 rounded-lg border px-3 py-2.5 text-left transition-colors",
        step.done
          ? "border-success/20 bg-success/5"
          : "border-border hover:border-bronze-soft hover:bg-surface-soft",
      )}
    >
      <div
        className={cn(
          "flex h-8 w-8 shrink-0 items-center justify-center rounded-full",
          step.done ? "bg-success/10" : "bg-surface-2",
        )}
      >
        {step.done ? (
          <CheckCircle2 size={16} className="text-success" />
        ) : (
          <Icon size={16} className="text-muted" />
        )}
      </div>
      <div className="min-w-0 flex-1">
        <div className="text-sm font-medium">{step.label}</div>
        <div className="truncate text-xs text-muted">{step.detail}</div>
      </div>
      {!step.done && <ArrowRight size={14} className="shrink-0 text-muted" />}
    </button>
  );
}
