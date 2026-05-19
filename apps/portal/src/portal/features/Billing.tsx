import { useEffect, useMemo, useState } from "react";
import { CreditCard, ExternalLink, Loader2, Ticket, WalletCards } from "lucide-react";
import type { Wallet } from "@elucid-relay/contracts";
import { api } from "../../api";
import { DataTable, Metric } from "../../components/DataTable";
import { CopyButton, PageNotice, SectionHeader, SectionTabs } from "../../components/Primitives";
import { available } from "../shared";

function WalletView() {
  const [wallet, setWallet] = useState<Wallet | null>(null);
  const [ledger, setLedger] = useState<any[]>([]);
  const [code, setCode] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [redeeming, setRedeeming] = useState(false);

  async function load() {
    setLoading(true);
    setError("");
    try {
      const [nextWallet, nextLedger] = await Promise.all([
        api.request<Wallet>("/api/portal/v1/wallet"),
        api.request<any[]>("/api/portal/v1/wallet/ledger"),
      ]);
      setWallet(nextWallet);
      setLedger(nextLedger);
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  async function redeem() {
    const nextCode = code.trim();
    if (!nextCode) return;
    setError("");
    setMessage("");
    setRedeeming(true);
    try {
      await api.request("/api/portal/v1/redeem", { method: "POST", body: JSON.stringify({ code: nextCode }) });
      setCode("");
      setMessage("兑换成功。");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setRedeeming(false);
    }
  }

  return (
    <div className="stack">
      <PageNotice error={error} message={message} />
      <div className="grid three">
        <Metric label="余额" value={`$${wallet?.balance ?? "0.00"}`} />
        <Metric label="已预留" value={`$${wallet?.reserved_balance ?? "0.00"}`} />
        <Metric label="可用余额" value={`$${available(wallet)}`} />
      </div>
      <section className="panel">
        <SectionHeader title="兑换码" action={<Ticket size={18} />} />
        <div className="form-grid relaxed">
          <input value={code} onChange={(event) => setCode(event.target.value)} placeholder="兑换码" />
          <button className="primary" onClick={redeem} disabled={!code.trim() || redeeming}>
            {redeeming ? <Loader2 className="spin" size={15} /> : <Ticket size={15} />}
            兑换
          </button>
        </div>
      </section>
      <section className="panel">
        <SectionHeader title="钱包流水" action={<button onClick={load} disabled={loading}>{loading ? "刷新中" : "刷新"}</button>} />
        <DataTable rows={ledger} columns={["entry_type", "amount", "balance_after", "reserved_after", "created_at"]} />
      </section>
    </div>
  );
}

export function BillingView() {
  type BillingTab = "wallet" | "topup" | "plans" | "subscriptions" | "orders" | "affiliate";
  const [tab, setTab] = useState<BillingTab>("wallet");
  const [plans, setPlans] = useState<any[]>([]);
  const [orders, setOrders] = useState<any[]>([]);
  const [subscriptions, setSubscriptions] = useState<any[]>([]);
  const [paymentMethods, setPaymentMethods] = useState<any[]>([]);
  const [amount, setAmount] = useState("20.00");
  const [planId, setPlanId] = useState("");
  const [paymentMethod, setPaymentMethod] = useState("");
  const [affiliateCode, setAffiliateCode] = useState("");
  const [checkoutUrl, setCheckoutUrl] = useState("");
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [creatingOrder, setCreatingOrder] = useState("");
  const [attributing, setAttributing] = useState(false);

  async function load() {
    setLoading(true);
    setError("");
    try {
      const [nextPlans, nextOrders, nextSubscriptions, nextPaymentMethods] = await Promise.all([
        api.request<any[]>("/api/portal/v1/subscription-plans"),
        api.request<any[]>("/api/portal/v1/orders"),
        api.request<any[]>("/api/portal/v1/subscriptions"),
        api.request<any[]>("/api/portal/v1/payment-methods"),
      ]);
      setPlans(nextPlans);
      setOrders(nextOrders);
      setSubscriptions(nextSubscriptions);
      setPaymentMethods(nextPaymentMethods);

      const defaultPlan = nextPlans.find((plan) => plan.status === "active") ?? nextPlans[0];
      if (defaultPlan && !nextPlans.some((plan) => plan.id === planId)) setPlanId(defaultPlan.id);
      if (nextPaymentMethods[0] && !nextPaymentMethods.some((method) => method.method === paymentMethod)) {
        setPaymentMethod(nextPaymentMethods[0].method);
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, []);

  const activePlans = plans.filter((plan) => plan.status === "active").length;
  const paidOrders = orders.filter((order) => order.status === "paid").length;
  const pendingOrders = orders.filter((order) => order.status === "pending").length;
  const activeSubscriptions = subscriptions.filter((subscription) => subscription.status === "active").length;
  const selectedPlan = useMemo(() => plans.find((plan) => plan.id === planId), [plans, planId]);
  const hasPaymentMethod = paymentMethods.length > 0 && Boolean(paymentMethod);

  async function createCheckout(orderType: "wallet_topup" | "subscription", override: { amountUsd?: string; planID?: string } = {}) {
    const nextAmount = (override.amountUsd ?? amount).trim();
    const nextPlanID = override.planID ?? planId;
    if (orderType === "wallet_topup" && !nextAmount) {
      setError("请输入充值金额。");
      return;
    }
    if (orderType === "subscription" && !nextPlanID) {
      setError("请选择订阅计划。");
      return;
    }
    if (!hasPaymentMethod) {
      setError("暂无可用支付方式。");
      return;
    }

    setError("");
    setMessage("");
    setCheckoutUrl("");
    setCreatingOrder(orderType === "wallet_topup" ? `topup:${nextAmount}` : `plan:${nextPlanID}`);
    try {
      const order = await api.request<any>("/api/portal/v1/orders", {
        method: "POST",
        body: JSON.stringify({
          order_type: orderType,
          amount_usd: orderType === "wallet_topup" ? nextAmount : undefined,
          plan_id: orderType === "subscription" ? nextPlanID : undefined,
          affiliate_code: affiliateCode.trim() || undefined,
        }),
      });
      const checkout = await api.request<any>(`/api/portal/v1/orders/${order.id}/checkout`, {
        method: "POST",
        body: JSON.stringify({ payment_method: paymentMethod }),
      });
      setCheckoutUrl(checkout.checkout_url);
      setMessage(orderType === "wallet_topup" ? "充值订单已创建。" : "订阅订单已创建。");
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setCreatingOrder("");
    }
  }

  async function attributeAffiliate() {
    const code = affiliateCode.trim();
    if (!code) return;
    setError("");
    setMessage("");
    setAttributing(true);
    try {
      await api.request("/api/portal/v1/affiliate-attribution", { method: "POST", body: JSON.stringify({ code }) });
      setMessage("推广码已记录。");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    } finally {
      setAttributing(false);
    }
  }

  return (
    <div className="stack">
      <PageNotice
        error={error}
        message={message}
        action={checkoutUrl && (
          <div className="actions">
            <a href={checkoutUrl} target="_blank" rel="noreferrer"><ExternalLink size={15} /> 打开支付页</a>
            <CopyButton value={checkoutUrl} label="复制链接" />
          </div>
        )}
      />
      <div className="metric-grid tight">
        <Metric label="可购买计划" value={String(activePlans)} detail={`${plans.length} 个计划总数`} />
        <Metric label="已支付订单" value={String(paidOrders)} detail={`${pendingOrders} 个待支付`} />
        <Metric label="有效订阅" value={String(activeSubscriptions)} detail={`${subscriptions.length} 条订阅记录`} />
        <Metric label="最近订单" value={String(orders.length)} detail="充值、订阅、退款状态" />
      </div>
      <SectionTabs
        active={tab}
        onChange={setTab}
        tabs={[
          { id: "wallet", label: "钱包" },
          { id: "topup", label: "充值" },
          { id: "plans", label: "订阅计划", count: plans.length },
          { id: "subscriptions", label: "我的订阅", count: subscriptions.length },
          { id: "orders", label: "订单", count: orders.length },
          { id: "affiliate", label: "推广" },
        ]}
      />
      {tab === "wallet" && <WalletView />}
      {tab === "topup" && (
        <section className="panel">
          <SectionHeader title="钱包充值" action={<CreditCard size={18} />} />
          <div className="quick-amounts">
            {["5.00", "20.00", "50.00", "100.00"].map((value) => (
              <button key={value} className={amount === value ? "active" : ""} onClick={() => setAmount(value)}>
                ${value}
              </button>
            ))}
          </div>
          <div className="form-grid relaxed">
            <label className="form-field">
              <span>金额 USD</span>
              <input value={amount} onChange={(event) => setAmount(event.target.value)} placeholder="20.00" />
            </label>
            <label className="form-field">
              <span>支付方式</span>
              <select value={paymentMethod} onChange={(event) => setPaymentMethod(event.target.value)}>
                <option value="">选择支付方式</option>
                {paymentMethods.map((method) => <option key={method.method} value={method.method}>{method.display_name}</option>)}
              </select>
            </label>
            <label className="form-field">
              <span>推广码</span>
              <input value={affiliateCode} onChange={(event) => setAffiliateCode(event.target.value)} placeholder="可空" />
            </label>
            <button className="primary" onClick={() => createCheckout("wallet_topup")} disabled={!hasPaymentMethod || Boolean(creatingOrder)}>
              {creatingOrder.startsWith("topup:") ? <Loader2 className="spin" size={15} /> : <WalletCards size={15} />}
              创建充值订单
            </button>
          </div>
        </section>
      )}
      {tab === "plans" && (
        <section className="panel">
          <SectionHeader
            title="订阅计划"
            action={selectedPlan && <button className="primary" onClick={() => createCheckout("subscription")} disabled={!hasPaymentMethod || Boolean(creatingOrder)}>{creatingOrder.startsWith("plan:") ? "创建中" : `订阅 ${selectedPlan.name}`}</button>}
          />
          <div className="plan-grid">
            {plans.map((plan) => (
              <article key={plan.id} className={`plan-card ${plan.id === planId ? "selected" : ""}`}>
                <header>
                  <strong>{plan.name}</strong>
                  <span>{plan.status}</span>
                </header>
                <div className="plan-price">${plan.price_usd}<small>/{plan.billing_period}</small></div>
                <dl>
                  <div><dt>赠送余额</dt><dd>${plan.wallet_credit_usd}</dd></div>
                  <div><dt>分组</dt><dd>{plan.group_name || "-"}</dd></div>
                  <div><dt>功能</dt><dd>{formatPlanFeatures(plan.features)}</dd></div>
                </dl>
                <div className="actions">
                  <button onClick={() => setPlanId(plan.id)}>选择</button>
                  <button className="primary" onClick={() => createCheckout("subscription", { planID: plan.id })} disabled={plan.status !== "active" || !hasPaymentMethod || Boolean(creatingOrder)}>
                    {creatingOrder === `plan:${plan.id}` ? <Loader2 className="spin" size={15} /> : <CreditCard size={15} />}
                    订阅
                  </button>
                </div>
              </article>
            ))}
          </div>
          {plans.length === 0 && !loading && <div className="empty">暂无订阅计划</div>}
        </section>
      )}
      {tab === "subscriptions" && (
        <section className="panel">
          <SectionHeader title="当前订阅" action={<button onClick={load} disabled={loading}>{loading ? "刷新中" : "刷新"}</button>} />
          <DataTable rows={subscriptions} columns={["plan_name", "status", "granted_group_id", "starts_at", "ends_at", "stripe_subscription_id"]} />
        </section>
      )}
      {tab === "orders" && (
        <section className="panel">
          <SectionHeader title="订单" action={<button onClick={load} disabled={loading}>{loading ? "刷新中" : "刷新"}</button>} />
          <DataTable rows={orders} columns={["order_type", "amount_usd", "status", "payment_method", "pay_currency", "pay_amount_cents", "fx_rate", "checkout_url", "paid_at", "refunded_at", "refund_blocked_reason", "created_at"]} />
        </section>
      )}
      {tab === "affiliate" && (
        <section className="panel">
          <SectionHeader title="推广归因" />
          <div className="form-grid relaxed">
            <input value={affiliateCode} onChange={(event) => setAffiliateCode(event.target.value)} placeholder="推广码" />
            <button onClick={attributeAffiliate} disabled={!affiliateCode.trim() || attributing}>
              {attributing ? <Loader2 className="spin" size={15} /> : null}
              记录推广码
            </button>
          </div>
        </section>
      )}
    </div>
  );
}

function formatPlanFeatures(value: unknown) {
  if (!value) return "-";
  if (Array.isArray(value)) return value.map((item) => String(item)).join(", ") || "-";
  if (typeof value === "string") return value || "-";
  if (typeof value === "object") return Object.entries(value).map(([key, item]) => `${key}: ${String(item)}`).join(", ") || "-";
  return String(value);
}
