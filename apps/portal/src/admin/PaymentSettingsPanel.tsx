import { useEffect, useState } from "react";
import { Plus, RefreshCcw, Send, ShieldCheck } from "lucide-react";
import type { PaymentMethodRoute, PaymentProvider, PaymentSettings } from "./types";
import { adminApi } from "../adminApi";
import { DataTable, Metric } from "../components/DataTable";
import { SectionHeader } from "../components/Primitives";

const providerTypes = ["stripe", "easypay", "alipay", "wechat"];
const methodOptions = ["stripe", "alipay", "wechat"];

export function PaymentSettingsPanel({ onSaved }: { onSaved?: () => void }) {
  const [settings, setSettings] = useState<PaymentSettings | null>(null);
  const [routes, setRoutes] = useState<PaymentMethodRoute[]>([]);
  const [providers, setProviders] = useState<PaymentProvider[]>([]);
  const [form, setForm] = useState({
    enabled: false,
    fx_usd_cny: "7.2",
    order_timeout_minutes: "30",
    max_pending_orders_per_user: "5",
    cancel_cooldown_seconds: "60",
    provider_selection: "priority",
    help_text: "",
    stripe_success_url: "",
    stripe_cancel_url: "",
  });
  const [providerForm, setProviderForm] = useState({
    provider_type: "easypay",
    name: "",
    status: "disabled",
    priority: "100",
    weight: "1",
    supported_methods: "alipay,wechat",
    min_amount_usd: "0",
    max_amount_usd: "0",
    daily_limit_usd: "0",
    config_json: "{}",
    secrets_json: "{}",
  });
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");

  async function load() {
    const [nextSettings, nextRoutes, nextProviders] = await Promise.all([
      adminApi.request<PaymentSettings>("/api/admin/v1/payment-settings"),
      adminApi.request<PaymentMethodRoute[]>("/api/admin/v1/payment-method-routes"),
      adminApi.request<PaymentProvider[]>("/api/admin/v1/payment-providers"),
    ]);
    setSettings(nextSettings);
    setRoutes(nextRoutes);
    setProviders(nextProviders);
    setForm({
      enabled: nextSettings.enabled,
      fx_usd_cny: nextSettings.fx_usd_cny,
      order_timeout_minutes: String(nextSettings.order_timeout_minutes),
      max_pending_orders_per_user: String(nextSettings.max_pending_orders_per_user),
      cancel_cooldown_seconds: String(nextSettings.cancel_cooldown_seconds),
      provider_selection: nextSettings.provider_selection || "priority",
      help_text: nextSettings.help_text || "",
      stripe_success_url: nextSettings.stripe_success_url || "",
      stripe_cancel_url: nextSettings.stripe_cancel_url || "",
    });
  }

  useEffect(() => {
    void load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"));
  }, []);

  async function saveSettings() {
    setError("");
    setMessage("");
    try {
      const data = await adminApi.request<PaymentSettings>("/api/admin/v1/payment-settings", {
        method: "PUT",
        body: JSON.stringify({
          enabled: form.enabled,
          fx_usd_cny: form.fx_usd_cny,
          order_timeout_minutes: Number(form.order_timeout_minutes),
          max_pending_orders_per_user: Number(form.max_pending_orders_per_user),
          cancel_cooldown_seconds: Number(form.cancel_cooldown_seconds),
          provider_selection: form.provider_selection,
          help_text: form.help_text,
          stripe_success_url: form.stripe_success_url,
          stripe_cancel_url: form.stripe_cancel_url,
        }),
      });
      setSettings(data);
      setMessage("支付全局设置已保存。");
      onSaved?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function saveRoutes(nextRoutes = routes) {
    setError("");
    setMessage("");
    try {
      const data = await adminApi.request<PaymentMethodRoute[]>("/api/admin/v1/payment-method-routes", {
        method: "PUT",
        body: JSON.stringify({ routes: nextRoutes }),
      });
      setRoutes(data);
      setMessage("支付方式路由已保存。");
      onSaved?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function createProvider() {
    setError("");
    setMessage("");
    try {
      await adminApi.request("/api/admin/v1/payment-providers", {
        method: "POST",
        body: JSON.stringify({
          provider_type: providerForm.provider_type,
          name: providerForm.name,
          status: providerForm.status,
          priority: Number(providerForm.priority),
          weight: Number(providerForm.weight),
          supported_methods: splitCSV(providerForm.supported_methods),
          min_amount_usd: providerForm.min_amount_usd,
          max_amount_usd: providerForm.max_amount_usd,
          daily_limit_usd: providerForm.daily_limit_usd,
          config: parseJSON(providerForm.config_json),
          secrets: parseJSON(providerForm.secrets_json),
          metadata: {},
        }),
      });
      setProviderForm({ ...providerForm, name: "", secrets_json: "{}" });
      setMessage("支付服务商已创建。");
      await load();
      onSaved?.();
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  async function testProvider(id: string) {
    setError("");
    setMessage("");
    try {
      await adminApi.request(`/api/admin/v1/payment-providers/${id}/test`, { method: "POST" });
      setMessage("服务商配置测试通过。");
    } catch (err) {
      setError(err instanceof Error ? err.message : "请求失败。");
    }
  }

  function patchRoute(method: string, patch: Partial<PaymentMethodRoute>) {
    setRoutes((current) => current.map((route) => route.method === method ? { ...route, ...patch } : route));
  }

  return (
    <div className="stack">
      <section className="panel">
        <SectionHeader
          title="统一支付设置"
          description="Stripe、支付宝、微信支付和 EasyPay 共用这里的支付开关、汇率、订单限制和服务商选择策略。"
          action={<button onClick={() => load().catch((err) => setError(err instanceof Error ? err.message : "请求失败。"))}><RefreshCcw size={15} /> 刷新</button>}
        />
        {(error || message) && <div className="inline-feedback">{error && <div className="error">{error}</div>}{message && <div className="success">{message}</div>}</div>}
        <div className="grid three">
          <Metric label="支付系统" value={settings?.enabled ? "已启用" : "未启用"} detail="Stripe 可独立保留，国内支付需要启用总开关" />
          <Metric label="USD/CNY" value={settings?.fx_usd_cny || form.fx_usd_cny} detail="订单创建时锁定汇率" />
          <Metric label="服务商" value={String(providers.filter((provider) => provider.status === "active").length)} detail={`${providers.length} 个实例`} />
        </div>
        <div className="form-grid">
          <label><input type="checkbox" checked={form.enabled} onChange={(event) => setForm({ ...form, enabled: event.target.checked })} /> 启用国内支付</label>
          <input value={form.fx_usd_cny} onChange={(event) => setForm({ ...form, fx_usd_cny: event.target.value })} placeholder="USD/CNY 固定汇率" />
          <input value={form.order_timeout_minutes} onChange={(event) => setForm({ ...form, order_timeout_minutes: event.target.value })} placeholder="订单超时分钟" />
          <input value={form.max_pending_orders_per_user} onChange={(event) => setForm({ ...form, max_pending_orders_per_user: event.target.value })} placeholder="用户待支付上限" />
          <input value={form.cancel_cooldown_seconds} onChange={(event) => setForm({ ...form, cancel_cooldown_seconds: event.target.value })} placeholder="取消冷却秒" />
          <select value={form.provider_selection} onChange={(event) => setForm({ ...form, provider_selection: event.target.value })}><option value="priority">优先级</option><option value="weighted">权重</option></select>
          <input value={form.stripe_success_url} onChange={(event) => setForm({ ...form, stripe_success_url: event.target.value })} placeholder="Stripe 成功 URL，可空" />
          <input value={form.stripe_cancel_url} onChange={(event) => setForm({ ...form, stripe_cancel_url: event.target.value })} placeholder="Stripe 取消 URL，可空" />
          <input value={form.help_text} onChange={(event) => setForm({ ...form, help_text: event.target.value })} placeholder="前台支付帮助文案，可空" />
          <button className="primary" onClick={saveSettings}><ShieldCheck size={15} /> 保存设置</button>
        </div>
      </section>

      <section className="panel">
        <SectionHeader title="支付方式路由" description="控制前台按钮是否展示，以及每个支付方式可使用的服务商类型。" action={<button onClick={() => saveRoutes()}><ShieldCheck size={15} /> 保存路由</button>} />
        <div className="dashboard-list">
          {routes.map((route) => (
            <section className="mini-panel" key={route.method}>
              <h3>{route.display_name || route.method}</h3>
              <div className="form-grid single">
                <label><input type="checkbox" checked={route.enabled} onChange={(event) => patchRoute(route.method, { enabled: event.target.checked })} /> 启用</label>
                <input value={route.display_name} onChange={(event) => patchRoute(route.method, { display_name: event.target.value })} placeholder="显示名称" />
                <input value={route.provider_types.join(",")} onChange={(event) => patchRoute(route.method, { provider_types: splitCSV(event.target.value) })} placeholder="provider types" />
                <input value={route.min_amount_usd} onChange={(event) => patchRoute(route.method, { min_amount_usd: event.target.value })} placeholder="最小 USD" />
                <input value={route.max_amount_usd} onChange={(event) => patchRoute(route.method, { max_amount_usd: event.target.value })} placeholder="最大 USD，0 不限" />
              </div>
            </section>
          ))}
        </div>
      </section>

      <section className="panel">
        <SectionHeader title="支付服务商实例" description="创建 Stripe、支付宝官方、微信官方或 EasyPay 实例。密钥只提交到后端加密保存，表格只显示是否已配置。" />
        <div className="form-grid">
          <select value={providerForm.provider_type} onChange={(event) => setProviderForm({ ...providerForm, provider_type: event.target.value, supported_methods: defaultMethods(event.target.value) })}>{providerTypes.map((type) => <option key={type} value={type}>{type}</option>)}</select>
          <input value={providerForm.name} onChange={(event) => setProviderForm({ ...providerForm, name: event.target.value })} placeholder="实例名称" />
          <select value={providerForm.status} onChange={(event) => setProviderForm({ ...providerForm, status: event.target.value })}><option value="disabled">禁用</option><option value="active">启用</option></select>
          <input value={providerForm.priority} onChange={(event) => setProviderForm({ ...providerForm, priority: event.target.value })} placeholder="优先级" />
          <input value={providerForm.weight} onChange={(event) => setProviderForm({ ...providerForm, weight: event.target.value })} placeholder="权重" />
          <input value={providerForm.supported_methods} onChange={(event) => setProviderForm({ ...providerForm, supported_methods: event.target.value })} placeholder={methodOptions.join(",")} />
          <input value={providerForm.min_amount_usd} onChange={(event) => setProviderForm({ ...providerForm, min_amount_usd: event.target.value })} placeholder="最小 USD" />
          <input value={providerForm.max_amount_usd} onChange={(event) => setProviderForm({ ...providerForm, max_amount_usd: event.target.value })} placeholder="最大 USD，0 不限" />
          <input value={providerForm.daily_limit_usd} onChange={(event) => setProviderForm({ ...providerForm, daily_limit_usd: event.target.value })} placeholder="日限额 USD，0 不限" />
          <textarea value={providerForm.config_json} onChange={(event) => setProviderForm({ ...providerForm, config_json: event.target.value })} placeholder='{"api_base_url":"https://pay.example.com","app_id":"...","mchid":"..."}' />
          <textarea value={providerForm.secrets_json} onChange={(event) => setProviderForm({ ...providerForm, secrets_json: event.target.value })} placeholder='{"secret_key":"sk_live_...","pid":"...","key":"...","private_key":"<PEM>"}' />
          <button className="primary" onClick={createProvider} disabled={!providerForm.name}><Plus size={15} /> 创建服务商</button>
        </div>
        <DataTable rows={providers} columns={["provider_type", "name", "status", "priority", "weight", "supported_methods", "min_amount_usd", "max_amount_usd", "daily_limit_usd", "secret_configured", "webhook_url", "created_at"]} action={(row) => <button onClick={() => testProvider(row.id)}><Send size={15} /> 测试</button>} />
      </section>
    </div>
  );
}

function splitCSV(value: string) {
  return value.split(",").map((item) => item.trim()).filter(Boolean);
}

function parseJSON(value: string) {
  const trimmed = value.trim();
  if (!trimmed) return {};
  return JSON.parse(trimmed);
}

function defaultMethods(providerType: string) {
  if (providerType === "stripe") return "stripe";
  if (providerType === "alipay") return "alipay";
  if (providerType === "wechat") return "wechat";
  return "alipay,wechat";
}
