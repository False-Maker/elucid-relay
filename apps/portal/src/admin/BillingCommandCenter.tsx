import { RefreshCcw } from "lucide-react";
import type { BillingTab, PaymentMethodRoute, PaymentProvider, PaymentSettings } from "./types";
import { DataTable, Metric } from "../components/DataTable";
import { SectionHeader } from "../components/Primitives";

export function BillingCommandCenter({
  summary,
  plans,
  orders,
  paymentEvents,
  subscriptions,
  rebates,
  paymentSettings,
  paymentProviders,
  paymentRoutes,
  onSelectTab,
  onRefresh,
}: {
  summary: Record<string, string>;
  plans: any[];
  orders: any[];
  paymentEvents: any[];
  subscriptions: any[];
  rebates: any[];
  paymentSettings: PaymentSettings | null;
  paymentProviders: PaymentProvider[];
  paymentRoutes: PaymentMethodRoute[];
  onSelectTab: (tab: BillingTab) => void;
  onRefresh: () => void;
}) {
  const activePlans = plans.filter((plan) => plan.status === "active").length;
  const paidOrders = orders.filter((order) => order.status === "paid").length;
  const blockedRefunds = orders.filter((order) => order.status === "refund_blocked").length;
  const failedEvents = paymentEvents.filter((event) => event.status === "failed").length;
  const activeSubscriptions = subscriptions.filter((subscription) => subscription.status === "active").length;
  const enabledRoutes = paymentRoutes.filter((route) => route.enabled).length;
  const activeProviders = paymentProviders.filter((provider) => provider.status === "active").length;

  return (
    <section className="panel">
      <SectionHeader
        title="商业化概况"
        action={<button onClick={onRefresh}><RefreshCcw size={15} /> 刷新</button>}
      />
      <div className="metric-grid tight">
        <Metric label="支付系统" value={paymentSettings?.enabled ? "已启用" : "未启用"} detail={`${enabledRoutes} 个支付方式 · ${activeProviders} 个活跃服务商`} />
        <Metric label="启用计划" value={String(activePlans)} detail={`${plans.length} 个计划总数`} />
        <Metric label="已支付订单" value={String(paidOrders)} detail={`${blockedRefunds} 个退款阻塞`} />
        <Metric label="失败事件" value={String(failedEvents)} detail={`${paymentEvents.length} 条支付事件`} />
        <Metric label="有效订阅" value={String(activeSubscriptions)} detail={`MRR $${summary.subscription_mrr_usd ?? "0"}`} />
      </div>
      <div className="actions panel-actions">
        <button onClick={() => onSelectTab("payments")}>支付设置</button>
        <button onClick={() => onSelectTab("plans")}>订阅计划</button>
        <button onClick={() => onSelectTab("orders")}>订单退款</button>
        <button onClick={() => onSelectTab("events")}>支付事件</button>
        <button onClick={() => onSelectTab("subscriptions")}>订阅</button>
      </div>
      <div className="dashboard-list">
        <section className="mini-panel">
          <h3>最近订单</h3>
          <DataTable rows={orders.slice(0, 5)} columns={["order_type", "amount_usd", "status", "paid_at", "created_at"]} />
        </section>
        <section className="mini-panel">
          <h3>待结算返利</h3>
          <DataTable rows={rebates.filter((row) => row.status === "pending").slice(0, 5)} columns={["code", "amount_usd", "status", "created_at"]} />
        </section>
      </div>
    </section>
  );
}
